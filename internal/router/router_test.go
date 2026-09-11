package router

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/erishen/tsm-hub/internal/store"
)

func newStore(t *testing.T, providers []store.Provider, routes []store.Route) *store.Store {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	for _, p := range providers {
		if err := st.UpsertProvider(p); err != nil {
			t.Fatalf("upsert provider: %v", err)
		}
	}
	for _, r := range routes {
		if err := st.UpsertRoute(r); err != nil {
			t.Fatalf("upsert route: %v", err)
		}
	}
	return st
}

func p(id string, priority, weight int, models ...string) store.Provider {
	return store.Provider{
		ID: id, Name: id, BaseURL: "http://" + id + "/v1", APIKey: "k",
		Models: models, Enabled: true, Weight: weight, Priority: priority, TimeoutMS: 1000,
	}
}

func TestPickFailoverOrdersByPriority(t *testing.T) {
	st := newStore(t,
		[]store.Provider{p("low", 9, 100, "m"), p("high", 1, 100, "m")},
		[]store.Route{{Model: "m", Strategy: "failover", Targets: []store.RouteTarget{
			{ProviderID: "low", Priority: 9, Weight: 100},
			{ProviderID: "high", Priority: 1, Weight: 100},
		}}},
	)
	rt := New(st, NewTracker(3, 60))
	cands, err := rt.Pick("m", false)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if len(cands) != 2 || cands[0].ProviderID != "high" {
		t.Fatalf("expected high first, got %+v", cands)
	}
}

func TestPickRewritesUpstreamModel(t *testing.T) {
	st := newStore(t,
		[]store.Provider{p("a", 1, 100, "real-model")},
		[]store.Route{{Model: "alias", Strategy: "weighted", Targets: []store.RouteTarget{
			{ProviderID: "a", Model: "real-model"},
		}}},
	)
	rt := New(st, NewTracker(3, 60))
	cands, err := rt.Pick("alias", false)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if cands[0].UpstreamModel != "real-model" {
		t.Fatalf("upstream model = %q", cands[0].UpstreamModel)
	}
}

func TestPickFallsBackToProvidersDeclaringModel(t *testing.T) {
	// 没有显式路由表：应回退到声明支持该模型的 enabled provider。
	st := newStore(t, []store.Provider{
		p("a", 1, 100, "m"),
		p("b", 1, 100, "other"),
		{ID: "off", Name: "off", Enabled: false, Models: []string{"m"}},
	}, nil)
	rt := New(st, NewTracker(3, 60))
	cands, err := rt.Pick("m", false)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if len(cands) != 1 || cands[0].ProviderID != "a" {
		t.Fatalf("expected only a, got %+v", cands)
	}
}

func TestPickSkipsUnhealthyAndKeepsHalfOpen(t *testing.T) {
	st := newStore(t,
		[]store.Provider{p("a", 1, 100, "m"), p("b", 2, 100, "m")},
		[]store.Route{{Model: "m", Strategy: "failover", Targets: []store.RouteTarget{
			{ProviderID: "a", Priority: 1}, {ProviderID: "b", Priority: 2},
		}}},
	)
	tracker := NewTracker(2, 60)
	rt := New(st, tracker)

	// a 连续失败两次 → 被摘除，只剩 b。
	tracker.ReportFailure("a", "boom")
	tracker.ReportFailure("a", "boom")
	cands, err := rt.Pick("m", false)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if len(cands) != 1 || cands[0].ProviderID != "b" {
		t.Fatalf("expected only b, got %+v", cands)
	}

	// 两个都挂：仍要放行一个半开候选，而不是整体不可用。
	tracker.ReportFailure("b", "boom")
	tracker.ReportFailure("b", "boom")
	cands, err = rt.Pick("m", false)
	if err != nil {
		t.Fatalf("pick when all down: %v", err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected single half-open candidate, got %+v", cands)
	}
	if !cands[0].HalfOpen {
		t.Fatalf("candidate should be marked half-open: %+v", cands[0])
	}
}

func TestPickFailoverUsesTargetPriorityOverProviderPriority(t *testing.T) {
	// 路由表 target 的优先级应与 provider 自身的优先级相互独立：
	// provider a 的优先级更高（1），但路由表里 b 排在前面，failover 应把 b 放第一位。
	st := newStore(t,
		[]store.Provider{p("a", 1, 100, "m"), p("b", 2, 100, "m")},
		[]store.Route{{Model: "m", Strategy: "failover", Targets: []store.RouteTarget{
			{ProviderID: "a", Priority: 2, Weight: 100},
			{ProviderID: "b", Priority: 1, Weight: 100},
		}}},
	)
	rt := New(st, NewTracker(3, 60))
	cands, err := rt.Pick("m", false)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if len(cands) != 2 || cands[0].ProviderID != "b" {
		t.Fatalf("expected b first (target priority), got %+v", cands)
	}
}

func TestPickFallsBackToProviderPriorityWhenTargetPriorityZero(t *testing.T) {
	// target 未显式配置 priority 时，应回落到 provider 自己的优先级。
	st := newStore(t,
		[]store.Provider{p("a", 1, 100, "m"), p("b", 2, 100, "m")},
		[]store.Route{{Model: "m", Strategy: "failover", Targets: []store.RouteTarget{
			{ProviderID: "a", Weight: 100}, // 未指定 target priority
			{ProviderID: "b", Weight: 100},
		}}},
	)
	rt := New(st, NewTracker(3, 60))
	cands, err := rt.Pick("m", false)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if cands[0].ProviderID != "a" {
		t.Fatalf("expected a first (provider priority fallback), got %+v", cands)
	}
}

func TestPickNoCandidate(t *testing.T) {
	st := newStore(t, []store.Provider{p("a", 1, 100, "m")}, nil)
	rt := New(st, NewTracker(3, 60))
	if _, err := rt.Pick("", false); err == nil {
		t.Fatal("empty model should error")
	}
	if _, err := rt.Pick("unknown-model", false); err == nil {
		t.Fatal("unknown model should error")
	}
}

func TestWeightedPrefersHigherWeight(t *testing.T) {
	st := newStore(t,
		[]store.Provider{p("heavy", 1, 1000, "m"), p("light", 1, 1, "m")},
		[]store.Route{{Model: "m", Strategy: "weighted", Targets: []store.RouteTarget{
			{ProviderID: "heavy", Weight: 1000}, {ProviderID: "light", Weight: 1},
		}}},
	)
	rt := New(st, NewTracker(3, 60))
	heavyFirst := 0
	for i := 0; i < 200; i++ {
		cands, err := rt.Pick("m", false)
		if err != nil {
			t.Fatalf("pick: %v", err)
		}
		if cands[0].ProviderID == "heavy" {
			heavyFirst++
		}
	}
	if heavyFirst < 180 {
		t.Fatalf("heavy should win most of the time, got %d/200", heavyFirst)
	}
}

func TestLatencyReducesWeight(t *testing.T) {
	st := newStore(t,
		[]store.Provider{p("fast", 1, 100, "m"), p("slow", 1, 100, "m")},
		[]store.Route{{Model: "m", Strategy: "weighted", Targets: []store.RouteTarget{
			{ProviderID: "fast", Weight: 100}, {ProviderID: "slow", Weight: 100},
		}}},
	)
	tracker := NewTracker(3, 60)
	tracker.ReportSuccess("fast", 100*time.Millisecond)
	tracker.ReportSuccess("slow", 4000*time.Millisecond)
	rt := New(st, tracker)

	fastFirst := 0
	for i := 0; i < 200; i++ {
		cands, _ := rt.Pick("m", false)
		if cands[0].ProviderID == "fast" {
			fastFirst++
		}
	}
	if fastFirst < 140 {
		t.Fatalf("slow provider should lose traffic, fast won only %d/200", fastFirst)
	}
}

func TestTrackerEvictAndRecover(t *testing.T) {
	tr := NewTracker(2, 60)
	if !tr.Available("a") {
		t.Fatal("fresh provider should be available")
	}
	tr.ReportFailure("a", "e1")
	if !tr.Available("a") {
		t.Fatal("single failure should not evict")
	}
	tr.ReportFailure("a", "e2")
	if tr.Available("a") {
		t.Fatal("should be evicted after threshold")
	}
	tr.ReportSuccess("a", 10*time.Millisecond)
	if !tr.Available("a") {
		t.Fatal("success should clear eviction")
	}
	if tr.Failures("a") != 0 {
		t.Fatalf("failures = %d, want 0", tr.Failures("a"))
	}
	if got := tr.Latency("a"); got != 10 {
		t.Fatalf("latency = %d, want 10", got)
	}
}

func TestTrackerSnapshot(t *testing.T) {
	tr := NewTracker(1, 60)
	tr.ReportSuccess("a", 10*time.Millisecond)
	tr.ReportFailure("b", "down")
	snap := tr.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	byID := map[string]ProviderHealth{}
	for _, s := range snap {
		byID[s.ProviderID] = s
	}
	if !byID["a"].Healthy {
		t.Fatal("a should be healthy")
	}
	if byID["b"].Healthy {
		t.Fatal("b should be unhealthy")
	}
	if byID["b"].LastError != "down" {
		t.Fatalf("last error = %q", byID["b"].LastError)
	}
}

func TestPickSmartPrefersFreeAndCheap(t *testing.T) {
	// 隐式路由（无路由表）默认 smart：免费优先，其次低价。
	paid := p("paid", 1, 100, "m")
	paid.ProbeModels = []store.ProbeModel{{ID: "m", Free: false, Pricing: &store.Pricing{Prompt: "5", Completion: "10"}}}
	free := p("free", 1, 100, "m")
	free.ProbeModels = []store.ProbeModel{{ID: "m", Free: true, Pricing: &store.Pricing{Prompt: "0", Completion: "0"}}}
	st := newStore(t, []store.Provider{paid, free}, nil)
	rt := New(st, NewTracker(3, 60))

	cands, err := rt.Pick("m", false)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if len(cands) != 2 || cands[0].ProviderID != "free" {
		t.Fatalf("expected free first, got %+v", cands)
	}
}

func TestPickSmartFreeOverridesLowerPrice(t *testing.T) {
	// 免费（探测未标记）但 id 带 :free 后缀 → 仍按免费优先于便宜付费家。
	cheap := p("cheap", 1, 100, "other:free")
	cheap.ProbeModels = []store.ProbeModel{{ID: "other:free", Free: false, Pricing: &store.Pricing{Prompt: "0.1", Completion: "0.2"}}}
	// 两个 provider 声明支持 "other:free"：一个免费标记、一个 id 后缀免费
	marked := p("marked", 1, 100, "other:free")
	marked.ProbeModels = []store.ProbeModel{{ID: "other:free", Free: true}}
	st := newStore(t, []store.Provider{cheap, marked}, nil)
	rt := New(st, NewTracker(3, 60))

	cands, err := rt.Pick("other:free", false)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if len(cands) != 2 || cands[0].ProviderID != "marked" {
		t.Fatalf("expected marked (free) first, got %+v", cands)
	}
}

func TestPickSkipsModelMarkedUnavailable(t *testing.T) {
	// 某 provider 上模型被上游 404 标记不可用后，Pick 应跳过它（冷却期内）。
	a := p("a", 1, 100, "m")
	b := p("b", 1, 100, "m")
	st := newStore(t, []store.Provider{a, b}, nil)
	rt := New(st, NewTracker(3, 60))

	st.MarkModelUnavailable("a", "m", "upstream 404 model not found", time.Hour)
	cands, err := rt.Pick("m", false)
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	if len(cands) != 1 || cands[0].ProviderID != "b" {
		t.Fatalf("expected only b (a unavailable), got %+v", cands)
	}

	// 到期后自动恢复。
	st.MarkModelUnavailable("a", "m", "gone", -time.Minute)
	cands, err = rt.Pick("m", false)
	if err != nil {
		t.Fatalf("pick after expiry: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("expected both after expiry, got %+v", cands)
	}
}

func TestPickAllMarkedUnavailableErrors(t *testing.T) {
	a := p("a", 1, 100, "m")
	st := newStore(t, []store.Provider{a}, nil)
	rt := New(st, NewTracker(3, 60))
	st.MarkModelUnavailable("a", "m", "gone", time.Hour)

	if _, err := rt.Pick("m", false); err == nil {
		t.Fatalf("expected error when all candidates unavailable")
	}
}

func TestPickFallsBackToCatchAllRoute(t *testing.T) {
	// 精确路由优先；未显式路由的模型走通配兜底路由。
	st := newStore(t,
		[]store.Provider{
			p("exact", 1, 100, "m"),
			p("catchall", 1, 100, "other-model"),
		},
		[]store.Route{
			{Model: "m", Strategy: "failover", Targets: []store.RouteTarget{{ProviderID: "exact"}}},
			{Model: "", Strategy: "failover", Targets: []store.RouteTarget{{ProviderID: "catchall"}}},
		},
	)
	rt := New(st, NewTracker(3, 60))

	cands, err := rt.Pick("m", false)
	if err != nil {
		t.Fatalf("pick m: %v", err)
	}
	if len(cands) != 1 || cands[0].ProviderID != "exact" {
		t.Fatalf("expected exact route for m, got %+v", cands)
	}

	cands, err = rt.Pick("anything-else", false)
	if err != nil {
		t.Fatalf("pick catchall: %v", err)
	}
	if len(cands) != 1 || cands[0].ProviderID != "catchall" {
		t.Fatalf("expected catchall route, got %+v", cands)
	}
	// 通配 target 未配模型时，透传请求的模型名。
	if cands[0].UpstreamModel != "anything-else" {
		t.Fatalf("expected upstream model passthrough, got %q", cands[0].UpstreamModel)
	}
}

func TestUpsertCatchAllRouteAllowsEmptyModel(t *testing.T) {
	st := newStore(t, nil, nil)
	if err := st.UpsertRoute(store.Route{Model: "", Strategy: "failover", Targets: []store.RouteTarget{{ProviderID: "a"}}}); err != nil {
		t.Fatalf("upsert catch-all: %v", err)
	}
	if err := st.UpsertRoute(store.Route{Model: "", Strategy: "smart", Targets: []store.RouteTarget{{ProviderID: "b"}}}); err != nil {
		t.Fatalf("upsert smart: %v", err)
	}
	routes := st.ListRoutes()
	empty := 0
	for _, r := range routes {
		if r.Model == "" {
			empty++
			if len(r.Targets) != 1 || r.Targets[0].ProviderID != "b" {
				t.Fatalf("expected catch-all replaced, got %+v", r)
			}
		}
	}
	if empty != 1 {
		t.Fatalf("expected exactly one catch-all route, got %d", empty)
	}
}
