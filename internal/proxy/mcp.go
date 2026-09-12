package proxy

// MCP host：连接配置的 MCP server（Model Context Protocol），
// 把其工具以 mcp_<server>_<tool> 注册进网关工具池，由网关在 agent 循环中执行。
// 传输方式：stdio（newline-delimited JSON-RPC 2.0 子进程）与
// Streamable HTTP（远程端点，POST JSON-RPC，响应可为 application/json 或 SSE）。
// 流程：initialize → notifications/initialized → tools/list → tools/call，零第三方依赖。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/erishen/tsm-hub/internal/store"
)

// stderrLog 把 MCP 子进程的 stderr 记入网关日志（原为丢弃，排查脚本崩溃全靠它）。
type stderrLog struct {
	name string
	buf  bytes.Buffer
}

func (l *stderrLog) Write(p []byte) (int, error) {
	if len(l.buf.Bytes()) > 4<<10 {
		l.buf.Reset()
	}
	l.buf.Write(p)
	if n := bytes.LastIndexByte(p, '\n'); n >= 0 {
		slog.Info("mcp stderr", "server", l.name, "line", strings.TrimSpace(string(p[:n])))
	}
	return len(p), nil
}

func (l *stderrLog) String() string { return l.buf.String() }

// mcpTool 是 MCP tools/list 返回的单个工具定义。
type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// mcpServer 持有一个 MCP server 连接（stdio 子进程或 Streamable HTTP 远程）。
type mcpServer struct {
	name string
	cfg  store.MCPServer

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
	read  *bufio.Reader
	id    int
	tools []mcpTool
	// pending 是 id → 响应 channel（stdio 用）。
	pending map[int]chan json.RawMessage
	// http 传输状态。
	httpClient *http.Client
	sessionID  string
}

// isHTTP 报告该 server 是否走 Streamable HTTP 传输。
func (s *mcpServer) isHTTP() bool {
	return s.cfg.Transport == "http" || s.cfg.URL != ""
}

// mcpManager 管理全部配置的 MCP servers。
type mcpManager struct {
	mu      sync.Mutex
	servers map[string]*mcpServer
	// store 供 supervisor 读取最新配置。
	store *store.Store
	// errs 记录最近一次连接失败原因（供管理台展示）。
	errs map[string]string
	// retry 是每个 server 的下次重试时间（失败退避）。
	retry map[string]time.Time
}

func newMCPManager() *mcpManager {
	return &mcpManager{
		servers: map[string]*mcpServer{},
		errs:    map[string]string{},
		retry:   map[string]time.Time{},
	}
}

// Start 启动 supervisor goroutine：网关启动后常驻连接全部配置的 MCP server，
// 断开后按退避自动重连，配置变更（ResetMCP）后下一轮自动重建。
func (m *mcpManager) Start(s *store.Store) {
	m.mu.Lock()
	m.store = s
	m.mu.Unlock()
	go m.supervise()
}

func (m *mcpManager) supervise() {
	// 启动后先立即预热一轮，再周期性巡检。
	for {
		m.mu.Lock()
		st := m.store
		m.mu.Unlock()
		if st != nil {
			for name, c := range st.Settings().Mcps {
				m.ensureAsync(name, c)
			}
		}
		time.Sleep(5 * time.Second)
	}
}

// ensureAsync 在后台确保 server 已连接；失败按 5~60s 退避重试，并记录原因。
func (m *mcpManager) ensureAsync(name string, cfg store.MCPServer) {
	m.mu.Lock()
	srv, ok := m.servers[name]
	if ok && srv.connected() {
		delete(m.errs, name)
		delete(m.retry, name)
		m.mu.Unlock()
		return
	}
	if next, pending := m.retry[name]; pending && time.Now().Before(next) {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	s, err := m.ensure(name, cfg)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.errs[name] = err.Error()
		// 退避：首次失败 5s 后重试；持续失败超过 30s 升级到 60s 档，避免空转。
		delay := 5 * time.Second
		if last, ok := m.retry[name]; ok && time.Since(last) > 30*time.Second {
			delay = 60 * time.Second
		}
		m.retry[name] = time.Now().Add(delay)
		return
	}
	delete(m.errs, name)
	delete(m.retry, name)
	_ = s
}

// Reset 关闭全部 MCP server 并清空连接（配置变更后调用，下次请求重新连接）。
func (m *mcpManager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, s := range m.servers {
		s.close()
		delete(m.servers, name)
	}
}

// MCPToolDetail 是单个 MCP 工具的能力定义（描述 + 参数 schema）。
type MCPToolDetail struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// MCPStatus 是单个 MCP server 的连接状态快照。
type MCPStatus struct {
	Connected   bool            `json:"connected"`
	Err         string          `json:"err,omitempty"`
	Tools       []string        `json:"tools"`
	ToolDetails []MCPToolDetail `json:"tool_details,omitempty"`
}

// MCPStatuses 返回全部配置 MCP server 的状态（供管理台展示）。
func (p *Proxy) MCPStatuses() map[string]MCPStatus {
	return p.mcps.Statuses(p.store.Settings().Mcps)
}

// StartMCP 启动 MCP supervisor：常驻连接全部配置 server，断开自动重连。
func (p *Proxy) StartMCP() {
	p.mcps.Start(p.store)
}

// ResetMCP 断开全部 MCP server（配置变更后调用）。
func (p *Proxy) ResetMCP() {
	p.mcps.Reset()
}

// Statuses 返回全部配置 MCP server 的状态（配置但未连接时返回占位 + 最近错误）。
func (m *mcpManager) Statuses(cfg map[string]store.MCPServer) map[string]MCPStatus {
	out := map[string]MCPStatus{}
	for name := range cfg {
		m.mu.Lock()
		s, ok := m.servers[name]
		errStr := m.errs[name]
		m.mu.Unlock()
		if !ok || !s.connected() {
			st := MCPStatus{Connected: false}
			if errStr != "" {
				st.Err = errStr
			}
			out[name] = st
			continue
		}
		out[name] = MCPStatus{Connected: true, Tools: s.toolNames(), ToolDetails: s.toolDetails()}
	}
	return out
}

// toolDetails 返回该 server 的工具能力定义（描述 + 参数 schema，按名排序）。
func (s *mcpServer) toolDetails() []MCPToolDetail {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MCPToolDetail, 0, len(s.tools))
	for _, t := range s.tools {
		out = append(out, MCPToolDetail{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
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
	if s.isHTTP() {
		return s.httpClient != nil && s.sessionID != ""
	}
	return s.cmd != nil && s.cmd.Process != nil && s.stdin != nil
}

// connect 建立连接并完成 initialize + tools/list。
func (s *mcpServer) connect() error {
	if s.isHTTP() {
		return s.connectHTTP()
	}
	return s.connectStdio()
}

func (s *mcpServer) connectStdio() error {
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
	cmd.Stderr = &stderrLog{name: s.name}
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
		"clientInfo":      map[string]any{"name": "tsm-hub", "version": "1.0"},
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

// connectHTTP 连接 Streamable HTTP 型 MCP server（initialize + initialized + tools/list）。
// 会话：每次调用为独立 JSON-RPC POST（协议允许无状态），拿到 Mcp-Session-Id 后复用；
// 会话失效时下次调用自动重新 initialize。
func (s *mcpServer) connectHTTP() error {
	s.mu.Lock()
	s.httpClient = &http.Client{Timeout: 15 * time.Second}
	s.sessionID = ""
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := s.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "tsm-hub", "version": "1.0"},
	}); err != nil {
		return fmt.Errorf("initialize %s: %w", s.name, err)
	}
	s.notify("notifications/initialized", map[string]any{})
	res, err := s.call(ctx, "tools/list", map[string]any{})
	if err != nil {
		return fmt.Errorf("tools/list %s: %w", s.name, err)
	}
	var list struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		return fmt.Errorf("tools/list decode %s: %w", s.name, err)
	}
	s.mu.Lock()
	s.tools = list.Tools
	s.mu.Unlock()
	return nil
}

// ensureHTTPInit 保证 http 会话已 initialize（连接后 / 会话失效时调用）。
func (s *mcpServer) ensureHTTPInit() error {
	s.mu.Lock()
	ok := s.httpClient != nil && s.sessionID != ""
	s.mu.Unlock()
	if ok {
		return nil
	}
	return s.connectHTTP()
}

// call 发送一个 JSON-RPC 请求并等待对应 id 的响应。
func (s *mcpServer) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if s.isHTTP() {
		return s.httpCall(ctx, method, params)
	}
	s.mu.Lock()
	if s.stdin == nil {
		// 连接已被 Reset/close 中断（配置变更与重建竞态）：返回可恢复错误，
		// 由 supervisor 退避重建，而不是 nil 指针 panic。
		s.mu.Unlock()
		return nil, fmt.Errorf("mcp %s: connection closed", s.name)
	}
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

// httpCall 通过 Streamable HTTP 发送一个 JSON-RPC 请求。
// 响应支持 application/json 与 text/event-stream 两种 Content-Type。
func (s *mcpServer) httpCall(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	if method != "initialize" {
		if err := s.ensureHTTPInit(); err != nil {
			return nil, err
		}
	}
	s.mu.Lock()
	s.id++
	id := s.id
	payload := map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
	raw, _ := json.Marshal(payload)
	url := s.cfg.URL
	sess := s.sessionID
	s.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sess != "" {
		req.Header.Set("Mcp-Session-Id", sess)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		s.mu.Lock()
		s.sessionID = sid
		s.mu.Unlock()
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return nil, fmt.Errorf("mcp http %d: %s", resp.StatusCode, compact(string(b)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		// SSE 封装：取最后一条 data: 行作为 JSON-RPC 响应。
		last := ""
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data:") {
				last = strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			}
		}
		body = []byte(last)
	}
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("mcp decode: %w", err)
	}
	if r.Error != nil {
		return nil, fmt.Errorf("mcp error: %s", r.Error.Message)
	}
	return r.Result, nil
}

// notify 发送一个 JSON-RPC notification（无 id、无响应）。
func (s *mcpServer) notify(method string, params map[string]any) {
	if s.isHTTP() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if s.ensureHTTPInit() == nil {
			_, _ = s.httpCall(ctx, method, params)
		}
		return
	}
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
				// server 退出：清空 pending 并标记断开。子进程已死，必须把
				// cmd/stdin 置 nil 让 connected() 返回 false，否则下次调用
				// 会复用它写 broken pipe（而 ensure 以为它还活着）。
				s.mu.Lock()
				for id, ch := range s.pending {
					ch <- json.RawMessage(`{"__error__":"mcp server exited"}`)
					delete(s.pending, id)
				}
				s.cmd, s.stdin, s.read = nil, nil, nil
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
	s.sessionID = ""
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
// 首次会并行触发各 server 连接，避免一个慢 server 拖住整个工具池。
func (p *Proxy) mcpToolSchemas() []map[string]any {
	return p.mcpToolSchemasAllow(nil, nil)
}

// mcpToolSchemasAllow 枚举已配置 MCP server 的工具 schema。
//   - serverAllow 非 nil 时：不在名单内的 server 直接跳过——既不建连也不枚举
//     （对应 key.McpsAllow，省 token + 省握手）。
//   - toolAllow 非 nil 时：只保留完整工具名（mcp_server_tool）在名单内的工具
//     （对应 key.ToolsAllow）。
func (p *Proxy) mcpToolSchemasAllow(serverAllow, toolAllow map[string]bool) []map[string]any {
	cfg := p.store.Settings().Mcps
	if len(cfg) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg))
	for n := range cfg {
		if serverAllow != nil && !serverAllow[n] {
			continue
		}
		names = append(names, n)
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	results := make([][]map[string]any, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			s, err := p.mcps.ensure(name, cfg[name])
			if err != nil {
				return // 连不上的 server 本次不提供工具
			}
			for _, t := range s.tools {
				full := mcpToolName(name, t.Name)
				if toolAllow != nil && !toolAllow[full] {
					continue
				}
				params := cleanSchema(t.InputSchema)
				results[i] = append(results[i], map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        full,
						"description": truncateDesc(t.Description, maxToolDescRunes),
						"parameters":  params,
					},
				})
			}
		}(i, name)
	}
	wg.Wait()
	out := []map[string]any{}
	for _, r := range results {
		out = append(out, r...)
	}
	return out
}

// mcpCallTimeout 返回某 MCP server 的工具调用超时：配置了 timeout_sec 用之，
// 否则默认 30s。
func (p *Proxy) mcpCallTimeout(srv store.MCPServer) time.Duration {
	if srv.TimeoutSec > 0 {
		return time.Duration(srv.TimeoutSec) * time.Second
	}
	return 30 * time.Second
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
	ctx, cancel := context.WithTimeout(context.Background(), p.mcpCallTimeout(srv))
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
// 同时做描述瘦身：截断 property 级 description（官方 MCP 的参数描述普遍啰嗦，
// 是 schema token 的大头）。
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
	if props, ok := out["properties"].(map[string]any); ok {
		for name, pv := range props {
			pm, ok := pv.(map[string]any)
			if !ok {
				continue
			}
			if d, ok := pm["description"].(string); ok && len(d) > 0 {
				pm["description"] = truncateDesc(d, maxParamDescRunes)
			}
			props[name] = pm
		}
	}
	if _, ok := out["type"]; !ok {
		out["type"] = "object"
	}
	if _, ok := out["properties"]; !ok {
		out["properties"] = map[string]any{}
	}
	return out
}

// 描述截断预算（rune 数）：官方 MCP 工具描述动辄数百字，是 schema token 大头。
// 截断保留首句（到中/英文句号、问叹号为止）且不超预算，语义足够模型选型。
const (
	maxToolDescRunes  = 200
	maxParamDescRunes = 120
)

// truncateDesc 把描述截到预算内：优先在句子边界截断（保留完整首句），
// 首句本身超预算时硬截 rune（不会切出半个多字节字符）。
func truncateDesc(s string, max int) string {
	if max <= 0 || len(s) <= max { // len(s) 是字节下界：字节都不超则 rune 必不超
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	// 在前 max 个 rune 里找最后一个句子边界（。／.／！／?／！／？）。
	last := -1
	for i := 0; i < max; i++ {
		r := runes[i]
		isSent := r == '。' || r == '！' || r == '？' || r == '!' || r == '?' || r == '.'
		if !isSent {
			continue
		}
		// 英文句号排除小数/版本号（前后都是数字则不算边界）。
		if r == '.' && i > 0 && i < max-1 &&
			runes[i-1] >= '0' && runes[i-1] <= '9' && runes[i+1] >= '0' && runes[i+1] <= '9' {
			continue
		}
		last = i + 1
	}
	if last > 0 && last >= max/3 { // 边界太靠前（如 "e.g." 之后）不如硬截
		return string(runes[:last])
	}
	return string(runes[:max])
}

func envMap(extra map[string]string) []string {
	out := make([]string, 0, len(extra))
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}
