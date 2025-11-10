package internal

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// LoadURLs 加载 URL 列表
// 如果输入以 .txt 结尾，视为文件路径，按行读取
// 否则视为逗号分隔的 URL 字符串
func LoadURLs(input string) ([]URLItem, error) {
	var rawURLs []string

	if strings.HasSuffix(input, ".txt") {
		// 从文件读取
		file, err := os.Open(input)
		if err != nil {
			return nil, fmt.Errorf("无法打开文件 %s: %w", input, err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			// 忽略空行和以 # 开头的注释
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			rawURLs = append(rawURLs, line)
		}

		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("读取文件时出错: %w", err)
		}
	} else {
		// 逗号分隔
		parts := strings.Split(input, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				rawURLs = append(rawURLs, part)
			}
		}
	}

	// 规范化 URL
	items := make([]URLItem, 0, len(rawURLs))
	for i, rawURL := range rawURLs {
		normalized, err := normalizeURL(rawURL)
		if err != nil {
			// 如果规范化失败，仍然保留原始 URL，但记录错误
			normalized = rawURL
		}

		items = append(items, URLItem{
			ID:            i + 1,
			RawURL:        rawURL,
			NormalizedURL: normalized,
		})
	}

	return items, nil
}

// normalizeURL 规范化 URL
func normalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("URL 为空")
	}

	// 如果没有 scheme，默认添加 http://
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}

	// 使用 net/url 解析
	u, err := url.Parse(raw)
	if err != nil {
		return raw, err
	}

	// 标准化 host（转小写）
	u.Host = strings.ToLower(u.Host)

	// 重组 URL（去掉多余斜杠等）
	normalized := u.String()

	return normalized, nil
}

