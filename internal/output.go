package internal

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// BuildReport 构建完整报告
func BuildReport(
	fetchResults []FetchResult,
	pagesWithFeatures []*PageWithFeatures,
	contentClusters map[string]*ClusterGroup,
	ruleAssignments map[int]RuleAssignment,
	opts Options,
) *FullReport {
	report := &FullReport{
		URLs:     make([]URLReport, 0, len(fetchResults)),
		Clusters: make([]ClusterInfo, 0, len(contentClusters)),
		Meta: MetaInfo{
			TotalURLs:        len(fetchResults),
			EligibleHTMLURLs:  0,
			TotalClusters:    len(contentClusters),
			SimThreshold:     opts.SimThreshold,
			GeneratedAt:      time.Now().Format(time.RFC3339),
		},
	}

	// 创建页面索引（按 ID）
	pageMap := make(map[int]*PageWithFeatures)
	for _, page := range pagesWithFeatures {
		pageMap[page.ID] = page
	}

	// 创建内容 cluster 索引（按页面 ID）
	clusterByPageID := make(map[int]string)
	canonicalByCluster := make(map[string]int)
	for clusterID, cluster := range contentClusters {
		memberIDs := make([]int, len(cluster.Members))
		for i, member := range cluster.Members {
			memberIDs[i] = member.ID
			clusterByPageID[member.ID] = clusterID
		}
		if cluster.Canonical != nil {
			canonicalByCluster[clusterID] = cluster.Canonical.ID
		}

		canonicalURL := ""
		if cluster.Canonical != nil {
			canonicalURL = cluster.Canonical.FinalURL
		}

		report.Clusters = append(report.Clusters, ClusterInfo{
			ClusterID:    clusterID,
			CanonicalURL: canonicalURL,
			MemberIDs:    memberIDs,
		})
	}

	// 统计 eligible HTML URLs
	eligibleCount := 0

	// 构建 URL 报告
	for _, fetchResult := range fetchResults {
		urlReport := URLReport{
			ID:            fetchResult.ID,
			URL:           fetchResult.RawURL,
			NormalizedURL: fetchResult.NormalizedURL,
			FinalURL:      fetchResult.FinalURL,
			RedirectChain: fetchResult.RedirectChain,
			StatusCode:    fetchResult.StatusCode,
			ContentLength: fetchResult.ContentLength,
			ContentType:   fetchResult.ContentType,
			Error:         fetchResult.Error,
			Title:         fetchResult.Title,
		}

		assigned := false

		// 1) 内容聚类优先（有 Features + 在 content cluster 里）
		page, hasFeatures := pageMap[fetchResult.ID]
		if hasFeatures && page.Features != nil {
			eligibleCount++

			// 设置聚类信息
			clusterID, inCluster := clusterByPageID[fetchResult.ID]
			if inCluster {
				urlReport.ClusterID = clusterID
				canonicalID := canonicalByCluster[clusterID]
				urlReport.IsCanonical = (fetchResult.ID == canonicalID)

				// 计算与 canonical 的相似度
				if cluster := contentClusters[clusterID]; cluster != nil && cluster.Canonical != nil && cluster.Canonical.Features != nil {
					contentSim, structSim, visualSim, behaviorSim, totalSim := CalculateSimilarities(
						page.Features,
						cluster.Canonical.Features,
					)
					urlReport.ContentSim = contentSim
					urlReport.StructureSim = structSim
					urlReport.VisualSim = visualSim
					urlReport.BehaviorSim = behaviorSim
					urlReport.SimilarityToCanonical = totalSim
				}
				assigned = true
			}
		}

		// 2) 如果没有内容聚类，则看规则聚类
		if !assigned {
			if ra, ok := ruleAssignments[fetchResult.ID]; ok {
				urlReport.ClusterID = ra.ClusterID
				urlReport.IsCanonical = ra.IsCanonical
				// 相似度相关字段保持 0（这些是错误页/无内容页）
				assigned = true
			} else {
				// 3) 没任何聚类：单独一个
				urlReport.ClusterID = ""
				urlReport.IsCanonical = true
			}
		}

		report.URLs = append(report.URLs, urlReport)
	}

	report.Meta.EligibleHTMLURLs = eligibleCount

	return report
}

// WriteJSON 写入 JSON 文件
func WriteJSON(report *FullReport, filepath string) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 JSON 失败: %w", err)
	}

	if err := os.WriteFile(filepath, data, 0644); err != nil {
		return fmt.Errorf("写入文件失败: %w", err)
	}

	return nil
}

// WriteCSV 写入 CSV 文件
func WriteCSV(report *FullReport, filepath string) error {
	file, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// 写入表头
	headers := []string{
		"id", "url", "normalized_url", "final_url",
		"status_code", "content_length", "content_type", "error", "title",
		"cluster_id", "is_canonical", "similarity_to_canonical",
		"content_sim", "structure_sim", "visual_sim", "behavior_sim",
	}
	if err := writer.Write(headers); err != nil {
		return fmt.Errorf("写入表头失败: %w", err)
	}

	// 写入数据行
	for _, urlReport := range report.URLs {
		row := []string{
			fmt.Sprintf("%d", urlReport.ID),
			urlReport.URL,
			urlReport.NormalizedURL,
			urlReport.FinalURL,
			fmt.Sprintf("%d", urlReport.StatusCode),
			fmt.Sprintf("%d", urlReport.ContentLength),
			urlReport.ContentType,
			urlReport.Error,
			urlReport.Title,
			urlReport.ClusterID,
			fmt.Sprintf("%t", urlReport.IsCanonical),
			fmt.Sprintf("%.4f", urlReport.SimilarityToCanonical),
			fmt.Sprintf("%.4f", urlReport.ContentSim),
			fmt.Sprintf("%.4f", urlReport.StructureSim),
			fmt.Sprintf("%.4f", urlReport.VisualSim),
			fmt.Sprintf("%.4f", urlReport.BehaviorSim),
		}
		if err := writer.Write(row); err != nil {
			return fmt.Errorf("写入数据行失败: %w", err)
		}
	}

	return nil
}

