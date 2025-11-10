package internal

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
)

// Renderer headless Chrome 渲染器
type Renderer struct {
	allocCtx       context.Context
	allocCancel    context.CancelFunc
	browserCtx     context.Context
	browserCancel  context.CancelFunc
	perPageTimeout time.Duration
	workerPool     chan struct{} // 限制并发渲染数量
}

// NewRenderer 创建新的渲染器
func NewRenderer(parentCtx context.Context, perPageTimeout time.Duration, maxWorkers int) (*Renderer, error) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.Flag("ignore-ssl-errors", true),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(parentCtx, opts...)

	browserCtx, browserCancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(func(format string, v ...interface{}) {
		_ = format
		_ = v
	}))

	if err := chromedp.Run(browserCtx, chromedp.Navigate("about:blank")); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("启动浏览器失败: %w", err)
	}

	workerPool := make(chan struct{}, maxWorkers)

	return &Renderer{
		allocCtx:       allocCtx,
		allocCancel:    allocCancel,
		browserCtx:     browserCtx,
		browserCancel:  browserCancel,
		perPageTimeout: perPageTimeout,
		workerPool:     workerPool,
	}, nil
}

// Close 关闭渲染器
func (r *Renderer) Close() {
	if r.browserCancel != nil {
		r.browserCancel()
	}
	if r.allocCancel != nil {
		r.allocCancel()
	}
}

// ExtractFeatures 提取页面特征，返回特征和渲染后的标题
func (r *Renderer) ExtractFeatures(ctx context.Context, finalURL string) (*PageFeatures, string, error) {
	r.workerPool <- struct{}{}
	defer func() { <-r.workerPool }()

	features := &PageFeatures{
		TagCount:  make(map[string]int),
		PathCount: make(map[string]int),
	}

	pageCtx, cancel := context.WithTimeout(ctx, r.perPageTimeout)
	defer cancel()

	tabCtx, cancelTab := chromedp.NewContext(r.browserCtx)
	defer cancelTab()

	var htmlContent string
	var domStatsJSON string
	var perfTimingJSON string
	var screenshotBuf []byte
	var title string

	// 监听 pageCtx 取消，同步取消 tabCtx
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-pageCtx.Done():
			cancelTab()
		case <-tabCtx.Done():
		}
	}()

	err := chromedp.Run(tabCtx,
		chromedp.Navigate(finalURL),
		chromedp.WaitReady("body"),
		waitForPageStable(),
		chromedp.Title(&title),
		chromedp.OuterHTML("html", &htmlContent),
		chromedp.Evaluate(getDOMStatsJS(), &domStatsJSON),
		chromedp.Evaluate(getPerfTimingJS(), &perfTimingJSON),
		chromedp.CaptureScreenshot(&screenshotBuf),
	)

	<-done

	if err != nil {
		return features, "", fmt.Errorf("渲染页面失败: %w", err)
	}

	if err := parseFeatures(features, htmlContent, domStatsJSON, perfTimingJSON, screenshotBuf); err != nil {
		return features, title, fmt.Errorf("解析特征失败: %w", err)
	}

	return features, title, nil
}

// getDOMStatsJS 返回用于获取 DOM 统计信息的 JS 代码
func getDOMStatsJS() string {
	return `
(function() {
  function getDepth(el) {
    var d = 0;
    while (el && el.parentElement) {
      d++;
      el = el.parentElement;
    }
    return d;
  }
  function getPath(el) {
    var parts = [];
    while (el && el.nodeType === 1 && el.tagName.toLowerCase() !== 'html') {
      parts.push(el.tagName.toLowerCase());
      el = el.parentElement;
    }
    parts.push('body');
    parts.push('html');
    parts.reverse();
    return parts.join('>');
  }
  var all = document.getElementsByTagName('*');
  var tagCount = {};
  var depthHist = [];
  var textNodeCount = 0;
  var pathCount = {};
  var maxPaths = 5000;
  for (var i = 0; i < all.length; i++) {
    var el = all[i];
    var tag = el.tagName.toLowerCase();
    tagCount[tag] = (tagCount[tag] || 0) + 1;
    var d = getDepth(el);
    depthHist[d] = (depthHist[d] || 0) + 1;
    for (var j = 0; j < el.childNodes.length; j++) {
      var n = el.childNodes[j];
      if (n.nodeType === Node.TEXT_NODE && n.textContent.trim().length > 0) {
        textNodeCount++;
      }
    }
    if (i < maxPaths) {
      var p = getPath(el);
      pathCount[p] = (pathCount[p] || 0) + 1;
    }
  }
  return JSON.stringify({
    domNodeCount: all.length,
    textNodeCount: textNodeCount,
    tagCount: tagCount,
    depthHist: depthHist,
    pathCount: pathCount
  });
})()
`
}

// getPerfTimingJS 返回用于获取性能时间信息的 JS 代码
func getPerfTimingJS() string {
	return `
JSON.stringify((function() {
  var res = { navigationStart: 0, responseStart: 0, domContentLoadedEventEnd: 0, loadEventEnd: 0 };
  try {
    if (window.performance && window.performance.timing) {
      var t = window.performance.timing;
      res.navigationStart = t.navigationStart;
      res.responseStart = t.responseStart;
      res.domContentLoadedEventEnd = t.domContentLoadedEventEnd;
      res.loadEventEnd = t.loadEventEnd;
    }
  } catch (e) {}
  return res;
})())
`
}

// waitForPageStable 等待页面稳定（网络空闲 + DOM 稳定）
func waitForPageStable() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		var lastDOMHash string
		stableCount := 0
		maxStableChecks := 2
		checkInterval := 500 * time.Millisecond
		maxWaitTime := 10 * time.Second
		firstCheck := true

		startTime := time.Now()
		for {
			if time.Since(startTime) > maxWaitTime {
				break
			}

			var currentDOMHash string
			err := chromedp.Evaluate(`
				(function() {
					var nodeCount = document.getElementsByTagName('*').length;
					var textLength = document.body ? document.body.innerText.length : 0;
					return nodeCount + '_' + textLength;
				})()
			`, &currentDOMHash).Do(ctx)
			if err != nil {
				return err
			}

			var networkIdle bool
			err = chromedp.Evaluate(`
				(function() {
					if (!window.performance || !window.performance.getEntriesByType) {
						return true;
					}
					var entries = window.performance.getEntriesByType('resource');
					var now = Date.now();
					for (var i = entries.length - 1; i >= 0; i--) {
						var entry = entries[i];
						var endTime = entry.responseEnd || entry.startTime;
						if (now - endTime < 500) {
							return false;
						}
					}
					return true;
				})()
			`, &networkIdle).Do(ctx)
			if err != nil {
				return err
			}

			if firstCheck && networkIdle {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(300 * time.Millisecond):
				}
				var secondDOMHash string
				err := chromedp.Evaluate(`
					(function() {
						var nodeCount = document.getElementsByTagName('*').length;
						var textLength = document.body ? document.body.innerText.length : 0;
						return nodeCount + '_' + textLength;
					})()
				`, &secondDOMHash).Do(ctx)
				if err == nil && secondDOMHash == currentDOMHash {
					return nil
				}
				firstCheck = false
				lastDOMHash = currentDOMHash
				continue
			}
			firstCheck = false

			if currentDOMHash == lastDOMHash && networkIdle {
				stableCount++
				if stableCount >= maxStableChecks {
					return nil
				}
			} else {
				stableCount = 0
				lastDOMHash = currentDOMHash
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(checkInterval):
			}
		}

		return nil
	})
}
