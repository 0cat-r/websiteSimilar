package internal

import (
	"time"
)

// Options 配置选项
type Options struct {
	URLs           []string
	Parallel       int // HTTP 抓取并发数
	RenderParallel int // headless 渲染并发数
	HTTPTimeout    time.Duration
	PerPageTimeout time.Duration
	BatchSize      int
	SimThreshold   float64
	OutputFormat   string // "json" or "csv"
}

// URLItem URL 项
type URLItem struct {
	ID            int
	RawURL        string
	NormalizedURL string
}

// FetchResult HTTP 抓取结果
type FetchResult struct {
	URLItem
	FinalURL      string   // 跟随重定向后的最终 URL
	RedirectChain []string // 顺序记录每一个 hop
	StatusCode    int
	ContentLength int64
	ContentType   string
	Error         string
	RawHTML       []byte // 最终响应的 HTML（仅 text/html）
	Title         string // 页面标题（从 HTML 中提取）
}

// PageFeatures 页面特征
type PageFeatures struct {
	// 文本特征
	TextSimHash uint64
	TextLength  int

	// DOM 结构特征
	DOMNodeCount  int
	TextNodeCount int
	TagCount      map[string]int
	DepthHist     []int
	PathCount     map[string]int

	// 视觉特征
	ScreenshotW int
	ScreenshotH int
	PHash       uint64 // 感知哈希值（pHash）

	// 行为特征
	TTFB             float64 // Time To First Byte (ms)
	DOMContentLoaded float64 // DOMContentLoaded 时间 (ms)
	LoadEvent        float64 // Load 事件时间 (ms)
}

// PageWithFeatures 带特征的页面
type PageWithFeatures struct {
	FetchResult
	Features *PageFeatures
}

// URLReport URL 报告
type URLReport struct {
	ID                    int      `json:"id"`
	URL                   string   `json:"url"`
	NormalizedURL         string   `json:"normalized_url"`
	FinalURL              string   `json:"final_url"`
	RedirectChain         []string `json:"redirect_chain"`
	StatusCode            int      `json:"status_code"`
	ContentLength         int64    `json:"content_length"`
	ContentType           string   `json:"content_type"`
	Error                 string   `json:"error"`
	Title                 string   `json:"title"`
	ClusterID             string   `json:"cluster_id"`
	IsCanonical           bool     `json:"is_canonical"`
	SimilarityToCanonical float64  `json:"similarity_to_canonical"`
	ContentSim            float64  `json:"content_sim"`
	StructureSim          float64  `json:"structure_sim"`
	VisualSim             float64  `json:"visual_sim"`
	BehaviorSim           float64  `json:"behavior_sim"`
}

// ClusterInfo 聚类信息
type ClusterInfo struct {
	ClusterID    string `json:"cluster_id"`
	CanonicalURL string `json:"canonical_url"`
	MemberIDs    []int  `json:"member_ids"`
}

// MetaInfo 元信息
type MetaInfo struct {
	TotalURLs        int     `json:"total_urls"`
	EligibleHTMLURLs int     `json:"eligible_html_urls"`
	TotalClusters    int     `json:"total_clusters"`
	SimThreshold     float64 `json:"sim_threshold"`
	GeneratedAt      string  `json:"generated_at"`
}

// FullReport 完整报告
type FullReport struct {
	URLs     []URLReport   `json:"urls"`
	Clusters []ClusterInfo `json:"clusters"`
	Meta     MetaInfo      `json:"meta"`
}

// DOMStats DOM 统计信息（从 JS 返回）
type DOMStats struct {
	DOMNodeCount  int            `json:"domNodeCount"`
	TextNodeCount int            `json:"textNodeCount"`
	TagCount      map[string]int `json:"tagCount"`
	DepthHist     []int          `json:"depthHist"`
	PathCount     map[string]int `json:"pathCount"`
}

// PerfTiming 性能时间信息（从 JS 返回）
type PerfTiming struct {
	NavigationStart          int64 `json:"navigationStart"`
	ResponseStart            int64 `json:"responseStart"`
	DomContentLoadedEventEnd int64 `json:"domContentLoadedEventEnd"`
	LoadEventEnd             int64 `json:"loadEventEnd"`
}

// 常量定义
const (
	MinTextLength = 200              // 最小文本长度（字符数）
	MaxRedirects  = 5                // 最大重定向次数
	MaxHTMLSize   = 10 * 1024 * 1024 // 最大 HTML 大小（10MB）
)
