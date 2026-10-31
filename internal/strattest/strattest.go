// Package strattest — проверка доступности URL и полный прогон стратегий
// zapret с рекомендацией лучшей для сети пользователя.
package strattest

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"
)

// UrlTestResult — результат проверки одного URL.
type UrlTestResult struct {
	Url    string `json:"url"`
	Ok     bool   `json:"ok"`
	Status int    `json:"status"`
	Ms     int64  `json:"ms"`
	Err    string `json:"err,omitempty"`
}

// StrategyResult — сводка по одной стратегии.
type StrategyResult struct {
	Strategy string          `json:"strategy"`
	Ok       int             `json:"ok"`
	Total    int             `json:"total"`
	Blocked  int             `json:"blocked"` // DPI-паттерн 16-20KB (терминальный тест)
	AvgMs    int64           `json:"avgMs"`
	Urls     []UrlTestResult `json:"urls,omitempty"`
}

// Suite — итог прогона всех стратегий.
type Suite struct {
	Results      []StrategyResult `json:"results"`
	Recommended  string           `json:"recommended"`
	RestoredMode string           `json:"restoredMode"` // service | process | off
}

// DefaultUrls — лёгкие эндпоинты-маркеры: доступны только если DPI-обход работает.
var DefaultUrls = []string{
	"https://www.youtube.com/generate_204",
	"https://discord.com/api/v10/gateway",
}

var client = &http.Client{
	Transport: &http.Transport{
		Proxy:           nil, // прокси не должен искажать тест
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
}

// TestUrl проверяет один URL (HTTP GET, тело не читаем).
func TestUrl(url string, timeout time.Duration) UrlTestResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return UrlTestResult{Url: url, Err: err.Error()}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) ZapretGuiTest")
	resp, err := client.Do(req)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return UrlTestResult{Url: url, Ms: ms, Err: shortErr(err.Error())}
	}
	defer resp.Body.Close()
	ok := resp.StatusCode > 0 && resp.StatusCode < 400
	return UrlTestResult{Url: url, Ok: ok, Status: resp.StatusCode, Ms: ms}
}

func shortErr(s string) string {
	if len(s) > 140 {
		return s[:140] + "…"
	}
	return s
}

// Recommend выбирает лучшую стратегию: больше успешных URL, затем быстрее.
// prev — стратегия, стоявшая до прогона: если она набирает столько же
// успешных URL и её время в пределах 15% от лучшей, оставляем её —
// миллисекунды от прогона к прогону неизбежно плавают, и без этой
// «липкости» рекомендация бы менялась на каждом запуске.
func Recommend(results []StrategyResult, prev string) string {
	best := ""
	var bestOk, bestAvg = -1, int64(1 << 62)
	for _, r := range results {
		switch {
		case r.Ok > bestOk:
			best, bestOk, bestAvg = r.Strategy, r.Ok, r.AvgMs
		case r.Ok == bestOk && r.AvgMs < bestAvg && r.Ok > 0:
			best, bestAvg = r.Strategy, r.AvgMs
		}
	}
	if best == "" || prev == "" || bestOk <= 0 {
		return best
	}
	for _, r := range results {
		if r.Strategy == prev && r.Ok == bestOk && r.AvgMs <= bestAvg+bestAvg*15/100 {
			return prev
		}
	}
	return best
}
