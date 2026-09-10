package proxy

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/erishen/tsm-gateway/internal/quota"
	"github.com/erishen/tsm-gateway/internal/router"
	"github.com/erishen/tsm-gateway/internal/skills"
	"github.com/erishen/tsm-gateway/internal/store"
)

// TestFailoverChainCollected 验证 failover 链被收集并写入用量流水：
// 第一个候选返回 500，第二个候选成功 → attempt=2，failover 含失败候选。
func TestFailoverChainCollected(t *testing.T) {
	// 两个 mock 上游：bad 恒 500，good 恒 200。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream down"}}`))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cmpl-1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer good.Close()

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
	defer rec.Close()
	sk := skills.New("")
	p := New(st, rt, h, rec, sk)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := httptest.NewRecorder()
	res := p.Handle(w, req, store.APIKey{ID: "k1"}, "/v1/chat/completions",
		[]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))

	if res.Status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (err=%s)", res.Status, res.Err)
	}
	if res.Attempt != 2 {
		t.Fatalf("attempt = %d, want 2", res.Attempt)
	}
	if len(res.Failover) != 1 {
		t.Fatalf("failover chain len = %d, want 1 (%+v)", len(res.Failover), res.Failover)
	}
	if res.Failover[0].ProviderID != "bad" || res.Failover[0].Error == "" {
		t.Fatalf("failover[0] = %+v, want bad with error", res.Failover[0])
	}
}
