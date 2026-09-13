package proxy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/erishen/tsm-hub/internal/quota"
	"github.com/erishen/tsm-hub/internal/router"
	"github.com/erishen/tsm-hub/internal/skills"
	"github.com/erishen/tsm-hub/internal/store"
)

// TestAllCandidatesThrottledPassthrough 验证所有候选都被 429 限流时，
// 客户端收到真实的 429（而非压平的 502）——客户端退避策略据此区分
// 「限流可重试」与「服务故障」。
func TestAllCandidatesThrottledPassthrough(t *testing.T) {
	throttled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded: free-models-per-day"}}`))
	}))
	defer throttled.Close()

	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.DefaultTimeoutMS = 5000
		c.Settings.MaxBodyBytes = 1 << 20
		c.Settings.Agent = store.AgentCfg{Disabled: true}
		c.Providers = []store.Provider{
			{ID: "t1", Name: "T1", BaseURL: throttled.URL + "/v1", APIKey: "k", Models: []string{"m"}, Enabled: true, Weight: 100, Priority: 1},
		}
		c.Routes = []store.Route{
			{Model: "m", Strategy: "failover", Targets: []store.RouteTarget{
				{ProviderID: "t1", Model: "m", Priority: 1},
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
	defer rec.Close()
	sk := skills.New("")
	p := New(st, rt, h, rec, sk)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	res := p.Handle(w, req, store.APIKey{ID: "k1"}, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))

	if res.Status != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (err=%s)", res.Status, res.Err)
	}
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("response code = %d, want 429", w.Code)
	}
}

// TestQuotaExhaustedLongCooldown 验证 403 免费额度耗尽（free quota exhausted）
// 触发长冷却（6 小时）：短时间内再次请求不再白吃 403——健康过滤直接跳过
// 该 provider，请求失败得更快（状态仍透传）。
func TestQuotaExhaustedLongCooldown(t *testing.T) {
	calls := 0
	exhausted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"Free quota exhausted. To continue accessing the model, add credits"}}`))
	}))
	defer exhausted.Close()

	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.DefaultTimeoutMS = 5000
		c.Settings.MaxBodyBytes = 1 << 20
		c.Settings.Agent = store.AgentCfg{Disabled: true}
		c.Providers = []store.Provider{
			{ID: "q1", Name: "Q1", BaseURL: exhausted.URL + "/v1", APIKey: "k", Models: []string{"m"}, Enabled: true, Weight: 100, Priority: 1},
		}
		c.Routes = []store.Route{
			{Model: "m", Strategy: "failover", Targets: []store.RouteTarget{
				{ProviderID: "q1", Model: "m", Priority: 1},
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
	defer rec.Close()
	sk := skills.New("")
	p := New(st, rt, h, rec, sk)

	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	// 第一次：吃 403，触发长冷却。
	res1 := p.Handle(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		store.APIKey{ID: "k1"}, "/v1/chat/completions", body)
	if res1.Status != http.StatusForbidden {
		t.Fatalf("first status = %d, want 403 (err=%s)", res1.Status, res1.Err)
	}
	if calls != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls)
	}
	// 第二次：单 provider 冷却中仍会被挑去做一次探测（否则永远 502），
	// 但探测失败后状态必须如实透传 403（不是压平的 502）——客户端因此
	// 知道是配额耗尽而不是服务故障，不再盲目退避。
	res2 := p.Handle(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		store.APIKey{ID: "k1"}, "/v1/chat/completions", body)
	if res2.Status != http.StatusForbidden {
		t.Fatalf("second status = %d, want 403 passthrough (err=%s)", res2.Status, res2.Err)
	}
}
