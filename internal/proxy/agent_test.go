package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/erishen/tsm-hub/internal/quota"
	"github.com/erishen/tsm-hub/internal/router"
	"github.com/erishen/tsm-hub/internal/skills"
	"github.com/erishen/tsm-hub/internal/store"
)

func TestEvalExpr(t *testing.T) {
	cases := map[string]float64{
		"1+2":               3,
		"2*3+4":             10,
		"(2+3)*4":           20,
		"10/4":              2.5,
		"2^10":              1024,
		"sqrt(16)":          4,
		"max(1,5,3)":        5,
		"min(7,2,9)":        2,
		"round(3.6)":        4,
		"floor(3.9)":        3,
		"ceil(3.1)":         4,
		"pow(2,8)":          256,
		"abs(-42)":          42,
		"2*(3+4)-10/2":      9,
		"1e3":               1000,
		"5%3":               2,
		"1+2*3^2":           19,
		"max(1,min(5,3))+1": 4,
	}
	for expr, want := range cases {
		got, err := evalExpr(expr)
		if err != nil || got != want {
			t.Errorf("evalExpr(%q) = %v, %v; want %v", expr, got, err, want)
		}
	}
	bad := []string{"", "1+", "(1", "1/0", "1%0", "unknown_fn(1)", "1 2", "abc"}
	for _, expr := range bad {
		if _, err := evalExpr(expr); err == nil {
			t.Errorf("evalExpr(%q) should error", expr)
		}
	}
}

func TestFormatNumber(t *testing.T) {
	if formatNumber(3) != "3" {
		t.Fatalf("formatNumber(3) = %q", formatNumber(3))
	}
	if formatNumber(2.5) != "2.5" {
		t.Fatalf("formatNumber(2.5) = %q", formatNumber(2.5))
	}
}

func TestInjectSkillBody(t *testing.T) {
	body := []byte(`{"model":"x","messages":[{"role":"user","content":"hi"}]}`)
	out := injectSkillBody(body, "SKILL TEXT")
	var m struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(m.Messages))
	}
	sys := m.Messages[0]
	if sys["role"] != "system" || sys["content"] != "SKILL TEXT" {
		t.Fatalf("bad system message: %v", sys)
	}
	// 非法 body 原样返回
	if got := injectSkillBody([]byte("not json"), "x"); string(got) != "not json" {
		t.Fatalf("invalid body should pass through")
	}
}

func TestParseAgentResponse(t *testing.T) {
	// 有 tool_calls
	body := `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"call_1","function":{"name":"calc","arguments":"{\"expression\":\"1+1\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	calls, text, usage, err := parseAgentResponse([]byte(body))
	if err != "" || text != "" || len(calls) != 1 || usage.total != 15 {
		t.Fatalf("tool_calls parse failed: %v %q %v %d", err, text, calls, usage.total)
	}
	// 有最终文本
	body2 := `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`
	calls2, text2, _, err2 := parseAgentResponse([]byte(body2))
	if err2 != "" || text2 != "hello" || calls2 != nil {
		t.Fatalf("text parse failed: %v %q", err2, text2)
	}
	// 上游错误
	body3 := `{"error":{"message":"boom"}}`
	_, _, _, err3 := parseAgentResponse([]byte(body3))
	if err3 == "" {
		t.Fatal("error should be surfaced")
	}
}

func TestExecToolBasic(t *testing.T) {
	// 纯函数工具不需要 Proxy；remember/recall 需要 SQLite 记忆库。
	p := &Proxy{}
	mem, err := openMemory(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("open memory: %v", err)
	}
	defer mem.Close()
	p.mem = mem
	if got := p.execTool("k1", "get_time", toolArgs{}); got == "" {
		t.Fatal("get_time empty")
	}
	if got := p.execTool("k1", "calc", toolArgs{"expression": "6*7"}); got != "42" {
		t.Fatalf("calc = %q", got)
	}
	if got := p.execTool("k1", "echo", toolArgs{"text": "hi"}); got != "hi" {
		t.Fatalf("echo = %q", got)
	}
	if got := p.execTool("k1", "nope", toolArgs{}); got != "error: unknown tool \"nope\"" {
		t.Fatalf("unknown = %q", got)
	}
	// remember/recall 按 key 隔离
	p.execTool("kA", "remember", toolArgs{"key": "color", "value": "red"})
	if got := p.execTool("kA", "recall", toolArgs{"key": "color"}); got != "red" {
		t.Fatalf("recall kA = %q", got)
	}
	if got := p.execTool("kB", "recall", toolArgs{"key": "color"}); got == "red" {
		t.Fatal("kB should not see kA memory")
	}
}

func TestAgentChatGate(t *testing.T) {
	// 未传 tools → 启用
	if !agentGate([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}]}`), false, false, false) {
		t.Fatal("no tools should enable agent")
	}
	// 传了 tools → 不启用（尊重客户端）
	if agentGate([]byte(`{"model":"m","messages":[],"tools":[{"type":"function"}]}`), false, false, false) {
		t.Fatal("client tools should disable agent")
	}
	// tools:null → 启用
	if !agentGate([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"tools":null}`), false, false, false) {
		t.Fatal("null tools should enable agent")
	}
	// disabled 配置 → 不启用
	if agentGate([]byte(`{"model":"m","messages":[]}`), true, false, false) {
		t.Fatal("disabled should disable agent")
	}
	// key 级 disabled → 不启用
	if agentGate([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}]}`), false, true, false) {
		t.Fatal("key disabled should disable agent")
	}
	// 请求头 off → 不启用
	if agentGate([]byte(`{"model":"m","messages":[]}`), false, false, true) {
		t.Fatal("header off should disable agent")
	}
	// 无 messages → 不启用（保持旧路径兼容）
	if agentGate([]byte(`{"model":"m"}`), false, false, false) {
		t.Fatal("no messages should disable agent")
	}
}

// newTwoCandAgentProxy 构造一个双候选（first/second）的 agent Proxy，
// handler 决定各上游的响应行为。策略 failover 保证 first 优先。
func newTwoCandAgentProxy(t *testing.T, first, second http.HandlerFunc) *Proxy {
	t.Helper()
	firstSrv := httptest.NewServer(first)
	t.Cleanup(firstSrv.Close)
	secondSrv := httptest.NewServer(second)
	t.Cleanup(secondSrv.Close)
	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.DefaultTimeoutMS = 5000
		c.Providers = []store.Provider{
			{ID: "first", Name: "first", BaseURL: firstSrv.URL + "/v1", APIKey: "k", Models: []string{"m"}, Enabled: true, Weight: 100, Priority: 1},
			{ID: "second", Name: "second", BaseURL: secondSrv.URL + "/v1", APIKey: "k", Models: []string{"m"}, Enabled: true, Weight: 100, Priority: 2},
		}
		c.Routes = []store.Route{
			{Model: "m", Strategy: "failover", Targets: []store.RouteTarget{
				{ProviderID: "first", Model: "m", Priority: 1},
				{ProviderID: "second", Model: "m", Priority: 2},
			}},
		}
		return nil
	}); err != nil {
		t.Fatalf("store update: %v", err)
	}
	h := router.NewTracker(3, 5)
	rec, err := quota.NewRecorder(filepath.Join(t.TempDir(), "usage"))
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return New(st, router.New(st, h), h, rec, skills.New(""))
}

// 客户端断开/取消时 agentRound 必须立即短路：归因当前候选、不再换下游
// （客户端已经离开，换候选只是浪费上游调用）。与主链路 attempt 一致。
func TestAgentRoundClientAbortShortCircuit(t *testing.T) {
	var secondHits int32
	p := newTwoCandAgentProxy(t,
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&secondHits, 1)
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已取消的上下文：任何上游 Do 都失败于 context canceled
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

	_, provider, errText, status, ok := p.agentRound(req, "/v1/chat/completions", "m", nil, nil)
	if ok || status != 0 {
		t.Fatalf("ok=%v status=%d, want failure", ok, status)
	}
	if provider != "first" {
		t.Fatalf("provider = %q, want first (abort must attribute current candidate)", provider)
	}
	if !strings.Contains(errText, "context canceled") {
		t.Fatalf("errText = %q, want context canceled", errText)
	}
	if n := atomic.LoadInt32(&secondHits); n != 0 {
		t.Fatalf("second upstream hits = %d, want 0 (no failover after client abort)", n)
	}
}

// 全部候选都失败时归因最后一个候选（它的错误就是最终透传的那条）；
// 历史上这里返回空 provider，用量统计出现无主记录。
func TestAgentRoundAttributesLastCandidate(t *testing.T) {
	p := newTwoCandAgentProxy(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"down"}}`))
		})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	_, provider, errText, status, ok := p.agentRound(req, "/v1/chat/completions", "m", nil, nil)
	if ok || status != 0 {
		t.Fatalf("ok=%v status=%d, want failure", ok, status)
	}
	if provider != "second" {
		t.Fatalf("provider = %q, want second (last candidate attribution)", provider)
	}
	if errText == "" {
		t.Fatal("errText empty, want last candidate error")
	}
}
