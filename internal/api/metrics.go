package api

import (
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/erishen/tsm-hub/internal/quota"
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
//
// 扩展指标（全部复用已有内存聚合，不新增状态）：
//   - tsm_hub_upstream_half_open / consecutive_failures /
//     cooldown_remaining_seconds / last_success_timestamp_seconds  健康状态机细节
//   - tsm_hub_usage_*  token / 成本 / 错误 / failover / 平均延迟（quota.Recorder）
//   - tsm_hub_failover_candidates_total / tool_calls_total  失败候选与工具来源归因
//   - tsm_hub_mcp_servers / mcp_tools / a2a_* / config_entities / runtime_*  子系统快照
//
// ⚠️ 隐私边界：/metrics 不鉴权。新增标签只允许「与现有 provider 列表同源的 ID」
// 和「代码内有限枚举」两类；绝不允许 api_key、key_id、model/tool 名、session/task ID、
// 原始 URL path、上游错误文本、client_ip 进入标签。scene 与 peer mode 一律经白名单折叠。
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
	// providerIDs 是扩展指标唯一的 provider 标签来源，保证与上面 7 个指标完全同源：
	// 历史 JSONL 里残留的、当前配置已删除的 provider 名不会被导出。
	providerIDs := make([]string, 0, len(providers))
	for _, p := range providers {
		providerIDs = append(providerIDs, p.ID)
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

	now := time.Now()
	s.writeHealthDetailMetrics(&b, providerIDs, healthByID, now)
	s.writeUsageMetrics(&b, providerIDs)
	s.writeSubsystemMetrics(&b, providerIDs)

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

// writeFamily 保证每个 family 的 HELP/TYPE 只出现一次，其后紧跟全部样本。
func writeFamily(b *strings.Builder, name, help, typ string, lines []string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
}

func metricLine(name, labels string, value any) string {
	if labels == "" {
		return fmt.Sprintf("%s %v", name, value)
	}
	return fmt.Sprintf("%s{%s} %v", name, labels, value)
}

func providerLabel(id string) string { return fmt.Sprintf("provider=%q", id) }

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// cooldownRemainingSeconds 返回熔断/限流冷却的剩余秒数（两者取较晚者，过期为 0）。
// 只导出相对时长，绝不导出绝对时间戳以外的任何状态文本。
func cooldownRemainingSeconds(ph router.ProviderHealth, now time.Time) int64 {
	until := ph.DownUntil
	if ph.ThrottleUntil.After(until) {
		until = ph.ThrottleUntil
	}
	if until.IsZero() || !until.After(now) {
		return 0
	}
	return int64(until.Sub(now).Seconds() + 0.5)
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// writeHealthDetailMetrics 把管理台 Providers 页已有的状态机字段导出，
// 数据源是 Tracker.Snapshot()，无需新增状态或埋点。
func (s *Server) writeHealthDetailMetrics(b *strings.Builder, ids []string, health map[string]router.ProviderHealth, now time.Time) {
	halfOpen := make([]string, 0, len(ids))
	failures := make([]string, 0, len(ids))
	cooldown := make([]string, 0, len(ids))
	lastOK := make([]string, 0, len(ids))
	for _, id := range ids {
		ph := health[id] // 无记录时为零值，语义与「健康且无历史」一致
		lbl := providerLabel(id)
		halfOpen = append(halfOpen, metricLine("tsm_hub_upstream_half_open", lbl, boolInt(ph.HalfOpen)))
		failures = append(failures, metricLine("tsm_hub_upstream_consecutive_failures", lbl, ph.Failures))
		cooldown = append(cooldown, metricLine("tsm_hub_upstream_cooldown_remaining_seconds", lbl, cooldownRemainingSeconds(ph, now)))
		lastOK = append(lastOK, metricLine("tsm_hub_upstream_last_success_timestamp_seconds", lbl, unixOrZero(ph.LastOKAt)))
	}
	writeFamily(b, "tsm_hub_upstream_half_open", "Whether provider is in half-open probe state (1) or not (0).", "gauge", halfOpen)
	writeFamily(b, "tsm_hub_upstream_consecutive_failures", "Effective consecutive failures (decays after the failure TTL).", "gauge", failures)
	writeFamily(b, "tsm_hub_upstream_cooldown_remaining_seconds", "Seconds until breaker or throttle cooldown expires (0 when idle).", "gauge", cooldown)
	writeFamily(b, "tsm_hub_upstream_last_success_timestamp_seconds", "Unix seconds of last successful upstream call (0 when none).", "gauge", lastOK)
}

// 代码内有限枚举：任何不在表内的动态值都折叠成 other，防止高基数/注入。
var metricScenes = []string{"chat", "reason", "code", "fast"}

func safeScene(v string) string {
	for _, s := range metricScenes {
		if v == s {
			return v
		}
	}
	return "other"
}

var metricPeerModes = []string{"off", "shared", "derived"}

func safePeerMode(v string) string {
	for _, m := range metricPeerModes {
		if v == m {
			return v
		}
	}
	return "other"
}

// toolSource 把工具名折叠成 3 类来源；工具名本身（含客户端自创名）绝不做标签。
func toolSource(name string) string {
	switch {
	case strings.HasPrefix(name, "mcp_"):
		return "mcp"
	case strings.HasPrefix(name, "skill:"):
		return "skill"
	default:
		return "builtin"
	}
}

// writeUsageMetrics 复用 quota.Recorder 的累计聚合（启动时由 data/usage/*.jsonl 回放，
// 进程重启不清零）。按 provider 维度只输出当前已配置的 provider，避免历史名字无限增长。
// 这些是累计 counter，查询一律用 rate()/increase()。
func (s *Server) writeUsageMetrics(b *strings.Builder, ids []string) {
	if s.rec == nil {
		return
	}
	byProvider := s.rec.Providers()
	reqs := make([]string, 0, len(ids))
	prompt := make([]string, 0, len(ids))
	completion := make([]string, 0, len(ids))
	cost := make([]string, 0, len(ids))
	errs := make([]string, 0, len(ids))
	failovers := make([]string, 0, len(ids))
	avgLat := make([]string, 0, len(ids))

	for _, id := range ids {
		agg := byProvider[id]
		lbl := providerLabel(id)
		reqs = append(reqs, metricLine("tsm_hub_usage_requests_total", lbl, agg.Requests))
		prompt = append(prompt, metricLine("tsm_hub_usage_tokens_total", lbl+",kind=\"prompt\"", agg.PromptTokens))
		completion = append(completion, metricLine("tsm_hub_usage_tokens_total", lbl+",kind=\"completion\"", agg.CompletionToken))
		cost = append(cost, metricLine("tsm_hub_usage_cost_usd_total", lbl, fmt.Sprintf("%.6f", agg.CostUSD)))
		errs = append(errs, metricLine("tsm_hub_usage_errors_total", lbl, agg.Errors))
		failovers = append(failovers, metricLine("tsm_hub_usage_failovers_total", lbl, agg.Failovers))
		avgLat = append(avgLat, metricLine("tsm_hub_usage_avg_latency_ms", lbl, agg.AvgLatency()))
	}
	writeFamily(b, "tsm_hub_usage_requests_total", "Accounted inference requests by upstream provider (cumulative, replayed from usage JSONL).", "counter", reqs)
	writeFamily(b, "tsm_hub_usage_tokens_total", "Accounted tokens by provider and direction (cumulative).", "counter",
		append(append([]string{}, prompt...), completion...))
	writeFamily(b, "tsm_hub_usage_cost_usd_total", "Estimated cost in USD by provider (cumulative; pricing is an estimate).", "counter", cost)
	writeFamily(b, "tsm_hub_usage_errors_total", "Accounted failed requests by provider (499 client aborts excluded).", "counter", errs)
	writeFamily(b, "tsm_hub_usage_failovers_total", "Requests that succeeded only after a failover by provider.", "counter", failovers)
	writeFamily(b, "tsm_hub_usage_avg_latency_ms", "Cumulative average accounted latency by provider in milliseconds.", "gauge", avgLat)

	// 场景维度：只导 4 个内置场景 + other，键来自 JSONL，可能是任意字符串。
	sceneAgg := map[string]quota.Agg{}
	for name, agg := range s.rec.Scenes() {
		k := safeScene(name)
		sceneAgg[k] = sceneAgg[k].Add(agg)
	}
	sceneReqs := make([]string, 0, len(metricScenes)+1)
	sceneTokens := make([]string, 0, len(metricScenes)+1)
	for _, name := range append(append([]string{}, metricScenes...), "other") {
		agg := sceneAgg[name]
		lbl := fmt.Sprintf("scene=%q", name)
		sceneReqs = append(sceneReqs, metricLine("tsm_hub_usage_scene_requests_total", lbl, agg.Requests))
		sceneTokens = append(sceneTokens, metricLine("tsm_hub_usage_scene_tokens_total", lbl, agg.TotalTokens))
	}
	writeFamily(b, "tsm_hub_usage_scene_requests_total", "Accounted requests by routing scene (chat/reason/code/fast/other).", "counter", sceneReqs)
	writeFamily(b, "tsm_hub_usage_scene_tokens_total", "Accounted total tokens by routing scene.", "counter", sceneTokens)

	// failover 失败候选：只按 provider，不导完整链路字符串。
	failBy := s.rec.FailoverBy()
	candidates := make([]string, 0, len(ids))
	for _, id := range ids {
		candidates = append(candidates, metricLine("tsm_hub_failover_candidates_total", providerLabel(id), failBy[id]))
	}
	writeFamily(b, "tsm_hub_failover_candidates_total", "Times a provider was recorded as a failed failover candidate.", "counter", candidates)

	// 工具执行：按来源聚合，工具名（含 skill:*、mcp_*、客户端自创名）不做标签。
	toolCalls := map[string]int{}
	for _, st := range s.rec.ToolStats() {
		toolCalls[toolSource(st.Name)] += st.Calls
	}
	toolLines := make([]string, 0, 3)
	for _, src := range []string{"builtin", "skill", "mcp"} {
		toolLines = append(toolLines, metricLine("tsm_hub_tool_calls_total", fmt.Sprintf("source=%q", src), toolCalls[src]))
	}
	writeFamily(b, "tsm_hub_tool_calls_total", "Tool executions by source (builtin/skill/mcp); tool names are never labels.", "counter", toolLines)
}

// asInt64 / asString 兼容 AdminOverview() 的 map[string]any 值类型。
func asInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func asString(v any) string {
	if str, ok := v.(string); ok {
		return str
	}
	return ""
}

// writeSubsystemMetrics 导出 MCP / A2A / 配置实体 / Go 运行时快照。
// 全部是不带高基数的 gauge；server 名、工具名、任务 ID、节点名都不做标签。
func (s *Server) writeSubsystemMetrics(b *strings.Builder, ids []string) {
	if s.proxy != nil {
		statuses := s.proxy.MCPStatuses()
		connected, tools := 0, 0
		for _, st := range statuses {
			if st.Connected {
				connected++
				tools += len(st.Tools)
			}
		}
		writeFamily(b, "tsm_hub_mcp_servers", "Configured MCP servers by connection state.", "gauge", []string{
			metricLine("tsm_hub_mcp_servers", "state=\"connected\"", connected),
			metricLine("tsm_hub_mcp_servers", "state=\"disconnected\"", len(statuses)-connected),
		})
		writeFamily(b, "tsm_hub_mcp_tools", "Tools exposed by connected MCP servers.", "gauge",
			[]string{metricLine("tsm_hub_mcp_tools", "", tools)})
	}

	if s.a2a != nil {
		ov := s.a2a.AdminOverview()
		writeFamily(b, "tsm_hub_a2a_tasks_active", "A2A tasks currently in an active state.", "gauge",
			[]string{metricLine("tsm_hub_a2a_tasks_active", "", asInt64(ov["tasks_active"]))})
		writeFamily(b, "tsm_hub_a2a_capabilities", "Registered A2A capabilities.", "gauge",
			[]string{metricLine("tsm_hub_a2a_capabilities", "", asInt64(ov["caps"]))})
		mode := safePeerMode(asString(ov["peer_mode"]))
		modeLines := make([]string, 0, len(metricPeerModes)+1)
		for _, m := range append(append([]string{}, metricPeerModes...), "other") {
			modeLines = append(modeLines, metricLine("tsm_hub_a2a_peer_mode", fmt.Sprintf("mode=%q", m), boolInt(m == mode)))
		}
		writeFamily(b, "tsm_hub_a2a_peer_mode", "A2A peer mode (1 on the active mode, 0 elsewhere).", "gauge", modeLines)
	}

	entities := map[string]int{
		"providers":          len(ids),
		"routes":             len(s.store.ListRoutes()),
		"keys":               len(s.store.ListKeys()),
		"mcps":               len(s.store.Settings().Mcps),
		"unavailable_models": len(s.store.UnavailableSnapshot()),
	}
	if s.skills != nil {
		entities["skills"] = len(s.skills.List())
	} else {
		entities["skills"] = 0
	}
	if s.proxy != nil {
		entities["tools"] = len(s.proxy.ToolCatalog())
	} else {
		entities["tools"] = 0
	}
	entityLines := make([]string, 0, len(entities))
	for _, name := range sortedMetricKeys(entities) {
		entityLines = append(entityLines, metricLine("tsm_hub_config_entities", fmt.Sprintf("entity=%q", name), entities[name]))
	}
	writeFamily(b, "tsm_hub_config_entities", "Number of configured entities by kind (live config snapshot).", "gauge", entityLines)

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	writeFamily(b, "tsm_hub_runtime_goroutines", "Current goroutines in the gateway process.", "gauge",
		[]string{metricLine("tsm_hub_runtime_goroutines", "", runtime.NumGoroutine())})
	writeFamily(b, "tsm_hub_runtime_heap_alloc_bytes", "Heap bytes currently allocated (runtime.MemStats.Alloc).", "gauge",
		[]string{metricLine("tsm_hub_runtime_heap_alloc_bytes", "", mem.Alloc)})
	writeFamily(b, "tsm_hub_runtime_cpu_cores", "Logical CPU cores visible to the gateway process.", "gauge",
		[]string{metricLine("tsm_hub_runtime_cpu_cores", "", runtime.NumCPU())})
}
