package quota

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/erishen/tsm-hub/internal/store"
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

// 499（客户端断开/取消）是客户端行为，不得计入错误率。
func TestAggClientAbortNotError(t *testing.T) {
	cases := []struct {
		name string
		rec  store.UsageRecord
		want int
	}{
		{"499 aborted", store.UsageRecord{Status: 499, Error: "client aborted: context canceled"}, 0},
		{"499 no text", store.UsageRecord{Status: 499}, 0},
		{"502 fault", store.UsageRecord{Status: 502, Error: "upstream request failed"}, 1},
		{"404 model gone", store.UsageRecord{Status: 404, Error: "upstream 404: NOT_FOUND"}, 1},
		{"400 bad request", store.UsageRecord{Status: 400, Error: "invalid json body"}, 1},
		{"200 ok", store.UsageRecord{Status: 200}, 0},
		{"200 with error text", store.UsageRecord{Status: 200, Error: "mid-stream note"}, 1},
	}
	for _, c := range cases {
		var a Agg
		a.add(c.rec)
		if a.Errors != c.want {
			t.Errorf("%s: errors = %d, want %d", c.name, a.Errors, c.want)
		}
		if a.Requests != 1 {
			t.Errorf("%s: requests = %d, want 1", c.name, a.Requests)
		}
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

func TestRecorderObservabilityDims(t *testing.T) {
	r := rec(t)
	now := time.Now()
	mk := func(providerID, scene string, status int, latencyMS int64, attempt int, errMsg string) store.UsageRecord {
		u := recOf(now, "k1", "chat", 100, errMsg)
		u.ProviderID = providerID
		u.Scene = scene
		u.LatencyMS = latencyMS
		u.Attempt = attempt
		u.Status = status
		return u
	}
	_ = r.Record(mk("agnes", "chat", 200, 120, 1, ""))
	_ = r.Record(mk("agnes", "chat", 200, 80, 1, ""))
	_ = r.Record(mk("kimi", "chat", 200, 400, 2, "")) // failover 命中
	_ = r.Record(mk("kimi", "reason", 502, 900, 3, "upstream 502"))

	agn := r.Provider("agnes")
	if agn.Requests != 2 || agn.Errors != 0 {
		t.Fatalf("agnes agg = %+v", agn)
	}
	if agn.AvgLatency() != 100 {
		t.Fatalf("agnes avg latency = %d, want 100", agn.AvgLatency())
	}
	kim := r.Provider("kimi")
	if kim.Requests != 2 || kim.Errors != 1 || kim.Failovers != 2 {
		t.Fatalf("kimi agg = %+v", kim)
	}
	if kim.ErrorRate() != 0.5 {
		t.Fatalf("kimi error rate = %v, want 0.5", kim.ErrorRate())
	}
	chat := r.Scene("chat")
	if chat.Requests != 3 || chat.Failovers != 1 {
		t.Fatalf("chat scene agg = %+v", chat)
	}
	reason := r.Scene("reason")
	if reason.Requests != 1 || reason.Errors != 1 {
		t.Fatalf("reason scene agg = %+v", reason)
	}
	if len(r.Providers()) != 2 || len(r.Scenes()) != 2 {
		t.Fatalf("providers=%d scenes=%d, want 2/2", len(r.Providers()), len(r.Scenes()))
	}
}

func TestRecorderFailoverBy(t *testing.T) {
	r := rec(t)
	now := time.Now()
	mk := func(providerID string, attempt int, chain ...string) store.UsageRecord {
		u := recOf(now, "k1", "chat", 10, "")
		u.ProviderID = providerID
		u.Attempt = attempt
		for _, p := range chain {
			u.Failover = append(u.Failover, store.FailoverStep{ProviderID: p, Error: "boom"})
		}
		return u
	}
	// agnes 两次被跳过（首候选失败换 kimi），kimi 一次被跳过（换 sensenova）。
	_ = r.Record(mk("kimi", 2, "agnes"))
	_ = r.Record(mk("sensenova", 2, "kimi"))
	_ = r.Record(mk("agnes", 3, "agnes", "kimi"))

	by := r.FailoverBy()
	if by["agnes"] != 2 || by["kimi"] != 2 || by["sensenova"] != 0 {
		t.Fatalf("failoverBy = %+v, want agnes=2 kimi=2 sensenova=0", by)
	}
}

// TestLimiterSweepRemovesStaleKeyEntries 验证 Key 长期不用 / 被删除后，
// hits 与 warn 里的条目不会永久驻留（原实现只裁剪时间戳、从不 delete）。
func TestLimiterSweepRemovesStaleKeyEntries(t *testing.T) {
	l := NewLimiter(rec(t))
	base := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	now := base
	l.clock = func() time.Time { return now }

	mk := func(id string) store.APIKey {
		return store.APIKey{ID: id, Enabled: true, Quota: store.Quota{RPM: 10}}
	}
	for _, id := range []string{"kA", "kB"} {
		if ok, reason := l.Check(mk(id)); !ok {
			t.Fatalf("%s should pass, got %+v", id, reason)
		}
	}
	l.warn["kA"] = "max_tokens"
	l.warn["kB"] = "daily_tokens"
	if len(l.hits) != 2 || len(l.warn) != 2 {
		t.Fatalf("before sweep: hits=%d warn=%d, want 2/2", len(l.hits), len(l.warn))
	}

	// 窗口滑过后，仅 kC 有一次请求；这一次请求应顺带清掉两个失效 key。
	now = base.Add(2 * time.Minute)
	if ok, reason := l.Check(mk("kC")); !ok {
		t.Fatalf("kC should pass, got %+v", reason)
	}
	if len(l.hits) != 1 || len(l.warn) != 0 {
		t.Fatalf("after sweep: hits=%d warn=%d, want 1/0 (%v / %v)", len(l.hits), len(l.warn), l.hits, l.warn)
	}
	if _, ok := l.hits["kC"]; !ok {
		t.Fatal("active key kC was removed")
	}

	// Snapshot 只报活跃 key；管理台按 rpm[k.ID] 取值，缺失即 0。
	snap := l.Snapshot()
	for _, id := range []string{"kA", "kB"} {
		if _, ok := snap[id]; ok {
			t.Errorf("Snapshot still reports cleaned key %q", id)
		}
	}
	if snap["kC"] != 1 {
		t.Errorf("Snapshot[kC] = %d, want 1", snap["kC"])
	}
}

// TestLimiterSweepKeepsActiveKeys 验证清理不会误删仍在窗口内的 key，
// 且 RPM 限流在清理后仍按滑动窗口正确工作。
func TestLimiterSweepKeepsActiveKeys(t *testing.T) {
	l := NewLimiter(rec(t))
	base := time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)
	now := base
	l.clock = func() time.Time { return now }
	key := store.APIKey{ID: "k", Enabled: true, Quota: store.Quota{RPM: 3}}

	for i := 0; i < 3; i++ {
		if ok, _ := l.Check(key); !ok {
			t.Fatalf("request %d should pass", i)
		}
	}
	if ok, _ := l.Check(key); ok {
		t.Fatal("4th request should be rate limited")
	}

	// 窗口整体滑过：sweep 会执行，但本次请求即新窗口首条，条目必须保留。
	now = base.Add(2 * time.Minute)
	for i := 0; i < 3; i++ {
		if ok, reason := l.Check(key); !ok {
			t.Fatalf("after sweep request %d should pass, got %+v", i, reason)
		}
	}
	if ok, reason := l.Check(key); ok || reason == nil || reason.Code != "rate_limited" {
		t.Fatalf("after sweep 4th request = (%v, %+v), want rate_limited", ok, reason)
	}
	if got := len(l.hits["k"]); got != 3 {
		t.Errorf("hits[k] length = %d, want 3 (expired timestamps compacted)", got)
	}
}

// TestSnapshotWarnings 验证 SnapshotWarnings 实时计算越线（>=80%）额度，
// 不改内部状态、跳过禁用 key、字段正确。与 CheckWarn 解耦。
func TestSnapshotWarnings(t *testing.T) {
	r := rec(t)
	now := time.Now()
	_ = r.Record(recOf(now, "k1", "m", 85, "")) // 85/100 = 85% ≥ 80%
	_ = r.Record(recOf(now, "k2", "m", 50, "")) // 50/100 = 50% < 80%
	_ = r.Record(recOf(now, "k3", "m", 10, ""))
	_ = r.Record(recOf(now, "k3", "m", 10, "")) // 成本 0.02 / 上限 0.02 = 100%
	l := NewLimiter(r)

	keys := []store.APIKey{
		{ID: "k1", Enabled: true, Name: "Alpha", Quota: store.Quota{MaxTokens: 100}},
		{ID: "k2", Enabled: true, Name: "Beta", Quota: store.Quota{MaxTokens: 100}},
		{ID: "k3", Enabled: true, Name: "Gamma", Quota: store.Quota{MaxCostUSD: 0.02}},
		{ID: "k4", Enabled: false, Name: "Off", Quota: store.Quota{MaxTokens: 1}}, // 禁用，跳过
	}
	warns := l.SnapshotWarnings(keys)
	got := map[string]bool{}
	for _, w := range warns {
		got[w.KeyID+"/"+w.Kind] = true
		if w.Ratio < 0 || w.Ratio > 1.0001 {
			t.Errorf("ratio out of range: %+v", w)
		}
	}
	if !got["k1/max_tokens"] {
		t.Errorf("expected k1 max_tokens warning, got %+v", warns)
	}
	if got["k2/max_tokens"] {
		t.Errorf("k2 should not warn (50%%)")
	}
	if !got["k3/max_cost_usd"] {
		t.Errorf("expected k3 max_cost_usd warning, got %+v", warns)
	}
	if got["k4/max_tokens"] {
		t.Errorf("disabled key k4 must be skipped")
	}
	if len(got) != 2 {
		t.Errorf("expected 2 warnings, got %d: %+v", len(got), warns)
	}
	for _, w := range warns {
		if w.KeyID == "k1" {
			if w.Name != "Alpha" {
				t.Errorf("k1 name = %q, want Alpha", w.Name)
			}
			if w.Ratio < 0.85 || w.Ratio >= 0.86 {
				t.Errorf("k1 ratio = %v, want ~0.85", w.Ratio)
			}
		}
	}
	// 不改变限流内部状态。
	if len(l.warn) != 0 {
		t.Errorf("SnapshotWarnings must not mutate warn state, got %v", l.warn)
	}
}
