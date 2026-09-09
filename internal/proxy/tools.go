package proxy

// 网关内置通用工具：客户端不传 tools 时由网关自动附加并在服务端执行。
// 工具执行结果作为 role=tool 消息回填给模型，客户端拿到最终答案。

import (
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

// 内置工具名（工具池的排序与 schema 输出）。
var builtinTools = []string{"calc", "echo", "fetch_url", "get_time", "recall", "remember", "skill-run"}

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
	}
	cfg := p.store.Settings().Mcps
	names := make([]string, 0, len(cfg))
	for n := range cfg {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		s, err := p.mcps.ensure(name, cfg[name])
		if err != nil {
			out = append(out, ToolInfo{Name: "mcp_" + name + "_*", Description: "MCP server 连接失败: " + err.Error(), Source: "mcp:" + name})
			continue
		}
		for _, t := range s.tools {
			out = append(out, ToolInfo{
				Name:        mcpToolName(name, t.Name),
				Description: t.Description,
				Source:      "mcp:" + name,
				Parameters:  cleanSchema(t.InputSchema),
			})
		}
	}
	return out
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
	case "skill-run":
		s, err := p.toolSkillRun(args)
		if err != nil {
			return "error: " + err.Error()
		}
		return s
	case "read_file":
		return p.toolReadFile(args)
	case "remember":
		ns := "mem:" + keyID + ":"
		registry.mu.Lock()
		registry.mem[ns+args.str("key")] = args.str("value")
		registry.mu.Unlock()
		return "remembered"
	case "recall":
		ns := "mem:" + keyID + ":"
		registry.mu.RLock()
		v, ok := registry.mem[ns+args.str("key")]
		registry.mu.RUnlock()
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
	}
	out = append(out, p.mcpToolSchemas()...)
	return out
}

func toolSchema(name, desc string) map[string]any {
	params := map[string]any{"type": "object", "properties": map[string]any{}, "required": []string{}}
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
	req.Header.Set("User-Agent", "llm-router-agent/1.0")
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
	if p.skills == nil || p.skills.Dir() == "" {
		return "", fmt.Errorf("skills library not configured")
	}
	body := p.skills.Render(name)
	if body == "" {
		return "", fmt.Errorf("unknown skill %q", name)
	}
	return body, nil
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

// toolRegistry 保存 remember/recall 的会话记忆。
type toolRegistry struct {
	mu  sync.RWMutex
	mem map[string]string
}

var registry = &toolRegistry{mem: map[string]string{}}

// reqTimeout 构造带超时的 context。
func (p *Proxy) reqTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

