package internal

import (
	"math"
)

// 相似度判定阈值常量
const (
	ContentSimThreshold    = 0.97  // 文本相似度阈值，规则1用
	StructureSimThreshold  = 0.85  // 结构相似度阈值，规则1用
	VisualSimThreshold     = 0.85  // 视觉相似度阈值，规则1用
	VisualHighSimThreshold = 0.99  // 视觉极高相似度阈值，规则2兜底用
	QuickSimHashMaxDist    = 8     // SimHash 预筛选最大汉明距离，超过这个值直接跳过（8 bit 约等于 87.5% 一致）
)

// simContent 计算文本相似度
func simContent(a, b *PageFeatures) float64 {
	if a.TextLength == 0 || b.TextLength == 0 {
		return 0
	}

	ratio := float64(min(a.TextLength, b.TextLength)) / float64(max(a.TextLength, b.TextLength))
	if ratio < 0.3 {
		return 0
	}

	d := hammingDistance64(a.TextSimHash, b.TextSimHash)
	if d >= 16 {
		return 0
	}

	return 1 - float64(d)/16.0
}

// simDOMStats 计算 DOM 统计相似度
func simDOMStats(a, b *PageFeatures) float64 {
	keyTags := []string{"div", "a", "img", "input", "script"}
	vecA := make([]float64, 2+len(keyTags))
	vecB := make([]float64, 2+len(keyTags))

	vecA[0], vecB[0] = float64(a.DOMNodeCount), float64(b.DOMNodeCount)
	vecA[1], vecB[1] = float64(a.TextNodeCount), float64(b.TextNodeCount)

	for i, tag := range keyTags {
		vecA[2+i] = float64(a.TagCount[tag])
		vecB[2+i] = float64(b.TagCount[tag])
	}

	return cosineSimilarity(vecA, vecB)
}

// simPath 计算路径相似度
func simPath(a, b *PageFeatures) float64 {
	if len(a.PathCount) == 0 || len(b.PathCount) == 0 {
		return 0
	}

	seen := make(map[string]struct{})
	for k := range a.PathCount {
		seen[k] = struct{}{}
	}
	for k := range b.PathCount {
		seen[k] = struct{}{}
	}

	var inter, uni float64
	for k := range seen {
		va := float64(a.PathCount[k])
		vb := float64(b.PathCount[k])
		inter += math.Min(va, vb)
		uni += math.Max(va, vb)
	}

	if uni == 0 {
		return 0
	}

	return inter / uni
}

// simStructure 计算结构相似度
func simStructure(a, b *PageFeatures) float64 {
	return 0.5*simDOMStats(a, b) + 0.5*simPath(a, b)
}

// simVisual 计算视觉相似度
func simVisual(a, b *PageFeatures) float64 {
	if a.PHash == 0 || b.PHash == 0 {
		return 0
	}

	d := hammingDistance64(a.PHash, b.PHash)
	if d >= 20 {
		return 0
	}

	return 1 - float64(d)/20.0
}

// simBehavior 计算行为相似度
func simBehavior(a, b *PageFeatures) float64 {
	va := []float64{a.TTFB, a.DOMContentLoaded, a.LoadEvent}
	vb := []float64{b.TTFB, b.DOMContentLoaded, b.LoadEvent}
	return cosineSimilarity(va, vb)
}

// totalSim 计算总相似度（仅用于展示）
func totalSim(contentSim, structSim, visualSim, behaviorSim float64) float64 {
	return 0.4*contentSim + 0.25*structSim + 0.25*visualSim + 0.10*behaviorSim
}

// IsDuplicate 判断两个页面是否为重复页面
func IsDuplicate(a, b *PageFeatures) bool {
	contentSim := simContent(a, b)
	structureSim := simStructure(a, b)
	visualSim := simVisual(a, b)

	if contentSim >= ContentSimThreshold && (structureSim >= StructureSimThreshold || visualSim >= VisualSimThreshold) {
		return true
	}

	if visualSim >= VisualHighSimThreshold {
		return true
	}

	return false
}

// CalculateSimilarities 计算所有维度的相似度
func CalculateSimilarities(a, b *PageFeatures) (contentSim, structureSim, visualSim, behaviorSim, total float64) {
	contentSim = simContent(a, b)
	structureSim = simStructure(a, b)
	visualSim = simVisual(a, b)
	behaviorSim = simBehavior(a, b)
	total = totalSim(contentSim, structureSim, visualSim, behaviorSim)
	return
}

// HammingDistance64 计算 64-bit 汉明距离
func HammingDistance64(a, b uint64) int {
	x := a ^ b
	count := 0
	for x != 0 {
		count++
		x &= x - 1 // 清除最低位的 1
	}
	return count
}

// hammingDistance64 内部使用的汉明距离计算
func hammingDistance64(a, b uint64) int {
	return HammingDistance64(a, b)
}

// cosineSimilarity 计算余弦相似度
func cosineSimilarity(vecA, vecB []float64) float64 {
	if len(vecA) != len(vecB) {
		return 0
	}

	var dotProduct, normA, normB float64
	for i := range vecA {
		dotProduct += vecA[i] * vecB[i]
		normA += vecA[i] * vecA[i]
		normB += vecB[i] * vecB[i]
	}

	if normA == 0 || normB == 0 {
		return 0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// min 返回两个整数中的较小值
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

