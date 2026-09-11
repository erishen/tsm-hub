package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/erishen/tsm-hub/internal/router"
)

// handleMetrics 暴露 Prometheus 文本格式指标（/metrics，不鉴权，语义同 /healthz）。
//
// 指标清单：
//   - tsm_hub_uptime_seconds           进程存活秒数（gauge）
//   - tsm_hub_http_requests_total{status}  网关 HTTP 请求数（counter）
//   - tsm_hub_quota_denials_total{code}    被限流/额度拒绝的请求数（counter）
//   - tsm_hub_upstream_healthy{provider}   上游是否可用 1/0（gauge）
//   - tsm_hub_upstream_requests_total{provider} 上游尝试次数（counter）
//   - tsm_hub_upstream_errors_total{provider}   上游失败计数（counter）
//   - tsm_hub_upstream_latency_ms{provider}     上游延迟 EWMA（gauge）
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	up := int64(time.Since(s.startAt).Seconds())
	statusCounts := make(map[string]int64, len(s.statusCounts))
	for k, v := range s.statusCounts {
		statusCounts[k] = v
	}
	denyCounts := make(map[string]int64, len(s.denyCounts))
	for k, v := range s.denyCounts {
		denyCounts[k] = v
	}
	s.mu.RUnlock()

	healthByID := map[string]router.ProviderHealth{}
	if s.health != nil {
		for _, ph := range s.health.Snapshot() {
			healthByID[ph.ProviderID] = ph
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# HELP tsm_hub_uptime_seconds Process uptime in seconds.\n")
	fmt.Fprintf(&b, "# TYPE tsm_hub_uptime_seconds gauge\ntsm_hub_uptime_seconds %d\n", up)

	fmt.Fprintf(&b, "# HELP tsm_hub_http_requests_total Total HTTP requests by status code.\n")
	fmt.Fprintf(&b, "# TYPE tsm_hub_http_requests_total counter\n")
	for _, st := range sortedMetricKeys(statusCounts) {
		fmt.Fprintf(&b, "tsm_hub_http_requests_total{status=%q} %d\n", st, statusCounts[st])
	}

	fmt.Fprintf(&b, "# HELP tsm_hub_quota_denials_total Requests denied by quota or rate limit.\n")
	fmt.Fprintf(&b, "# TYPE tsm_hub_quota_denials_total counter\n")
	for _, c := range sortedMetricKeys(denyCounts) {
		fmt.Fprintf(&b, "tsm_hub_quota_denials_total{code=%q} %d\n", c, denyCounts[c])
	}

	fmt.Fprintf(&b, "# HELP tsm_hub_upstream_healthy Whether upstream provider is currently usable (1) or not (0).\n")
	fmt.Fprintf(&b, "# TYPE tsm_hub_upstream_healthy gauge\n")
	fmt.Fprintf(&b, "# HELP tsm_hub_upstream_requests_total Requests attempted per upstream provider.\n")
	fmt.Fprintf(&b, "# TYPE tsm_hub_upstream_requests_total counter\n")
	fmt.Fprintf(&b, "# HELP tsm_hub_upstream_errors_total Upstream errors counted per provider.\n")
	fmt.Fprintf(&b, "# TYPE tsm_hub_upstream_errors_total counter\n")
	fmt.Fprintf(&b, "# HELP tsm_hub_upstream_latency_ms EWMA latency per provider in milliseconds.\n")
	fmt.Fprintf(&b, "# TYPE tsm_hub_upstream_latency_ms gauge\n")
	providers := s.store.ListProviders()
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	for _, p := range providers {
		healthy, reqs, errs, lat := 1, int64(0), int64(0), int64(0)
		if !p.Enabled {
			healthy = 0
		} else if ph, ok := healthByID[p.ID]; ok {
			if !ph.Healthy {
				healthy = 0
			}
			reqs, errs, lat = ph.Requests, ph.Errors, ph.LatencyMS
		}
		// %q 生成带引号且转义合法字符的标签值；provider ID 为 slugify 产物（字母数字-），安全。
		fmt.Fprintf(&b, "tsm_hub_upstream_healthy{provider=%q} %d\n", p.ID, healthy)
		fmt.Fprintf(&b, "tsm_hub_upstream_requests_total{provider=%q} %d\n", p.ID, reqs)
		fmt.Fprintf(&b, "tsm_hub_upstream_errors_total{provider=%q} %d\n", p.ID, errs)
		fmt.Fprintf(&b, "tsm_hub_upstream_latency_ms{provider=%q} %d\n", p.ID, lat)
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

// sortedMetricKeys 返回 map 键的字典序切片，保证 /metrics 输出稳定。
func sortedMetricKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
