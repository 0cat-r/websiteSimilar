# websiteSimilar

一个网页去重工具，通过渲染页面并提取多维特征来判断哪些页面是重复的。


## 核心思路

这个工具主要解决两个问题：
1. 哪些页面内容相似（去重）
2. 哪些页面是错误页、登录页、重定向等（规则归类）

对于内容相似的页面，用特征相似度来判断；对于错误页、登录页这些，用规则来归类。

## 去重算法

### 特征提取

每个页面会提取四类特征：

**文本特征**
- 用 SimHash 算法计算文本指纹（64位）
- 记录正文文本长度
- 正文提取会跳过导航、页脚这些区域，优先找 article、main 这些语义标签

**DOM 结构特征**
- 节点总数、文本节点数
- 关键标签的分布（div、a、img、input、script）
- DOM 路径频次（比如 `html>body>div>p` 这种路径出现的次数）
- 节点深度分布

**视觉特征**
- 页面截图
- 用感知哈希（pHash）计算截图指纹

**行为特征**
- TTFB（首字节时间）
- DOMContentLoaded 时间
- Load 事件时间

### 相似度计算

**文本相似度**
- 先看文本长度比例，差异超过 70% 直接判为不相似
- 计算 SimHash 的汉明距离，距离越大相似度越低
- 如果汉明距离 >= 16，相似度为 0

**结构相似度**
- DOM 统计相似度：用余弦相似度比较节点数、文本节点数、关键标签分布
- 路径相似度：用加权 Jaccard 比较 DOM 路径频次
- 最终结构相似度 = 0.5 × DOM统计相似度 + 0.5 × 路径相似度

**视觉相似度**
- 计算 pHash 的汉明距离
- 如果距离 >= 20，相似度为 0
- 否则相似度 = 1 - (距离 / 20)

**行为相似度**
- 用余弦相似度比较 TTFB、DOMContentLoaded、Load 这三个时间

### 重复判定规则

两个页面被认为是重复的，需要满足以下**任意一条**：

**规则1（主规则）**
- 文本相似度 >= 0.97（文本几乎一样）
- **且**（结构相似度 >= 0.85 **或** 视觉相似度 >= 0.85）

**规则2（视觉兜底）**
- 视觉相似度 >= 0.99（截图几乎一模一样）

规则1 的逻辑是：如果文本几乎一样，那结构或视觉至少有一个要相似，这样能避免误判。规则2 是兜底，有些页面文本可能被动态替换但视觉完全一样，这种情况也能识别。

### 聚类算法

1. **粗分组**：先用 host + SimHash 高16位 + 文本长度分桶，减少比较次数
2. **预筛选**：SimHash 汉明距离 > 8 的直接跳过，文本长度差异 > 50% 的也跳过
3. **并查集聚类**：
   - 先选一个 canonical 页面（优先 200 状态码、文本最长、ID 最小）
   - 其他页面只和 canonical 比较（canonical-centered 策略，避免链式误差）
   - 如果和 canonical 相似就合并到同一 cluster
   - 对于没和 canonical 合并的页面，它们之间再比较一次（处理 canonical 选择不当的情况）

## 规则和逻辑判定

除了内容相似度去重，还会用规则把一些特殊页面归类：

### 规则聚类
每个人需求不同，有些规则可能不符合个人的需求，可以自行修改代码并编译
这些规则按优先级执行，优先级高的先执行：

**E1：5xx 错误页**
- 同 origin 下所有 5xx 状态码的页面归为一类
- Cluster ID 格式：`err5xx-{origin}`

**E3：统一错误模板**
- 404、401、403 或 200 但包含错误关键词的页面
- 按 HTML 指纹分组（相同指纹的归一类）
- 长度差异 < 20% 的才归为一类
- Cluster ID 格式：`errtpl-{origin}-{hash}`

**L1：登录墙**
- 包含登录关键词的页面（登录、login、password 等）
- 按 HTML 指纹分组
- Cluster ID 格式：`loginwall-{origin}-{hash}`

**W1：WAF 拦截页**
- 包含 WAF 关键词的页面（access denied、防火墙、cloudflare 等）
- 按 HTML 指纹分组
- Cluster ID 格式：`waf-{origin}-{hash}`

**M1：维护/升级页**
- 包含维护关键词的页面（维护中、maintenance、upgrading 等）
- 按 HTML 指纹分组
- Cluster ID 格式：`maint-{origin}-{hash}`

**T1：超短/空页**
- HTML 大小 < 1KB 或文本长度 < 200 字符的页面
- 包括 2xx、401、403 状态码
- 按 HTML 指纹分组
- Cluster ID 格式：`thin-{origin}-{hash}`

**R1：重定向归并**
- 最终 URL 相同的页面归为一类（不同 URL 重定向到同一个页面）
- Cluster ID 格式：`redir-{hash}`

**U1：URL 小变体归一**
- 规范化 path 后相同的 URL 归为一类（比如 `/index.html` 和 `/`）
- Cluster ID 格式：`urlcanon-{origin}-{path}`

### 可判定条件

只有满足以下条件的页面才会参与内容相似度去重：

1. HTTP 状态码为 2xx（主要是 200）
2. Content-Type 包含 `text/html`
3. HTML 大小 >= 1KB
4. 渲染后正文文本长度 >= 200 字符

不满足条件的页面（错误页、非 HTML、文本太短等）会：
- 不参与内容聚类
- 可能被规则聚类归类
- `cluster_id` 可能为空（如果没命中任何规则）
- `is_canonical = true`（单独一个）
- 所有相似度字段为 0

## 安装和使用

### 安装

```bash
go mod download
go build -o websiteSimilar ./cmd
```

### 基本用法

```bash
# 从文件读取 URL 列表
./websiteSimilar -l urls.txt -o result.json

# 直接提供逗号分隔的 URL
./websiteSimilar -l "https://example.com,https://example.org" -o result.json

# 输出 CSV 格式
./websiteSimilar -l urls.txt -o result.csv
```

### 命令行参数

- `-l`（必选）：URL 列表
  - 如果以 `.txt` 结尾，视为文件路径，按行读取（支持空行和 `#` 注释）
  - 否则视为逗号分隔的 URL 字符串
- `-o`（必选）：输出文件路径（支持 .json 或 .csv 扩展名）
- `-t`：并发数，默认 20
- `-http-timeout`：HTTP 请求超时，默认 10s
- `-page-timeout`：单个页面渲染超时，默认 20s
- `-batch-size`：批处理大小，默认 1000
- `-sim-threshold`：相似度阈值（仅用于 meta，实际判定使用严格规则），默认 0.85

### URL 文件格式

`urls.txt` 示例：

```
https://example.com/page1
https://example.com/page2
# 这是注释
https://example.org/page1
```

## 输出格式

### JSON 格式

```json
{
  "urls": [
    {
      "id": 1,
      "url": "https://example.com",
      "normalized_url": "http://example.com",
      "final_url": "https://example.com/",
      "redirect_chain": ["http://example.com", "https://example.com/"],
      "status_code": 200,
      "content_length": 12345,
      "content_type": "text/html",
      "error": "",
      "title": "Example",
      "cluster_id": "cluster-00001",
      "is_canonical": true,
      "similarity_to_canonical": 1.0,
      "content_sim": 1.0,
      "structure_sim": 0.95,
      "visual_sim": 0.98,
      "behavior_sim": 0.92
    }
  ],
  "clusters": [
    {
      "cluster_id": "cluster-00001",
      "canonical_url": "https://example.com/",
      "member_ids": [1, 2]
    }
  ],
  "meta": {
    "total_urls": 100,
    "eligible_html_urls": 85,
    "total_clusters": 10,
    "sim_threshold": 0.85,
    "generated_at": "2024-01-01T00:00:00Z"
  }
}
```

### CSV 格式

CSV 文件包含以下列：

- `id`：URL ID
- `url`：原始 URL
- `normalized_url`：规范化后的 URL
- `final_url`：最终 URL（跟随重定向后）
- `status_code`：HTTP 状态码
- `content_length`：响应大小
- `content_type`：Content-Type
- `error`：错误信息（如有）
- `title`：页面标题
- `cluster_id`：聚类 ID
- `is_canonical`：是否为该聚类的代表页面
- `similarity_to_canonical`：与代表页面的相似度
- `content_sim`：文本相似度
- `structure_sim`：结构相似度
- `visual_sim`：视觉相似度
- `behavior_sim`：行为相似度

## 去重方法

要获取去重后的 URL 列表，只需筛选 `is_canonical = true` 的行：

```bash
# 使用 jq（JSON）
jq '.urls[] | select(.is_canonical == true) | .final_url' result.json

# 使用 awk（CSV）
awk -F',' '$11 == "true" {print $4}' result.csv
```

## 技术细节

### 渲染机制

- 使用 headless Chrome 渲染页面，支持 React/Vue/Angular/Next.js 等框架
- 等待页面稳定：检查网络空闲（500ms 内无新请求）和 DOM 稳定（连续 3 次检查 DOM 无变化），最多等待 10 秒
- 对于需要登录或验证码的页面，实际拿到的是登录页/挑战页，会被视为"不可判定"
- 无限滚动页面只采样首屏内容来判定相似度

### 性能优化

- SimHash 预筛选：快速排除明显不相似的页面
- 粗分组：按 host + SimHash 高16位 + 文本长度分桶
- 批处理：支持分批处理大量 URL，避免内存溢出
- 并发控制：HTTP 抓取和渲染都支持并发，可配置并发数

## 限制说明

1. **登录/认证页面**：无法访问需要登录的内容，只能获取登录页本身
2. **反爬虫/验证码**：可能被 challenge 页面拦截，视为"不可判定"
3. **无限滚动**：只分析首屏内容，后续滚动内容不参与判定
4. **动态内容**：如果页面内容在渲染后 10 秒内仍未稳定，可能影响特征抽取
5. **规则误报**：规则可能有误报或者一些增删改的需求，那就自己改啦

## 许可证

MIT License
