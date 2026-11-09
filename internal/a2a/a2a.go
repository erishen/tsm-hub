// a2a.go — A2A 标准面：Agent Card + JSON-RPC（message/send / tasks/get /
// tasks/cancel）+ 任务状态机与落盘 + peer 签名面（/peer/health、
// /peer/invoke、/peer/relay）。
//
// 任务模型与 lume-a2a 的 src/a2a.lume 对齐：
//   * 状态值域取自 A2A 规范：submitted / working / completed / failed /
//     rejected / input-required / canceled；
//   * 任务一创建就落盘（长任务崩掉时客户端看到 working 而非 not found）；
//   * 输入缺失不猜 → input-required + status.message（客户端才知道缺什么）；
//   * 多轮续发：已终结任务不可重启（-32004），合并 args（空串不覆盖）。
package a2a

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ── 状态值域（A2A 规范，不自造） ────────────────────────────────────

const (
	StateSubmitted     = "submitted"
	StateWorking       = "working"
	StateCompleted     = "completed"
	StateFailed        = "failed"
	StateRejected      = "rejected"
	StateInputRequired = "input-required"
	StateCanceled      = "canceled"
)

// IsTerminal 报告状态是否终结（tasks/cancel 与续发门共用）。
func IsTerminal(state string) bool {
	switch state {
	case StateCompleted, StateFailed, StateCanceled, StateRejected:
		return true
	default:
		return false
	}
}

// ── 任务模型 ────────────────────────────────────────────────────────

type message struct {
	Role      string   `json:"role"`
	MessageID string   `json:"messageId"`
	Kind      string   `json:"kind"`
	Parts     []part   `json:"parts"`
}

type part struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type artifact struct {
	ArtifactID string   `json:"artifactId"`
	Parts      []part   `json:"parts"`
}

type taskStatus struct {
	State     string   `json:"state"`
	Timestamp string   `json:"timestamp"`
	Message   *message `json:"message,omitempty"`
}

type taskTraceStep struct {
	Node string `json:"node"`
	At   int64  `json:"at"`
	Note string `json:"note,omitempty"`
	Cap  string `json:"cap,omitempty"`
}

type taskMeta struct {
	Capability string         `json:"capability"`
	Hops       int            `json:"hops"`
	Trace      []taskTraceStep `json:"trace"`
	Args       map[string]any `json:"args,omitempty"`
}

// Task 是 A2A 的可查询承诺。ID 只允许 [A-Za-z0-9_-]：它会拼进文件路径，
// 这是路径穿越的唯一入口。
type Task struct {
	ID        string     `json:"id"`
	ContextID string     `json:"contextId"`
	Kind      string     `json:"kind"`
	Status    taskStatus `json:"status"`
	Artifacts []artifact `json:"artifacts"`
	History   []message  `json:"history"`
	Metadata  taskMeta   `json:"metadata"`
	Node      string     `json:"node"`
	Created   int64      `json:"created"`
	Updated   int64      `json:"updated"`
}

func validTaskID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	return !strings.ContainsAny(id, `\/\:*?"<>|`)
}

// newTaskID 对齐 peer.new_task_id() 的 24 位 hex 长度语义；
// crypto/rand 直接取 12 字节，分布等价。
func newTaskID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newMessageID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nowText() string { return time.Now().Format(time.RFC3339) }

// newTask 创建 submitted 任务（调用方负责立即落盘）。
func newTask(node, ctxID, capability, text string, hops int) *Task {
	if ctxID == "" {
		ctxID = newMessageID()
	}
	now := time.Now().Unix()
	return &Task{
		ID:        newTaskID(),
		ContextID: ctxID,
		Kind:      "task",
		Status:    taskStatus{State: StateSubmitted, Timestamp: nowText()},
		Artifacts: []artifact{},
		History: []message{{Role: "user", MessageID: newMessageID(), Kind: "message",
			Parts: []part{{Kind: "text", Text: text}}}},
		Metadata: taskMeta{Capability: capability, Hops: hops,
			Trace: []taskTraceStep{{Node: node, At: now, Cap: capability, Note: "accepted"}}},
		Node:    node,
		Created: now,
		Updated: now,
	}
}

// advance 状态流转（artifacts 为 nil 时保留原产物；传空切片可清空）。
// note 非空时追加 trace 一步 —— 前端据此画"任务在各节点发生了什么"。
func (t *Task) advance(state, note string, artifacts []artifact) {
	t.Status.State = state
	t.Status.Timestamp = nowText()
	if artifacts != nil {
		t.Artifacts = artifacts
	}
	if note != "" {
		t.Metadata.Trace = append(t.Metadata.Trace, taskTraceStep{
			Node: t.Node, At: time.Now().Unix(), Note: note,
			Cap: t.Metadata.Capability,
		})
	}
	t.Updated = time.Now().Unix()
}

// advanceWithMessage 是 input-required 专用流转：提问放 status.message
// 而不是 artifacts —— 塞进 artifacts 会让客户端以为"已经有结果了"，
// 于是不再追问（lume-a2a 的 advance_with_message 注释原话）。
func (t *Task) advanceWithMessage(state, question string) {
	t.advance(state, "", nil)
	t.Status.Message = &message{
		Role: "agent", MessageID: newMessageID(), Kind: "message",
		Parts: []part{{Kind: "text", Text: question}},
	}
	t.Updated = time.Now().Unix()
}

// mergedArgs 合并多轮参数：原有 args 作底，本轮覆盖；本轮的空串不覆盖
// 已有值（客户端续发时常常只填一个字段，其余传空）。
func mergedArgs(prev, current map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range prev {
		out[k] = v
	}
	for k, v := range current {
		if s, ok := v.(string); ok && s == "" {
			continue
		}
		out[k] = v
	}
	return out
}

// firstArtifactBody 取第一个 artifact 的正文文本；取不到给空串（调用方
// 拿到的永远是字符串，少一处判空 —— 与 lume-a2a 的 first_artifact_body 同）。
func firstArtifactBody(arts []artifact) string {
	if len(arts) == 0 {
		return ""
	}
	for _, p := range arts[0].Parts {
		if p.Kind == "text" {
			return p.Text
		}
	}
	return ""
}

// ── 任务存储：按节点名隔离落盘 ──────────────────────────────────────
//
// 隔离不是洁癖：两个节点从同一数据目录启动时会"互相看到对方的任务"，
// tasks/get 能查出来别的节点的任务（lume-a2a 实测踩过）。目录名带节点名。

// TaskStore 是任务目录（内存索引 + 文件真相源）。文件是真相源：重启后
// 任务还在；内存是热索引。
type TaskStore struct {
	dir string

	mu    sync.Mutex
	index map[string]*Task
}

func NewTaskStore(baseDir, node string) *TaskStore {
	dir := filepath.Join(baseDir, "a2a-tasks", node)
	_ = os.MkdirAll(dir, 0o755)
	s := &TaskStore{dir: dir, index: map[string]*Task{}}
	s.restore()
	return s
}

// path 用 validTaskID 挡路径穿越（id 是调用方给的，tasks/get 是外部输入）。
func (s *TaskStore) path(id string) string {
	if !validTaskID(id) {
		return ""
	}
	return filepath.Join(s.dir, id+".json")
}

func (s *TaskStore) save(t *Task) error {
	p := s.path(t.ID)
	if p == "" {
		return fmt.Errorf("invalid task id %q", t.ID)
	}
	raw, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp" // 原子写：tmp + rename，中途崩了不留半截 JSON
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	s.mu.Lock()
	s.index[t.ID] = t
	s.mu.Unlock()
	return nil
}

func (s *TaskStore) load(id string) (*Task, error) {
	s.mu.Lock()
	if t, ok := s.index[id]; ok {
		s.mu.Unlock()
		return t, nil
	}
	s.mu.Unlock()
	p := s.path(id)
	if p == "" {
		return nil, os.ErrNotExist
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var t Task
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("task %s: %w", id, err)
	}
	s.mu.Lock()
	s.index[id] = &t
	s.mu.Unlock()
	return &t, nil
}

// ActiveCount 返回非终结状态的任务数（submitted / working / input-required）。
// 给 tsm.overview 用："还有几个任务没跑完"。
func (s *TaskStore) ActiveCount() int {
	s.mu.Lock()
	n := 0
	for _, t := range s.index {
		if !IsTerminal(t.Status.State) {
			n++
		}
	}
	s.mu.Unlock()
	return n
}

// recent 按 updated 倒序取前 n 条。
func (s *TaskStore) recent(n int) []*Task {
	s.mu.Lock()
	out := make([]*Task, 0, len(s.index))
	for _, t := range s.index {
		out = append(out, t)
	}
	s.mu.Unlock()
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Updated > out[j-1].Updated; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// restore 启动时把磁盘任务读回内存索引（重启后 tasks/get 仍可用）。
func (s *TaskStore) restore() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if _, err := s.load(id); err != nil {
			// 半截 JSON（写一半崩了）不阻塞启动：保留在磁盘上供排查。
			fmt.Printf("[a2a] restore task %s failed: %v\n", id, err)
		}
	}
}

// ── 能力（capabilities）─────────────────────────────────────────────
//
// 全部只读 + 目录查询，不暴露任何写入/执行能力。白名单语义与 lume-a2a
// 的 /peer/invoke 一致：只认这里声明过的名字，未知名直接拒，不兜底。

// Capability 在 Agent Card（skills[]）与 dispatch 之间是**单一来源**：
// card 与调用走同一张表，不会出现"card 里声明了但点了 404"的双真值。
type Capability struct {
	Name        string
	Title       string
	Desc        string
	Tag         string
	ArgsExample string
	// WhenToUse 是 Lume 侧 /peer/tools 清单里的 when_to_use 字段
	// （"什么时候该调我"，对齐 crm.stats 等真实节点的写法）。
	WhenToUse string
}

// DispatchResult 是一个 capability 的处置结果。State 是任务最终状态；
// Artifacts/Note 与 lume-a2a 的 dispatch() 返回 { state, artifacts, note } 同形。
type DispatchResult struct {
	State     string
	Artifacts []artifact
	Note      string
}

// DispatchFunc 实现一个 capability 的业务逻辑。
type DispatchFunc func(args map[string]any) DispatchResult

// Capabilities 是能力注册表。
type Capabilities struct {
	ordered []Capability
	table   map[string]DispatchFunc
}

func NewCapabilities() *Capabilities {
	return &Capabilities{table: map[string]DispatchFunc{}}
}

// Register 登记一个能力。重复名字以**先注册的为准**（不覆盖），
// 避免配置顺序差异造成行为漂移。
func (c *Capabilities) Register(cap Capability, fn DispatchFunc) {
	if _, exists := c.table[cap.Name]; exists {
		return
	}
	c.ordered = append(c.ordered, cap)
	c.table[cap.Name] = fn
}

func (c *Capabilities) Get(name string) (Capability, DispatchFunc, bool) {
	fn, ok := c.table[name]
	if !ok {
		return Capability{}, nil, false
	}
	for _, cap := range c.ordered {
		if cap.Name == name {
			return cap, fn, true
		}
	}
	return Capability{}, nil, false
}

func (c *Capabilities) All() []Capability { return c.ordered }

// Names 用于 input-required 的"请指定 capability: a, b, c"提示。
func (c *Capabilities) Names() []string {
	out := make([]string, len(c.ordered))
	for i, cap := range c.ordered {
		out[i] = cap.Name
	}
	return out
}

// completedArtifact 便捷构造单 text-part 的 completed 结果。
func completedArtifact(v any) DispatchResult {
	raw, err := json.Marshal(v)
	if err != nil {
		return DispatchResult{State: StateFailed, Note: "serialize result: " + err.Error()}
	}
	return DispatchResult{
		State:     StateCompleted,
		Artifacts: []artifact{{ArtifactID: newMessageID(),
			Parts: []part{{Kind: "text", Text: string(raw)}}}},
		Note: "ok",
	}
}

// ── Agent Card（发现端点，公开未认证）──────────────────────────────
//
// 规范要求 Agent Card 公开未认证：客户端必须先能发现，再决定是否认证。
// 所以这里只放能公开的信息（名称/能力/地址），不放任何凭据。

// Card 是 Agent Card 响应体。字段取 A2A v1.0，同时写 v0.3 的 url 兜底
// （老客户端只认 url，两者都写，代价几行 JSON，收益是调用方不用猜版本）。
func Card(nodeName, base, desc string, caps *Capabilities) map[string]any {
	skills := make([]map[string]any, 0, len(caps.ordered))
	for _, cap := range caps.ordered {
		tags := []string{cap.Tag}
		if cap.Tag == "" {
			tags = []string{"general"}
		}
		skills = append(skills, map[string]any{
			"id":          cap.Name,
			"name":        cap.Title,
			"description": cap.Desc,
			"tags":        tags,
			"examples":    []string{cap.ArgsExample},
		})
	}
	return map[string]any{
		"name":             nodeName,
		"description":    desc,
		"version":         "1.0.0",
		"protocolVersion":  "1.0",
		"supportedInterfaces": []map[string]any{
			{"protocolVersion": "1.0", "protocolBinding": "JSONRPC", "url": base + "/a2a"},
		},
		"url":                base + "/a2a",
		"capabilities":       map[string]any{"streaming": true, "pushNotifications": false, "agent_backed": false},
		"defaultInputModes":  []string{"text/plain", "application/json"},
		"defaultOutputModes": []string{"text/plain", "application/json"},
		"skills":             skills,
	}
}

// ── JSON-RPC 外壳 ───────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
	Params  struct {
		Message struct {
			Role    string `json:"role"`
			Parts   []struct {
				Kind string `json:"kind"`
				Text string `json:"text"`
			} `json:"parts"`
			TaskID string `json:"taskId"` // 多轮续发（A2A 规范字段）
		} `json:"message"`
		ContextID string `json:"contextId"`
		TaskID    string `json:"id"` // 多轮续发（params.id 形式，lume-a2a 也认）
		Metadata  struct {
			Capability string         `json:"capability"`
			Args       map[string]any `json:"args"`
			Hops       int            `json:"hops"`
		} `json:"metadata"`
	} `json:"params"`
}

func rpcResult(id any, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

// rpcError 与 lume-a2a 的 rpc_error() 同形。
func rpcError(id any, code int, msg string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	}
}

// messageText 从 parts 里取文本（kind=text；kind 缺省的按 text 处理），
// 多条用 \n 连接（与 lume-a2a 的 message_text() 一致）。
func (p *rpcRequest) messageText() string {
	var out []string
	for _, pt := range p.Params.Message.Parts {
		if pt.Kind == "text" || pt.Kind == "" {
			out = append(out, pt.Text)
		}
	}
	return strings.Join(out, "\n")
}

// ── A2A 服务端 ──────────────────────────────────────────────────────

// Server 把 Agent Card / /a2a / peer 签名面绑到 tsm-hub 的能力上。
type Server struct {
	Node  string
	Caps  *Capabilities
	Tasks *TaskStore
	PEER  *Peer // 可为 nil（peer 签名面关闭）
	// Uptime 返回服务已运行时长（admin 能力盘展示用；nil 时不显示）。
	Uptime func() time.Duration
}

// AdminOverview 是管理台 A2A 页的总览数据（只读，不含任何凭据）。
func (s *Server) AdminOverview() map[string]any {
	out := map[string]any{
		"node": s.Node,
		"caps": len(s.Caps.All()),
	}
	if s.Tasks != nil {
		out["tasks_active"] = s.Tasks.ActiveCount()
	}
	// peer 面状态：未配凭据 = off（fail-closed），配了 = derived/shared。
	if s.PEER == nil {
		out["peer_mode"] = "off"
	} else {
		out["peer_mode"] = string(s.PEER.Mode())
		if w := s.PEER.registryWarning(); w != "" {
			out["peer_warnings"] = []string{w}
		}
	}
	if s.Uptime != nil {
		out["uptime_secs"] = int64(s.Uptime().Seconds())
	}
	return out
}

// RecentTasks 返回最近 n 条任务（按 updated 倒序），供管理台画轨迹时间线。
func (s *Server) RecentTasks(n int) []*Task {
	if s.Tasks == nil {
		return nil
	}
	return s.Tasks.recent(n)
}

// InvokeCapability 同步执行一个已登记能力（管理台"快速调用"入口）。
// 未知能力返回 ok=false + 提示，不做兜底（与 /peer/invoke 白名单语义一致）。
func (s *Server) InvokeCapability(tool string, args map[string]any) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	if _, _, ok := s.Caps.Get(tool); !ok {
		return map[string]any{
			"ok": false, "tool": tool, "error": "unknown tool '" + tool + "'",
			"known": s.Caps.Names(),
		}
	}
	_, fn, _ := s.Caps.Get(tool)
	res := fn(args)
	return map[string]any{
		"ok":     res.State == StateCompleted,
		"tool":   tool,
		"state":  res.State,
		"result": firstArtifactBody(res.Artifacts),
		"note":   res.Note,
	}
}

// HandleAgentCard 是 GET /.well-known/agent-card.json。
func (s *Server) HandleAgentCard(w http.ResponseWriter, r *http.Request) {
	writeJSONBody(w, http.StatusOK, Card(s.Node, s.baseURL(r),
		"tsm-hub 能力节点：LLM 网关的工具/技能/MCP 目录、用量与 provider 健康查询。只读，不提供写入。", s.Caps))
}

// HandleA2A 是 POST /a2a（JSON-RPC）。
func (s *Server) HandleA2A(w http.ResponseWriter, r *http.Request) {
	// GET /a2a 一般是浏览器直接访问：重定向到管理台能力盘页，避免 405 死胡同。
	// 协议端 JSON-RPC 只走 POST（Lume 侧 A2A 客户端、Admin API 均如此），不受影响。
	if r.Method == http.MethodGet {
		http.Redirect(w, r, "/a2a-panel", http.StatusFound)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONBody(w, http.StatusOK, rpcError(nil, -32700, "parse error: "+err.Error()))
		return
	}
	switch req.Method {
	case "message/send", "tasks/send":
		s.handleSend(w, &req)
	case "tasks/get", "GetTask":
		s.handleTasksGet(w, &req)
	case "tasks/cancel", "CancelTask":
		s.handleTasksCancel(w, &req)
	default:
		writeJSONBody(w, http.StatusOK, rpcError(req.ID, -32601, "method not found: "+req.Method))
	}
}

func (s *Server) handleSend(w http.ResponseWriter, req *rpcRequest) {
	text := req.messageText()
	// 多轮续发：params.id 或 message.taskId。续发时 capability 必须从**原任务
	// 的 metadata** 取（resumeTask 里取）—— 客户端这一轮可能只带补的参数，
	// 重新按 metadata.capability 解析会把它当成"新任务 + 未指定能力"，
	// 回 input-required 让客户端再答一遍同样的问题（实测踩过的坑）。
	resume := req.Params.TaskID
	if resume == "" {
		resume = req.Params.Message.TaskID
	}
	if resume != "" {
		s.resumeTask(w, req, resume, text, req.Params.Metadata.Args)
		return
	}

	capName := req.Params.Metadata.Capability
	args := req.Params.Metadata.Args
	if args == nil {
		args = map[string]any{}
	}
	// 认不出能力：不猜、不兜底 → input-required 让客户端明确指定。
	// 猜错的代价是"看起来成功了但答非所问"，比多问一轮糟（lume-a2a 原话）。
	if capName == "" {
		t := newTask(s.Node, req.Params.ContextID, "unknown", text, req.Params.Metadata.Hops)
		t.Metadata.Args = args
		_ = s.Tasks.save(t)
		t.advanceWithMessage(StateInputRequired, "请指定 capability: "+strings.Join(s.Caps.Names(), ", "))
		_ = s.Tasks.save(t)
		writeJSONBody(w, http.StatusOK, rpcResult(req.ID, t))
		return
	}
	if _, _, ok := s.Caps.Get(capName); !ok {
		// 能力名存在但未注册：追问也没用（不会再有人补出这个名字），
		// 直接 rejected 并点名，而不是 input-required 空等。
		t := newTask(s.Node, req.Params.ContextID, capName, text, req.Params.Metadata.Hops)
		t.Metadata.Args = args
		_ = s.Tasks.save(t)
		t.advance(StateRejected, "unknown capability '"+capName+"'", []artifact{})
		_ = s.Tasks.save(t)
		writeJSONBody(w, http.StatusOK, rpcResult(req.ID, t))
		return
	}
	_, fn, _ := s.Caps.Get(capName)
	t := newTask(s.Node, req.Params.ContextID, capName, text, req.Params.Metadata.Hops)
	t.Metadata.Args = args
	_ = s.Tasks.save(t)
	t.advance(StateWorking, "dispatch "+capName, nil)
	_ = s.Tasks.save(t)

	res := fn(args)
	if res.State == StateInputRequired {
		t.advanceWithMessage(res.State, res.Note)
		_ = s.Tasks.save(t)
	} else {
		t.advance(res.State, res.Note, res.Artifacts)
		_ = s.Tasks.save(t)
	}
	writeJSONBody(w, http.StatusOK, rpcResult(req.ID, t))
}

// resumeTask 处理 input-required 后的续发。三个必须守住的点：
//  1. 已终结任务不可重启（任务不可变性）→ -32004；
//  2. 本轮参数**合并**进原有 args（空串不覆盖），不是整个替换；
//  3. 合并结果立即写回任务 —— 否则下一次续发又从旧参数开始，
//     表现为"补了还说缺"（lume-a2a 实测踩过）。
func (s *Server) resumeTask(w http.ResponseWriter, req *rpcRequest, taskID, text string, args map[string]any) {
	t, err := s.Tasks.load(taskID)
	if err != nil {
		writeJSONBody(w, http.StatusOK, rpcError(req.ID, -32001, "task not found: "+taskID))
		return
	}
	state := t.Status.State
	if IsTerminal(state) {
		writeJSONBody(w, http.StatusOK, rpcError(req.ID, -32004,
			"任务已终结("+state+"), 不可恢复; 请在同一 contextId 下发起新任务"))
		return
	}
	if state != StateInputRequired {
		// 还在跑的任务不接受续发 —— 两条并行推进会互相覆盖结果。
		writeJSONBody(w, http.StatusOK, rpcError(req.ID, -32005,
			"任务正在处理中("+state+"), 无需补充"))
		return
	}
	// 追加用户消息（多轮 history 是"完整对话轨迹"的可审计要求）。
	t.History = append(t.History, message{
		Role: "user", MessageID: newMessageID(), Kind: "message",
		Parts: []part{{Kind: "text", Text: text}},
	})
	merged := mergedArgs(t.Metadata.Args, args)
	t.Metadata.Args = merged
	// 续发时客户端可能**现在才指定 capability**（input-required 本来就是
	// "请指定 capability" 的追问）。本轮 metadata.capability 优先，缺省时
	// 回落原任务记的 capability。
	capName := req.Params.Metadata.Capability
	if capName == "" || capName == "unknown" {
		capName = t.Metadata.Capability
	}
	t.Metadata.Capability = capName
	_ = s.Tasks.save(t)

	_, fn, ok := s.Caps.Get(capName)
	if !ok {
		t.advance(StateRejected, "unknown capability '"+capName+"'", []artifact{})
		_ = s.Tasks.save(t)
	} else {
		t.advance(StateWorking, "resumed by client", nil)
		_ = s.Tasks.save(t)
		res := fn(merged)
		if res.State == StateInputRequired {
			t.advanceWithMessage(res.State, res.Note)
		} else {
			t.advance(res.State, res.Note, res.Artifacts)
		}
		_ = s.Tasks.save(t)
	}
	writeJSONBody(w, http.StatusOK, rpcResult(req.ID, t))
}

// handleTasksGet：tasks/get 的 params 直接带 {id}（lume-a2a 的 tasks/get 分支
// 读的就是 params.id）。这里同时接受 params.id 与 params.message.taskId，
// 覆盖两种客户端写法。
func (s *Server) handleTasksGet(w http.ResponseWriter, req *rpcRequest) {
	id := req.Params.TaskID
	if id == "" {
		id = req.Params.Message.TaskID
	}
	t, err := s.Tasks.load(id)
	if err != nil {
		writeJSONBody(w, http.StatusOK, rpcError(req.ID, -32001, "task not found: "+id))
		return
	}
	writeJSONBody(w, http.StatusOK, rpcResult(req.ID, t))
}

func (s *Server) handleTasksCancel(w http.ResponseWriter, req *rpcRequest) {
	id := req.Params.TaskID
	if id == "" {
		id = req.Params.Message.TaskID
	}
	t, err := s.Tasks.load(id)
	if err != nil {
		writeJSONBody(w, http.StatusOK, rpcError(req.ID, -32001, "task not found: "+id))
		return
	}
	if IsTerminal(t.Status.State) {
		writeJSONBody(w, http.StatusOK, rpcError(req.ID, -32002,
			"task not cancelable (state="+t.Status.State+")"))
		return
	}
	t.advance(StateCanceled, "canceled by client", nil)
	_ = s.Tasks.save(t)
	writeJSONBody(w, http.StatusOK, rpcResult(req.ID, t))
}

// baseURL 推导本节点对外可被连上的地址。Agent Card 里的 url 会被客户端
// **直接拿来发请求**，所以 0.0.0.0 不能原样宣告 —— 回落 127.0.0.1（与
// lume-a2a 的 base_url() 同行为，但 Lume 侧在 card 里挂 warning，Go 侧的
// card 由调用方决定是否附 warning）。
// 环境变量：TSM_HUB_A2A_PUBLIC_URL 优先，A2A_PUBLIC_URL 兜底（与 PEER_* 同款 fallback）。
func (s *Server) baseURL(r *http.Request) string {
	if v := firstNonEmptyEnv("TSM_HUB_A2A_PUBLIC_URL", "A2A_PUBLIC_URL"); v != "" {
		return v
	}
	host := r.Host
	if i := strings.LastIndex(host, ":"); i > 0 {
		if h := host[:i]; h == "0.0.0.0" || h == "::" {
			host = "127.0.0.1:" + host[i+1:]
		}
	}
	return "http://" + host
}

// ── peer 签名面（/peer/health, /peer/invoke, /peer/relay）──────────
//
// 与 Lume 节点的同名端点逐字段对齐（lume-a2a node.lume 第 632-710 行）：
// 认证参数走 query（Lume 读不到自定义请求头），body 参与 POST 签名，
// 白名单只认 Capabilities 声明过的名字。

// HandlePeerHealth 是签名 GET：身份与健康。
func (s *Server) HandlePeerHealth(w http.ResponseWriter, r *http.Request) {
	if s.PEER == nil {
		writePeerUnauthorized(w, "peer disabled: PEER_SHARED / PEER_ROOT not set")
		return
	}
	v := s.PEER.Verify(r, "GET", scopeGET(r.URL.Path))
	if !v.OK {
		writePeerUnauthorized(w, v.Err)
		return
	}
	writeJSONBody(w, http.StatusOK, map[string]any{
		"ok": true,
		"node": s.Node,
		"site": "tsm-hub",
		"peer": v.Node,
		"ts":   time.Now().Unix(),
		"mode": string(s.PEER.Mode()),
		"caps": len(s.Caps.All()),
	})
}

// HandlePeerInvoke 是签名 POST：按 tool 名执行一个能力。
// body 形状：{"tool":"<name>","arguments":{...}}（与 Lume 侧一致）。
func (s *Server) HandlePeerInvoke(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if s.PEER == nil {
		writePeerUnauthorized(w, "peer disabled: PEER_SHARED / PEER_ROOT not set")
		return
	}
	v := s.PEER.Verify(r, "POST", scopePOST(r.URL.Path, string(body)))
	if !v.OK {
		writePeerUnauthorized(w, v.Err)
		return
	}
	var req struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONBody(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "body 不是合法 JSON"})
		return
	}
	if req.Tool == "" {
		writeJSONBody(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "缺少 tool 字段"})
		return
	}
	if req.Arguments == nil {
		req.Arguments = map[string]any{}
	}
	if _, _, ok := s.Caps.Get(req.Tool); !ok {
		// 未知名直接拒，不做任何兜底转发（白名单是**层**：只信
		// "清单里有"而不信"名字看起来像"）。
		writeJSONBody(w, http.StatusOK, map[string]any{
			"ok": false, "tool": req.Tool, "error": "unknown tool '" + req.Tool + "'",
		})
		return
	}
	_, fn, _ := s.Caps.Get(req.Tool)
	res := fn(req.Arguments)
	writeJSONBody(w, http.StatusOK, map[string]any{
		"ok":     res.State == StateCompleted,
		"node":   s.Node,
		"tool":   req.Tool,
		"state":  res.State,
		"result": structuredResult(res.Artifacts),
		"note":   res.Note,
	})
}

// structuredResult 把 artifact 正文转成**结构化值**放进 /peer/invoke 的
// result 字段。Lume 侧（crm/scanner）的 result 经 bridge 序列化是嵌套
// JSON 对象，对端 fleet.invoke 的 shape_note() 会对 result 做
// get(map, key) 取值 —— 若这里是字符串就会抛 "get() expects a map or
// list" 崩掉 handler。所以：正文是合法 JSON（对象/数组）就解出来传结构，
// 不是 JSON 才落回原文字符串（此时对端 shape_note 收不到可取字段，
// 只会少一句"拿到什么"的摘要，不影响 ok/result 透传）。
func structuredResult(arts []artifact) any {
	raw := firstArtifactBody(arts)
	if raw == "" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		return parsed
	}
	return raw
}

// HandlePeerTools 是签名 GET：能力清单。
// 形状对齐 Lume 侧 /peer/tools（lume-a2a fleet probe 的第二个探测目标）：
//   {ok, node, site, summary, tools:[{name, desc, when_to_use}]}
// 清单与 Agent Card 同源（Capabilities 单一事实源），只出已登记能力，
// 不含任何凭据/配置细节。
func (s *Server) HandlePeerTools(w http.ResponseWriter, r *http.Request) {
	if s.PEER == nil {
		writePeerUnauthorized(w, "peer disabled: PEER_SHARED / PEER_ROOT not set")
		return
	}
	v := s.PEER.Verify(r, "GET", scopeGET(r.URL.Path))
	if !v.OK {
		writePeerUnauthorized(w, v.Err)
		return
	}
	tools := make([]map[string]any, 0, len(s.Caps.All()))
	for _, cap := range s.Caps.All() {
		tools = append(tools, map[string]any{
			"name":        cap.Name,
			"desc":        cap.Desc,
			"when_to_use": cap.WhenToUse,
		})
	}
	writeJSONBody(w, http.StatusOK, map[string]any{
		"ok":      true,
		"node":    s.Node,
		"site":    "tsm-hub",
		"summary": "LLM 网关的能力/工具/用量/健康查询（只读）。问『我有什么能用』『某 provider 挂没挂』『今天烧了多少』用这里。",
		"tools":   tools,
	})
}

// HandlePeerRelay 与 lume-a2a 的 /peer/relay 一致：端点保留但回 410
// （多跳转发已退役）—— 删掉路由的话，老客户端收到 404 会以为是路径写错。
// peer 面未配凭据时照例 fail-closed（先验签，验不过不给退役提示）。
func (s *Server) HandlePeerRelay(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if s.PEER == nil {
		writePeerUnauthorized(w, "peer disabled: PEER_SHARED / PEER_ROOT not set")
		return
	}
	v := s.PEER.Verify(r, "POST", scopePOST(r.URL.Path, string(body)))
	if !v.OK {
		writePeerUnauthorized(w, v.Err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusGone)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": 410,
		"ok":     false,
		"node":   s.Node,
		"error":  "relay 已退役: 多跳转发能力在项目收敛时移除。",
		"hint":   "请改用 POST /peer/invoke 直接调用本节点的能力; 能力清单见 /.well-known/agent-card.json",
	})
}

// firstNonEmptyEnv 取第一个非空的环境变量（TSM_HUB_* 优先，裸名兜底，
// 与 config/envOrFallback 的 fallback 风格一致）。
func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// writePeerUnauthorized 与 Lume 侧的响应形状对齐：Lume bridge 对 handler
// 返回的 map **一律回 HTTP 200**（bridge_serve.c 的 JSON 分支没有状态码
// 协商，只按 body 内容判断）。Lume 的 fleet probe 因此靠 json 里的
// ok/error 字段区分"认证被拒 vs 连不上"两种故障——tsm-hub 若回 401 +
// 非 JSON，Lume 侧 json() 解析失败只能看到 err，两类故障又混在一起了。
// 所以未授权也回 200 + 同样的 JSON 形状。
func writePeerUnauthorized(w http.ResponseWriter, err string) {
	writeJSONBody(w, http.StatusOK, map[string]any{
		"ok": false, "unauthorized": true, "error": err,
	})
}

func writeJSONBody(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
