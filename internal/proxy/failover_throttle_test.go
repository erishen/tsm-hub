package proxy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erishen/llm-router/internal/quota"
	"github.com/erishen/llm-router/internal/router"
	"github.com/erishen/llm-router/internal/skills"
	"github.com/erishen/llm-router/internal/store"
)

// newFailoverFixture 构造 bad→good 两级 failover 路由的完整 proxy。
// badHandler / goodHandler 由调用方注入，便于模拟 403/429/长响应。
func newFailoverFixture(t *testing.T, badHandler, goodHandler http.HandlerFunc) (*Proxy, *httptest.ResponseRecorder, *router.Tracker) {
	t.Helper()
	bad := httptest.NewServer(http.HandlerFunc(badHandler))
	t.Cleanup(bad.Close)
	good := httptest.NewServer(http.HandlerFunc(goodHandler))
	t.Cleanup(good.Close)

	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.DefaultTimeoutMS = 5000
		c.Settings.MaxBodyBytes = 1 << 20
		c.Settings.FailThreshold = 3
		c.Settings.CooldownSec = 5
		c.Settings.Agent = store.AgentCfg{Disabled: true} // 关 agent，直接走路由
		c.Settings.Smart = store.SmartScoreCfg{ThrottleSec: 120, FreeBonus: 100, HalfOpenPenalty: 30}
		c.Providers = []store.Provider{
			{ID: "bad", Name: "Bad", BaseURL: bad.URL + "/v1", APIKey: "k", Models: []string{"m"}, Enabled: true, Weight: 100, Priority: 1},
			{ID: "good", Name: "Good", BaseURL: good.URL + "/v1", APIKey: "k", Models: []string{"m"}, Enabled: true, Weight: 100, Priority: 2},
		}
		c.Routes = []store.Route{
			{Model: "m", Strategy: "failover", Targets: []store.RouteTarget{
				{ProviderID: "bad", Model: "m", Priority: 1},
				{ProviderID: "good", Model: "m", Priority: 2},
			}},
		}
		return nil
	}); err != nil {
		t.Fatalf("store update: %v", err)
	}
	h := router.NewTracker(3, 5)
	rt := router.New(st, h)
	rec, err := quota.NewRecorder(filepath.Join(t.TempDir(), "usage"))
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	t.Cleanup(func() { rec.Close() })
	p := New(st, rt, h, rec, skills.New(""))
	return p, httptest.NewRecorder(), h
}

func runChat(t *testing.T, p *Proxy, w *httptest.ResponseRecorder) Result {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return p.Handle(w, req, store.APIKey{ID: "k1"}, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
}

// TestFailoverOn403 验证上游 403（免费额度耗尽/key 无权限）不再透传：
// 与 429 同等处理——记冷却并换下一候选，最终由 good 兜底成功。
func TestFailoverOn403(t *testing.T) {
	p, w, _ := newFailoverFixture(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"free quota exhausted","type":"forbidden_error"}}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
		},
	)
	res := runChat(t, p, w)
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (err=%s)", res.Status, res.Err)
	}
	if res.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2 (failover must happen on 403)", res.Attempt)
	}
	if len(res.Failover) != 1 || res.Failover[0].ProviderID != "bad" {
		t.Fatalf("failover chain = %+v, want [bad]", res.Failover)
	}
	if !strings.Contains(res.Failover[0].Error, "403") {
		t.Fatalf("failover error = %q, want contains 403", res.Failover[0].Error)
	}
}

// TestFailoverOn429 验证 429（tpm/rpm 限流）同样换下一候选。
func TestFailoverOn429(t *testing.T) {
	p, w, _ := newFailoverFixture(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"inference exceeds tpm/rpm limit","type":"rate_limit_error","code":"429001"}}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"cmpl-2","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
		},
	)
	res := runChat(t, p, w)
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (err=%s)", res.Status, res.Err)
	}
	if res.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2 (failover must happen on 429)", res.Attempt)
	}
	if len(res.Failover) != 1 || res.Failover[0].ProviderID != "bad" {
		t.Fatalf("failover chain = %+v, want [bad]", res.Failover)
	}
}

// Test403ThrottlesProvider 验证 403 后 provider 立即进入冷却（downUntil 未来），
// 后续请求 Pick 不再选中它（只有 good 可用时直接走 good）。
func Test403ThrottlesProvider(t *testing.T) {
	p, w, h := newFailoverFixture(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"forbidden"}}`))
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"cmpl-3","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		},
	)
	res := runChat(t, p, w)
	if res.Status != http.StatusOK {
		t.Fatalf("first status = %d, want 200", res.Status)
	}
	if !h.Throttled("bad") {
		t.Fatalf("expected bad to be throttled after 403")
	}
	if h.Available("bad") {
		t.Fatalf("expected bad unavailable during throttle cooldown")
	}
	// 第二次请求：bad 已冷却，应直接走 good（attempt=1，无 failover）。
	res2 := runChat(t, p, w)
	if res2.Status != http.StatusOK {
		t.Fatalf("second status = %d, want 200 (err=%s)", res2.Status, res2.Err)
	}
	if res2.Attempt != 1 {
		t.Fatalf("second attempt = %d, want 1 (bad should be filtered)", res2.Attempt)
	}
	if len(res2.Failover) != 0 {
		t.Fatalf("second failover = %+v, want empty", res2.Failover)
	}
}

// TestNonStreamLongBodyNotTruncated 验证非流式 2xx 长响应不再被 8KB 截断：
// 上游返回超过 8KB 的中文内容，客户端应收到完整 body。
func TestNonStreamLongBodyNotTruncated(t *testing.T) {
	longText := strings.Repeat("这是一个很长的中文响应内容用于验证截断修复。", 400) // > 8KB
	p, w, _ := newFailoverFixture(t,
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			body := `{"id":"cmpl-4","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"` + longText + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
			_, _ = w.Write([]byte(body))
		},
		func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("good should not be called when bad succeeds")
		},
	)
	res := runChat(t, p, w)
	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (err=%s)", res.Status, res.Err)
	}
	body := w.Body.String()
	if !strings.Contains(body, longText) {
		t.Fatalf("response body truncated: len(body)=%d, want to contain longText(%d)", len(body), len(longText))
	}
	if len(body) < 8000 {
		t.Fatalf("response body too short: %d bytes, want > 8000", len(body))
	}
}
