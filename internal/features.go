package internal

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
	"github.com/corona10/goimagehash"
)

// parseFeatures 解析页面特征
// 从渲染后的 HTML、DOM 统计、性能时间、截图中提取特征
func parseFeatures(features *PageFeatures, htmlContent, domStatsJSON, perfTimingJSON string, screenshotBuf []byte) error {
	logger := GetLogger()

	// 解析文本特征
	if err := extractTextFeatures(features, htmlContent); err != nil {
		logger.Debug("文本特征提取失败: %v", err)
		// 文本特征提取失败不影响其他特征
	}

	// 解析 DOM 统计
	if err := parseDOMStats(features, domStatsJSON); err != nil {
		logger.Debug("DOM 统计解析失败: %v", err)
		// DOM 统计失败不影响其他特征
	}

	// 解析性能时间
	if err := parsePerfTiming(features, perfTimingJSON); err != nil {
		logger.Debug("性能时间解析失败: %v", err)
		// 性能时间失败不影响其他特征
	}

	// 解析截图
	if err := parseScreenshot(features, screenshotBuf); err != nil {
		logger.Debug("截图解析失败: %v", err)
		// 截图失败不影响其他特征
	}

	return nil
}

// extractTextFeatures 提取文本特征
// 提取正文文本，计算 SimHash 和文本长度
func extractTextFeatures(features *PageFeatures, htmlContent string) error {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
	if err != nil {
		return err
	}

	// 抽取正文文本
	bodyText := extractMainText(doc)

	// 清洗文本
	cleaned := cleanText(bodyText)

	// 计算文本长度
	features.TextLength = utf8.RuneCountInString(cleaned)

	// 计算 SimHash
	features.TextSimHash = computeSimHash(cleaned)

	return nil
}

// extractMainText 抽取正文文本
// 优先找 article、main 这些语义标签，找不到就按文本密度排序选 top
func extractMainText(doc *goquery.Document) string {
	type textBlock struct {
		text   string
		length int
		score  float64 // 优先级分数
	}

	var blocks []textBlock

	// 忽略导航、脚注、版权等区域
	skipSelectors := []string{
		"nav", "footer", "header", "aside",
		"[class*='nav']", "[class*='footer']", "[class*='header']",
		"[class*='copyright']", "[class*='sidebar']", "[class*='ad']",
		"[id*='nav']", "[id*='footer']", "[id*='header']", "[id*='copyright']",
	}

	// 优先查找语义标签
	prioritySelectors := []string{"article", "main", "[role='main']", "[class*='content']", "[class*='article']"}
	var priorityBlocks []textBlock

	for _, sel := range prioritySelectors {
		doc.Find(sel).Each(func(i int, s *goquery.Selection) {
			text := strings.TrimSpace(s.Text())
			if len(text) > 50 {
				priorityBlocks = append(priorityBlocks, textBlock{
					text:   text,
					length: len(text),
					score:  100.0, // 高优先级
				})
			}
		})
	}

	// 如果找到优先级块，直接使用
	if len(priorityBlocks) > 0 {
		sort.Slice(priorityBlocks, func(i, j int) bool {
			return priorityBlocks[i].length > priorityBlocks[j].length
		})
		// 取前 3 个最长的优先级块
		if len(priorityBlocks) > 3 {
			priorityBlocks = priorityBlocks[:3]
		}
		result := make([]string, len(priorityBlocks))
		for i, b := range priorityBlocks {
			result[i] = b.text
		}
		return strings.Join(result, " ")
	}

	// 否则，遍历 body 下的所有元素
	doc.Find("body *").Each(func(i int, s *goquery.Selection) {
		// 检查是否应该跳过
		shouldSkip := false
		for _, sel := range skipSelectors {
			if s.Is(sel) {
				shouldSkip = true
				break
			}
		}
		if shouldSkip {
			return
		}

		text := strings.TrimSpace(s.Text())
		if len(text) > 50 { // 至少 50 字符才考虑
			// 计算文本密度
			childCount := s.Children().Length()
			density := float64(len(text)) / float64(maxInt(childCount, 1))
			if density > 10 { // 文本密度阈值
				// 计算分数：长度 + 密度
				score := float64(len(text)) + density*10
				blocks = append(blocks, textBlock{
					text:   text,
					length: len(text),
					score:  score,
				})
			}
		}
	})

	// 按分数排序，选择 top blocks
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].score > blocks[j].score
	})

	// 取前 5 个最高分的块（比之前多取一些）
	topN := 5
	if len(blocks) > topN {
		blocks = blocks[:topN]
	}

	result := make([]string, len(blocks))
	for i, b := range blocks {
		result[i] = b.text
	}

	return strings.Join(result, " ")
}

// cleanText 清洗文本
func cleanText(text string) string {
	// 转小写
	text = strings.ToLower(text)

	// 替换多余空白字符为一个空格
	re := regexp.MustCompile(`\s+`)
	text = re.ReplaceAllString(text, " ")

	// 去掉很短的 token（少于 2 个字符）
	words := strings.Fields(text)
	var filtered []string
	for _, word := range words {
		if len(word) >= 2 {
			filtered = append(filtered, word)
		}
	}

	return strings.Join(filtered, " ")
}

// computeSimHash 计算 64-bit SimHash
// 对每个 token 计算 hash，然后累加每个 bit 位，最后生成指纹
func computeSimHash(text string) uint64 {
	tokens := strings.Fields(text)
	if len(tokens) == 0 {
		return 0
	}

	// 64 个 bit 位的累加器
	var bits [64]int

	for _, token := range tokens {
		// 计算 token 的 64-bit hash
		hash := hash64(token)
		// 对每个 bit 位累加
		for i := 0; i < 64; i++ {
			if hash&(1<<uint(i)) != 0 {
				bits[i]++
			} else {
				bits[i]--
			}
		}
	}

	// 生成最终指纹
	var fingerprint uint64
	for i := 0; i < 64; i++ {
		if bits[i] > 0 {
			fingerprint |= 1 << uint(i)
		}
	}

	return fingerprint
}

// hash64 简单的 64-bit hash 函数（FNV-1a 变种）
func hash64(s string) uint64 {
	var h uint64 = 14695981039346656037 // FNV offset basis
	for _, c := range s {
		h ^= uint64(c)
		h *= 1099511628211 // FNV prime
	}
	return h
}

// parseDOMStats 解析 DOM 统计信息
func parseDOMStats(features *PageFeatures, jsonStr string) error {
	var stats DOMStats
	if err := json.Unmarshal([]byte(jsonStr), &stats); err != nil {
		return err
	}

	features.DOMNodeCount = stats.DOMNodeCount
	features.TextNodeCount = stats.TextNodeCount
	features.TagCount = stats.TagCount
	features.DepthHist = stats.DepthHist
	features.PathCount = stats.PathCount

	return nil
}

// parsePerfTiming 解析性能时间信息
func parsePerfTiming(features *PageFeatures, jsonStr string) error {
	var timing PerfTiming
	if err := json.Unmarshal([]byte(jsonStr), &timing); err != nil {
		return err
	}

	if timing.NavigationStart == 0 {
		return nil
	}

	base := float64(timing.NavigationStart)
	features.TTFB = float64(timing.ResponseStart) - base
	features.DOMContentLoaded = float64(timing.DomContentLoadedEventEnd) - base
	features.LoadEvent = float64(timing.LoadEventEnd) - base

	return nil
}

// parseScreenshot 解析截图并计算 pHash
// 用感知哈希算法计算截图指纹，用于视觉相似度比较
func parseScreenshot(features *PageFeatures, screenshotBuf []byte) error {
	if len(screenshotBuf) == 0 {
		return fmt.Errorf("截图数据为空")
	}

	// 解码 PNG
	img, err := png.Decode(bytes.NewReader(screenshotBuf))
	if err != nil {
		return err
	}

	// 记录尺寸
	bounds := img.Bounds()
	features.ScreenshotW = bounds.Dx()
	features.ScreenshotH = bounds.Dy()

	// 计算感知哈希（只保存 hash 值，不保存原始图片以节省内存）
	hash, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return err
	}

	features.PHash = hash.GetHash()

	return nil
}

// maxInt 返回两个整数中的较大值（避免与其他包冲突）
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ExtractNonHTMLFeatures 提取非 HTML 内容的特征
// 比 HTML 简单得多：文本类直接 SimHash，图片直接 pHash，其他用 MD5
func ExtractNonHTMLFeatures(category ContentCategory, body []byte) *PageFeatures {
	if len(body) == 0 {
		return nil
	}

	features := &PageFeatures{
		Category: category, // 设置内容类型，用于后续相似度判断
	}

	switch category {
	case ContentCategoryText:
		// 文本类内容（JSON, XML, 纯文本等）直接计算 SimHash
		text := string(body)
		cleaned := cleanText(text)
		features.TextLength = utf8.RuneCountInString(cleaned)
		features.TextSimHash = computeSimHash(cleaned)

	case ContentCategoryImage:
		// 图片直接计算 pHash
		if err := parseImageFeatures(features, body); err != nil {
			// 图片解析失败，降级到 MD5
			features.TextSimHash = computeMD5Hash(body)
			features.TextLength = len(body)
			features.Category = ContentCategoryBinary // 降级为二进制处理
		}

	case ContentCategoryBinary:
		// 二进制内容用 MD5 作为指纹（完全匹配）
		features.TextSimHash = computeMD5Hash(body)
		features.TextLength = len(body)

	default:
		return nil
	}

	return features
}

// parseImageFeatures 解析图片特征（支持多种格式）
func parseImageFeatures(features *PageFeatures, imgData []byte) error {
	if len(imgData) == 0 {
		return fmt.Errorf("图片数据为空")
	}

	// 尝试解码图片（支持 PNG, JPEG, GIF）
	img, format, err := image.Decode(bytes.NewReader(imgData))
	if err != nil {
		return fmt.Errorf("图片解码失败 (%s): %w", format, err)
	}

	// 记录尺寸
	bounds := img.Bounds()
	features.ScreenshotW = bounds.Dx()
	features.ScreenshotH = bounds.Dy()

	// 计算感知哈希
	hash, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return err
	}

	features.PHash = hash.GetHash()
	features.TextLength = len(imgData) // 用文件大小作为 TextLength

	return nil
}

// computeMD5Hash 计算 MD5 哈希，转换为 uint64（取前 8 字节）
func computeMD5Hash(data []byte) uint64 {
	hash := md5.Sum(data)
	// 取前 8 字节转为 uint64
	var result uint64
	for i := 0; i < 8; i++ {
		result = (result << 8) | uint64(hash[i])
	}
	return result
}
