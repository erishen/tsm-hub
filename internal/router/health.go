// Package router 负责「对外模型名 → 上游 provider」的智能路由。
//
// 路由决策综合了三件事：路由表配置（优先级 / 权重）、provider 实时健康度、
// 以及历史延迟（EWMA）。上游连续失败会被摘除并进入冷却，冷却结束后半开探测。
package router

import (
	"sync"
	"time"
)

// defaultFailureTTL 是「连续失败计数」的有效期。超过该时长没有新的失败，
// 历史 failures 自动视为过期归零：一次偶发 500/429 不该把 provider 永久钉在
// 半开池里（详见 router.Pick 的健康分档说明）。5 分钟 > 默认冷却 60s，
// 保证真正持续故障的 provider 仍会被熔断与指数退避覆盖。
const defaultFailureTTL = 5 * time.Minute

// ProviderHealth 是单个 provider 的运行时健康状态。
type ProviderHealth struct {
	ProviderID string `json:"provider_id"`
	Healthy    bool   `json:"healthy"`
	// HalfOpen 是真半开态：曾被熔断摘除（down_until 被设置过）、冷却已过、
	// 尚未有一次成功恢复（down_until 未清零）。区别于 failures>0（可能只是
	// 轻微失败或客户端取消残留），前端只对 HalfOpen 显示 "half-open"。
	HalfOpen      bool      `json:"half_open"`
	Failures      int       `json:"failures"`
	LastError     string    `json:"last_error,omitempty"`
	LastOKAt      time.Time `json:"last_ok_at,omitempty"`
	LastFailAt    time.Time `json:"last_fail_at,omitempty"`
	DownUntil     time.Time `json:"down_until,omitempty"`
	ThrottleUntil time.Time `json:"throttle_until,omitempty"`
	LatencyMS     int64     `json:"latency_ms"`
	Requests      int64     `json:"requests"`
	Errors        int64     `json:"errors"`
}

type state struct {
	failures      int
	lastErr       string
	lastOK        time.Time
	lastFail      time.Time
	downUntil     time.Time
	throttleUntil time.Time // 429 限流冷却到期时间
	latencyMS     int64     // EWMA
	requests      int64
	errors        int64
}

// Tracker 记录所有 provider 的健康状态。
type Tracker struct {
	mu         sync.RWMutex
	states     map[string]*state
	failMax    int
	cool       time.Duration
	deadMin    int           // 从未成功的 provider 连续失败达到该值后，冷却指数退避
	failureTTL time.Duration // 连续失败计数有效期，过期后惰性归零
	persistFn  func()        // 状态变更后的持久化回调（由调用方注入）
}

// NewTracker 创建健康跟踪器。
func NewTracker(failThreshold int, cooldownSec int) *Tracker {
	if failThreshold <= 0 {
		failThreshold = 3
	}
	if cooldownSec <= 0 {
		cooldownSec = 60
	}
	return &Tracker{
		states:     map[string]*state{},
		failMax:    failThreshold,
		cool:       time.Duration(cooldownSec) * time.Second,
		deadMin:    3 * failThreshold,
		failureTTL: defaultFailureTTL,
	}
}

// SetFailureTTL 覆盖连续失败计数的有效期；<=0 时恢复默认值。
// 由 main 从 settings.failure_ttl_sec 注入，测试可用短 TTL 验证自愈。
func (t *Tracker) SetFailureTTL(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if d <= 0 {
		d = defaultFailureTTL
	}
	t.failureTTL = d
}

// effectiveFailuresLocked 返回当前「有效」的连续失败数。
//
// 惰性计算：只在读取时判断 lastFail 是否已过期，不改 state、不起后台
// goroutine，也不额外触发持久化。因此：
//   - 持续故障（每次失败都会刷新 lastFail）→ 计数不清零，熔断/退避照常；
//   - 一次偶发失败后长期无请求 → TTL 到期自动回到健康池；
//   - 重启 Restore 回来的陈旧计数（如十几天前的 500）→ 当场过期。
//
// 调用方必须已持有 t.mu（读锁或写锁）。
func (t *Tracker) effectiveFailuresLocked(s *state, now time.Time) int {
	if s == nil || s.failures <= 0 {
		return 0
	}
	// lastFail 为零值只可能来自手写快照；保守起见按「未过期」处理，
	// 避免把真实故障误判成健康。
	if !s.lastFail.IsZero() && now.Sub(s.lastFail) >= t.failureTTL {
		return 0
	}
	return s.failures
}

func (t *Tracker) get(id string) *state {
	s := t.states[id]
	if s == nil {
		s = &state{}
		t.states[id] = s
	}
	return s
}

// Available 判断 provider 此刻是否可接客（未被摘除，或冷却已过进入半开）。
func (t *Tracker) Available(id string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.states[id]
	if !ok {
		return true
	}
	return s.availableLocked(time.Now())
}

func (s *state) availableLocked(now time.Time) bool {
	if s.downUntil.IsZero() {
		return true
	}
	return !now.Before(s.downUntil)
}

// ReportSuccess 上报一次成功，重置失败计数并更新延迟 EWMA。
func (t *Tracker) ReportSuccess(id string, latency time.Duration) {
	t.mu.Lock()
	s := t.get(id)
	s.failures = 0
	s.lastErr = ""
	s.downUntil = time.Time{}
	s.throttleUntil = time.Time{}
	s.lastOK = time.Now()
	s.requests++
	ms := latency.Milliseconds()
	if s.latencyMS == 0 {
		s.latencyMS = ms
	} else {
		s.latencyMS = (s.latencyMS*7 + ms*3) / 10
	}
	t.mu.Unlock()
	// persist 必须在锁外调用：RLock 会与上面的写锁互斥（同 goroutine 死锁）。
	t.persist()
}

// ReportFailure 上报一次失败；连续失败达到阈值则摘除并冷却。
//
// 恒死 provider 指数退避：从未成功过（lastOK 为零值）且连续失败达到
// deadMin 后，冷却时长按 2^k 翻倍、封顶 1h。普通「failMax 次 → 冷却
// cool → 半开」节奏对配置性死亡（恒 deadline exceeded、域名失效等）是
// 永久空转：每隔 cool 秒就被半开探测一次，每次白吃一个完整上游超时，
// 还长期占着 failover 链尾。退避后最长 1 小时才探一次；成功一次即由
// ReportSuccess 清零 failures，自然回到普通节奏（充值/修复后自愈不变慢）。
// 已成功过的 provider 不退避——间歇性失败与「从未成功」是两回事。
func (t *Tracker) ReportFailure(id, errMsg string) {
	t.mu.Lock()
	s := t.get(id)
	now := time.Now()
	// 基于「有效」失败数累加：陈旧计数已过期时从 1 重新爬，
	// 否则十几天前的一次历史失败会被无上限累加，让新故障直接触发指数退避。
	s.failures = t.effectiveFailuresLocked(s, now) + 1
	s.lastErr = errMsg
	s.lastFail = now
	s.requests++
	s.errors++
	if s.failures >= t.failMax {
		cool := t.cool
		if s.lastOK.IsZero() && s.failures >= t.deadMin {
			k := s.failures - t.deadMin
			if k > 6 {
				k = 6
			}
			if backoff := t.cool * (1 << uint(k)); backoff > cool {
				cool = backoff
			}
			if cool > time.Hour {
				cool = time.Hour
			}
		}
		s.downUntil = now.Add(cool)
	}
	t.mu.Unlock()
	t.persist()
}

// ReportThrottle 上报一次上游 429（限流/免费额度耗尽）：
// 立即摘除冷却 cool 秒（不等待失败阈值），期间该 provider 不再被选中，
// 避免免费家被 429 后每次请求都先白吃一次限流再降级。
func (t *Tracker) ReportThrottle(id, errMsg string, cool time.Duration) {
	t.mu.Lock()
	s := t.get(id)
	now := time.Now()
	s.failures = t.effectiveFailuresLocked(s, now) + 1
	s.lastErr = errMsg
	s.lastFail = now
	s.requests++
	s.errors++
	until := now.Add(cool)
	if until.After(s.downUntil) {
		s.downUntil = until
	}
	s.throttleUntil = until
	t.mu.Unlock()
	t.persist()
}

// Throttled 判断 provider 是否处于 429 冷却期。
func (t *Tracker) Throttled(id string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.states[id]
	if !ok {
		return false
	}
	return time.Now().Before(s.throttleUntil)
}

// Snapshot 导出全部健康状态，供管理台展示。
// Healthy 语义：当前不在冷却期即可接客（含半开探测态）；failures 字段单独展示历史失败计数。
func (t *Tracker) Snapshot() []ProviderHealth {
	t.mu.RLock()
	defer t.mu.RUnlock()
	now := time.Now()
	out := make([]ProviderHealth, 0, len(t.states))
	for id, s := range t.states {
		avail := s.availableLocked(now)
		out = append(out, ProviderHealth{
			ProviderID:    id,
			Healthy:       avail,
			HalfOpen:      !s.downUntil.IsZero() && avail,
			Failures:      t.effectiveFailuresLocked(s, now),
			LastError:     s.lastErr,
			LastOKAt:      s.lastOK,
			LastFailAt:    s.lastFail,
			DownUntil:     s.downUntil,
			ThrottleUntil: s.throttleUntil,
			LatencyMS:     s.latencyMS,
			Requests:      s.requests,
			Errors:        s.errors,
		})
	}
	return out
}

// Health 返回单个 provider 的当前状态；尚无任何请求记录时返回健康默认值，
// 供管理台完整展示所有 provider。
// Healthy 语义：当前不在冷却期即可接客（含半开探测态）。
func (t *Tracker) Health(id string) ProviderHealth {
	t.mu.RLock()
	defer t.mu.RUnlock()
	now := time.Now()
	s, ok := t.states[id]
	if !ok {
		return ProviderHealth{ProviderID: id, Healthy: true}
	}
	avail := s.availableLocked(now)
	return ProviderHealth{
		ProviderID: id,
		Healthy:    avail,
		HalfOpen:   !s.downUntil.IsZero() && avail,
		Failures:   t.effectiveFailuresLocked(s, now),
		LastError:  s.lastErr,
		LastOKAt:   s.lastOK,
		LastFailAt: s.lastFail,
		DownUntil:  s.downUntil,
		LatencyMS:  s.latencyMS,
		Requests:   s.requests,
		Errors:     s.errors,
	}
}

// Failures 返回 provider 当前「有效」的连续失败次数，用于判断是否处于半开探测。
// 超过 failureTTL 没有新失败时返回 0（惰性衰减，见 effectiveFailuresLocked）。
func (t *Tracker) Failures(id string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.effectiveFailuresLocked(t.states[id], time.Now())
}

// DownUntil 返回 provider 的冷却到期时间，零值表示未处于冷却。
func (t *Tracker) DownUntil(id string) time.Time {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if s, ok := t.states[id]; ok {
		return s.downUntil
	}
	return time.Time{}
}

// Latency 返回 provider 的延迟 EWMA（毫秒）。
func (t *Tracker) Latency(id string) int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if s, ok := t.states[id]; ok {
		return s.latencyMS
	}
	return 0
}

// SetPersist 注册状态变更后的持久化回调；变更合并由调用方负责。
func (t *Tracker) SetPersist(fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.persistFn = fn
}

func (t *Tracker) persist() {
	t.mu.RLock()
	fn := t.persistFn
	t.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

// Restore 从持久化快照恢复健康状态（重启后保留冷却/失败计数/延迟 EWMA）。
// 只恢复已观测过的 provider；时间类字段原样恢复，是否可用由 availableLocked
// 结合当前时间自然判断（已过期的冷却自动进入半开）。
func (t *Tracker) Restore(ps []ProviderHealth) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range ps {
		s := t.get(p.ProviderID)
		s.failures = p.Failures
		s.lastErr = p.LastError
		s.lastOK = p.LastOKAt
		s.lastFail = p.LastFailAt
		s.downUntil = p.DownUntil
		s.throttleUntil = p.ThrottleUntil
		s.latencyMS = p.LatencyMS
		s.requests = p.Requests
		s.errors = p.Errors
	}
}
