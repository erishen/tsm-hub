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
