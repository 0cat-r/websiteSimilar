package internal

import (
	"crypto/md5"
	"fmt"
	"net/url"
	"sort"
)

// quickSimHashCheck SimHash 预筛选
// 快速排除明显不相似的页面，减少详细比较的次数
func quickSimHashCheck(a, b *PageFeatures) bool {
	if a == nil || b == nil {
		return false
	}
	// SimHash 汉明距离太大直接跳过
	simHashDist := hammingDistance64(a.TextSimHash, b.TextSimHash)
	if simHashDist > QuickSimHashMaxDist {
		return false
	}
	// 文本长度差异超过 50% 也跳过
	if a.TextLength == 0 || b.TextLength == 0 {
		return false
	}
	lenA, lenB := a.TextLength, b.TextLength
	if lenA > lenB {
		lenA, lenB = lenB, lenA
	}
	lengthRatio := float64(lenA) / float64(lenB)
	return lengthRatio >= 0.5
}

// UnionFind 并查集
// 用于把相似的页面合并到同一个 cluster
type UnionFind struct {
	parent map[int]int
	rank   map[int]int
}

// NewUnionFind 创建新的并查集
func NewUnionFind(size int) *UnionFind {
	uf := &UnionFind{
		parent: make(map[int]int),
		rank:   make(map[int]int),
	}
	for i := 0; i < size; i++ {
		uf.parent[i] = i
		uf.rank[i] = 0
	}
	return uf
}

// Find 查找根节点
func (uf *UnionFind) Find(x int) int {
	if uf.parent[x] != x {
		uf.parent[x] = uf.Find(uf.parent[x]) // 路径压缩
	}
	return uf.parent[x]
}

// Union 合并两个集合
func (uf *UnionFind) Union(x, y int) {
	rootX := uf.Find(x)
	rootY := uf.Find(y)

	if rootX == rootY {
		return
	}

	// 按秩合并
	if uf.rank[rootX] < uf.rank[rootY] {
		uf.parent[rootX] = rootY
	} else if uf.rank[rootX] > uf.rank[rootY] {
		uf.parent[rootY] = rootX
	} else {
		uf.parent[rootY] = rootX
		uf.rank[rootX]++
	}
}

// GetClusters 获取所有聚类
func (uf *UnionFind) GetClusters() map[int][]int {
	clusters := make(map[int][]int)
	for i := range uf.parent {
		root := uf.Find(i)
		clusters[root] = append(clusters[root], i)
	}
	return clusters
}

// Cluster 对页面进行聚类
// 先用 host + SimHash 高16位 + 文本长度分桶，减少比较次数
// 然后对每个桶内用并查集聚类
func Cluster(pages []*PageWithFeatures) map[string]*ClusterGroup {
	// 生成粗桶分组
	buckets := make(map[string][]*PageWithFeatures)

	for _, page := range pages {
		if page.Features == nil {
			continue
		}
		bucketKey := generateBucketKey(page)
		buckets[bucketKey] = append(buckets[bucketKey], page)
	}

	// 对每个桶内进行聚类
	allClusters := make(map[string]*ClusterGroup)
	clusterIDCounter := 1

	for _, bucketPages := range buckets {
		if len(bucketPages) < 2 {
			// 单个页面不聚类
			continue
		}

		// 创建并查集
		uf := NewUnionFind(len(bucketPages))

		// 先选一个 canonical 页面（优先 200 状态码、文本最长、ID 最小）
		canonicalPage := selectCanonical(bucketPages)
		if canonicalPage == nil {
			continue
		}

		// 找到 canonical 在 bucketPages 中的索引
		canonicalIdx := 0
		for i, p := range bucketPages {
			if p.ID == canonicalPage.ID {
				canonicalIdx = i
				break
			}
		}

		canonical := bucketPages[canonicalIdx]

		// canonical-centered 策略：其他页面只和 canonical 比较
		// 这样可以减少链式误差，避免 A 和 B 相似、B 和 C 相似，但 A 和 C 不相似的情况
		for i := 0; i < len(bucketPages); i++ {
			if i == canonicalIdx {
				continue
			}
			// SimHash 预筛选
			if !quickSimHashCheck(canonical.Features, bucketPages[i].Features) {
				continue
			}
			// 详细比较
			if IsDuplicate(canonical.Features, bucketPages[i].Features) {
				uf.Union(canonicalIdx, i)
			}
		}

		// 对于没和 canonical 合并的页面，它们之间再比较一次
		// 这是为了处理 canonical 选择不当的情况
		for i := 0; i < len(bucketPages); i++ {
			if i == canonicalIdx || uf.Find(i) == uf.Find(canonicalIdx) {
				continue // 跳过 canonical 和已合并的页面
			}
			for j := i + 1; j < len(bucketPages); j++ {
				if j == canonicalIdx || uf.Find(j) == uf.Find(canonicalIdx) {
					continue
				}
				// SimHash 预筛选
				if !quickSimHashCheck(bucketPages[i].Features, bucketPages[j].Features) {
					continue
				}
				if IsDuplicate(bucketPages[i].Features, bucketPages[j].Features) {
					uf.Union(i, j)
				}
			}
		}

		// 获取聚类结果
		clusters := uf.GetClusters()
		for _, members := range clusters {
			if len(members) < 2 {
				continue // 单个页面不创建 cluster
			}

			// 创建 ClusterGroup
			clusterID := fmt.Sprintf("cluster-%05d", clusterIDCounter)
			clusterIDCounter++

			clusterPages := make([]*PageWithFeatures, len(members))
			for idx, memberIdx := range members {
				clusterPages[idx] = bucketPages[memberIdx]
			}

			// 选择 canonical
			canonical := selectCanonical(clusterPages)

			allClusters[clusterID] = &ClusterGroup{
				ClusterID: clusterID,
				Canonical: canonical,
				Members:   clusterPages,
			}
		}
	}

	return allClusters
}

// ClusterGroup 聚类组
type ClusterGroup struct {
	ClusterID string
	Canonical *PageWithFeatures
	Members   []*PageWithFeatures
}

// generateBucketKey 生成粗桶 key
// 用 host + SimHash 高16位 + 文本长度分桶，减少比较次数
func generateBucketKey(page *PageWithFeatures) string {
	// 提取 host
	u, err := url.Parse(page.FinalURL)
	if err != nil {
		u, _ = url.Parse(page.NormalizedURL)
	}
	host := ""
	if u != nil {
		host = u.Host
	}

	// SimHash 的高 16 位
	top16Bits := (page.Features.TextSimHash >> 48) & 0xFFFF

	// 文本长度按 1000 分桶
	lengthBucket := page.Features.TextLength / 1000

	// 组合后 MD5 缩短
	key := fmt.Sprintf("%s|%d|%d", host, top16Bits, lengthBucket)
	hash := md5.Sum([]byte(key))
	return fmt.Sprintf("%x", hash)
}

// selectCanonical 选择 canonical 页面
// 优先 200 状态码，其次文本最长，最后 ID 最小
func selectCanonical(pages []*PageWithFeatures) *PageWithFeatures {
	if len(pages) == 0 {
		return nil
	}

	// 排序：优先 StatusCode == 200，其次 TextLength 最大，最后 ID 最小
	sort.Slice(pages, func(i, j int) bool {
		a, b := pages[i], pages[j]

		// 优先 200
		if a.StatusCode == 200 && b.StatusCode != 200 {
			return true
		}
		if a.StatusCode != 200 && b.StatusCode == 200 {
			return false
		}

		// 其次 TextLength 最大
		if a.Features != nil && b.Features != nil {
			if a.Features.TextLength != b.Features.TextLength {
				return a.Features.TextLength > b.Features.TextLength
			}
		}

		// 最后 ID 最小
		return a.ID < b.ID
	})

	return pages[0]
}
