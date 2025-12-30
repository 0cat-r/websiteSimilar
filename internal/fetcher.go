package internal

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Fetcher HTTP 抓取器
type Fetcher struct {
	client       *http.Client
	maxRedirects int
}

// NewFetcher 创建新的抓取器
func NewFetcher(timeout time.Duration, maxRedirects int) *Fetcher {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // 忽略 SSL 证书错误
		},
	}

	fetcher := &Fetcher{
		maxRedirects: maxRedirects,
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// 限制重定向次数
			if len(via) >= maxRedirects {
				return fmt.Errorf("重定向次数超过限制 (%d)", maxRedirects)
			}
			return nil
		},
	}

	fetcher.client = client
	return fetcher
}

// Fetch 抓取单个 URL（使用 context 支持取消）
func (f *Fetcher) Fetch(ctx context.Context, item URLItem) FetchResult {
	result := FetchResult{
		URLItem:       item,
		RedirectChain: []string{item.NormalizedURL},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", item.NormalizedURL, nil)
	if err != nil {
		result.Error = fmt.Sprintf("创建请求失败: %v", err)
		return result
	}

	// 设置 User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	// 为本次请求创建独立的重定向链记录（避免并发竞态）
	redirectChain := make([]string, 0)

	// 创建临时 client 用于记录重定向链
	tempClient := &http.Client{
		Timeout:   f.client.Timeout,
		Transport: f.client.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// 限制重定向次数
			if len(via) >= f.maxRedirects {
				return fmt.Errorf("重定向次数超过限制 (%d)", f.maxRedirects)
			}
			// CheckRedirect 会被多次调用，每次调用时：
			// - via 包含所有之前的请求（包括原始请求）
			// - req.URL 是下一个跳转目标（Location header 指向的 URL）
			// 我们只需要记录 req.URL（下一个跳转目标），避免重复
			redirectChain = append(redirectChain, req.URL.String())
			return nil
		},
	}

	resp, err := tempClient.Do(req)
	if err != nil {
		result.Error = fmt.Sprintf("请求失败: %v", err)
		return result
	}
	defer resp.Body.Close()

	// 记录重定向链：起点 + 中间 hop + 终点
	result.RedirectChain = []string{item.NormalizedURL}
	finalURL := resp.Request.URL.String()

	// 添加中间跳转（redirectChain 中已经记录了所有中间跳转目标）
	result.RedirectChain = append(result.RedirectChain, redirectChain...)

	// 如果最终 URL 与最后一个跳转目标不同，添加最终 URL
	// （通常 finalURL 应该等于 redirectChain 的最后一个元素，但为了保险还是检查一下）
	if finalURL != item.NormalizedURL {
		if len(redirectChain) == 0 || redirectChain[len(redirectChain)-1] != finalURL {
			result.RedirectChain = append(result.RedirectChain, finalURL)
		}
	}

	// 记录最终状态
	result.StatusCode = resp.StatusCode
	result.FinalURL = resp.Request.URL.String()
	result.ContentType = resp.Header.Get("Content-Type")
	result.ContentLength = resp.ContentLength

	// 读取 body（所有类型都读取，以支持非 HTML 内容的相似性检测）
	limitReader := io.LimitReader(resp.Body, MaxHTMLSize)
	body, err := io.ReadAll(limitReader)
	if err != nil {
		result.Error = fmt.Sprintf("读取响应体失败: %v", err)
		return result
	}
	result.ContentLength = int64(len(body))

	// 根据 Content-Type 分类处理
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	result.ContentCategory = categorizeContent(contentType)

	switch result.ContentCategory {
	case ContentCategoryHTML:
		result.RawHTML = body
		result.Title = extractTitle(body)
	case ContentCategoryText, ContentCategoryImage, ContentCategoryBinary:
		result.RawBody = body
	default:
		result.ContentCategory = ContentCategoryEmpty
	}

	return result
}

// FetchBatch 批量抓取（并发，支持 ctx 取消）
func (f *Fetcher) FetchBatch(ctx context.Context, items []URLItem, parallel int) []FetchResult {
	results := make([]FetchResult, len(items))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup

	for i, item := range items {
		// 检查 ctx 是否已取消
		select {
		case <-ctx.Done():
			// ctx 已取消，填充剩余结果为空
			for j := i; j < len(items); j++ {
				results[j] = FetchResult{
					URLItem: items[j],
					Error:   "context cancelled",
				}
			}
			return results
		default:
		}

		wg.Add(1)
		sem <- struct{}{}
		go func(idx int, it URLItem) {
			defer wg.Done()
			defer func() { <-sem }()

			// 再次检查 ctx（goroutine 内）
			select {
			case <-ctx.Done():
				results[idx] = FetchResult{
					URLItem: it,
					Error:   "context cancelled",
				}
				return
			default:
			}

			results[idx] = f.Fetch(ctx, it)
		}(i, item)
	}

	wg.Wait()
	return results
}

// isHTML 判断 Content-Type 是否为 HTML
func isHTML(contentType string) bool {
	return strings.Contains(strings.ToLower(contentType), "text/html")
}

// categorizeContent 根据 Content-Type 分类内容类型
func categorizeContent(contentType string) ContentCategory {
	ct := strings.ToLower(contentType)

	// HTML
	if strings.Contains(ct, "text/html") {
		return ContentCategoryHTML
	}

	// 文本类（JSON, XML, 纯文本等）
	if strings.Contains(ct, "application/json") ||
		strings.Contains(ct, "application/xml") ||
		strings.Contains(ct, "text/xml") ||
		strings.Contains(ct, "text/plain") ||
		strings.Contains(ct, "text/css") ||
		strings.Contains(ct, "text/javascript") ||
		strings.Contains(ct, "application/javascript") {
		return ContentCategoryText
	}

	// 图片
	if strings.Contains(ct, "image/") {
		return ContentCategoryImage
	}

	// 其他有内容的响应归为二进制
	if ct != "" {
		return ContentCategoryBinary
	}

	return ContentCategoryEmpty
}

// extractTitle 从 HTML 内容中提取标题
func extractTitle(html []byte) string {
	// 使用正则表达式提取 <title> 标签内容
	titleRegex := regexp.MustCompile(`(?i)<title[^>]*>\s*(.*?)\s*</title>`)
	matches := titleRegex.FindSubmatch(html)
	if len(matches) > 1 {
		title := strings.TrimSpace(string(matches[1]))
		// 清理 HTML 实体
		title = cleanTitleContent(title)
		return title
	}
	return ""
}

// cleanTitleContent 清理标题内容（处理 HTML 实体等）
func cleanTitleContent(title string) string {
	// HTML 实体映射
	entities := map[string]string{
		"&amp;":   "&",
		"&lt;":    "<",
		"&gt;":    ">",
		"&quot;":  "\"",
		"&#39;":   "'",
		"&nbsp;":  " ",
		"&#160;":  " ",
		"&#8203;": "",
	}

	for entity, replacement := range entities {
		title = strings.ReplaceAll(title, entity, replacement)
	}

	// 清理多余的空白字符
	spaceRegex := regexp.MustCompile(`\s+`)
	title = spaceRegex.ReplaceAllString(title, " ")

	return strings.TrimSpace(title)
}
