package internal

import (
	"context"
	"fmt"
	"sync"
)

// Run 主运行函数，支持批处理避免内存溢出
func Run(ctx context.Context, opts Options) (*FullReport, error) {
	logger := GetLogger()
	logger.Info("开始处理，共 %d 个 URL 输入源", len(opts.URLs))

	var allItems []URLItem
	for _, urlInput := range opts.URLs {
		items, err := LoadURLs(urlInput)
		if err != nil {
			return nil, fmt.Errorf("加载 URL 失败 (%s): %w", urlInput, err)
		}
		baseID := len(allItems)
		for i := range items {
			items[i].ID = baseID + i + 1
		}
		allItems = append(allItems, items...)
	}
	items := allItems
	if len(items) == 0 {
		return nil, fmt.Errorf("没有有效的 URL 输入")
	}
	logger.Info("加载完成，共 %d 个 URL", len(items))

	fetcher := NewFetcher(opts.HTTPTimeout, MaxRedirects)
	renderer, err := NewRenderer(ctx, opts.PerPageTimeout, opts.RenderParallel)
	if err != nil {
		return nil, fmt.Errorf("创建渲染器失败: %w", err)
	}
	defer renderer.Close()

	fetchResults := make([]FetchResult, 0, len(items))
	pagesWithFeatures := make([]*PageWithFeatures, 0)

	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = len(items)
	}

	totalBatches := (len(items) + batchSize - 1) / batchSize
	logger.Info("将分 %d 批处理，每批 %d 个 URL", totalBatches, batchSize)

	for batchIdx := 0; batchIdx < totalBatches; batchIdx++ {
		start := batchIdx * batchSize
		end := start + batchSize
		if end > len(items) {
			end = len(items)
		}
		batchItems := items[start:end]

		logger.Info("处理第 %d/%d 批（URL %d-%d）", batchIdx+1, totalBatches, start+1, end)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		logger.Progress(start, len(items), "HTTP 抓取")
		batchFetchResults := fetcher.FetchBatch(ctx, batchItems, opts.Parallel)
		logger.Info("HTTP 抓取完成，本批 %d 个结果", len(batchFetchResults))

		// 分类：HTML 需要渲染，非 HTML 直接提取特征
		var batchEligibleHTML []FetchResult
		var batchEligibleNonHTML []FetchResult
		for _, fr := range batchFetchResults {
			if isEligibleHTML(fr) {
				batchEligibleHTML = append(batchEligibleHTML, fr)
			} else if isEligibleNonHTML(fr) {
				batchEligibleNonHTML = append(batchEligibleNonHTML, fr)
			}
		}
		logger.Info("可判定页面：HTML %d 个，非 HTML %d 个（共 %d 个）",
			len(batchEligibleHTML), len(batchEligibleNonHTML), len(batchFetchResults))

		var mu sync.Mutex

		// 先处理非 HTML（简单快速）
		for _, fr := range batchEligibleNonHTML {
			features := ExtractNonHTMLFeatures(fr.ContentCategory, fr.RawBody)
			if features == nil {
				continue
			}

			// 根据内容类型使用不同的最小阈值
			eligible := false
			switch fr.ContentCategory {
			case ContentCategoryText:
				// 文本类：使用字符数阈值
				eligible = features.TextLength >= MinTextLength
			case ContentCategoryImage:
				// 图片：只要解析成功（有 pHash）就算有效
				eligible = features.PHash != 0
			case ContentCategoryBinary:
				// 二进制：只要有内容就算有效
				eligible = features.TextLength > 0
			}

			if eligible {
				mu.Lock()
				pagesWithFeatures = append(pagesWithFeatures, &PageWithFeatures{
					FetchResult: fr,
					Features:    features,
				})
				mu.Unlock()
			}
		}

		var wg sync.WaitGroup
		cancelled := false
		titleUpdates := make(map[int]string)
		var titleMu sync.Mutex

		// 处理 HTML 页面（需要渲染）
		for idx, fr := range batchEligibleHTML {
			select {
			case <-ctx.Done():
				logger.Warn("渲染被取消，跳过剩余任务")
				cancelled = true
			default:
			}

			if cancelled {
				break
			}

			wg.Add(1)
			go func(fr FetchResult, pos int) {
				defer wg.Done()

				defer func() {
					if r := recover(); r != nil {
						logger.Error("渲染 panic (URL %d, %s): %v", fr.ID, fr.FinalURL, r)
					}
				}()

				logger.Progress(start+pos+1, len(items), "渲染中")

				features, renderedTitle, err := renderer.ExtractFeatures(ctx, fr.FinalURL)
				if err != nil {
					logger.Debug("渲染失败 (URL %d, %s): %v", fr.ID, fr.FinalURL, err)
					features = nil
				}

				if renderedTitle != "" {
					titleMu.Lock()
					titleUpdates[fr.ID] = renderedTitle
					titleMu.Unlock()
					fr.Title = renderedTitle
				}

				if features != nil && features.TextLength < MinTextLength {
					features = nil
				}

				mu.Lock()
				pagesWithFeatures = append(pagesWithFeatures, &PageWithFeatures{
					FetchResult: fr,
					Features:    features,
				})
				mu.Unlock()
			}(fr, idx)
		}

		wg.Wait()

		titleMu.Lock()
		for i := range batchFetchResults {
			if updatedTitle, ok := titleUpdates[batchFetchResults[i].ID]; ok {
				batchFetchResults[i].Title = updatedTitle
			}
		}
		titleMu.Unlock()

		if cancelled {
			logger.Info("本批渲染被取消，继续处理下一批")
		}

		// 清理原始内容，释放内存
		for i := range batchFetchResults {
			batchFetchResults[i].RawHTML = nil
			batchFetchResults[i].RawBody = nil
		}
		fetchResults = append(fetchResults, batchFetchResults...)

		logger.Info("第 %d/%d 批处理完成", batchIdx+1, totalBatches)
	}

	logger.Info("所有批次处理完成")

	logger.Info("开始全局聚类...")
	contentClusters := Cluster(pagesWithFeatures)
	logger.Info("内容聚类完成，生成 %d 个 cluster", len(contentClusters))

	logger.Info("开始规则聚类...")
	ruleAssignments := BuildRuleAssignments(fetchResults)
	logger.Info("规则聚类完成，分配 %d 个 URL", len(ruleAssignments))

	logger.Info("构建报告...")
	report := BuildReport(fetchResults, pagesWithFeatures, contentClusters, ruleAssignments, opts)

	logger.Info("完成！共处理 %d 个 URL，其中 %d 个可判定的 HTML 页面，生成 %d 个聚类",
		report.Meta.TotalURLs,
		report.Meta.EligibleHTMLURLs,
		report.Meta.TotalClusters,
	)

	return report, nil
}

// isEligibleHTML 判断是否为可判定的 HTML 页面
func isEligibleHTML(result FetchResult) bool {
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return false
	}

	if result.ContentCategory != ContentCategoryHTML {
		return false
	}

	if len(result.RawHTML) < MinHTMLSize {
		return false
	}

	return true
}

// isEligibleNonHTML 判断是否为可判定的非 HTML 内容
func isEligibleNonHTML(result FetchResult) bool {
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		return false
	}

	switch result.ContentCategory {
	case ContentCategoryText:
		return len(result.RawBody) >= MinTextSize
	case ContentCategoryImage:
		return len(result.RawBody) >= MinImageSize
	case ContentCategoryBinary:
		return len(result.RawBody) >= MinBinarySize
	default:
		return false
	}
}
