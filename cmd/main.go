package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/0cat/websiteSimilar/internal"
)

// detectFormat 从文件路径检测输出格式
// 根据扩展名判断是 json 还是 csv
func detectFormat(filepath string) string {
	filepath = strings.ToLower(filepath)
	if strings.HasSuffix(filepath, ".json") {
		return "json"
	}
	if strings.HasSuffix(filepath, ".csv") {
		return "csv"
	}
	return ""
}

func main() {
	var (
		urlList      = flag.String("l", "", "URL 列表：文件路径（.txt）或逗号分隔的 URL 字符串（必选）")
		output       = flag.String("o", "", "输出文件路径（必选，支持 .json 或 .csv 扩展名）")
		threads      = flag.Int("t", 20, "并发数：同时处理的 URL 数（包含抓取和渲染）")
		httpTimeout  = flag.Duration("http-timeout", 10*time.Second, "HTTP 请求超时")
		pageTimeout  = flag.Duration("page-timeout", 20*time.Second, "单个页面 headless 渲染超时")
		batchSize    = flag.Int("batch-size", 1000, "批处理大小")
		simThreshold = flag.Float64("sim-threshold", 0.85, "相似度阈值（仅用于 meta，实际判定使用严格规则）")
	)

	flag.Parse()

	// 验证必选参数
	if *urlList == "" {
		fmt.Fprintf(os.Stderr, "错误: -l 参数是必选的\n")
		flag.Usage()
		os.Exit(1)
	}

	if *output == "" {
		fmt.Fprintf(os.Stderr, "错误: -o 参数是必选的\n")
		flag.Usage()
		os.Exit(1)
	}

	// 从文件扩展名自动判断格式
	format := detectFormat(*output)
	if format == "" {
		fmt.Fprintf(os.Stderr, "错误: 输出文件必须是 .json 或 .csv 格式\n")
		os.Exit(1)
	}

	// 避免用户传 0 或负数
	concurrency := *threads
	if concurrency <= 0 {
		concurrency = 1
	}

	// 构建选项
	opts := internal.Options{
		URLs:           []string{*urlList},
		Parallel:       concurrency,    // HTTP 抓取并发
		RenderParallel: concurrency,    // 渲染并发
		HTTPTimeout:    *httpTimeout,
		PerPageTimeout: *pageTimeout,
		BatchSize:      *batchSize,
		SimThreshold:   *simThreshold,
		OutputFormat:   format,
	}

	// 运行
	ctx := context.Background()
	report, err := internal.Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}

	// 输出报告
	if err := writeReport(report, *output, format); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 写入输出文件失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("完成！共处理 %d 个 URL，其中 %d 个可判定的 HTML 页面，生成 %d 个聚类\n",
		report.Meta.TotalURLs,
		report.Meta.EligibleHTMLURLs,
		report.Meta.TotalClusters,
	)
}

// writeReport 写入报告
// 根据格式选择 json 或 csv
func writeReport(report *internal.FullReport, filepath, format string) error {
	if format == "json" {
		return internal.WriteJSON(report, filepath)
	} else if format == "csv" {
		return internal.WriteCSV(report, filepath)
	}
	return fmt.Errorf("不支持的格式: %s", format)
}

