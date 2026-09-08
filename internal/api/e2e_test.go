package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erishen/llm-router/internal/auth"
	"github.com/erishen/llm-router/internal/proxy"
	"github.com/erishen/llm-router/internal/quota"
	"github.com/erishen/llm-router/internal/router"
	"github.com/erishen/llm-router/internal/store"
)

// mockUpstream 是一个假的上游 LLM 服务。
type mockUpstream struct {
	mode   string // "ok" | "500" | "400" | "401" | "flaky" | "stream"
	model  string
	apiKey string
	hits   int
	bodies []string
}

func (m *mockUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.hits++
	body, _ := io.ReadAll(r.Body)
	m.bodies = append(m.bodies, string(body))
	m.apiKey = r.Header.Get("Authorization")

	// flaky：/v1/models 第一次调用直接断开连接（模拟 unexpected EOF），
	// 第二次起正常返回——用于验证探测的瞬断自动重试。
	if m.mode == "flaky" && r.URL.Path == "/v1/models" && m.hits == 1 {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		panic("hijack unavailable")
	}
	if m.mode == "500" {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
		return
	}
	if m.mode == "400" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"client bad request"}}`))
		return
	}
	if m.mode == "401" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided","type":"incorrect_api_key_error"}}`))
		return
	}
	if r.URL.Path == "/v1/models" {
		writeMockJSON(w, map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "mock-model", "object": "model", "owned_by": "mock", "context_length": 131072},
			map[string]any{"id": "mock-extra", "object": "model", "owned_by": "mock", "context_length": 262144, "is_free": true},
		}})
		return
	}
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	m.model, _ = req["model"].(string)

	if stream, _ := req["stream"].(bool); stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			_, _ = io.WriteString(w, "data: "+mustJSON(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"content": "tok"}}},
			})+"\n\n")
			fl.Flush()
		}
		_, _ = io.WriteString(w, "data: "+mustJSON(map[string]any{
			"choices": []any{},
			"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
		})+"\n\n")
		fl.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		fl.Flush()
		return
	}

	writeMockJSON(w, map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"model":   m.model,
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "hello"}}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 4, "total_tokens": 14},
	})
}

func writeMockJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// env 是一套可用于测试的运行环境。
type env struct {
	store  *store.Store
	rec    *quota.Recorder
	server *httptest.Server
	upOK   *mockUpstream
	upBad  *mockUpstream
	okURL  string
	badURL string
	srv    *Server
	plain  string
	keyID  string
}

func newEnv(t *testing.T, mode string) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := store.New(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.AdminToken = "test-admin"
		c.Settings.Pricing = map[string]store.Price{
			"default": {InputPer1K: 0.001, OutputPer1K: 0.002},
		}
		return nil
	}); err != nil {
		t.Fatalf("settings: %v", err)
	}

	upOK := &mockUpstream{mode: mode}
	upBad := &mockUpstream{mode: "500"}
	okSrv := httptest.NewServer(upOK)
	badSrv := httptest.NewServer(upBad)
	t.Cleanup(func() { okSrv.Close(); badSrv.Close() })

	if err := st.UpsertProvider(store.Provider{
		ID: "primary", Name: "Primary", BaseURL: badSrv.URL + "/v1",
		APIKey: "sk-up-primary", Models: []string{"gpt-mock"}, Enabled: true,
		Weight: 100, Priority: 1, TimeoutMS: 5000,
	}); err != nil {
		t.Fatalf("upsert primary: %v", err)
	}
	if err := st.UpsertProvider(store.Provider{
		ID: "backup", Name: "Backup", BaseURL: okSrv.URL + "/v1",
		APIKey: "sk-up-backup", Models: []string{"gpt-mock"}, Enabled: true,
		Weight: 100, Priority: 2, TimeoutMS: 5000,
	}); err != nil {
		t.Fatalf("upsert backup: %v", err)
	}
	if err := st.UpsertRoute(store.Route{
		Model: "smart", Strategy: "failover",
		Targets: []store.RouteTarget{
			{ProviderID: "primary", Model: "gpt-mock", Priority: 1, Weight: 100},
			{ProviderID: "backup", Model: "gpt-mock", Priority: 2, Weight: 100},
		},
	}); err != nil {
		t.Fatalf("upsert route: %v", err)
	}

	rec, err := quota.NewRecorder(filepath.Join(dir, "usage"))
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	limiter := quota.NewLimiter(rec)
	tracker := router.NewTracker(3, 60)
	rt := router.New(st, tracker)
	px := proxy.New(st, rt, tracker, rec)
	srv := New(Options{Store: st, Rec: rec, Limiter: limiter, Router: rt, Health: tracker, Proxy: px})

	plaintext, hash, display, err := auth.Generate()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	keyID := "k-test"
	if err := st.AddKey(store.APIKey{
		ID: keyID, Name: "test", Prefix: display, Hash: hash, Enabled: true,
		Quota: store.Quota{RPM: 100}, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("add key: %v", err)
	}

	return &env{
		store: st, rec: rec, srv: srv, upOK: upOK, upBad: upBad,
		okURL: okSrv.URL, badURL: badSrv.URL,
		server: httptest.NewServer(srv.Handler()), plain: plaintext, keyID: keyID,
	}
}

func (e *env) do(t *testing.T, method, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

func (e *env) authHeaders() map[string]string {
	return map[string]string{"Authorization": "Bearer " + e.plain, "Content-Type": "application/json"}
}

func (e *env) adminHeaders() map[string]string {
	return map[string]string{"X-Admin-Token": "test-admin", "Content-Type": "application/json"}
}

func TestHealthz(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()
	resp := e.do(t, http.MethodGet, "/healthz", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

// TestHealthzReflectsProviderHealth 验证 healthz 带上 provider 健康状态：
// 全部上游连续失败 3 次进入冷却后，degraded=true 且 providers 里能查到各 provider 状态。
// 注：路由设计是「任一失败即降级半开」——只要还有健康候选，带失败的 provider 不再接流量，
// 所以要用全失败场景才能触发累计到 fail_threshold 摘除。
func TestHealthzReflectsProviderHealth(t *testing.T) {
	e := newEnv(t, "500")
	defer e.server.Close()

	for i := 0; i < 3; i++ {
		resp := e.do(t, http.MethodPost, "/v1/chat/completions",
			`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, e.authHeaders())
		resp.Body.Close()
	}

	resp := e.do(t, http.MethodGet, "/healthz", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatalf("missing X-Request-ID header")
	}
	var body struct {
		Status    string `json:"status"`
		Degraded  bool   `json:"degraded"`
		Providers []struct {
			ProviderID string `json:"provider_id"`
			Healthy    bool   `json:"healthy"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q, want ok", body.Status)
	}
	if !body.Degraded {
		t.Fatalf("degraded = false, want true (all providers in cooldown)")
	}
	byID := map[string]bool{}
	for _, p := range body.Providers {
		byID[p.ProviderID] = p.Healthy
	}
	if byID["primary"] {
		t.Fatalf("primary should be unhealthy after 3 failures")
	}
	if byID["backup"] {
		t.Fatalf("backup should be unhealthy after 3 failures")
	}
}

// TestProviderEnvAPIKeyUsedUpstream 验证 api_key 支持 env:VAR 引用：
// 上游收到的是环境变量解析后的真实 Key，管理台回显保留 env 引用本身（不脱敏、不泄露）。
func TestProviderEnvAPIKeyUsedUpstream(t *testing.T) {
	t.Setenv("LLM_ROUTER_TEST_UPSTREAM_KEY", "sk-from-env")
	e := newEnv(t, "ok")
	defer e.server.Close()

	if err := e.store.UpsertProvider(store.Provider{
		ID: "envp", Name: "Env", BaseURL: e.okURL + "/v1",
		APIKey: "env:LLM_ROUTER_TEST_UPSTREAM_KEY", Models: []string{"gpt-mock"},
		Enabled: true, Weight: 100, Priority: 1, TimeoutMS: 5000,
	}); err != nil {
		t.Fatalf("upsert env provider: %v", err)
	}
	if err := e.store.UpsertRoute(store.Route{
		Model: "smart2", Strategy: "failover",
		Targets: []store.RouteTarget{{ProviderID: "envp", Model: "gpt-mock", Priority: 1, Weight: 100}},
	}); err != nil {
		t.Fatalf("upsert route: %v", err)
	}

	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart2","messages":[{"role":"user","content":"hi"}]}`, e.authHeaders())
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := e.upOK.apiKey; got != "Bearer sk-from-env" {
		t.Fatalf("upstream authorization = %q, want env-resolved key", got)
	}

	// 管理台回显 env 引用本身。
	resp2 := e.do(t, http.MethodGet, "/api/admin/providers", "", e.adminHeaders())
	raw, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(string(raw), `"api_key":"env:LLM_ROUTER_TEST_UPSTREAM_KEY"`) {
		t.Fatalf("env ref not echoed verbatim in admin listing: %s", raw)
	}
}

// TestMetricsEndpoint 验证 /metrics 输出 Prometheus 文本指标，
// 覆盖请求状态计数、配额拒绝计数与上游健康/请求/错误计数。
func TestMetricsEndpoint(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	// 1) 正常请求（主 key）
	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, e.authHeaders())
	resp.Body.Close()
	// 2) 未授权请求 → 401
	resp = e.do(t, http.MethodPost, "/v1/chat/completions", `{"model":"smart"}`, nil)
	resp.Body.Close()
	// 3) RPM=1 的新 Key：第一次放行，第二次 429（触发配额拒绝计数）
	plain2, hash2, display2, err := auth.Generate()
	if err != nil {
		t.Fatalf("generate rpm key: %v", err)
	}
	if err := e.store.AddKey(store.APIKey{
		ID: "k-rmp1", Name: "rmp1", Prefix: display2, Hash: hash2, Enabled: true,
		Quota: store.Quota{RPM: 1}, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("add key: %v", err)
	}
	rpmHeaders := map[string]string{"Authorization": "Bearer " + plain2, "Content-Type": "application/json"}
	resp = e.do(t, http.MethodPost, "/v1/chat/completions", `{"model":"smart"}`, rpmHeaders)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rpm key first status = %d", resp.StatusCode)
	}
	resp = e.do(t, http.MethodPost, "/v1/chat/completions", `{"model":"smart"}`, rpmHeaders)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rpm key second status = %d, want 429", resp.StatusCode)
	}

	resp = e.do(t, http.MethodGet, "/metrics", "", nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Type") == "" {
		t.Fatal("missing content-type")
	}
	body := string(raw)
	for _, want := range []string{
		"llm_router_uptime_seconds",
		`llm_router_http_requests_total{status="200"} 2`,
		`llm_router_http_requests_total{status="401"} 1`,
		`llm_router_http_requests_total{status="429"} 1`,
		`llm_router_quota_denials_total{code="rate_limited"} 1`,
		`llm_router_upstream_healthy{provider="primary"} 1`,
		`llm_router_upstream_requests_total{provider="primary"} 1`,
		`llm_router_upstream_errors_total{provider="primary"} 1`,
		`llm_router_upstream_requests_total{provider="backup"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q", want)
		}
	}
}

// TestPanicRecovery 验证 panic 兜底：未写出响应头时回 500 + request_id，
// 已写出（如 SSE 流中途）时只记日志、客户端拿到已写的部分响应。
func TestPanicRecovery(t *testing.T) {
	s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after_write") == "1" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("partial"))
		}
		panic("boom")
	})
	ts := httptest.NewServer(s.withRecovery(inner))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/x")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	rid := resp.Header.Get("X-Request-ID")
	if rid == "" {
		t.Fatalf("missing X-Request-ID header")
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), "internal_error") {
		t.Fatalf("body lacks internal_error: %s", raw)
	}
	if !strings.Contains(string(raw), rid) {
		t.Fatalf("body lacks request_id %q: %s", rid, raw)
	}

	resp2, err := http.Get(ts.URL + "/y?after_write=1")
	if err != nil {
		t.Fatalf("get after_write: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("after_write status = %d, want 200", resp2.StatusCode)
	}
	b2, _ := io.ReadAll(resp2.Body)
	if string(b2) != "partial" {
		t.Fatalf("after_write body = %q, want partial", b2)
	}
}

func TestProxyNonStream(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, e.authHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s", resp.StatusCode, b)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["model"] != "gpt-mock" {
		t.Fatalf("upstream model = %v, want gpt-mock", payload["model"])
	}
	usage := payload["usage"].(map[string]any)
	if usage["total_tokens"].(float64) != 14 {
		t.Fatalf("usage = %v", usage)
	}
	// 上游收到的是 provider 自己的 Key，而不是用户的自制 Key。
	if e.upOK.apiKey != "Bearer sk-up-backup" {
		t.Fatalf("upstream auth = %q", e.upOK.apiKey)
	}
	// 用量已记账。
	agg := e.rec.Total(e.keyID)
	if agg.Requests != 1 || agg.TotalTokens != 14 {
		t.Fatalf("usage = %+v", agg)
	}
}

func TestProxyFailoverToBackup(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	// primary 固定 500，应自动降级到 backup。
	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, e.authHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("failover failed: %d %s", resp.StatusCode, b)
	}
	if e.upBad.hits == 0 || e.upOK.hits == 0 {
		t.Fatalf("expected both providers to be tried: bad=%d ok=%d", e.upBad.hits, e.upOK.hits)
	}
}

func TestProxySetsProviderHeader(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, e.authHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// primary 固定 500，实际由 backup 服务，响应头应标注 backup。
	if got := resp.Header.Get("X-Llm-Router-Provider"); got != "backup" {
		t.Fatalf("X-Llm-Router-Provider = %q, want backup", got)
	}
}

func TestClient4xxDoesNotEvictProvider(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	// 加一个固定返回 400 的上游，用单独路由指向它。
	up4 := &mockUpstream{mode: "400"}
	srv4 := httptest.NewServer(up4)
	defer srv4.Close()
	if err := e.store.UpsertProvider(store.Provider{
		ID: "four", Name: "Four", BaseURL: srv4.URL + "/v1",
		APIKey: "sk-up-4xx", Models: []string{"gpt-mock"}, Enabled: true,
		Weight: 100, Priority: 1, TimeoutMS: 5000,
	}); err != nil {
		t.Fatalf("upsert 4xx provider: %v", err)
	}
	if err := e.store.UpsertRoute(store.Route{
		Model: "smart4", Strategy: "failover",
		Targets: []store.RouteTarget{{ProviderID: "four", Model: "gpt-mock", Priority: 1, Weight: 100}},
	}); err != nil {
		t.Fatalf("upsert route: %v", err)
	}

	// 连续 3 次 400（客户端问题），达到 fail_threshold=3，provider 也不应被熔断。
	for i := 0; i < 3; i++ {
		resp := e.do(t, http.MethodPost, "/v1/chat/completions",
			`{"model":"smart4","messages":[{"role":"user","content":"hi"}]}`, e.authHeaders())
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("request %d: status = %d, want 400", i, resp.StatusCode)
		}
	}
	if !e.srv.health.Available("four") {
		t.Fatal("provider was evicted by client-side 4xx responses")
	}
}

func TestPricingUsesUpstreamModel(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	// 给上游真实模型名设一个显著区别于 default 的单价。
	if err := e.store.Update(func(c *store.Config) error {
		c.Settings.Pricing["gpt-mock"] = store.Price{InputPer1K: 2, OutputPer1K: 3}
		return nil
	}); err != nil {
		t.Fatalf("set pricing: %v", err)
	}
	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, e.authHeaders())
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// mock 上游返回 prompt=10, completion=4 → 10/1000*2 + 4/1000*3 = 0.032。
	// 若按别名 "smart" 查价会落到 default（0.001/0.002）→ 0.000018，测试将失败。
	const want = 0.032
	if got := e.rec.Total(e.keyID).CostUSD; math.Abs(got-want) > 1e-9 {
		t.Fatalf("cost = %v, want %v (priced by upstream model)", got, want)
	}
}

func TestAdminLoginThrottledAfterFailures(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	// 连错 loginMaxFails 次后，即使口令正确也应被限流。
	for i := 0; i < loginMaxFails; i++ {
		resp := e.do(t, http.MethodPost, "/api/admin/login",
			`{"token":"wrong"}`, map[string]string{"Content-Type": "application/json"})
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("failed attempt %d: status = %d, want 401", i, resp.StatusCode)
		}
	}
	resp := e.do(t, http.MethodPost, "/api/admin/login",
		`{"token":"test-admin"}`, map[string]string{"Content-Type": "application/json"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after lockout, got %d", resp.StatusCode)
	}
}

func TestRequestBodyTooLarge(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	// 把 MaxBodyBytes 调到 64 字节，发更大的请求应得 413，而不是截断后的解析错误。
	if err := e.store.Update(func(c *store.Config) error {
		c.Settings.MaxBodyBytes = 64
		return nil
	}); err != nil {
		t.Fatalf("set max body: %v", err)
	}
	big := `{"model":"smart","messages":[{"role":"user","content":"` + strings.Repeat("x", 200) + `"}]}`
	resp := e.do(t, http.MethodPost, "/v1/chat/completions", big, e.authHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s, want 413", resp.StatusCode, b)
	}
}

func TestProxyStream(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","stream":true,"messages":[{"role":"user","content":"hi"}]}`, e.authHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	chunks, done := 0, false
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "data:") {
			chunks++
			if strings.Contains(line, "[DONE]") {
				done = true
			}
		}
	}
	if chunks < 4 || !done {
		t.Fatalf("unexpected stream: chunks=%d done=%v", chunks, done)
	}
	// 流式也应采集到 usage。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if e.rec.Total(e.keyID).TotalTokens >= 8 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := e.rec.Total(e.keyID).TotalTokens; got != 8 {
		t.Fatalf("stream usage tokens = %d, want 8", got)
	}
	// 流式请求应被注入 stream_options 以索取 usage。
	if !strings.Contains(e.upOK.bodies[len(e.upOK.bodies)-1], `"include_usage":true`) {
		t.Fatalf("stream_options not injected: %s", e.upOK.bodies[len(e.upOK.bodies)-1])
	}
}

func TestAuthRejectsBadKey(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	cases := []struct {
		name   string
		header map[string]string
		want   int
	}{
		{"missing", map[string]string{}, http.StatusUnauthorized},
		{"wrong prefix", map[string]string{"Authorization": "Bearer sk-openai-xxx"}, http.StatusUnauthorized},
		{"unknown", map[string]string{"Authorization": "Bearer " + auth.Prefix + strings.Repeat("a", 48)}, http.StatusUnauthorized},
	}
	for _, c := range cases {
		resp := e.do(t, http.MethodPost, "/v1/chat/completions", `{"model":"smart"}`, c.header)
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Fatalf("%s: status = %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
}

func TestModelsEndpoint(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()
	resp := e.do(t, http.MethodGet, "/v1/models", "", e.authHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct{ ID string } `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, m := range payload.Data {
		if m.ID == "smart" {
			found = true
		}
	}
	if !found {
		t.Fatalf("model list = %+v, want to contain smart", payload.Data)
	}
}

func TestRateLimit(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	// 把该 Key 的 RPM 调到 2，第 3 次请求应被拒。
	keys := e.store.ListKeys()
	k := keys[0]
	k.Quota.RPM = 2
	if err := e.store.Update(func(c *store.Config) error {
		for i := range c.Keys {
			if c.Keys[i].ID == k.ID {
				c.Keys[i].Quota.RPM = 2
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("update key: %v", err)
	}
	for i := 0; i < 2; i++ {
		resp := e.do(t, http.MethodPost, "/v1/chat/completions", `{"model":"smart"}`, e.authHeaders())
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status = %d", i, resp.StatusCode)
		}
	}
	resp := e.do(t, http.MethodPost, "/v1/chat/completions", `{"model":"smart"}`, e.authHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
}

func TestAdminKeyLifecycle(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	// 未授权访问管理 API。
	resp := e.do(t, http.MethodGet, "/api/admin/keys", "", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d", resp.StatusCode)
	}

	// 登录拿 session。
	resp = e.do(t, http.MethodPost, "/api/admin/login", `{"token":"test-admin"}`, map[string]string{"Content-Type": "application/json"})
	var login struct {
		SessionToken string `json:"session_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&login); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	resp.Body.Close()
	if login.SessionToken == "" {
		t.Fatal("empty session token")
	}

	// 用 session 签发新 Key，明文只出现一次。
	resp = e.do(t, http.MethodPost, "/api/admin/keys",
		`{"name":"ci","models":["smart"],"quota":{"rpm":10,"max_tokens":1000}}`,
		map[string]string{"X-Session-Token": login.SessionToken, "Content-Type": "application/json"})
	var created struct {
		Key     string `json:"key"`
		Prefix  string `json:"prefix"`
		Warning string `json:"warning"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create key: %v", err)
	}
	resp.Body.Close()
	if !strings.HasPrefix(created.Key, auth.Prefix) {
		t.Fatalf("bad key: %q", created.Key)
	}
	if created.Warning == "" {
		t.Fatal("expected one-time warning")
	}

	// 新 Key 立即可用。
	resp = e.do(t, http.MethodPost, "/v1/chat/completions", `{"model":"smart"}`,
		map[string]string{"Authorization": "Bearer " + created.Key, "Content-Type": "application/json"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("new key status = %d", resp.StatusCode)
	}

	// 列表里不应再出现明文（只有 prefix + hash）。
	resp = e.do(t, http.MethodGet, "/api/admin/keys", "", e.adminHeaders())
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), created.Key) {
		t.Fatal("plaintext key leaked in admin listing")
	}

	// 停用后失效。
	resp = e.do(t, http.MethodPost, "/api/admin/keys/"+e.keyID+"/toggle", `{"enabled":false}`,
		map[string]string{"X-Session-Token": login.SessionToken, "Content-Type": "application/json"})
	resp.Body.Close()
	resp = e.do(t, http.MethodPost, "/v1/chat/completions", `{"model":"smart"}`, e.authHeaders())
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disabled key status = %d, want 401", resp.StatusCode)
	}
}

func TestConfigPersisted(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()
	path := e.store.Path()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config is not valid json: %v", err)
	}
	if _, ok := cfg["providers"]; !ok {
		t.Fatal("providers missing in persisted config")
	}
}

// errorMessage 从 writeError 的响应体中提取 message 字段。
func errorMessage(t *testing.T, r io.Reader) string {
	t.Helper()
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(r).Decode(&payload); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	return payload.Error.Message
}

func TestProbeProviderModels(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	// 成功：探测 ok 上游（/v1/models 返回 mock-model/mock-extra，按字典序排序）
	resp := e.do(t, http.MethodPost, "/api/admin/providers/probe",
		fmt.Sprintf(`{"base_url":%q,"api_key":"sk-probe"}`, e.okURL+"/v1"), e.adminHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var payload struct {
		Models []struct {
			ID            string `json:"id"`
			ContextLength int64  `json:"context_length"`
			Free          bool   `json:"free"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Models) != 2 || payload.Models[0].ID != "mock-extra" || payload.Models[1].ID != "mock-model" {
		t.Fatalf("models = %v, want sorted [mock-extra mock-model]", payload.Models)
	}
	if payload.Models[0].ContextLength != 262144 || !payload.Models[0].Free {
		t.Fatalf("mock-extra meta = %+v, want ctx 262144 free", payload.Models[0])
	}
	if payload.Models[1].ContextLength != 131072 || payload.Models[1].Free {
		t.Fatalf("mock-model meta = %+v, want ctx 131072 not free", payload.Models[1])
	}
	if !strings.HasPrefix(e.upOK.apiKey, "Bearer sk-probe") {
		t.Fatalf("upstream auth = %q, want Bearer sk-probe", e.upOK.apiKey)
	}

	// 上游 500 → 400 + 可读错误
	resp2 := e.do(t, http.MethodPost, "/api/admin/providers/probe",
		fmt.Sprintf(`{"base_url":%q}`, e.badURL+"/v1"), e.adminHeaders())
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad upstream: status = %d", resp2.StatusCode)
	}

	// 脱敏 Key 拒绝探测
	resp3 := e.do(t, http.MethodPost, "/api/admin/providers/probe",
		`{"base_url":"http://x","api_key":"sk-…abc"}`, e.adminHeaders())
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusBadRequest {
		t.Fatalf("masked key: status = %d", resp3.StatusCode)
	}

	// env: 引用解析后带上
	t.Setenv("PROBE_ENV_KEY", "sk-env-val")
	resp4 := e.do(t, http.MethodPost, "/api/admin/providers/probe",
		fmt.Sprintf(`{"base_url":%q,"api_key":"env:PROBE_ENV_KEY"}`, e.okURL+"/v1"), e.adminHeaders())
	defer resp4.Body.Close()
	if resp4.StatusCode != http.StatusOK {
		t.Fatalf("env key: status = %d", resp4.StatusCode)
	}
	if !strings.HasPrefix(e.upOK.apiKey, "Bearer sk-env-val") {
		t.Fatalf("env upstream auth = %q", e.upOK.apiKey)
	}

	// 上游 401 + 未携带 Key → 提示需重新输入 Key（而非让用户误以为 Key 无效）
	unauth := httptest.NewServer(&mockUpstream{mode: "401"})
	defer unauth.Close()
	resp5 := e.do(t, http.MethodPost, "/api/admin/providers/probe",
		fmt.Sprintf(`{"base_url":%q}`, unauth.URL+"/v1"), e.adminHeaders())
	defer resp5.Body.Close()
	if resp5.StatusCode != http.StatusBadRequest {
		t.Fatalf("401 no key: status = %d", resp5.StatusCode)
	}
	msg5 := errorMessage(t, resp5.Body)
	if !strings.Contains(msg5, "未携带 API Key") {
		t.Fatalf("401 no key msg = %q, want 提示未携带", msg5)
	}

	// 上游 401 + 携带 Key → 提示检查 Key
	resp6 := e.do(t, http.MethodPost, "/api/admin/providers/probe",
		fmt.Sprintf(`{"base_url":%q,"api_key":"sk-wrong"}`, unauth.URL+"/v1"), e.adminHeaders())
	defer resp6.Body.Close()
	if resp6.StatusCode != http.StatusBadRequest {
		t.Fatalf("401 with key: status = %d", resp6.StatusCode)
	}
	msg6 := errorMessage(t, resp6.Body)
	if !strings.Contains(msg6, "拒绝了该 API Key") {
		t.Fatalf("401 with key msg = %q, want 提示检查 Key", msg6)
	}

	// flaky：第一次连接被中断（EOF）→ 自动重试后成功
	flaky := httptest.NewServer(&mockUpstream{mode: "flaky"})
	defer flaky.Close()
	resp7 := e.do(t, http.MethodPost, "/api/admin/providers/probe",
		fmt.Sprintf(`{"base_url":%q}`, flaky.URL+"/v1"), e.adminHeaders())
	defer resp7.Body.Close()
	if resp7.StatusCode != http.StatusOK {
		t.Fatalf("flaky: status = %d, want 200 after retry", resp7.StatusCode)
	}
	var flakyPayload struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp7.Body).Decode(&flakyPayload); err != nil {
		t.Fatalf("flaky decode: %v", err)
	}
	if len(flakyPayload.Models) != 2 {
		t.Fatalf("flaky models = %v, want 2 after retry", flakyPayload.Models)
	}
}
