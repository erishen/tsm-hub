package router

import (
	"testing"
	"time"
)

// deadDrain 把 provider 打到连续 n 次失败（从未成功）。
func deadDrain(t *testing.T, tr *Tracker, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		tr.ReportFailure(id, "boom")
	}
}

// 普通熔断语义不变：已经成功过的 provider（lastOK 非零），连续失败再多
// 也只冷却 cool，绝不触发恒死退避——间歇性失败与「从未成功」是两回事。
func TestReportFailure_KnownGoodNeverEscalates(t *testing.T) {
	tr := NewTracker(3, 60) // failMax=3, deadMin=9, cool=60s
	const id = "p-ok"
	tr.ReportSuccess(id, 100*time.Millisecond) // 先成功一次
	deadDrain(t, tr, id, 20)                   // 远超 deadMin

	d := time.Until(tr.DownUntil(id))
	if d <= 0 || d > tr.cool+5*time.Second {
		t.Fatalf("已成功过的 provider 冷却应≈cool(60s)，got %v", d)
	}
}

// 从未成功且连续失败达到 deadMin 后，冷却按 2^k 指数翻倍。
func TestReportFailure_NeverSucceededEscalates(t *testing.T) {
	tr := NewTracker(3, 60) // deadMin=9
	const id = "p-dead"

	// deadMin 之前：普通熔断（failMax=3 起就 60s 冷却）
	deadDrain(t, tr, id, 3)
	if d := time.Until(tr.DownUntil(id)); d <= 0 || d > tr.cool+5*time.Second {
		t.Fatalf("deadMin 前应普通冷却 60s，got %v", d)
	}

	// 刚到 deadMin：1 倍 cool；之后每多失败一次翻倍
	deadDrain(t, tr, id, 7) // 累计 failures=10, k=1 → 120s
	d := time.Until(tr.DownUntil(id))
	if d < 110*time.Second || d > 130*time.Second {
		t.Fatalf("failures=10 应退避 120s，got %v", d)
	}

	deadDrain(t, tr, id, 2) // failures=12, k=3 → 480s
	d = time.Until(tr.DownUntil(id))
	if d < 470*time.Second || d > 490*time.Second {
		t.Fatalf("failures=12 应退避 480s，got %v", d)
	}
}

// 恒死退避封顶 1 小时：无限失败也不会把冷却推到 1h 以上。
func TestReportFailure_DeadCooldownCapped(t *testing.T) {
	tr := NewTracker(3, 60)
	const id = "p-capped"
	deadDrain(t, tr, id, 100)
	d := time.Until(tr.DownUntil(id))
	if d <= 0 || d > time.Hour {
		t.Fatalf("冷却应封顶 1h，got %v", d)
	}
	// 退避期间必须出轮转（不再可接客）
	if tr.Available(id) {
		t.Fatal("退避期内 provider 不应 Available")
	}
}

// 成功一次即复位：failures 清零，后续失败从普通熔断重新爬，不继承退避。
func TestReportSuccess_ResetsEscalation(t *testing.T) {
	tr := NewTracker(3, 60)
	const id = "p-heal"
	deadDrain(t, tr, id, 20) // 已处于 1h 封顶退避
	tr.ReportSuccess(id, 50*time.Millisecond)

	if f := tr.Failures(id); f != 0 {
		t.Fatalf("成功后 failures 应清零，got %d", f)
	}
	// 重新爬到 failMax=3：连续 3 次失败后应回到普通 60s 冷却（不继承退避）
	deadDrain(t, tr, id, 3)
	if d := time.Until(tr.DownUntil(id)); d <= 0 || d > tr.cool+5*time.Second {
		t.Fatalf("复位后重新熔断应普通冷却 60s，got %v", d)
	}
}

// 一次偶发失败不能把 provider 永久钉在半开：超过 failureTTL 且无新失败时，
// Failures/Health/Snapshot 都应读到 0（惰性衰减，不依赖成功样本）。
func TestFailuresDecayAfterTTL(t *testing.T) {
	tr := NewTracker(3, 60)
	tr.SetFailureTTL(2 * time.Minute)
	const id = "p-stale"

	tr.ReportFailure(id, "boom")
	if got := tr.Failures(id); got != 1 {
		t.Fatalf("刚失败应为 1，got %d", got)
	}
	// 把 lastFail 拨到 TTL 之前，模拟「十几天前的一次 500」。
	tr.mu.Lock()
	tr.states[id].lastFail = time.Now().Add(-3 * time.Minute)
	tr.mu.Unlock()

	if got := tr.Failures(id); got != 0 {
		t.Fatalf("过期失败应衰减为 0，got %d", got)
	}
	if got := tr.Health(id).Failures; got != 0 {
		t.Fatalf("Health().Failures 应同步衰减，got %d", got)
	}
	for _, h := range tr.Snapshot() {
		if h.ProviderID == id && h.Failures != 0 {
			t.Fatalf("Snapshot().Failures 应同步衰减，got %d", h.Failures)
		}
	}
}

// 陈旧计数不得继续累加：TTL 过期后的新失败从 1 重新爬，
// 不会因为历史 failures=10 而直接命中指数退避。
func TestReportFailureRestartsAfterTTL(t *testing.T) {
	tr := NewTracker(3, 60)
	tr.SetFailureTTL(time.Minute)
	const id = "p-restart"

	deadDrain(t, tr, id, 10) // 历史 failures=10，已进入长退避
	tr.mu.Lock()
	tr.states[id].lastFail = time.Now().Add(-2 * time.Minute)
	tr.mu.Unlock()

	tr.ReportFailure(id, "new boom")
	if got := tr.Failures(id); got != 1 {
		t.Fatalf("新失败应从 1 重新计数，got %d", got)
	}
}

// 持续故障不受衰减影响：每次失败都刷新 lastFail，计数照常累加并熔断。
func TestFailuresDoNotDecayWhileFailing(t *testing.T) {
	tr := NewTracker(3, 60)
	tr.SetFailureTTL(time.Minute)
	const id = "p-active"

	deadDrain(t, tr, id, 3)
	if got := tr.Failures(id); got != 3 {
		t.Fatalf("持续失败应保持计数，got %d", got)
	}
	if tr.Available(id) {
		t.Fatal("达到阈值后仍应处于冷却")
	}
}

// 重启 Restore 回来的陈旧失败计数应立即过期 —— 这正是 agnes 因 10-24
// 那次 500 被锁死 11 天的原始场景（状态持久化在 data/memory.db）。
func TestRestoreStaleFailuresDecay(t *testing.T) {
	tr := NewTracker(3, 60)
	tr.SetFailureTTL(5 * time.Minute)
	tr.Restore([]ProviderHealth{{
		ProviderID: "agnes",
		Failures:   1,
		LastFailAt: time.Now().AddDate(0, 0, -11),
		LastOKAt:   time.Now().AddDate(0, 0, -17),
	}})
	if got := tr.Failures("agnes"); got != 0 {
		t.Fatalf("11 天前的失败应过期，got %d", got)
	}
}

// 未配置时回落到默认 TTL，且 SetFailureTTL(0) 可恢复默认。
func TestFailureTTLDefault(t *testing.T) {
	tr := NewTracker(3, 60)
	if tr.failureTTL != defaultFailureTTL {
		t.Fatalf("默认 TTL = %v, want %v", tr.failureTTL, defaultFailureTTL)
	}
	tr.SetFailureTTL(time.Second)
	tr.SetFailureTTL(0)
	if tr.failureTTL != defaultFailureTTL {
		t.Fatalf("SetFailureTTL(0) 应恢复默认，got %v", tr.failureTTL)
	}
}

// 延迟 EWMA 与失败计数一样需要惰性过期：只在 ReportSuccess 更新、失败不动，
// 没有 TTL 时 2026-11-04 一次慢成功会让 58185ms 一直挂到 11-14（kimi 假告警
// 的根因），同时让 Router.Pick 的延迟降权长期误判。
func TestLatencyDecaysAfterTTL(t *testing.T) {
	tr := NewTracker(3, 60)
	tr.SetLatencyTTL(2 * time.Minute)
	const id = "p-latency-stale"

	tr.ReportSuccess(id, 4000*time.Millisecond)
	if got := tr.Latency(id); got != 4000 {
		t.Fatalf("刚成功应为 4000，got %d", got)
	}
	// 把最后一次成功拨到 TTL 之前，模拟「十几天前的一次慢响应」。
	tr.mu.Lock()
	tr.states[id].lastOK = time.Now().Add(-3 * time.Minute)
	tr.mu.Unlock()

	if got := tr.Latency(id); got != 0 {
		t.Fatalf("过期 EWMA 应衰减为 0，got %d", got)
	}
	if got := tr.Health(id).LatencyMS; got != 0 {
		t.Fatalf("Health().LatencyMS 应同步衰减，got %d", got)
	}
	for _, h := range tr.Snapshot() {
		if h.ProviderID == id && h.LatencyMS != 0 {
			t.Fatalf("Snapshot().LatencyMS 应同步衰减（/metrics 读这里），got %d", h.LatencyMS)
		}
	}
}

// 陈旧值过期后的第一次成功必须直接采新样本，不能让化石值以 7/10 权重继续污染。
func TestLatencyStaleBaseDoesNotPollute(t *testing.T) {
	tr := NewTracker(3, 60)
	tr.SetLatencyTTL(time.Minute)
	const id = "p-latency-reseed"

	tr.ReportSuccess(id, 5000*time.Millisecond)
	tr.mu.Lock()
	tr.states[id].lastOK = time.Now().Add(-2 * time.Minute)
	tr.mu.Unlock()

	tr.ReportSuccess(id, 100*time.Millisecond)
	if got := tr.Latency(id); got != 100 {
		t.Fatalf("过期后首次成功应重置为 100ms，got %d（若读到 ~3530 说明旧值仍在污染）", got)
	}
}

// 重启 Restore 回来的陈旧延迟应立即过期 —— kimi 的 health 快照正是
// latency_ms=58185 + last_ok_at=2026-11-04 的原始场景。
func TestRestoreStaleLatencyDecays(t *testing.T) {
	tr := NewTracker(3, 60)
	tr.Restore([]ProviderHealth{{
		ProviderID: "kimi",
		LatencyMS:  58185,
		LastOKAt:   time.Now().AddDate(0, 0, -10),
		Requests:   33,
		Errors:     22,
	}})
	if got := tr.Latency("kimi"); got != 0 {
		t.Fatalf("10 天前的 EWMA 应过期，got %d", got)
	}
	for _, h := range tr.Snapshot() {
		if h.ProviderID == "kimi" && h.LatencyMS != 0 {
			t.Fatalf("Snapshot 不得把陈旧 EWMA 导出到 /metrics，got %d", h.LatencyMS)
		}
	}
	// 计数与失败状态不受影响。
	if h := tr.Health("kimi"); h.Requests != 33 || h.Errors != 22 {
		t.Fatalf("requests/errors 应原样恢复，got %+v", h)
	}
}

// 手写/旧版快照没有 lastOK 时保守按过期处理。
func TestLatencyZeroLastOKIsStale(t *testing.T) {
	tr := NewTracker(3, 60)
	tr.Restore([]ProviderHealth{{ProviderID: "legacy", LatencyMS: 9000}})
	if got := tr.Latency("legacy"); got != 0 {
		t.Fatalf("无成功时间戳的 EWMA 应视为过期，got %d", got)
	}
}

// 持续成功不受 TTL 影响：每次都刷新 lastOK，EWMA 按 7:3 正常收敛。
func TestLatencyDoesNotDecayWhileSucceeding(t *testing.T) {
	tr := NewTracker(3, 60)
	tr.SetLatencyTTL(time.Minute)
	const id = "p-active"
	tr.ReportSuccess(id, 1000*time.Millisecond)
	tr.ReportSuccess(id, 1000*time.Millisecond)
	if got := tr.Latency(id); got != 1000 {
		t.Fatalf("持续成功应保持 1000，got %d", got)
	}
	tr.ReportSuccess(id, 500*time.Millisecond)
	if got := tr.Latency(id); got != 850 {
		t.Fatalf("EWMA 应为 (1000*7+500*3)/10=850，got %d", got)
	}
}

// 默认 TTL 与 SetLatencyTTL(0) 回落。
func TestLatencyTTLDefault(t *testing.T) {
	tr := NewTracker(3, 60)
	if tr.latencyTTL != defaultLatencyTTL {
		t.Fatalf("默认 latencyTTL = %v, want %v", tr.latencyTTL, defaultLatencyTTL)
	}
	tr.SetLatencyTTL(time.Second)
	if tr.latencyTTL != time.Second {
		t.Fatalf("SetLatencyTTL 未生效，got %v", tr.latencyTTL)
	}
	tr.SetLatencyTTL(0)
	if tr.latencyTTL != defaultLatencyTTL {
		t.Fatalf("SetLatencyTTL(0) 应恢复默认，got %v", tr.latencyTTL)
	}
}
