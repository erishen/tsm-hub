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
