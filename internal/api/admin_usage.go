package api

import (
	"fmt"
	"github.com/erishen/tsm-hub/internal/quota"
	"net/http"
	"sort"
	"strings"
)


func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &days); err != nil || days <= 0 {
			days = 7
		}
		if days > 90 {
			days = 90
		}
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &limit); err != nil || limit <= 0 {
			limit = 50
		}
		if limit > 500 {
			limit = 500
		}
	}
	byModel := s.rec.ByModel()
	models := make([]map[string]any, 0, len(byModel))
	for m, a := range byModel {
		models = append(models, map[string]any{"model": m, "usage": a})
	}
	perKey := s.rec.PerKey()
	keys := make([]map[string]any, 0, len(perKey))
	for k, a := range perKey {
		keys = append(keys, map[string]any{"key_id": k, "usage": a})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days":   s.rec.Daily(days),
		"models": models,
		"keys":   keys,
		"recent": s.rec.Recent(limit),
	})
}

// handleClearUsage 清空全部用量流水（DELETE 语义用 POST 便于前端一键触发）。

func (s *Server) handleClearUsage(w http.ResponseWriter, r *http.Request) {
	if err := s.rec.Clear(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	s.recordAudit(r, "clear", "usage", "", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cleared": true})
}

// ---------- observability ----------

// aggView 是带派生指标的用量聚合视图（平均延迟、错误率）。
type aggView struct {
	quota.Agg
	AvgLatencyMS int64   `json:"avg_latency_ms"`
	ErrorRate    float64 `json:"error_rate"`
}

// validProviderID 判定 provider id 是否为合法 id（历史 bug 曾把上游错误体写进
// provider_id，此类记录无法归因，聚合展示时跳过）。

func validProviderID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}



func toAggView(a quota.Agg) aggView {
	return aggView{Agg: a, AvgLatencyMS: a.AvgLatency(), ErrorRate: a.ErrorRate()}
}

// knownToolName 判断调用方声明的工具名是否命中网关现有能力。
// 除精确匹配外，兼容 MCP 客户端命名风格 server__tool ↔ 网关 mcp_server_tool
// （如 fs__read_file ↔ mcp_fs_read_file），避免把自家能力误判为外部自创。

func knownToolName(name string, known map[string]bool) bool {
	if known[name] {
		return true
	}
	if i := strings.Index(name, "__"); i > 0 {
		alt := "mcp_" + name[:i] + "_" + name[i+2:]
		if known[alt] {
			return true
		}
	}
	return false
}

// handleObservability 返回可观测性总览：总览卡片、provider 维度、场景维度、趋势。
// 数据全部来自内存聚合（启动时回放，运行中增量），不触发任何上游查询。

func (s *Server) handleObservability(w http.ResponseWriter, r *http.Request) {
	days := 14
	if v := r.URL.Query().Get("days"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &days); err != nil || days <= 0 {
			days = 14
		}
		if days > 90 {
			days = 90
		}
	}
	nameByID := map[string]string{}
	for _, p := range s.store.ListProviders() {
		nameByID[p.ID] = p.Name
	}

	var today, week, month quota.Agg
	for _, dp := range s.rec.Daily(days) {
		month = month.Add(dp.Agg)
	}
	for _, dp := range s.rec.Daily(7) {
		week = week.Add(dp.Agg)
	}
	for _, dp := range s.rec.Daily(1) {
		today = today.Add(dp.Agg)
	}

	failoverBy := s.rec.FailoverBy()
	// 工具执行归因：TOP 20 个被实际调用的工具（含 mcp_* 与 skill 名）。
	allTools := s.rec.ToolStats()
	if len(allTools) > 20 {
		allTools = allTools[:20]
	}
	toolStats := make([]map[string]any, 0, len(allTools))
	for _, t := range allTools {
		toolStats = append(toolStats, map[string]any{
			"name": t.Name, "calls": t.Calls, "key_count": t.KeyCount,
		})
	}
	// 技能维度：skill:<name> 前缀的执行统计。
	skillStats := make([]map[string]any, 0)
	for _, t := range s.rec.ToolStats() {
		if !strings.HasPrefix(t.Name, "skill:") {
			continue
		}
		skillStats = append(skillStats, map[string]any{
			"skill": strings.TrimPrefix(t.Name, "skill:"), "calls": t.Calls, "key_count": t.KeyCount,
		})
	}
	// MCP 维度：mcp_<server>_<tool> 按 server 聚合。
	mcpCalls := map[string]int{}
	mcpKeys := map[string]int{}
	for _, t := range s.rec.ToolStats() {
		if !strings.HasPrefix(t.Name, "mcp_") {
			continue
		}
		server := strings.TrimPrefix(t.Name, "mcp_")
		if i := strings.Index(server, "_"); i > 0 {
			server = server[:i]
		}
		if server == "" {
			server = "unknown"
		}
		mcpCalls[server] += t.Calls
		if mcpKeys[server] < t.KeyCount {
			mcpKeys[server] = t.KeyCount
		}
	}
	mcpStats := make([]map[string]any, 0, len(mcpCalls))
	for sv, calls := range mcpCalls {
		mcpStats = append(mcpStats, map[string]any{"server": sv, "calls": calls, "key_count": mcpKeys[sv]})
	}
	sort.Slice(mcpStats, func(i, j int) bool { return mcpStats[i]["calls"].(int) > mcpStats[j]["calls"].(int) })
	// 外部自创工具发现：客户端声明但不在网关目录里的工具（含录用状态）。
	known := map[string]bool{}
	for _, t := range s.proxy.ToolCatalog() {
		known[t.Name] = true
	}
	// 条件性内置工具（ReadRoot/Sandbox 未启用时不在目录，但仍是网关能力，不算外部自创）。
	for _, n := range []string{"read_file", "csv_analyze", "execute_code"} {
		known[n] = true
	}
	adopted := map[string]bool{}
	for _, t := range s.store.ListExternalTools() {
		adopted[t.Name] = true
	}
	extStats := make([]map[string]any, 0)
	for _, t := range s.rec.ClientToolStats() {
		if knownToolName(t.Name, known) {
			continue // 系统内已有能力（含命名归一化），不算外部自创
		}
		extStats = append(extStats, map[string]any{
			"name": t.Name, "calls": t.Calls, "key_count": t.KeyCount,
			"adopted": adopted[t.Name],
		})
	}
	if len(extStats) > 20 {
		extStats = extStats[:20]
	}
	provs := make([]map[string]any, 0)
	for id, a := range s.rec.Providers() {
		if !validProviderID(id) {
			continue // 历史坏数据（错误体被写入 provider_id），无归因价值
		}
		name := nameByID[id]
		if name == "" {
			name = id
		}
		provs = append(provs, map[string]any{
			"id": id, "name": name, "usage": toAggView(a),
			// 作为 failover 失败候选被跳过的次数（稳定性反向指标）。
			"skipped": failoverBy[id],
		})
	}
	sort.Slice(provs, func(i, j int) bool {
		return provs[i]["usage"].(aggView).Requests > provs[j]["usage"].(aggView).Requests
	})

	scenes := make([]map[string]any, 0)
	for sc, a := range s.rec.Scenes() {
		scenes = append(scenes, map[string]any{"scene": sc, "usage": toAggView(a)})
	}
	sort.Slice(scenes, func(i, j int) bool {
		return scenes[i]["usage"].(aggView).Requests > scenes[j]["usage"].(aggView).Requests
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"today":     toAggView(today),
		"week":      toAggView(week),
		"month":     toAggView(month),
		"providers": provs,
		"scenes":    scenes,
		"trend":     s.rec.Daily(days),
		"tools":        toolStats,
		"skills":       skillStats,
		"mcps":         mcpStats,
		"external_tools": extStats,
	})
}

// ---------- health ----------

