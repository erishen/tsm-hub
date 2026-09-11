package proxy

// 网关内置通用工具：客户端不传 tools 时由网关自动附加并在服务端执行。
// 工具执行结果作为 role=tool 消息回填给模型，客户端拿到最终答案。

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	fetchMaxBytes = 2 << 20 // fetch_url 响应上限 2MB
	readMaxBytes  = 512 << 10
)

// toolArgs 是工具调用的参数（OpenAI function.arguments 解包后）。
type toolArgs map[string]any

func (a toolArgs) str(k string) string {
	v, _ := a[k].(string)
	return v
}

func (a toolArgs) num(k string) float64 {
	switch v := a[k].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	}
	return 0
}

// 内置工具名（工具池的排序与 schema 输出）。
var builtinTools = []string{"calc", "echo", "fetch_url", "get_time", "query_exchange_rate", "recall", "remember", "skill-run", "system_info"}

// ToolInfo 是工具池目录项（管理台 /mcps 页展示）。
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Source      string         `json:"source"` // builtin / builtin-conditional / mcp:<server>
	// Parameters 是 OpenAI function parameters schema（管理台一键测试动态表单用）。
	Parameters map[string]any `json:"parameters"`
}

// ToolCatalog 返回当前工具池目录（内置 + 条件 + MCP，MCP 触发连接）。
func (p *Proxy) ToolCatalog() []ToolInfo {
	out := make([]ToolInfo, 0, len(builtinTools)+4)
	for _, n := range builtinTools {
		s := toolSchema(n, toolDef(n))
		fn, _ := s["function"].(map[string]any)
		params, _ := fn["parameters"].(map[string]any)
		out = append(out, ToolInfo{Name: n, Description: toolDef(n), Source: "builtin", Parameters: params})
	}
	if p.store.Settings().Agent.ReadRoot != "" {
		s := toolSchema("read_file", "读取本地文件内容（白名单根目录内）")
		fn, _ := s["function"].(map[string]any)
		params, _ := fn["parameters"].(map[string]any)
		out = append(out, ToolInfo{Name: "read_file", Description: "读取本地文件内容（白名单根目录内）", Source: "builtin-conditional", Parameters: params})
		s2 := toolSchema("csv_analyze", toolDef("csv_analyze"))
		fn2, _ := s2["function"].(map[string]any)
		params2, _ := fn2["parameters"].(map[string]any)
		out = append(out, ToolInfo{Name: "csv_analyze", Description: toolDef("csv_analyze"), Source: "builtin-conditional", Parameters: params2})
	}
	if p.store.Settings().Sandbox.Enabled {
		s := toolSchema("execute_code", toolDef("execute_code"))
		fn, _ := s["function"].(map[string]any)
		params, _ := fn["parameters"].(map[string]any)
		out = append(out, ToolInfo{Name: "execute_code", Description: toolDef("execute_code"), Source: "builtin-conditional", Parameters: params})
	}
	cfg := p.store.Settings().Mcps
	names := make([]string, 0, len(cfg))
	for n := range cfg {
		names = append(names, n)
	}
	sort.Strings(names)
	// 并行 ensure：首次加载/断线重连时同时拉起全部 MCP 进程，
	// 总耗时从「串行累加」降为「最慢者」，避免工具池接口卡住页面。
	type mcpRes struct {
		name string
		s    *mcpServer
		err  error
	}
	ch := make(chan mcpRes, len(names))
	for _, name := range names {
		go func(name string) {
			s, err := p.mcps.ensure(name, cfg[name])
			ch <- mcpRes{name: name, s: s, err: err}
		}(name)
	}
	byName := make(map[string]*mcpServer, len(names))
	errs := map[string]error{}
	for range names {
		r := <-ch
		if r.err != nil {
			errs[r.name] = r.err
			continue
		}
		byName[r.name] = r.s
	}
	for _, name := range names {
		if e, bad := errs[name]; bad {
			out = append(out, ToolInfo{Name: "mcp_" + name + "_*", Description: "MCP server 连接失败: " + e.Error(), Source: "mcp:" + name})
			continue
		}
		for _, t := range byName[name].tools {
			out = append(out, ToolInfo{
				Name:        mcpToolName(name, t.Name),
				Description: t.Description,
				Source:      "mcp:" + name,
				Parameters:  cleanSchema(t.InputSchema),
			})
		}
	}
	// 晋升为工具的 fastpath 检测器（gen_*）。
	for _, pl := range p.FastToolPlugins() {
		desc := "FastPath 检测器（晋升为工具，由 LLM 生成）：输入问题文本，返回确定性答案。"
		if pl.Trigger != "" {
			desc = "FastPath 检测器：" + pl.Trigger
		}
		out = append(out, ToolInfo{
			Name:        pl.Name,
			Description: desc,
			Source:      "fastpath:" + pl.Mode,
			Parameters: map[string]any{
				"type": "object", "required": []string{"query"},
				"properties": map[string]any{"query": map[string]any{
					"type": "string", "description": "要检测/回答的问题文本",
				}},
			},
		})
	}
	// 从外部调用方录用的工具（external）。
	for _, t := range p.store.ListExternalTools() {
		params := map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}
		if t.ImplType == "js" || t.ImplType == "none" {
			params["properties"].(map[string]any)["query"] = map[string]any{
				"type": "string", "description": "要检测/回答的问题文本",
			}
			params["required"] = []string{"query"}
		}
		out = append(out, ToolInfo{
			Name:        t.Name,
			Description: orDesc(t.Description, "外部调用方声明并录用的工具"),
			Source:      "external:" + t.ImplType,
			Parameters:  params,
		})
	}
	return out
}

func orDesc(d, fallback string) string {
	if d == "" {
		return fallback
	}
	return d
}

// InvokeTool 手动调用一个工具（管理台一键测试用，keyID 用 admin 命名空间）。
func (p *Proxy) InvokeTool(name string, args map[string]any) string {
	return p.execTool("admin", name, toolArgs(args))
}

// execTool 执行工具（keyID 用于 remember/recall 的隔离命名空间）。
func (p *Proxy) execTool(keyID, name string, args toolArgs) string {
	switch name {
	case "get_time":
		return time.Now().Format("2006-01-02 15:04:05 MST")
	case "echo":
		return args.str("text")
	case "calc":
		expr := args.str("expression")
		if expr == "" {
			return "error: missing 'expression'"
		}
		v, err := evalExpr(expr)
		if err != nil {
			return "error: " + err.Error()
		}
		return formatNumber(v)
	case "fetch_url":
		s, err := p.toolFetchURL(args)
		if err != nil {
			return "error: " + err.Error()
		}
		return s
	case "query_exchange_rate":
		s, err := p.toolExchangeRate(args)
		if err != nil {
			return "error: " + err.Error()
		}
		return s
	case "system_info":
		return systemInfo()
	case "csv_analyze":
		s, err := p.toolCSVAnalyze(args)
		if err != nil {
			return "error: " + err.Error()
		}
		return s
	case "execute_code":
		s, err := p.toolExecuteCode(args)
		if err != nil {
			return "error: " + err.Error()
		}
		return s
	case "skill-run":
		s, err := p.toolSkillRun(args)
		if err != nil {
			return "error: " + err.Error()
		}
		return s
	case "read_file":
		return p.toolReadFile(args)
	case "remember":
		if p.mem == nil {
			return "error: memory store unavailable（记忆库打开失败，详见网关日志）"
		}
		ns := "mem:" + keyID + ":"
		if err := p.mem.Set(ns+args.str("key"), args.str("value")); err != nil {
			return "error: remember failed: " + err.Error()
		}
		return "remembered"
	case "recall":
		if p.mem == nil {
			return "error: memory store unavailable（记忆库打开失败，详见网关日志）"
		}
		ns := "mem:" + keyID + ":"
		v, ok := p.mem.Get(ns + args.str("key"))
		if !ok {
			return fmt.Sprintf("error: nothing remembered for %q", args.str("key"))
		}
		return v
	}
	if strings.HasPrefix(name, "mcp_") {
		server, tool := splitMCPToolName(name)
		if tool == "" {
			return fmt.Sprintf("error: bad mcp tool name %q", name)
		}
		return p.mcpExec(server, tool, args)
	}
	// MCP 客户端命名风格 server__tool（如 fs__read_file）：网关已配置该 server 时，
	// 归一化到 mcp_server_tool 执行，保证外部调用方按自己习惯声明也能命中网关能力。
	if p.store != nil {
		if i := strings.Index(name, "__"); i > 0 {
			server, tool := name[:i], name[i+2:]
			if _, ok := p.store.Settings().Mcps[server]; ok && tool != "" {
				return p.mcpExec(server, tool, args)
			}
		}
	}
	if strings.HasPrefix(name, "gen_") {
		// 晋升为工具的 fastpath 检测器：参数 query -> detect(query)。
		q := args.str("query")
		if q == "" {
			return "error: missing 'query'"
		}
		for _, pl := range p.FastToolPlugins() {
			if pl.Name != name {
				continue
			}
			if ans, hit := runJSDetector(pl.Source, q); hit {
				return ans
			}
			return fmt.Sprintf("no match: 检测器 %s 未命中问题", name)
		}
		return fmt.Sprintf("error: fastpath tool %q not promoted", name)
	}
	if p.store != nil {
		if t, ok := p.store.ExternalTool(name); ok {
			// 录用的外部工具：按实现方式执行。
			switch t.ImplType {
		case "js":
			q := args.str("query")
			if q == "" {
				return "error: missing 'query'"
			}
			if ans, hit := runJSDetector(t.ImplSource, q); hit {
				return ans
			}
			return fmt.Sprintf("no match: external tool %s 未命中问题", name)
		case "alias":
			return p.execTool(keyID, t.ImplSource, args)
			default:
				return fmt.Sprintf("error: external tool %q 无网关实现（由调用方侧执行）", name)
			}
		}
	}
	return fmt.Sprintf("error: unknown tool %q", name)
}

// toolSchemas 返回 OpenAI tools 参数（内置工具 + 按配置启用的条件工具 + MCP 工具）。
func (p *Proxy) toolSchemas() []map[string]any {
	out := make([]map[string]any, 0, len(builtinTools)+2)
	for _, n := range builtinTools {
		out = append(out, toolSchema(n, toolDef(n)))
	}
	if p.store.Settings().Agent.ReadRoot != "" {
		out = append(out, toolSchema("read_file", "读取本地文件内容（仅限白名单根目录内；目录返回其内容列表）。"))
		out = append(out, toolSchema("csv_analyze", toolDef("csv_analyze")))
	}
	if p.store.Settings().Sandbox.Enabled {
		out = append(out, toolSchema("execute_code", toolDef("execute_code")))
	}
	out = append(out, p.mcpToolSchemas()...)
	for _, pl := range p.FastToolPlugins() {
		desc := "FastPath 检测器（晋升为工具，由 LLM 生成）：输入问题文本，返回确定性答案。"
		if pl.Trigger != "" {
			desc = "FastPath 检测器：" + pl.Trigger
		}
		out = append(out, toolSchema(pl.Name, desc))
	}
	for _, t := range p.store.ListExternalTools() {
		out = append(out, toolSchema(t.Name, orDesc(t.Description, "外部调用方声明并录用的工具")))
	}
	return out
}

func toolSchema(name, desc string) map[string]any {
	params := map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}
	if strings.HasPrefix(name, "gen_") {
		params["properties"].(map[string]any)["query"] = map[string]any{
			"type": "string", "description": "要检测/回答的问题文本",
		}
		params["required"] = []string{"query"}
		return map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": desc,
				"parameters":  params,
			},
		}
	}
	switch name {
	case "calc":
		params["properties"].(map[string]any)["expression"] = map[string]any{
			"type": "string", "description": "数学表达式，如 (2+3)*4 或 sqrt(16)+max(1,5)",
		}
		params["required"] = []string{"expression"}
	case "echo":
		params["properties"].(map[string]any)["text"] = map[string]any{"type": "string", "description": "要回显的文本"}
		params["required"] = []string{"text"}
	case "fetch_url":
		params["properties"].(map[string]any)["url"] = map[string]any{
			"type": "string", "description": "http(s) URL；默认拒绝内网地址",
		}
		params["required"] = []string{"url"}
	case "skill-run":
		params["properties"].(map[string]any)["skill"] = map[string]any{
			"type": "string", "description": "技能名（GET /v1/skills 可查），如 weekly-investment",
		}
		params["required"] = []string{"skill"}
	case "read_file":
		params["properties"].(map[string]any)["path"] = map[string]any{
			"type": "string", "description": "read_root 内的路径（相对或绝对）",
		}
		params["required"] = []string{"path"}
	case "remember":
		params["properties"].(map[string]any)["key"] = map[string]any{"type": "string"}
		params["properties"].(map[string]any)["value"] = map[string]any{"type": "string"}
		params["required"] = []string{"key", "value"}
	case "recall":
		params["properties"].(map[string]any)["key"] = map[string]any{"type": "string"}
		params["required"] = []string{"key"}
	case "query_exchange_rate":
		params["properties"].(map[string]any)["from"] = map[string]any{
			"type": "string", "description": "源货币代码，如 USD/EUR/JPY/HKD/GBP",
		}
		params["properties"].(map[string]any)["to"] = map[string]any{
			"type": "string", "description": "目标货币代码，默认 CNY",
		}
		params["required"] = []string{"from"}
	case "system_info":
		// 无参数。
	case "csv_analyze":
		params["properties"].(map[string]any)["path"] = map[string]any{
			"type": "string", "description": "read_root 内的 CSV 文件路径（相对或绝对）",
		}
		params["required"] = []string{"path"}
	case "execute_code":
		params["properties"].(map[string]any)["language"] = map[string]any{
			"type": "string", "description": "语言：python / javascript / shell / java / go / rust / c / cpp（支持 py/js/sh/c++ 等别名）",
		}
		params["properties"].(map[string]any)["code"] = map[string]any{
			"type": "string", "description": "要执行的完整代码",
		}
		params["properties"].(map[string]any)["timeout"] = map[string]any{
			"type": "number", "description": "超时秒数（默认 30，最大 300）",
		}
		params["required"] = []string{"language", "code"}
	}
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": toolDef(name),
			"parameters":  params,
		},
	}
}

func toolDef(name string) string {
	switch name {
	case "get_time":
		return "获取服务器当前本地时间（含时区）。"
	case "echo":
		return "原样回显文本，用于测试。"
	case "calc":
		return "计算数学表达式，支持四则运算 + 括号 + 函数（sqrt/pow/abs/min/max/round/floor/ceil/sin/cos/tan/log/exp）。"
	case "fetch_url":
		return "抓取一个网页/API 的文本内容（仅 http/https，默认拒绝内网地址）。"
	case "skill-run":
		return "加载网关技能库中某个技能的完整说明（SKILL.md）并按其执行。"
	case "read_file":
		return "读取本地文件内容（仅限白名单根目录内；目录返回其内容列表）。"
	case "remember":
		return "记住一条信息（key/value），后续可用 recall 取回。"
	case "recall":
		return "取回之前 remember 的信息。"
	case "query_exchange_rate":
		return "查询实时汇率（open.er-api.com 免费数据）：指定源货币与目标货币（默认 CNY），返回汇率与更新时间。需要换算货币时必须调用，不要自行估算汇率。"
	case "system_info":
		return "获取网关运行环境信息：操作系统/架构/CPU 核数/内存占用/磁盘可用空间/进程启动时长。"
	case "csv_analyze":
		return "分析 read_root 内 CSV 文件的结构：行列数、列名、每列类型（数值/文本）、数值列统计与数据预览。"
	case "execute_code":
		return "在 Docker 沙箱中执行代码（python/javascript/shell/java/go/rust/c/cpp）。沙箱禁网络、只读根文件系统、限制内存/CPU/超时，执行完容器自动销毁。适用于复杂计算、数据分析、算法验证、文件处理等需要实际运行代码的场景。"
	}
	return ""
}

func (p *Proxy) toolFetchURL(args toolArgs) (string, error) {
	url := args.str("url")
	if url == "" {
		return "", fmt.Errorf("missing 'url'")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("only http/https URLs are allowed")
	}
	if !p.store.Settings().Agent.AllowPrivateURL {
		if blocked, why := isPrivateURL(url); blocked {
			return "", fmt.Errorf("blocked: %s is a private/loopback address (set agent.allow_private_url to allow)", why)
		}
	}
	ctx, cancel := p.reqTimeout(15 * time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "tsm-hub-agent/1.0")
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(b) > fetchMaxBytes {
		return "", fmt.Errorf("response exceeds %d bytes", fetchMaxBytes)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") ||
		strings.Contains(ct, "html") || strings.Contains(ct, "xml") {
		return string(b), nil
	}
	return fmt.Sprintf("(%d bytes, content-type %s — 非文本，已省略)", len(b), ct), nil
}

func (p *Proxy) toolSkillRun(args toolArgs) (string, error) {
	name := args.str("skill")
	if name == "" {
		return "", fmt.Errorf("missing 'skill'")
	}
	// 1. 目录技能（SKILL.md），与历史行为一致。
	if p.skills != nil && p.skills.Dir() != "" {
		if body := p.skills.Render(name); body != "" {
			return body, nil
		}
	}
	// 2. 录用的外部技能（kind=skill，技能说明模式）：技能说明/描述作为指令注入，
	//    与目录技能行为一致（返回说明文本，由 agent 依据执行）。
	if p.store != nil {
		for _, cand := range []string{name, "skill:" + name} {
			if t, ok := p.store.ExternalTool(cand); ok && t.Kind == "skill" {
				if s := strings.TrimSpace(t.ImplSource); s != "" {
					return s, nil
				}
				if t.Description != "" {
					return t.Description, nil
				}
				return "", fmt.Errorf("skill %q 已录用但未配置说明", name)
			}
		}
	}
	return "", fmt.Errorf("unknown skill %q", name)
}

func (p *Proxy) toolReadFile(args toolArgs) string {
	root := p.store.Settings().Agent.ReadRoot
	if root == "" {
		return "error: read_file is not enabled (no agent.read_root configured)"
	}
	path := args.str("path")
	if path == "" {
		return "error: missing 'path'"
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	clean := filepath.Clean(path)
	if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return "error: path outside read_root"
	}
	fi, err := os.Stat(clean)
	if err != nil {
		return "error: " + err.Error()
	}
	if fi.IsDir() {
		entries, err := os.ReadDir(clean)
		if err != nil {
			return "error: " + err.Error()
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return "目录内容: " + strings.Join(names, ", ")
	}
	b, err := os.ReadFile(clean)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(b) > readMaxBytes {
		return string(b[:readMaxBytes]) + fmt.Sprintf("\n…(截断, 共 %d 字节)", len(b))
	}
	return string(b)
}

func formatNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return fmt.Sprintf("%.0f", v)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.10f", v), "0"), ".")
}

// isPrivateURL 判断目标地址是否为私网/环回/链路本地（SSRF 防护）。
func isPrivateURL(raw string) (bool, string) {
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return false, ""
	}
	host := req.URL.Hostname()
	if host == "" {
		return false, ""
	}
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateIP(ip), ip.String()
	}
	addrs, err := net.LookupIP(host)
	if err != nil {
		return false, ""
	}
	for _, a := range addrs {
		if isPrivateIP(a) {
			return true, a.String()
		}
	}
	return false, ""
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsPrivate() || ip.IsMulticast() ||
		(ip.To4() != nil && ip[0] == 169 && ip[1] == 254)
}

// ListMemory 返回全部会话记忆（ns 形如 mem:<keyID>:<key>，value 为记忆内容）。
func (p *Proxy) ListMemory() []map[string]string {
	if p.mem == nil {
		return nil
	}
	out, err := p.mem.List()
	if err != nil {
		return nil
	}
	return out
}

// ClearMemory 清空全部会话记忆（remember/recall 数据，SQLite 持久化）。
func (p *Proxy) ClearMemory() {
	if p.mem == nil {
		return
	}
	_ = p.mem.Clear()
}

// CloseMemory 关闭记忆库。
func (p *Proxy) CloseMemory() {
	_ = p.mem.Close()
}

// reqTimeout 构造带超时的 context。
func (p *Proxy) reqTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// procStart 记录进程启动时间（system_info 用）。
var procStart = time.Now()

// toolExchangeRate 查询实时汇率（open.er-api.com 免费，无需 API Key）。
func (p *Proxy) toolExchangeRate(args toolArgs) (string, error) {
	from := strings.ToUpper(strings.TrimSpace(args.str("from")))
	if from == "" {
		return "", fmt.Errorf("missing 'from'")
	}
	to := strings.ToUpper(strings.TrimSpace(args.str("to")))
	if to == "" {
		to = "CNY"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://open.er-api.com/v6/latest/"+url.PathEscape(from), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "tsm-hub/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var d struct {
		Result      string             `json:"result"`
		TimeLastUTC string             `json:"time_last_update_utc"`
		Rates       map[string]float64 `json:"rates"`
		ErrorType   string             `json:"error-type"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return "", fmt.Errorf("invalid rate response: %w", err)
	}
	if d.Result != "success" {
		return fmt.Sprintf("汇率查询失败（%s）", d.ErrorType), nil
	}
	rate, ok := d.Rates[to]
	if !ok {
		return fmt.Sprintf("不支持的目标货币 %q（可用：USD/EUR/JPY/HKD/GBP/CNY 等）", to), nil
	}
	return fmt.Sprintf("1 %s = %.4f %s（更新时间 %s）", from, rate, to, d.TimeLastUTC), nil
}

// systemInfo 返回网关运行环境信息（标准库，无外部依赖）。
func systemInfo() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	host, _ := os.Hostname()
	var sb strings.Builder
	fmt.Fprintf(&sb, "host=%s os=%s arch=%s cpu_cores=%d goroutines=%d uptime=%s",
		host, runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.NumGoroutine(),
		time.Since(procStart).Round(time.Second))
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		var st syscall.Statfs_t
		if err := syscall.Statfs(".", &st); err == nil {
			avail := float64(st.Bavail) * float64(st.Bsize)
			total := float64(st.Blocks) * float64(st.Bsize)
			fmt.Fprintf(&sb, " disk_avail=%.1fGB/%.1fGB", avail/1e9, total/1e9)
		}
	}
	fmt.Fprintf(&sb, " mem_alloc=%.1fMB", float64(m.Alloc)/1e6)
	return sb.String()
}

// toolCSVAnalyze 分析 read_root 内 CSV 文件的结构（行列、列类型、数值统计、预览）。
func (p *Proxy) toolCSVAnalyze(args toolArgs) (string, error) {
	root := p.store.Settings().Agent.ReadRoot
	if root == "" {
		return "", fmt.Errorf("csv_analyze is not enabled (no agent.read_root configured)")
	}
	path := args.str("path")
	if path == "" {
		return "", fmt.Errorf("missing 'path'")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	clean := filepath.Clean(path)
	if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside read_root")
	}
	f, err := os.Open(clean)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return "", fmt.Errorf("not a readable CSV: %w", err)
	}
	if len(rows) == 0 {
		return "空 CSV（0 行 0 列）", nil
	}
	headers := rows[0]
	ncol := len(headers)
	sums := make([]float64, ncol)
	counts := make([]int, ncol)
	isNum := make([]bool, ncol)
	for i := range isNum {
		isNum[i] = true
	}
	for _, row := range rows[1:] {
		for i := 0; i < ncol && i < len(row); i++ {
			if !isNum[i] {
				continue
			}
			if v, err := strconv.ParseFloat(strings.TrimSpace(row[i]), 64); err == nil {
				sums[i] += v
				counts[i]++
			} else {
				isNum[i] = false
			}
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "文件 %s：%d 行数据，%d 列\n列: %s\n", clean, len(rows)-1, ncol, strings.Join(headers, ", "))
	for i, h := range headers {
		if isNum[i] && counts[i] > 0 {
			fmt.Fprintf(&sb, "  - %s：数值列，非空 %d 个，均值 %.2f\n", h, counts[i], sums[i]/float64(counts[i]))
		} else {
			fmt.Fprintf(&sb, "  - %s：文本列\n", h)
		}
	}
	sb.WriteString("预览（前 3 行）:\n")
	for _, row := range rows[1:4] {
		sb.WriteString("  " + strings.Join(row, " | ") + "\n")
	}
	return sb.String(), nil
}

