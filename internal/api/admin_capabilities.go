package api

import (
	"encoding/json"
	"fmt"
	"github.com/erishen/tsm-hub/internal/proxy"
	"github.com/erishen/tsm-hub/internal/store"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)


func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	if s.skills == nil || s.skills.Dir() == "" {
		writeJSON(w, http.StatusOK, map[string]any{"dir": "", "skills": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dir":    s.skills.Dir(),
		"skills": s.skills.List(),
	})
}



func (s *Server) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.skills == nil {
		writeError(w, http.StatusNotFound, "not_found", "skills library not configured")
		return
	}
	d, ok := s.skills.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "skill not found")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// ---------- mcps / tools ----------

// handleListMcps 返回 MCP server 配置 + 连接状态 + 工具数。

func (s *Server) handleListMcps(w http.ResponseWriter, r *http.Request) {
	cfg := s.store.Settings().Mcps
	names := make([]string, 0, len(cfg))
	for n := range cfg {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		c := cfg[name]
		st := s.proxy.MCPStatuses()[name]
		out = append(out, map[string]any{
			"name":      name,
			"command":   c.Command,
			"args":      c.Args,
			"env":       c.Env,
			"transport": c.Transport,
			"url":       c.URL,
			"connected":     st.Connected,
			"tools":         st.Tools,
			"tool_details":  st.ToolDetails,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"mcps": out})
}

// handleUpsertMcp 新增/更新一个 MCP server 配置，并重建连接。

func (s *Server) handleUpsertMcp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid mcp name")
		return
	}
	var c struct {
		Command    string            `json:"command"`
		Args       []string          `json:"args"`
		Env        map[string]string `json:"env"`
		Transport  string            `json:"transport"`
		URL        string            `json:"url"`
		TimeoutSec int               `json:"timeout_sec"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if c.Transport == "" {
		c.Transport = "stdio"
	}
	if c.Transport == "http" {
		if c.URL == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "url required for http transport")
			return
		}
		if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
			writeError(w, http.StatusBadRequest, "bad_request", "url must be http(s)")
			return
		}
	} else if c.Command == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "command required for stdio transport")
		return
	}
	if err := s.store.Update(func(cfg *store.Config) error {
		if cfg.Settings.Mcps == nil {
			cfg.Settings.Mcps = map[string]store.MCPServer{}
		}
		cfg.Settings.Mcps[name] = store.MCPServer{Command: c.Command, Args: c.Args, Env: c.Env, Transport: c.Transport, URL: c.URL, TimeoutSec: c.TimeoutSec}
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.proxy.ResetMCP()
	s.recordAudit(r, "upsert", "mcp", name, map[string]any{"transport": c.Transport, "command": c.Command, "url": c.URL})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteMcp 删除一个 MCP server 配置并断开连接。

func (s *Server) handleDeleteMcp(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.Update(func(cfg *store.Config) error {
		if cfg.Settings.Mcps != nil {
			delete(cfg.Settings.Mcps, name)
		}
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.proxy.ResetMCP()
	s.recordAudit(r, "delete", "mcp", name, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleListFastpath 返回快路径状态：内置匹配器 + 晋升/运行时插件。

func (s *Server) handleListFastpath(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"builtin": s.proxy.FastMatchers(),
		"plugins": s.proxy.FastPluginList(),
	})
}

// handlePromoteFastpath 把插件晋升为正式检测器（移入 promoted 目录）。
// mode: fastpath（拦截，默认）/ tool（注册为工具，可被 LLM 与外部调用）/ both（两者）。

func (s *Server) handlePromoteFastpath(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var c struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if err := s.proxy.PromoteFastPlugin(name, c.Mode); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteFastpath 删除插件（promoted 也可删）。

func (s *Server) handleDeleteFastpath(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.proxy.DeleteFastPlugin(name); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	s.recordAudit(r, "delete", "fastpath", name, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleFastpathGenerate 手动触发 codegen：对 query 生成检测器并立即验证。

func (s *Server) handleFastpathGenerate(w http.ResponseWriter, r *http.Request) {
	var c struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if strings.TrimSpace(c.Query) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "query required")
		return
	}
	answer, method, chain := s.proxy.FastPathProbe(r, store.APIKey{}, "/v1/chat/completions", c.Query)
	writeJSON(w, http.StatusOK, map[string]any{"answer": answer, "method": method, "chain": chain})
}

// handleListTools 返回网关工具池目录（内置 + 条件 + MCP）。

func (s *Server) handleListTools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"tools": s.proxy.ToolCatalog()})
}

// handleListExternalTools 列出外部自创工具：声明统计 + 录用状态 + 已录用实现。

func (s *Server) handleListExternalTools(w http.ResponseWriter, r *http.Request) {
	known := map[string]bool{}
	for _, t := range s.proxy.ToolCatalog() {
		known[t.Name] = true
	}
	// 条件性内置工具（ReadRoot/Sandbox 未启用时不在目录，但仍是网关能力，不算外部自创）。
	for _, n := range []string{"read_file", "csv_analyze", "execute_code"} {
		known[n] = true
	}
	adopted := map[string]bool{}
	impls := map[string]store.ExternalTool{}
	for _, t := range s.store.ListExternalTools() {
		adopted[t.Name] = true
		impls[t.Name] = t
	}
	out := make([]map[string]any, 0)
	for _, t := range s.rec.ClientToolStats() {
		if knownToolName(t.Name, known) {
			continue
		}
		item := map[string]any{
			"name": t.Name, "calls": t.Calls, "key_count": t.KeyCount,
			"adopted": adopted[t.Name],
		}
		if t2, ok := impls[t.Name]; ok {
			item["impl_type"] = t2.ImplType
			item["description"] = t2.Description
			item["kind"] = t2.Kind
			if item["kind"] == "" {
				item["kind"] = "tool"
			}
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"external_tools": out})
}

// handleListExternalSkillCandidates 返回外部技能候选（技能库页"外部技能"区）：
//   - 已录用 kind=skill 的能力（管理员在监控页录用为技能的），合并调用统计；
//   - 未录用但调用方声明带技能特征（skill: / skill_ 前缀）的候选，
//     供管理员在技能库页择优录用（adopt kind=skill）。

func (s *Server) handleListExternalSkillCandidates(w http.ResponseWriter, r *http.Request) {
	known := map[string]bool{}
	for _, t := range s.proxy.ToolCatalog() {
		known[t.Name] = true
	}
	for _, n := range []string{"read_file", "csv_analyze", "execute_code"} {
		known[n] = true
	}
	stats := map[string]struct{ Calls, Keys int }{}
	for _, t := range s.rec.ClientToolStats() {
		stats[t.Name] = struct{ Calls, Keys int }{t.Calls, t.KeyCount}
	}
	adopted := map[string]store.ExternalTool{}
	for _, t := range s.store.ListExternalTools() {
		if t.Kind == "skill" {
			adopted[t.Name] = t
		}
	}
	out := make([]map[string]any, 0, len(adopted)+4)
	seen := map[string]bool{}
	for _, t := range s.store.ListExternalTools() {
		if t.Kind != "skill" {
			continue
		}
		item := map[string]any{
			"name": t.Name, "description": t.Description, "adopted": true,
			"adopted_at": t.AdoptedAt, "kind": "skill",
		}
		if st, ok := stats[t.Name]; ok {
			item["calls"] = st.Calls
			item["key_count"] = st.Keys
		}
		out = append(out, item)
		seen[t.Name] = true
	}
	for _, t := range s.rec.ClientToolStats() {
		if seen[t.Name] || known[t.Name] {
			continue
		}
		if !strings.HasPrefix(t.Name, "skill:") && !strings.HasPrefix(t.Name, "skill_") {
			continue
		}
		out = append(out, map[string]any{
			"name": t.Name, "calls": t.Calls, "key_count": t.KeyCount,
			"adopted": false, "kind": "skill",
		})
	}
	sort.Slice(out, func(i, j int) bool {
		ai, _ := out[i]["adopted"].(bool)
		aj, _ := out[j]["adopted"].(bool)
		if ai != aj {
			return ai
		}
		ci, _ := out[i]["calls"].(int)
		cj, _ := out[j]["calls"].(int)
		if ci != cj {
			return ci > cj
		}
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	writeJSON(w, http.StatusOK, map[string]any{"candidates": out})
}

// handleAdoptExternalTool 录用（或更新）一个外部自创能力（工具或技能）。
// body: {description, kind(tool|skill), impl_type(none/js/alias), impl_source}
//   kind=tool  —— 与 fastpath 晋升一致，js 检测器可被 gen_ 调用；
//   kind=skill —— 技能说明模式：impl_type 仅 none，impl_source 为该技能的指令说明，
//                 skill-run 调用时作为技能说明注入（与目录技能行为一致）。

func (s *Server) handleAdoptExternalTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || strings.ContainsAny(name, "/\\ ") {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid tool name")
		return
	}
	var c struct {
		Description string `json:"description"`
		Kind        string `json:"kind"`
		ImplType    string `json:"impl_type"`
		ImplSource  string `json:"impl_source"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if c.Kind == "" {
		c.Kind = "tool"
	}
	if c.Kind != "tool" && c.Kind != "skill" {
		writeError(w, http.StatusBadRequest, "bad_request", "kind must be tool/skill")
		return
	}
	if c.ImplType == "" {
		c.ImplType = "none"
	}
	if c.Kind == "skill" {
		// 技能录用只登记说明（skill-run 注入指令文本），不支持 js/alias 执行。
		c.ImplType = "none"
	} else if c.ImplType != "none" && c.ImplType != "js" && c.ImplType != "alias" {
		writeError(w, http.StatusBadRequest, "bad_request", "impl_type must be none/js/alias")
		return
	}
	if (c.ImplType == "js" || c.ImplType == "alias") && strings.TrimSpace(c.ImplSource) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "impl_source required for js/alias")
		return
	}
	if c.ImplType == "js" {
		if !s.proxy.ValidateJSDetector(c.ImplSource) {
			writeError(w, http.StatusBadRequest, "bad_request", "js implementation must define detect(text)")
			return
		}
	}
	if err := s.store.AdoptExternalTool(store.ExternalTool{
		Name:        name,
		Description: c.Description,
		Kind:        c.Kind,
		ImplType:    c.ImplType,
		ImplSource:  c.ImplSource,
		AdoptedAt:   time.Now().Format("2006-01-02 15:04:05"),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleDeleteExternalTool 取消录用一个外部工具。

func (s *Server) handleDeleteExternalTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteExternalTool(name); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// 常见 MCP server 的 npx 启动命令（与前端常用模板一致）：外部候选接入时自动预填。
// 调用方声明只包含工具名（server__tool），不含连接配置，命中此表即可一键预填。
var knownMcpCommands = map[string]string{
	"fs":         "npx -y @modelcontextprotocol/server-filesystem",
	"memory":     "npx -y @modelcontextprotocol/server-memory",
	"serena":     "npx -y serena-mcp",
	"think":      "npx -y @modelcontextprotocol/server-think",
	"fetch":      "npx -y mcp-server-fetch",
	"github":     "npx -y @modelcontextprotocol/server-github",
	"git":        "npx -y @modelcontextprotocol/server-git",
	"sqlite":     "npx -y @modelcontextprotocol/server-sqlite",
	"time":       "npx -y @modelcontextprotocol/server-time",
	"context7":   "npx -y @upstash/context7-mcp",
	"puppeteer":  "npx -y @modelcontextprotocol/server-puppeteer",
	"playwright": "npx -y @executeautomation/playwright-mcp-server",
	"google-maps": "npx -y @modelcontextprotocol/server-google-maps",
	"brave-search": "npx -y @modelcontextprotocol/server-brave-search",
	"firecrawl":  "npx -y firecrawl-mcp",
}

// handleSuggestExternalMcp 用网关自身 LLM 为外部 MCP 候选推断接入方式：
// 调用方声明只含工具名，不含连接配置；让模型根据 server/工具名给出
// transport/command/args/url 建议，命中常见包或自建服务均可解释。

func (s *Server) handleSuggestExternalMcp(w http.ResponseWriter, r *http.Request) {
	var c struct {
		Server string `json:"server"`
		Tools  []struct {
			Name  string `json:"name"`
			Calls int    `json:"calls"`
		} `json:"tools"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil || strings.TrimSpace(c.Server) == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "server required")
		return
	}
	if len(c.Tools) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "tools required")
		return
	}
	var sb strings.Builder
	for _, t := range c.Tools {
		sb.WriteString(fmt.Sprintf("- %s（调用 %d 次）\n", t.Name, t.Calls))
	}
	prompt := fmt.Sprintf(`你是 MCP（Model Context Protocol）服务器接入助手。外部调用方在 LLM 网关里声明了以下外部 MCP 工具，但没有提供连接配置。请根据 server 名与工具名给出可直接落地的接入建议。

server 名: %s
工具声明:
%s
只输出 JSON（不要 markdown 围栏、不要任何解释文字）：
{"transport":"stdio 或 http","command":"启动命令，如 npx -y @xxx/yyy","args":["参数数组，可空"],"url":"http 模式的端点 URL；stdio 模式空串","env_hint":"可能需要配置的环境变量或密钥名；没有则空串","notes":"一句中文说明：这是公开知名包 / 自建服务 / 不确定，以及为什么"}

规则（按优先级）：
1. server 名对得上公开知名的 npx 包 → 给出确切命令（如 @modelcontextprotocol/*、serena-mcp 等），notes 说明是公开包。
2. 疑似自建/私有服务（内部项目缩写、私有工具名）→ 也要给出可执行的模板命令：优先 "npx -y <server>"，若名字明显是脚本/进程名则用 "node ./<server>.js"；args 按需；notes 写"按命名惯例推断的模板命令，实际启动方式与所需密钥请向服务提供方核实"。
3. 绝不允许 command 为空，绝不允许只输出"不确定"；拿不准就按规则 2 给模板并在 notes 里说明这是推断。`, c.Server, sb.String())
	msgs := []proxy.ChatMessage{{"role": "user", "content": prompt}}
	body, why := s.proxy.Complete(r, store.APIKey{}, "/v1/chat/completions", msgs)
	if len(body) == 0 {
		if why == "" {
			why = "无可用 chat 路由"
		}
		writeError(w, http.StatusBadGateway, "suggest_failed", "LLM 建议失败："+why)
		return
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Choices) == 0 {
		writeError(w, http.StatusBadGateway, "suggest_failed", "LLM 响应无 choices")
		return
	}
	sug, err := parseSuggestJSON(resp.Choices[0].Message.Content)
	if err != nil {
		writeError(w, http.StatusBadGateway, "suggest_failed", "LLM 未按 JSON 格式返回："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestion": sug})
}

// parseSuggestJSON 从模型输出中提取建议 JSON（兼容 ```json 围栏与前后杂文）。

func parseSuggestJSON(content string) (map[string]any, error) {
	s := strings.TrimSpace(content)
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "```"); j >= 0 {
			s = s[:j]
		}
		s = strings.TrimSpace(s)
		if i := strings.Index(s, "\n"); i >= 0 && strings.Contains(s[:i], "json") {
			s = strings.TrimSpace(s[i+1:])
		}
	}
	if i := strings.Index(s, "{"); i >= 0 {
		s = s[i:]
		if j := strings.LastIndex(s, "}"); j >= 0 {
			s = s[:j+1]
		}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	// 归一化字段
	if v, ok := out["command"].(string); ok {
		out["command"] = v
	} else {
		out["command"] = ""
	}
	if v, ok := out["transport"].(string); ok && (v == "stdio" || v == "http") {
		out["transport"] = v
	} else {
		out["transport"] = "stdio"
	}
	for _, k := range []string{"url", "env_hint", "notes"} {
		if _, ok := out[k].(string); !ok {
			out[k] = ""
		}
	}
	if _, ok := out["args"].([]any); !ok {
		out["args"] = []any{}
	}
	return out, nil
}

// handleListExternalMcpCandidates 返回外部 MCP server 候选：
// 调用方声明的 server__tool 风格工具（未命中网关能力、未在网关 MCP 配置）按 server 聚合，
// 供管理员择优接入网关（一键写入 Settings.Mcps 并常驻连接）。

func (s *Server) handleListExternalMcpCandidates(w http.ResponseWriter, r *http.Request) {
	known := map[string]bool{}
	for _, t := range s.proxy.ToolCatalog() {
		known[t.Name] = true
	}
	configured := s.store.Settings().Mcps
	ignored := map[string]bool{}
	for _, sv := range s.store.Settings().IgnoredMcpServers {
		ignored[sv] = true
	}
	byServer := map[string]map[string]int{} // server -> tool -> calls
	serverCalls := map[string]int{}
	serverKeys := map[string]int{}
	for _, t := range s.rec.ClientToolStats() {
		if known[t.Name] {
			continue
		}
		i := strings.Index(t.Name, "__")
		if i <= 0 {
			continue // 非 server__tool 风格，不是 MCP 候选
		}
		server := t.Name[:i]
		if !validProviderID(server) {
			continue
		}
		if _, ok := configured[server]; ok {
			continue // 已接入网关的 server，不算外部候选
		}
		if ignored[server] {
			continue // 管理员已忽略的 server，不显示在候选里
		}
		if byServer[server] == nil {
			byServer[server] = map[string]int{}
		}
		byServer[server][t.Name] += t.Calls
		serverCalls[server] += t.Calls
		if serverKeys[server] < t.KeyCount {
			serverKeys[server] = t.KeyCount
		}
	}
	out := make([]map[string]any, 0, len(byServer))
	for sv, tools := range byServer {
		toolList := make([]map[string]any, 0, len(tools))
		for name, calls := range tools {
			toolList = append(toolList, map[string]any{"name": name, "calls": calls})
		}
		sort.Slice(toolList, func(i, j int) bool {
			return toolList[i]["calls"].(int) > toolList[j]["calls"].(int)
		})
		out = append(out, map[string]any{
			"server": sv, "calls": serverCalls[sv], "key_count": serverKeys[sv],
			"tools":             toolList,
			"suggested_command": knownMcpCommands[sv],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["calls"].(int) > out[j]["calls"].(int)
	})
	writeJSON(w, http.StatusOK, map[string]any{"candidates": out})
}

// handleIgnoreExternalMcp 把外部 MCP 候选加入忽略列表，不再显示在候选里。
// 忽略是软删除：工具统计仍保留，只是从候选列表过滤；可通过从 config.json
// 的 settings.ignored_mcp_servers 移除来恢复。

func (s *Server) handleIgnoreExternalMcp(w http.ResponseWriter, r *http.Request) {
	server := r.PathValue("server")
	if server == "" || !validProviderID(server) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid server name")
		return
	}
	if err := s.store.Update(func(c *store.Config) error {
		for _, sv := range c.Settings.IgnoredMcpServers {
			if sv == server {
				return nil // 已在忽略列表，幂等
			}
		}
		c.Settings.IgnoredMcpServers = append(c.Settings.IgnoredMcpServers, server)
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "ignore failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "server": server, "ignored": true})
}

// handleAdoptExternalMcp 把外部 MCP 候选接入网关：写入 Settings.Mcps 并重连。
// body: {transport(stdio|http), url, command, args, env}

func (s *Server) handleAdoptExternalMcp(w http.ResponseWriter, r *http.Request) {
	server := r.PathValue("server")
	if server == "" || !validProviderID(server) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid mcp server name")
		return
	}
	var c struct {
		Transport  string            `json:"transport"`
		URL        string            `json:"url"`
		Command    string            `json:"command"`
		Args       []string          `json:"args"`
		Env        map[string]string `json:"env"`
		TimeoutSec int               `json:"timeout_sec"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&c); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if c.Transport == "" {
		c.Transport = "stdio"
	}
	if c.Transport == "http" {
		if c.URL == "" || (!strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://")) {
			writeError(w, http.StatusBadRequest, "bad_request", "valid http(s) url required for http transport")
			return
		}
	} else if c.Transport == "stdio" {
		if c.Command == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "command required for stdio transport")
			return
		}
	} else {
		writeError(w, http.StatusBadRequest, "bad_request", "transport must be stdio/http")
		return
	}
	if err := s.store.Update(func(cfg *store.Config) error {
		if cfg.Settings.Mcps == nil {
			cfg.Settings.Mcps = map[string]store.MCPServer{}
		}
		cfg.Settings.Mcps[server] = store.MCPServer{Transport: c.Transport, URL: c.URL, Command: c.Command, Args: c.Args, Env: c.Env, TimeoutSec: c.TimeoutSec}
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	s.proxy.ResetMCP()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSandboxStatus 返回 Docker 沙箱（execute_code）状态：配置 + docker 可用性 + 支持语言。

func (s *Server) handleSandboxStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.proxy.SandboxStatus())
}

// handleListMemory 返回会话记忆（remember/recall 内容，ns 形如 mem:<keyID>:<key>）。

func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"entries": s.proxy.ListMemory()})
}

// handleClearMemory 清空全部会话记忆。

func (s *Server) handleClearMemory(w http.ResponseWriter, r *http.Request) {
	s.proxy.ClearMemory()
	s.recordAudit(r, "clear", "memory", "", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleInvokeTool 一键测试工具：执行内置或 MCP 工具并返回结果文本。

func (s *Server) handleInvokeTool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "tool name required")
		return
	}
	if req.Args == nil {
		req.Args = map[string]any{}
	}
	result := s.proxy.InvokeTool(req.Name, req.Args)
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}

