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

	"github.com/erishen/tsm-gateway/internal/auth"
	"github.com/erishen/tsm-gateway/internal/proxy"
	"github.com/erishen/tsm-gateway/internal/quota"
	"github.com/erishen/tsm-gateway/internal/router"
	"github.com/erishen/tsm-gateway/internal/store"
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
	// error200：HTTP 200 但 body 是 OpenAI 错误体（部分上游过载时如此），应触发 failover。
	if m.mode == "error200" {
		writeMockJSON(w, map[string]any{"error": map[string]any{
			"code": 502, "message": "Upstream error from Nvidia: Service temporarily overloaded",
		}})
		return
	}
	if r.URL.Path == "/v1/models" {
		writeMockJSON(w, map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "mock-model", "object": "model", "owned_by": "mock", "context_length": 131072,
				"pricing": map[string]any{"prompt": "0.45", "completion": "0.90"}},
			map[string]any{"id": "mock-extra", "object": "model", "owned_by": "mock", "context_length": 262144, "is_free": true},
		}})
		return
	}
	if r.URL.Path == "/v1/users/me/balance" {
		writeMockJSON(w, map[string]any{
			"code": 0,
			"data": map[string]any{
				"available_balance": 14.99736,
				"voucher_balance":   14.99736,
				"cash_balance":      0,
			},
			"status": true,
		})
		return
	}
	if r.URL.Path == "/v1/user/balance" {
		// DeepSeek 风格：余额字段是字符串
		writeMockJSON(w, map[string]any{
			"is_available": true,
			"balance_infos": []any{
				map[string]any{"currency": "CNY", "total_balance": "51.75", "granted_balance": "0.00", "topped_up_balance": "51.75"},
			},
		})
		return
	}
	// agent 模式：第一轮返回 calc tool_call，第二轮（带 tool 结果）返回最终答案。
	if m.mode == "agent" {
		var req2 map[string]any
		_ = json.Unmarshal(body, &req2)
		msgs, _ := req2["messages"].([]any)
		hasTool := false
		for _, mm := range msgs {
			if role, ok := mm.(map[string]any)["role"].(string); ok && role == "tool" {
				hasTool = true
			}
		}
		if !hasTool {
			writeMockJSON(w, map[string]any{
				"id": "chatcmpl-mock-agent", "object": "chat.completion", "model": m.model,
				"choices": []any{map[string]any{"message": map[string]any{
					"role": "assistant", "content": nil,
					"tool_calls": []any{map[string]any{
						"id": "call_1", "type": "function",
						"function": map[string]any{"name": "calc", "arguments": `{"expression":"1+1"}`},
					}},
				}}},
				"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
			})
			return
		}
		writeMockJSON(w, map[string]any{
			"id": "chatcmpl-mock-agent2", "object": "chat.completion", "model": m.model,
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "答案是 2（已用 calc 工具计算）"}}},
			"usage":   map[string]any{"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30},
		})
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
		// e2e 测 agent/路由，确定性快路径会拦截算术类消息，先禁用。
		c.Settings.Fastpath.Enabled = false
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
	px := proxy.New(st, rt, tracker, rec, nil)
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

// passthruHeaders 显式关闭网关 agent，用于验证纯透传路径（旧行为回归）。
func (e *env) passthruHeaders() map[string]string {
	h := e.authHeaders()
	h["X-Llm-Router-Agent"] = "off"
	return h
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

// TestModelsCatalog 验证模型目录：合并去重、知识表归类/用途/免费/定价、未收录模型按 id 推断。
func TestModelsCatalog(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	upsert := func(id, base string, models ...string) {
		if err := e.store.UpsertProvider(store.Provider{
			ID: id, Name: id, BaseURL: base, APIKey: "sk-x", Models: models,
		}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	upsert("cat-agnes", "http://a/v1", "agnes-2.0-flash", "agnes-video-v2.0")
	upsert("cat-kimi", "http://k/v1", "kimi-k2.7-code", "agnes-2.0-flash") // 重复模型 → 合并 providers
	upsert("cat-or", "http://o/v1", "nex-agi/nex-n2.5-mini:free", "brand-new-video-gen")

	// 探测快照覆盖：模拟 cat-agnes 最近一次探测，上游把 agnes-2.0-flash 改成收费（$0.01/$0.02）且上下文 200K。
	if err := e.store.UpsertProvider(store.Provider{
		ID: "cat-agnes", Name: "cat-agnes", BaseURL: "http://a/v1", APIKey: "sk-x",
		Models:  []string{"agnes-2.0-flash", "agnes-video-v2.0"},
		ProbeAt: time.Now(),
		ProbeModels: []store.ProbeModel{
			{ID: "agnes-2.0-flash", ContextLength: 200000, Free: false,
				Pricing: &store.Pricing{Prompt: "0.01", Completion: "0.02"}},
		},
	}); err != nil {
		t.Fatalf("upsert snapshot: %v", err)
	}

	resp := e.do(t, http.MethodGet, "/api/admin/models/catalog", "", e.adminHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var payload struct {
		ProbeAt string `json:"probe_at"`
		Models  []struct {
			ID            string `json:"id"`
			Provider      string `json:"provider"`
			Category      string `json:"category"`
			Purpose       string `json:"purpose"`
			Ctx           string `json:"context"`
			ContextLength int    `json:"context_length"`
			Free          bool   `json:"free"`
			Pricing       *struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byKey := map[string]struct {
		Provider      string
		Category      string
		Ctx           string
		ContextLength int
		Free          bool
		Pricing       *struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		}
	}{}
	for _, m := range payload.Models {
		byKey[m.ID+"@"+m.Provider] = struct {
			Provider      string
			Category      string
			Ctx           string
			ContextLength int
			Free          bool
			Pricing       *struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			}
		}{m.Provider, m.Category, m.Ctx, m.ContextLength, m.Free, m.Pricing}
	}
	// 同一模型在不同 Provider 分行展示，各自独立免费/价格状态。
	aAgnes := byKey["agnes-2.0-flash@cat-agnes"]
	if aAgnes.Provider != "cat-agnes" {
		t.Fatalf("agnes@cat-agnes missing: %+v", aAgnes)
	}
	// 快照优先：cat-agnes 最近探测把 agnes-2.0-flash 改成收费（$0.01/$0.02）且上下文 200K。
	if aAgnes.ContextLength != 200000 || aAgnes.Free || aAgnes.Pricing == nil ||
		aAgnes.Pricing.Prompt != "0.01" || aAgnes.Pricing.Completion != "0.02" {
		t.Fatalf("agnes@cat-agnes = %+v, want snapshot 200K/paid/$0.01-$0.02", aAgnes)
	}
	// cat-kimi 无探测快照 → 保持静态表 256K/FREE。
	aKimi := byKey["agnes-2.0-flash@cat-kimi"]
	if aKimi.Provider != "cat-kimi" || aKimi.Free || aKimi.Ctx != "256K" {
		t.Fatalf("agnes@cat-kimi = %+v, want static 256K", aKimi)
	}
	if payload.ProbeAt == "" {
		t.Fatalf("probe_at empty, want latest probe timestamp")
	}
	k := byKey["kimi-k2.7-code@cat-kimi"]
	if k.Category != "text" || k.Ctx != "256K" {
		t.Fatalf("kimi-k2.7-code = %+v, want text/256K", k)
	}
	o := byKey["nex-agi/nex-n2.5-mini:free@cat-or"]
	if o.Category != "text" || !o.Free {
		t.Fatalf("nex :free = %+v, want text/free", o)
	}
	v := byKey["brand-new-video-gen@cat-or"]
	if v.Category != "video" || v.Pricing != nil {
		t.Fatalf("brand-new-video-gen = %+v, want inferred video/no pricing", v)
	}
	// mock-local 不进目录
	for _, m := range payload.Models {
		if m.ID == "mock-model" || m.ID == "mock-extra" {
			t.Fatalf("mock-local model %s should be excluded", m.ID)
		}
	}
}

// TestRefreshModels 验证批量刷新：并行探测所有有 Key 的 Provider 写快照，
// 返回目录 + 各 Provider 状态；失败项不阻塞、无 Key 项跳过。
func TestRefreshModels(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	// ok 上游：/v1/models 返回 mock-model（付费）+ mock-extra（免费）
	if err := e.store.UpsertProvider(store.Provider{
		ID: "rf-a", Name: "A", BaseURL: e.okURL + "/v1", APIKey: "sk-tr-a",
		Models: []string{"mock-model"},
	}); err != nil {
		t.Fatalf("upsert a: %v", err)
	}
	// 无 Key：应跳过（不参与探测）
	if err := e.store.UpsertProvider(store.Provider{
		ID: "rf-b", Name: "B", BaseURL: "http://127.0.0.1:9/v1", APIKey: "",
		Models: []string{"x"},
	}); err != nil {
		t.Fatalf("upsert b: %v", err)
	}
	// 坏上游：应失败但不阻塞
	if err := e.store.UpsertProvider(store.Provider{
		ID: "rf-c", Name: "C", BaseURL: "http://127.0.0.1:1/v1", APIKey: "sk-c",
		Models: []string{"y"},
	}); err != nil {
		t.Fatalf("upsert c: %v", err)
	}

	resp := e.do(t, http.MethodPost, "/api/admin/models/refresh", "", e.adminHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var payload struct {
		ProbeAt string `json:"probe_at"`
		Models  []struct {
			ID   string `json:"id"`
			Free bool   `json:"free"`
			Ctx  int    `json:"context_length"`
		} `json:"models"`
		Providers map[string]string `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.ProbeAt == "" {
		t.Fatalf("probe_at empty")
	}
	if payload.Providers["rf-a"] != "ok" {
		t.Fatalf("rf-a = %q, want ok", payload.Providers["rf-a"])
	}
	if _, ok := payload.Providers["rf-b"]; ok {
		t.Fatalf("rf-b (no key) should be skipped")
	}
	if payload.Providers["rf-c"] == "" || payload.Providers["rf-c"] == "ok" {
		t.Fatalf("rf-c = %q, want failure message", payload.Providers["rf-c"])
	}
	found := false
	for _, m := range payload.Models {
		if m.ID == "mock-model" && !m.Free {
			found = true
		}
	}
	if !found {
		t.Fatalf("mock-model not in refreshed catalog")
	}
	// 快照已写回
	ps := e.store.ListProviders()
	var a *store.Provider
	for i := range ps {
		if ps[i].ID == "rf-a" {
			a = &ps[i]
		}
	}
	if a == nil || len(a.ProbeModels) == 0 || a.ProbeAt.IsZero() {
		t.Fatalf("rf-a snapshot not persisted: %+v", a)
	}
}

// TestProviderBalances 验证批量余额查询：带 Key 的 Provider 返回余额，
// 无 Key 的返回 no_key，失败项不阻塞其余项。
func TestProviderBalances(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	if err := e.store.UpsertProvider(store.Provider{
		ID: "bal-p1", Name: "Bal Provider", BaseURL: e.okURL + "/v1",
		APIKey: "sk-balance", Models: []string{"mock-model"},
	}); err != nil {
		t.Fatalf("upsert p1: %v", err)
	}
	if err := e.store.UpsertProvider(store.Provider{
		ID: "bal-p2", Name: "No Key", BaseURL: e.okURL + "/v1", Models: []string{"mock-model"},
	}); err != nil {
		t.Fatalf("upsert p2: %v", err)
	}
	// mock-local（一键 Mock 联调）不应出现在额度列表
	if err := e.store.UpsertProvider(store.Provider{
		ID: "mock-local", Name: "Mock 本地联调", BaseURL: e.okURL + "/v1", Models: []string{"mock-model"},
	}); err != nil {
		t.Fatalf("upsert mock-local: %v", err)
	}
	// deepseek 风格：独立 mock 仅提供 /v1/user/balance（字符串余额字段），
	// 避免被通用 mock 的 moonshot 余额端点抢先命中。
	dsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/user/balance" {
			writeMockJSON(w, map[string]any{
				"is_available": true,
				"balance_infos": []any{
					map[string]any{"currency": "CNY", "total_balance": "51.75", "granted_balance": "0.00", "topped_up_balance": "51.75"},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer dsSrv.Close()
	if err := e.store.UpsertProvider(store.Provider{
		ID: "bal-ds", Name: "DeepSeek", BaseURL: dsSrv.URL + "/v1",
		APIKey: "sk-ds", Models: []string{"mock-model"},
	}); err != nil {
		t.Fatalf("upsert ds: %v", err)
	}
	// openrouter 风格：独立 mock 仅提供 /v1/auth/key（usage/limit/免费层）
	orSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/key" {
			writeMockJSON(w, map[string]any{"data": map[string]any{
				"label": "sk-or-x", "usage": 1.25, "limit": nil, "is_free_tier": true,
				"expires_at": "2026-09-21T01:25:00.001Z",
			}})
			return
		}
		http.NotFound(w, r)
	}))
	defer orSrv.Close()
	if err := e.store.UpsertProvider(store.Provider{
		ID: "bal-or", Name: "OpenRouter", BaseURL: orSrv.URL + "/v1",
		APIKey: "sk-or-x", Models: []string{"mock-model"},
	}); err != nil {
		t.Fatalf("upsert or: %v", err)
	}

	resp := e.do(t, http.MethodGet, "/api/admin/providers/balances", "", e.adminHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var payload struct {
		Balances []struct {
			ID      string `json:"id"`
			Balance *struct {
				Kind      string  `json:"kind"`
				Available float64 `json:"available"`
				Voucher   float64 `json:"voucher"`
				Cash      float64 `json:"cash"`
				Total     float64 `json:"total"`
				Granted   float64 `json:"granted"`
				ToppedUp  float64 `json:"topped_up"`
				Currency  string  `json:"currency"`
				Usage     float64 `json:"usage"`
				IsFree    bool    `json:"is_free_tier"`
				Expires   string  `json:"expires_at"`
			} `json:"balance"`
			Error string `json:"error"`
		} `json:"balances"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var p1, p2, p3, p4 *struct {
		ID      string `json:"id"`
		Balance *struct {
			Kind      string  `json:"kind"`
			Available float64 `json:"available"`
			Voucher   float64 `json:"voucher"`
			Cash      float64 `json:"cash"`
			Total     float64 `json:"total"`
			Granted   float64 `json:"granted"`
			ToppedUp  float64 `json:"topped_up"`
			Currency  string  `json:"currency"`
			Usage     float64 `json:"usage"`
			IsFree    bool    `json:"is_free_tier"`
			Expires   string  `json:"expires_at"`
		} `json:"balance"`
		Error string `json:"error"`
	}
	for i := range payload.Balances {
		if payload.Balances[i].ID == "mock-local" {
			t.Fatalf("mock-local should be excluded from balances")
		}
		switch payload.Balances[i].ID {
		case "bal-p1":
			p1 = &payload.Balances[i]
		case "bal-p2":
			p2 = &payload.Balances[i]
		case "bal-ds":
			p3 = &payload.Balances[i]
		case "bal-or":
			p4 = &payload.Balances[i]
		}
	}
	if p1 == nil || p1.Balance == nil || p1.Balance.Kind != "moonshot" || p1.Balance.Available != 14.99736 {
		t.Fatalf("p1 balance = %+v, want moonshot 14.99736", p1)
	}
	if p1.Balance.Voucher != 14.99736 || p1.Balance.Cash != 0 {
		t.Fatalf("p1 free = %+v, want voucher only", p1.Balance)
	}
	if p2 == nil || p2.Error != "no_key" {
		t.Fatalf("p2 = %+v, want no_key", p2)
	}
	if p3 == nil || p3.Balance == nil || p3.Balance.Kind != "deepseek" ||
		p3.Balance.Total != 51.75 || p3.Balance.Granted != 0 || p3.Balance.ToppedUp != 51.75 || p3.Balance.Currency != "CNY" {
		t.Fatalf("p3 = %+v, want deepseek 51.75/0/51.75 CNY", p3)
	}
	if p4 == nil || p4.Balance == nil || p4.Balance.Kind != "openrouter" ||
		p4.Balance.Usage != 1.25 || !p4.Balance.IsFree || p4.Balance.Expires[:10] != "2026-09-21" {
		t.Fatalf("p4 = %+v, want openrouter usage 1.25 free tier", p4)
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

	// 1) 正常请求（主 key，纯透传路径）
	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, e.passthruHeaders())
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
		"tsm_gateway_uptime_seconds",
		`tsm_gateway_http_requests_total{status="200"} 2`,
		`tsm_gateway_http_requests_total{status="401"} 1`,
		`tsm_gateway_http_requests_total{status="429"} 1`,
		`tsm_gateway_quota_denials_total{code="rate_limited"} 1`,
		`tsm_gateway_upstream_healthy{provider="primary"} 1`,
		`tsm_gateway_upstream_requests_total{provider="primary"} 1`,
		`tsm_gateway_upstream_errors_total{provider="primary"} 1`,
		`tsm_gateway_upstream_requests_total{provider="backup"} 2`,
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
		`{"model":"smart","messages":[{"role":"user","content":"hi"}]}`, e.passthruHeaders())
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
			`{"model":"smart4","messages":[{"role":"user","content":"hi"}]}`, e.passthruHeaders())
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
		`{"model":"smart","stream":true,"messages":[{"role":"user","content":"hi"}]}`, e.passthruHeaders())
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

// TestAgnesPricingFallback 验证 agnes 官方定价表回退：上游 models 不带 pricing 时，
// 按模型 id 从静态定价表补齐（免费模型标 free）。
func TestAgnesPricingFallback(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			writeMockJSON(w, map[string]any{"object": "list", "data": []any{
				map[string]any{"id": "agnes-2.0-flash"},
				map[string]any{"id": "agnes-2.5-pro"},
				map[string]any{"id": "unknown-model"},
			}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	resp := e.do(t, http.MethodPost, "/api/admin/providers/probe",
		fmt.Sprintf(`{"base_url":%q,"api_key":"sk-x"}`, srv.URL+"/v1"), e.adminHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var payload struct {
		Models []struct {
			ID      string `json:"id"`
			Free    bool   `json:"free"`
			Pricing *struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]struct {
		Free    bool
		Pricing *struct {
			Prompt     string `json:"prompt"`
			Completion string `json:"completion"`
		}
	}{}
	for _, m := range payload.Models {
		byID[m.ID] = struct {
			Free    bool
			Pricing *struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			}
		}{m.Free, m.Pricing}
	}
	if f, ok := byID["agnes-2.0-flash"]; !ok || !f.Free || f.Pricing == nil ||
		f.Pricing.Prompt != "0" || f.Pricing.Completion != "0" {
		t.Fatalf("agnes-2.0-flash = %+v, want free with $0/$0", byID["agnes-2.0-flash"])
	}
	if p, ok := byID["agnes-2.5-pro"]; !ok || p.Pricing == nil ||
		p.Pricing.Prompt != "0.45" || p.Pricing.Completion != "0.90" || p.Free {
		t.Fatalf("agnes-2.5-pro = %+v, want $0.45/$0.90 not free", byID["agnes-2.5-pro"])
	}
	if m, ok := byID["unknown-model"]; !ok || m.Pricing != nil {
		t.Fatalf("unknown-model = %+v, want no pricing", byID["unknown-model"])
	}
}

// TestProbeProviderModels 验证探测上游 /v1/models：
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
			Pricing       *struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"models"`
		Balance *struct {
			Kind      string  `json:"kind"`
			Available float64 `json:"available"`
			Voucher   float64 `json:"voucher"`
			Cash      float64 `json:"cash"`
		} `json:"balance"`
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
	if payload.Models[1].Pricing == nil || payload.Models[1].Pricing.Prompt != "0.45" || payload.Models[1].Pricing.Completion != "0.90" {
		t.Fatalf("mock-model pricing = %+v, want 0.45/0.90", payload.Models[1].Pricing)
	}
	if !strings.HasPrefix(e.upOK.apiKey, "Bearer sk-probe") {
		t.Fatalf("upstream auth = %q, want Bearer sk-probe", e.upOK.apiKey)
	}
	// 余额/额度：探测带 key 时返回 Moonshot 风格余额（全赠送额度）
	if payload.Balance == nil || payload.Balance.Kind != "moonshot" {
		t.Fatalf("balance = %+v, want moonshot", payload.Balance)
	}
	if payload.Balance.Available != 14.99736 || payload.Balance.Voucher != 14.99736 || payload.Balance.Cash != 0 {
		t.Fatalf("balance values = %+v, want available 14.99736 voucher 14.99736 cash 0", payload.Balance)
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

// TestAgentToolLoop 验证网关 agent：客户端不传 tools，网关自动附加工具池并在
// 服务端执行 tool_calls 循环，最终返回带工具结果的答案。
func TestAgentToolLoop(t *testing.T) {
	e := newEnv(t, "agent")
	defer e.server.Close()

	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"1+1=?"}]}`, e.authHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d %s", resp.StatusCode, b)
	}
	if got := resp.Header.Get("X-Llm-Router-Provider"); got != "backup" {
		t.Fatalf("X-Llm-Router-Provider = %q, want backup (agent served by backup)", got)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) != 1 || !strings.Contains(out.Choices[0].Message.Content, "2") {
		t.Fatalf("answer = %q, want content containing 2", out.Choices[0].Message.Content)
	}
	if out.Usage.TotalTokens != 30 {
		t.Fatalf("usage total = %d, want 30 (final round)", out.Usage.TotalTokens)
	}
	// 工具循环确实发生了：mock 收到过第二轮带 tool 结果的请求。
	if up := e.upOK; up != nil && len(up.bodies) < 2 {
		t.Fatalf("expected >=2 upstream calls (tool loop), got %d", len(up.bodies))
	}
}

// TestAgentStreamReplay 验证流式：agent 内部跑完工具循环后，以 SSE 回放最终答案。
func TestAgentStreamReplay(t *testing.T) {
	e := newEnv(t, "agent")
	defer e.server.Close()

	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","stream":true,"messages":[{"role":"user","content":"1+1=?"}]}`, e.authHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d %s", resp.StatusCode, b)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	text, done, chunks := "", false, 0
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		chunks++
		if strings.Contains(line, "[DONE]") {
			done = true
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk)
		for _, c := range chunk.Choices {
			text += c.Delta.Content
		}
	}
	if !done || chunks < 3 {
		t.Fatalf("stream: chunks=%d done=%v", chunks, done)
	}
	if !strings.Contains(text, "2") {
		t.Fatalf("streamed text = %q, want containing 2", text)
	}
}

// TestAgentError200Failover 验证上游返回 200 + error body（过载/坏响应）时，
// agent 路径视为上游故障并 failover 到下一候选，而不是把坏响应当成功透传。
func TestAgentError200Failover(t *testing.T) {
	e := newEnv(t, "agent")
	defer e.server.Close()
	e.upBad.mode = "error200" // primary 返回 200 + {"error":{...}}

	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"1+1=?"}]}`, e.authHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d %s", resp.StatusCode, b)
	}
	if got := resp.Header.Get("X-Llm-Router-Provider"); got != "backup" {
		t.Fatalf("X-Llm-Router-Provider = %q, want backup (error200 should failover)", got)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) != 1 || !strings.Contains(out.Choices[0].Message.Content, "2") {
		t.Fatalf("answer = %q, want content containing 2", out.Choices[0].Message.Content)
	}
}

// TestAgent4xxPassthrough 验证 agent 路径下上游 4xx 原样透传（不 502、不熔断）。
func TestAgent4xxPassthrough(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

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
	// agent 默认启用（无 off 头）。
	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart4","messages":[{"role":"user","content":"hi"}]}`, e.authHeaders())
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !e.srv.health.Available("four") {
		t.Fatal("provider was evicted by client-side 4xx response (agent path)")
	}
}

// TestAgentSkillsInjection 验证 agent 路径也执行 key 的技能注入。
func TestAgentSkillsInjection(t *testing.T) {
	e := newEnv(t, "agent")
	defer e.server.Close()

	// 给主 key 开启技能注入（list 无技能库时为空，仅验证不破坏循环）。
	if err := e.store.UpdateKey("k-test", "", nil, store.Quota{}, "list"); err != nil {
		t.Fatalf("update key: %v", err)
	}
	resp := e.do(t, http.MethodPost, "/v1/chat/completions",
		`{"model":"smart","messages":[{"role":"user","content":"1+1=?"}]}`, e.authHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d %s", resp.StatusCode, b)
	}
}
