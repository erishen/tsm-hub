package proxy

// MCP host：连接配置的 stdio 型 MCP server（Model Context Protocol），
// 把其工具以 mcp_<server>_<tool> 注册进网关工具池，由网关在 agent 循环中执行。
// 协议：newline-delimited JSON-RPC 2.0（initialize → notifications/initialized →
// tools/list → tools/call），零第三方依赖。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/erishen/llm-router/internal/store"
)

// mcpTool 是 MCP tools/list 返回的单个工具定义。
type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// mcpServer 持有一个 MCP server 子进程连接。
type mcpServer struct {
	name string
	cfg  store.MCPServer

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	read  *bufio.Reader
	id    int
	tools []mcpTool
	// pending 是 id → 响应 channel。
	pending map[int]chan json.RawMessage
}

// mcpManager 管理全部配置的 MCP servers。
type mcpManager struct {
	mu      sync.Mutex
	servers map[string]*mcpServer
}

func newMCPManager() *mcpManager {
	return &mcpManager{servers: map[string]*mcpServer{}}
}

// ensure 确保 server 已连接并拉取了工具列表（懒连接 + 失败重连）。
func (m *mcpManager) ensure(name string, cfg store.MCPServer) (*mcpServer, error) {
	m.mu.Lock()
	s, ok := m.servers[name]
	if ok && s.connected() {
		m.mu.Unlock()
		return s, nil
	}
	s = &mcpServer{name: name, cfg: cfg, pending: map[int]chan json.RawMessage{}}
	m.servers[name] = s
	m.mu.Unlock()

	if err := s.connect(); err != nil {
		m.mu.Lock()
		delete(m.servers, name)
		m.mu.Unlock()
		return nil, err
	}
	return s, nil
}

func (s *mcpServer) connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd != nil && s.cmd.Process != nil && s.stdin != nil
}

// connect 启动子进程并完成 initialize + tools/list。
func (s *mcpServer) connect() error {
	cmd := exec.Command(s.cfg.Command, s.cfg.Args...)
	if len(s.cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), envMap(s.cfg.Env)...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start mcp server: %w", err)
	}

	s.mu.Lock()
	s.cmd, s.stdin, s.read = cmd, stdin, bufio.NewReader(stdout)
	s.mu.Unlock()
	go s.readLoop()

	// MCP 协议握手。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "llm-router", "version": "1.0"},
	}); err != nil {
		s.close()
		return fmt.Errorf("initialize %s: %w", s.name, err)
	}
	s.notify("notifications/initialized", map[string]any{})
	res, err := s.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		s.close()
		return fmt.Errorf("tools/list %s: %w", s.name, err)
	}
	var list struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		s.close()
		return fmt.Errorf("tools/list decode %s: %w", s.name, err)
	}
	s.mu.Lock()
	s.tools = list.Tools
	s.mu.Unlock()
	return nil
}

// call 发送一个 JSON-RPC 请求并等待对应 id 的响应。
func (s *mcpServer) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	s.mu.Lock()
	s.id++
	id := s.id
	ch := make(chan json.RawMessage, 1)
	s.pending[id] = ch
	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	raw, err := json.Marshal(req)
	if err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if _, err := s.stdin.Write(append(raw, '\n')); err != nil {
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("write: %w", err)
	}
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	case msg := <-ch:
		return msg, nil
	}
}

// notify 发送一个 JSON-RPC notification（无 id、无响应）。
func (s *mcpServer) notify(method string, params map[string]any) {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	s.mu.Lock()
	if s.stdin != nil {
		_, _ = s.stdin.Write(append(raw, '\n'))
	}
	s.mu.Unlock()
}

// readLoop 持续读取 stdout，把响应投递到 pending channel。
func (s *mcpServer) readLoop() {
	for {
		line, err := s.read.ReadString('\n')
		if line != "" {
			var msg struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
				Method string `json:"method"`
			}
			if json.Unmarshal([]byte(line), &msg) == nil {
				if msg.Method != "" {
					continue // server 主动通知，忽略
				}
				s.mu.Lock()
				ch, ok := s.pending[msg.ID]
				if ok {
					delete(s.pending, msg.ID)
				}
				s.mu.Unlock()
				if !ok {
					continue
				}
				if msg.Error != nil {
					ch <- json.RawMessage(fmt.Sprintf(`{"__error__":%q}`, msg.Error.Message))
				} else {
					ch <- msg.Result
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				// server 退出：清空 pending 并标记断开。
				s.mu.Lock()
				for id, ch := range s.pending {
					ch <- json.RawMessage(`{"__error__":"mcp server exited"}`)
					delete(s.pending, id)
				}
				s.mu.Unlock()
			}
			return
		}
	}
}

func (s *mcpServer) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	s.cmd, s.stdin, s.read = nil, nil, nil
}

// toolNames 返回该 server 的工具名列表（已排序）。
func (s *mcpServer) toolNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

// mcpToolSchemas 返回全部已连接 MCP server 的工具 schema（mcp_<server>_<tool>）。
func (p *Proxy) mcpToolSchemas() []map[string]any {
	cfg := p.store.Settings().Mcps
	if len(cfg) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg))
	for n := range cfg {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []map[string]any{}
	for _, name := range names {
		s, err := p.mcps.ensure(name, cfg[name])
		if err != nil {
			continue // 连不上的 server 本次不提供工具
		}
		for _, t := range s.tools {
			params := cleanSchema(t.InputSchema)
			out = append(out, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        mcpToolName(name, t.Name),
					"description": t.Description,
					"parameters":  params,
				},
			})
		}
	}
	return out
}

// mcpExec 调用 MCP server 的工具并返回文本结果。
func (p *Proxy) mcpExec(name, tool string, args toolArgs) string {
	cfg := p.store.Settings().Mcps
	srv, ok := cfg[name]
	if !ok {
		return fmt.Sprintf("error: unknown mcp server %q", name)
	}
	s, err := p.mcps.ensure(name, srv)
	if err != nil {
		return fmt.Sprintf("error: mcp server %s unavailable: %v", name, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := s.call(ctx, "tools/call", map[string]any{
		"name":      tool,
		"arguments": map[string]any(args),
	})
	if err != nil {
		return fmt.Sprintf("error: mcp call %s/%s: %v", name, tool, err)
	}
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	_ = json.Unmarshal(res, &out)
	parts := make([]string, 0, len(out.Content))
	for _, c := range out.Content {
		if c.Type == "text" && c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	if len(parts) == 0 {
		if out.IsError {
			return "error: mcp tool returned isError=true with no text"
		}
		return "(mcp tool returned empty result)"
	}
	return strings.Join(parts, "\n")
}

// mcpToolName 生成全局唯一工具名。
func mcpToolName(server, tool string) string {
	return "mcp_" + server + "_" + tool
}

// splitMCPToolName 从全局工具名还原 server 与 tool。
func splitMCPToolName(full string) (server, tool string) {
	rest := strings.TrimPrefix(full, "mcp_")
	i := strings.Index(rest, "_")
	if i < 0 {
		return rest, ""
	}
	return rest[:i], rest[i+1:]
}

// cleanSchema 把 MCP inputSchema 清洗成 OpenAI function parameters。
func cleanSchema(s map[string]any) map[string]any {
	if s == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	out := map[string]any{}
	for k, v := range s {
		if k == "$schema" || k == "title" || k == "additionalProperties" {
			continue
		}
		out[k] = v
	}
	if _, ok := out["type"]; !ok {
		out["type"] = "object"
	}
	if _, ok := out["properties"]; !ok {
		out["properties"] = map[string]any{}
	}
	return out
}

func envMap(extra map[string]string) []string {
	out := make([]string, 0, len(extra))
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}
