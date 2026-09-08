package quota

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/erishen/llm-router/internal/store"
)

func rec(t *testing.T) *Recorder {
	t.Helper()
	r, err := NewRecorder(filepath.Join(t.TempDir(), "usage"))
	if err != nil {
		t.Fatalf("recorder: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func recOf(ts time.Time, keyID, model string, tokens int, errMsg string) store.UsageRecord {
	return store.UsageRecord{
		TS: ts, KeyID: keyID, Model: model, ProviderID: "p", UpstreamModel: model,
		PromptTokens: tokens / 2, CompletionToken: tokens / 2, TotalTokens: tokens,
		CostUSD: 0.01, Status: 200, Error: errMsg,
	}
}

func TestRecorderAccumulatesAndPersists(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "usage")
	r1, _ := NewRecorder(dir)
	now := time.Now()
	if err := r1.Record(recOf(now, "k1", "m", 100, "")); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := r1.Record(recOf(now, "k1", "m", 50, "")); err != nil {
		t.Fatalf("record: %v", err)
	}
	if got := r1.Total("k1").TotalTokens; got != 150 {
		t.Fatalf("total = %d, want 150", got)
	}
	if got := r1.Today("k1").Requests; got != 2 {
		t.Fatalf("today requests = %d", got)
	}
	if got := r1.ByModel()["m"].TotalTokens; got != 150 {
		t.Fatalf("model total = %d", got)
	}
	_ = r1.Close()

	// 重开应回放历史。
	r2, err := NewRecorder(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := r2.Total("k1").TotalTokens; got != 150 {
		t.Fatalf("replayed total = %d, want 150", got)
	}
}

func TestRecorderDailyBuckets(t *testing.T) {
	r := rec(t)
	now := time.Now()
	_ = r.Record(recOf(now.AddDate(0, 0, -2), "k1", "m", 10, ""))
	_ = r.Record(recOf(now, "k1", "m", 20, ""))
	days := r.Daily(3)
	if len(days) != 3 {
		t.Fatalf("days = %d", len(days))
	}
	if days[2].TotalTokens != 20 {
		t.Fatalf("today = %+v, want 20 tokens", days[2])
	}
	if days[0].TotalTokens != 10 {
		t.Fatalf("two days ago = %+v, want 10 tokens", days[0])
	}
}

func TestRecorderErrorsCounted(t *testing.T) {
	r := rec(t)
	_ = r.Record(recOf(time.Now(), "k1", "m", 10, "upstream 500"))
	if got := r.Total("k1").Errors; got != 1 {
		t.Fatalf("errors = %d, want 1", got)
	}
}

func TestLimiterRPM(t *testing.T) {
	r := rec(t)
	l := NewLimiter(r)
	key := store.APIKey{ID: "k", Enabled: true, Quota: store.Quota{RPM: 3}}
	for i := 0; i < 3; i++ {
		if ok, _ := l.Check(key); !ok {
			t.Fatalf("request %d should pass", i)
		}
	}
	ok, reason := l.Check(key)
	if ok {
		t.Fatal("4th request should be limited")
	}
	if reason == nil || reason.Code != "rate_limited" {
		t.Fatalf("reason = %+v", reason)
	}
	// 窗口滑过之后应恢复。
	l.clock = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if ok, _ := l.Check(key); !ok {
		t.Fatal("should recover after window slides")
	}
}

func TestLimiterQuotas(t *testing.T) {
	r := rec(t)
	_ = r.Record(recOf(time.Now(), "k", "m", 1000, ""))
	l := NewLimiter(r)

	cases := []struct {
		name string
		key  store.APIKey
		code string
	}{
		{"token quota", store.APIKey{ID: "k", Enabled: true, Quota: store.Quota{MaxTokens: 500}}, "quota_exhausted"},
		{"cost quota", store.APIKey{ID: "k", Enabled: true, Quota: store.Quota{MaxCostUSD: 0.001}}, "quota_exhausted"},
		{"daily quota", store.APIKey{ID: "k", Enabled: true, Quota: store.Quota{DailyTokens: 500}}, "daily_quota_exhausted"},
		{"disabled", store.APIKey{ID: "k", Enabled: false}, "key_disabled"},
		{"expired", store.APIKey{ID: "k", Enabled: true, ExpiresAt: time.Now().Add(-time.Hour)}, "key_expired"},
	}
	for _, c := range cases {
		ok, reason := l.Check(c.key)
		if ok {
			t.Fatalf("%s: should be denied", c.name)
		}
		if reason == nil || reason.Code != c.code {
			t.Fatalf("%s: code = %+v, want %s", c.name, reason, c.code)
		}
	}

	// 未超限的 Key 应放行。
	if ok, _ := l.Check(store.APIKey{ID: "k", Enabled: true, Quota: store.Quota{MaxTokens: 100000}}); !ok {
		t.Fatal("generous quota should pass")
	}
}

func TestLimiterTopKeys(t *testing.T) {
	r := rec(t)
	_ = r.Record(recOf(time.Now(), "k1", "m", 10, ""))
	_ = r.Record(recOf(time.Now(), "k2", "m", 999, ""))
	top := NewLimiter(r).TopKeys(1)
	if len(top) != 1 || top[0].KeyID != "k2" {
		t.Fatalf("top = %+v", top)
	}
}

func TestRecorderRecent(t *testing.T) {
	r := rec(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		_ = r.Record(recOf(now.Add(time.Duration(i)*time.Second), "k", "m", 1, ""))
	}
	recent := r.Recent(3)
	if len(recent) != 3 {
		t.Fatalf("recent = %d, want 3", len(recent))
	}
	// 应当是倒序（最近的在前）。
	if recent[0].TS.Before(recent[2].TS) {
		t.Fatalf("recent not in descending order: %v", recent)
	}
}

func TestCheckWarn(t *testing.T) {
	r := rec(t)
	l := NewLimiter(r)
	now := time.Now()
	_ = r.Record(recOf(now, "k1", "m", 85, ""))

	// 85% ≥ 80% 阈值：首次跨越提示。
	key := store.APIKey{ID: "k1", Quota: store.Quota{MaxTokens: 100}}
	kind, used, limit, ok := l.CheckWarn(key)
	if !ok || kind != "max_tokens" || used != 85 || limit != 100 {
		t.Fatalf("warn = (%q, %v, %v, %v), want (max_tokens, 85, 100, true)", kind, used, limit, ok)
	}
	// 同类型不重复提示（避免每个请求刷 warn 日志）。
	if _, _, _, ok := l.CheckWarn(key); ok {
		t.Fatal("duplicate warn for same kind")
	}
	// 无配额 Key 不提示。
	if _, _, _, ok := l.CheckWarn(store.APIKey{ID: "k2"}); ok {
		t.Fatal("warn without quota configured")
	}
	// 低于阈值不提示。
	low := store.APIKey{ID: "k3", Quota: store.Quota{MaxTokens: 100}}
	_ = r.Record(recOf(now, "k3", "m", 10, ""))
	if _, _, _, ok := l.CheckWarn(low); ok {
		t.Fatal("warn below threshold")
	}
}
