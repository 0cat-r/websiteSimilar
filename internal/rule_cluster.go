package internal

import (
	"crypto/md5"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// RuleAssignment 规则聚类分配结果
type RuleAssignment struct {
	ClusterID   string // 例如 "err5xx-http_example.com_80"
	IsCanonical bool
	Priority    int // 内部优先级，优先级高的先执行，避免被覆盖
}

// HtmlFingerprint HTML 指纹
// 用于判断错误模板、短页等是否一致
type HtmlFingerprint struct {
	Length int
	Hash   uint64
}

// perURLInfo 每个 URL 的规则聚类信息
type perURLInfo struct {
	FR     FetchResult
	Origin string
	HtmlFP HtmlFingerprint
	IsHTML bool
}

// OriginKey 计算 origin key
// 格式：scheme://host:port
func OriginKey(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}

	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()

	// 如果 scheme 或 host 为空，无法构建有效的 origin
	if scheme == "" || host == "" {
		return ""
	}

	// 如果没有端口，使用默认端口
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	return fmt.Sprintf("%s://%s:%s", scheme, host, port)
}

// FingerprintHTML 计算 HTML 指纹
func FingerprintHTML(html []byte) HtmlFingerprint {
	if len(html) == 0 {
		return HtmlFingerprint{Length: 0, Hash: 0}
	}

	// 提取纯文本（简单版本，用于错误页/短页）
	text := extractSimpleText(html)
	cleaned := cleanTextForFingerprint(text)

	return HtmlFingerprint{
		Length: len(cleaned),
		Hash:   hash64ForRule(cleaned),
	}
}

// extractSimpleText 简单提取 HTML 文本（用于指纹）
func extractSimpleText(html []byte) string {
	// 移除 HTML 标签，保留文本
	text := string(html)
	// 简单替换：移除 <...> 标签
	for {
		start := strings.Index(text, "<")
		if start == -1 {
			break
		}
		end := strings.Index(text[start:], ">")
		if end == -1 {
			break
		}
		text = text[:start] + " " + text[start+end+1:]
	}
	return text
}

// cleanTextForFingerprint 清洗文本用于指纹计算
func cleanTextForFingerprint(text string) string {
	// 转小写，移除多余空白
	text = strings.ToLower(text)
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\t", " ")
	// 压缩空白
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}
	return strings.TrimSpace(text)
}

// hash64ForRule 计算字符串的 64 位哈希（用于规则聚类）
func hash64ForRule(s string) uint64 {
	h := md5.Sum([]byte(s))
	// 取前 8 字节作为 uint64
	var result uint64
	for i := 0; i < 8; i++ {
		result = result<<8 | uint64(h[i])
	}
	return result
}

// BuildRuleAssignments 构建规则聚类分配
// 按优先级顺序执行规则，优先级高的先执行，避免被低优先级规则覆盖
func BuildRuleAssignments(fetchResults []FetchResult) map[int]RuleAssignment {
	assignments := make(map[int]RuleAssignment)

	// 先收集 per-origin 和 per-finalURL 的信息
	originMap := make(map[string][]perURLInfo)
	finalURLMap := make(map[string][]perURLInfo)

	for _, fr := range fetchResults {
		origin := OriginKey(fr.FinalURL)
		if origin == "" {
			origin = OriginKey(fr.NormalizedURL)
		}

		// origin 为空无法归类，跳过
		if origin == "" {
			continue
		}

		info := perURLInfo{
			FR:     fr,
			Origin: origin,
			IsHTML: strings.Contains(strings.ToLower(fr.ContentType), "text/html"),
		}

		// 如果是 HTML，计算指纹
		if info.IsHTML && len(fr.RawHTML) > 0 {
			info.HtmlFP = FingerprintHTML(fr.RawHTML)
		}

		originMap[origin] = append(originMap[origin], info)
		finalURLMap[fr.FinalURL] = append(finalURLMap[fr.FinalURL], info)
	}

	// 按优先级顺序执行规则
	// E1: 同 origin + 5xx 错误
	applyRuleE1(originMap, assignments)

	// E3: 统一错误模板（404、401、403 等）
	applyRuleE3(originMap, assignments)

	// L1: 统一登录墙
	applyRuleL1(originMap, assignments)

	// W1: WAF 拦截页
	applyRuleW1(originMap, assignments)

	// M1: 维护/升级页
	applyRuleM1(originMap, assignments)

	// T1: 超短/空 HTML 页
	applyRuleT1(originMap, assignments)

	// R1: 重定向归并（同 FinalURL）
	applyRuleR1(finalURLMap, assignments)

	// U1: URL 小变体归一
	applyRuleU1(originMap, assignments)

	return assignments
}

// applyRuleE1 规则 E1：同 origin + 5xx 错误
// 同一个 origin 下所有 5xx 状态码的页面归为一类
func applyRuleE1(originMap map[string][]perURLInfo, assignments map[int]RuleAssignment) {
	for origin, urls := range originMap {
		var err5xx []perURLInfo
		for _, info := range urls {
			if info.FR.StatusCode >= 500 && info.FR.StatusCode < 600 {
				err5xx = append(err5xx, info)
			}
		}

		if len(err5xx) < 2 {
			continue
		}

		// 生成 cluster ID
		originSanitized := sanitizeForClusterID(origin)
		clusterID := fmt.Sprintf("err5xx-%s", originSanitized)

		// 选 canonical：path 最短，ID 最小
		canonicalID := selectCanonicalByPath(err5xx)

		// 分配
		for _, info := range err5xx {
			if _, exists := assignments[info.FR.ID]; !exists {
				assignments[info.FR.ID] = RuleAssignment{
					ClusterID:   clusterID,
					IsCanonical: info.FR.ID == canonicalID,
					Priority:    1, // E1 优先级
				}
			}
		}
	}
}

// applyRuleE3 规则 E3：统一错误模板
// 404、401、403 或 200 但包含错误关键词的页面
// 按 HTML 指纹分组，长度差异 < 20% 的才归为一类
func applyRuleE3(originMap map[string][]perURLInfo, assignments map[int]RuleAssignment) {
	for origin, urls := range originMap {
		// 找出 404、401、403 或 200 但包含错误关键词的页面
		var errorPages []perURLInfo
		for _, info := range urls {
			if info.FR.StatusCode == 404 || info.FR.StatusCode == 401 || info.FR.StatusCode == 403 {
				errorPages = append(errorPages, info)
			} else if info.FR.StatusCode == 200 && info.IsHTML && len(info.FR.RawHTML) > 0 {
				// 检查是否包含错误关键词
				htmlLower := strings.ToLower(string(info.FR.RawHTML))
				if containsErrorKeywords(htmlLower) {
					errorPages = append(errorPages, info)
				}
			}
		}

		if len(errorPages) < 2 {
			continue
		}

		// 按 HTML 指纹分组（包括非 HTML 的 404，用 0 作为指纹）
		fpGroups := make(map[uint64][]perURLInfo)
		for _, info := range errorPages {
			if info.IsHTML {
				fpGroups[info.HtmlFP.Hash] = append(fpGroups[info.HtmlFP.Hash], info)
			} else {
				// 非 HTML 的 404 页面，使用 0 作为指纹
				fpGroups[0] = append(fpGroups[0], info)
			}
		}

		originSanitized := sanitizeForClusterID(origin)
		for fpHash, group := range fpGroups {
			if len(group) < 2 {
				continue
			}

			// 检查长度是否接近（差异 < 20%）
			if !isLengthSimilar(group) {
				continue
			}

			clusterID := fmt.Sprintf("errtpl-%s-%x", originSanitized, fpHash&0xFFFF)
			canonicalID := selectCanonicalByPath(group)

			for _, info := range group {
				if _, exists := assignments[info.FR.ID]; !exists {
					assignments[info.FR.ID] = RuleAssignment{
						ClusterID:   clusterID,
						IsCanonical: info.FR.ID == canonicalID,
						Priority:    3, // E3 优先级
					}
				}
			}
		}
	}
}

// applyRuleL1 规则 L1：统一登录墙
// 包含登录关键词的页面按 HTML 指纹分组
func applyRuleL1(originMap map[string][]perURLInfo, assignments map[int]RuleAssignment) {
	for origin, urls := range originMap {
		var loginPages []perURLInfo
		for _, info := range urls {
			if info.IsHTML && len(info.FR.RawHTML) > 0 {
				htmlLower := strings.ToLower(string(info.FR.RawHTML))
				if containsLoginKeywords(htmlLower) {
					loginPages = append(loginPages, info)
				}
			}
		}

		if len(loginPages) < 2 {
			continue
		}

		// 按指纹分组
		fpGroups := make(map[uint64][]perURLInfo)
		for _, info := range loginPages {
			fpGroups[info.HtmlFP.Hash] = append(fpGroups[info.HtmlFP.Hash], info)
		}

		originSanitized := sanitizeForClusterID(origin)
		for fpHash, group := range fpGroups {
			if len(group) < 2 {
				continue
			}

			if !isLengthSimilar(group) {
				continue
			}

			clusterID := fmt.Sprintf("loginwall-%s-%x", originSanitized, fpHash&0xFFFF)
			canonicalID := selectCanonicalByPath(group)

			for _, info := range group {
				if _, exists := assignments[info.FR.ID]; !exists {
					assignments[info.FR.ID] = RuleAssignment{
						ClusterID:   clusterID,
						IsCanonical: info.FR.ID == canonicalID,
						Priority:    4, // L1 优先级
					}
				}
			}
		}
	}
}

// applyRuleW1 规则 W1：WAF 拦截页
// 包含 WAF 关键词的页面按 HTML 指纹分组
func applyRuleW1(originMap map[string][]perURLInfo, assignments map[int]RuleAssignment) {
	for origin, urls := range originMap {
		var wafPages []perURLInfo
		for _, info := range urls {
			if info.IsHTML && len(info.FR.RawHTML) > 0 {
				htmlLower := strings.ToLower(string(info.FR.RawHTML))
				if containsWAFKeywords(htmlLower) {
					wafPages = append(wafPages, info)
				}
			}
		}

		if len(wafPages) < 2 {
			continue
		}

		fpGroups := make(map[uint64][]perURLInfo)
		for _, info := range wafPages {
			fpGroups[info.HtmlFP.Hash] = append(fpGroups[info.HtmlFP.Hash], info)
		}

		originSanitized := sanitizeForClusterID(origin)
		for fpHash, group := range fpGroups {
			if len(group) < 2 {
				continue
			}

			if !isLengthSimilar(group) {
				continue
			}

			clusterID := fmt.Sprintf("waf-%s-%x", originSanitized, fpHash&0xFFFF)
			canonicalID := selectCanonicalByPath(group)

			for _, info := range group {
				if _, exists := assignments[info.FR.ID]; !exists {
					assignments[info.FR.ID] = RuleAssignment{
						ClusterID:   clusterID,
						IsCanonical: info.FR.ID == canonicalID,
						Priority:    5, // W1 优先级
					}
				}
			}
		}
	}
}

// applyRuleM1 规则 M1：维护/升级页
// 包含维护关键词的页面按 HTML 指纹分组
func applyRuleM1(originMap map[string][]perURLInfo, assignments map[int]RuleAssignment) {
	for origin, urls := range originMap {
		var maintPages []perURLInfo
		for _, info := range urls {
			if info.IsHTML && len(info.FR.RawHTML) > 0 {
				htmlLower := strings.ToLower(string(info.FR.RawHTML))
				if containsMaintenanceKeywords(htmlLower) {
					maintPages = append(maintPages, info)
				}
			}
		}

		if len(maintPages) < 2 {
			continue
		}

		fpGroups := make(map[uint64][]perURLInfo)
		for _, info := range maintPages {
			fpGroups[info.HtmlFP.Hash] = append(fpGroups[info.HtmlFP.Hash], info)
		}

		originSanitized := sanitizeForClusterID(origin)
		for fpHash, group := range fpGroups {
			if len(group) < 2 {
				continue
			}

			if !isLengthSimilar(group) {
				continue
			}

			clusterID := fmt.Sprintf("maint-%s-%x", originSanitized, fpHash&0xFFFF)
			canonicalID := selectCanonicalByPath(group)

			for _, info := range group {
				if _, exists := assignments[info.FR.ID]; !exists {
					assignments[info.FR.ID] = RuleAssignment{
						ClusterID:   clusterID,
						IsCanonical: info.FR.ID == canonicalID,
						Priority:    6, // M1 优先级
					}
				}
			}
		}
	}
}

// applyRuleT1 规则 T1：超短/空 HTML 页
// HTML 大小 < 1KB 或文本长度 < 200 字符的页面
// 包括 2xx、401、403 状态码
func applyRuleT1(originMap map[string][]perURLInfo, assignments map[int]RuleAssignment) {
	for origin, urls := range originMap {
		var thinPages []perURLInfo
		for _, info := range urls {
			// 包括 2xx、401、403 状态码的 HTML 页面
			if ((info.FR.StatusCode >= 200 && info.FR.StatusCode < 300) ||
				info.FR.StatusCode == 401 || info.FR.StatusCode == 403) &&
				info.IsHTML {
				// 超短：小于 1KB 或（已计算指纹且）文本长度小于阈值
				if len(info.FR.RawHTML) < 1024 {
					thinPages = append(thinPages, info)
				} else if info.HtmlFP.Length > 0 && info.HtmlFP.Length < MinTextLength {
					// 只有当指纹已计算且长度小于阈值时才加入
					thinPages = append(thinPages, info)
				}
			}
		}

		if len(thinPages) < 2 {
			continue
		}

		// 按指纹分组
		fpGroups := make(map[uint64][]perURLInfo)
		for _, info := range thinPages {
			fpGroups[info.HtmlFP.Hash] = append(fpGroups[info.HtmlFP.Hash], info)
		}

		originSanitized := sanitizeForClusterID(origin)
		for fpHash, group := range fpGroups {
			if len(group) < 2 {
				continue
			}

			if !isLengthSimilar(group) {
				continue
			}

			clusterID := fmt.Sprintf("thin-%s-%x", originSanitized, fpHash&0xFFFF)
			canonicalID := selectCanonicalByPath(group)

			for _, info := range group {
				if _, exists := assignments[info.FR.ID]; !exists {
					assignments[info.FR.ID] = RuleAssignment{
						ClusterID:   clusterID,
						IsCanonical: info.FR.ID == canonicalID,
						Priority:    7, // T1 优先级
					}
				}
			}
		}
	}
}

// applyRuleR1 规则 R1：重定向归并
// 最终 URL 相同的页面归为一类（不同 URL 重定向到同一个页面）
func applyRuleR1(finalURLMap map[string][]perURLInfo, assignments map[int]RuleAssignment) {
	for finalURL, urls := range finalURLMap {
		if len(urls) < 2 {
			continue
		}

		// 只处理还没有被规则分配的 URL
		var unassigned []perURLInfo
		for _, info := range urls {
			if _, exists := assignments[info.FR.ID]; !exists {
				unassigned = append(unassigned, info)
			}
		}

		if len(unassigned) < 2 {
			continue
		}

		// 生成 cluster ID
		hash := md5.Sum([]byte(finalURL))
		clusterID := fmt.Sprintf("redir-%x", hash[:8])

		// 选 canonical：StatusCode 2xx 优先，其次 path 最短，再其次 ID 最小
		canonicalID := selectCanonicalForRedirect(unassigned)

		for _, info := range unassigned {
			assignments[info.FR.ID] = RuleAssignment{
				ClusterID:   clusterID,
				IsCanonical: info.FR.ID == canonicalID,
				Priority:    8, // R1 优先级（较低）
			}
		}
	}
}

// applyRuleU1 规则 U1：URL 小变体归一
// 规范化 path 后相同的 URL 归为一类（比如 /index.html 和 /）
func applyRuleU1(originMap map[string][]perURLInfo, assignments map[int]RuleAssignment) {
	for origin, urls := range originMap {
		// 按规范化 path 分组
		pathGroups := make(map[string][]perURLInfo)
		for _, info := range urls {
			normalizedPath := normalizePath(info.FR.FinalURL)
			if normalizedPath == "" {
				normalizedPath = normalizePath(info.FR.NormalizedURL)
			}
			pathGroups[normalizedPath] = append(pathGroups[normalizedPath], info)
		}

		originSanitized := sanitizeForClusterID(origin)
		for path, group := range pathGroups {
			if len(group) < 2 {
				continue
			}

			// 只处理还没有被规则分配的 URL
			var unassigned []perURLInfo
			for _, info := range group {
				if _, exists := assignments[info.FR.ID]; !exists {
					unassigned = append(unassigned, info)
				}
			}

			if len(unassigned) < 2 {
				continue
			}

			pathSanitized := sanitizeForClusterID(path)
			clusterID := fmt.Sprintf("urlcanon-%s-%s", originSanitized, pathSanitized)
			canonicalID := selectCanonicalByPath(unassigned)

			for _, info := range unassigned {
				assignments[info.FR.ID] = RuleAssignment{
					ClusterID:   clusterID,
					IsCanonical: info.FR.ID == canonicalID,
					Priority:    9, // U1 优先级（最低）
				}
			}
		}
	}
}

// 辅助函数

// sanitizeForClusterID 清理字符串用于 cluster ID
func sanitizeForClusterID(s string) string {
	// 移除特殊字符，只保留字母数字和连字符
	var result strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			result.WriteRune(r)
		} else if r == ':' || r == '/' {
			result.WriteRune('_')
		}
	}
	return result.String()
}

// selectCanonicalByPath 根据 path 长度和 ID 选择 canonical
func selectCanonicalByPath(urls []perURLInfo) int {
	if len(urls) == 0 {
		return 0
	}

	sort.Slice(urls, func(i, j int) bool {
		pathI := getPath(urls[i].FR.FinalURL)
		pathJ := getPath(urls[j].FR.FinalURL)

		// path 长度短的优先
		if len(pathI) != len(pathJ) {
			return len(pathI) < len(pathJ)
		}

		// 相同长度，ID 小的优先
		return urls[i].FR.ID < urls[j].FR.ID
	})

	return urls[0].FR.ID
}

// selectCanonicalForRedirect 为重定向选择 canonical
func selectCanonicalForRedirect(urls []perURLInfo) int {
	if len(urls) == 0 {
		return 0
	}

	sort.Slice(urls, func(i, j int) bool {
		// StatusCode 2xx 优先
		is2xxI := urls[i].FR.StatusCode >= 200 && urls[i].FR.StatusCode < 300
		is2xxJ := urls[j].FR.StatusCode >= 200 && urls[j].FR.StatusCode < 300

		if is2xxI != is2xxJ {
			return is2xxI
		}

		// 其次 path 最短
		pathI := getPath(urls[i].FR.FinalURL)
		pathJ := getPath(urls[j].FR.FinalURL)
		if len(pathI) != len(pathJ) {
			return len(pathI) < len(pathJ)
		}

		// 最后 ID 最小
		return urls[i].FR.ID < urls[j].FR.ID
	})

	return urls[0].FR.ID
}

// getPath 从 URL 提取 path
func getPath(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return u
	}
	return parsed.Path
}

// normalizePath 规范化 path（用于 URL 小变体归一）
func normalizePath(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}

	path := parsed.Path

	// 移除 index.html/index.htm/index.php 等
	path = strings.TrimSuffix(path, "/index.html")
	path = strings.TrimSuffix(path, "/index.htm")
	path = strings.TrimSuffix(path, "/index.php")
	path = strings.TrimSuffix(path, "/index")

	// 规范化：空路径或根路径统一为 "/"
	if path == "" {
		path = "/"
	} else if path != "/" && !strings.HasSuffix(path, "/") {
		// 非根路径且不以 / 结尾，添加 /
		path = path + "/"
	}

	return path
}

// isLengthSimilar 检查长度是否相似（差异 < 20%）
func isLengthSimilar(group []perURLInfo) bool {
	if len(group) < 2 {
		return false
	}

	lengths := make([]int, 0, len(group))
	for _, info := range group {
		// 只考虑已计算指纹的页面（Length > 0）
		if info.HtmlFP.Length > 0 {
			lengths = append(lengths, info.HtmlFP.Length)
		}
	}

	// 如果所有页面都没有指纹（Length == 0），认为相似
	if len(lengths) == 0 {
		return true
	}

	// 如果只有部分页面有指纹，需要至少 2 个有指纹的页面
	if len(lengths) < 2 {
		return false
	}

	sort.Ints(lengths)
	min, max := lengths[0], lengths[len(lengths)-1]

	if max == 0 {
		return true
	}

	ratio := float64(min) / float64(max)
	return ratio >= 0.8 // 差异 < 20%
}

// 关键词检测函数

func containsErrorKeywords(html string) bool {
	keywords := []string{
		"404", "page not found", "页面不存在", "not found",
		"error", "错误", "无法找到", "找不到",
	}
	return containsAny(html, keywords)
}

func containsLoginKeywords(html string) bool {
	keywords := []string{
		"登录", "登陆", "login", "sign in", "signin",
		"password", "密码", "username", "用户名",
		"type=\"password\"", "type='password'",
	}
	return containsAny(html, keywords)
}

func containsWAFKeywords(html string) bool {
	keywords := []string{
		"access denied", "防火墙", "安全验证", "滑动验证",
		"checking your browser", "cloudflare", "waf",
		"security check", "安全检查", "验证码",
	}
	return containsAny(html, keywords)
}

func containsMaintenanceKeywords(html string) bool {
	keywords := []string{
		"维护中", "升级中", "maintenance", "under maintenance",
		"service unavailable", "系统维护", "网站维护",
		"upgrading", "升级", "维护",
	}
	return containsAny(html, keywords)
}

func containsAny(text string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}
