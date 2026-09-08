// Package router 负责「对外模型名 → 上游 provider」的智能路由。
//
// 路由决策综合了三件事：路由表配置（优先级 / 权重）、provider 实时健康度、
// 以及历史延迟（EWMA）。上游连续失败会被摘除并进入冷却，冷却结束后半开探测。
package router

import (
	"sync"
	"time"
)

// ProviderHealth 是单个 provider 的运行时健康状态。
type ProviderHealth struct {
	ProviderID string    `json:"provider_id"`
	Healthy    bool      `json:"healthy"`
	Failures   int       `json:"failures"`
	LastError  string    `json:"last_error,omitempty"`
	LastOKAt   time.Time `json:"last_ok_at,omitempty"`
	LastFailAt time.Time `json:"last_fail_at,omitempty"`
	DownUntil  time.Time `json:"down_until,omitempty"`
	LatencyMS  int64     `json:"latency_ms"`
	Requests   int64     `json:"requests"`
	Errors     int64     `json:"errors"`
}

type state struct {
	failures  int
	lastErr   string
	lastOK    time.Time
	lastFail  time.Time
	downUntil time.Time
	latencyMS int64 // EWMA
	requests  int64
	errors    int64
}

// Tracker 记录所有 provider 的健康状态。
type Tracker struct {
	mu      sync.RWMutex
	states  map[string]*state
	failMax int
	cool    time.Duration
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
		states:  map[string]*state{},
		failMax: failThreshold,
		cool:    time.Duration(cooldownSec) * time.Second,
	}
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
	defer t.mu.Unlock()
	s := t.get(id)
	s.failures = 0
	s.lastErr = ""
	s.downUntil = time.Time{}
	s.lastOK = time.Now()
	s.requests++
	ms := latency.Milliseconds()
	if s.latencyMS == 0 {
		s.latencyMS = ms
	} else {
		s.latencyMS = (s.latencyMS*7 + ms*3) / 10
	}
}

// ReportFailure 上报一次失败；连续失败达到阈值则摘除并冷却。
func (t *Tracker) ReportFailure(id, errMsg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.get(id)
	s.failures++
	s.lastErr = errMsg
	s.lastFail = time.Now()
	s.requests++
	s.errors++
	if s.failures >= t.failMax {
		s.downUntil = time.Now().Add(t.cool)
	}
}

// Snapshot 导出全部健康状态，供管理台展示。
func (t *Tracker) Snapshot() []ProviderHealth {
	t.mu.RLock()
	defer t.mu.RUnlock()
	now := time.Now()
	out := make([]ProviderHealth, 0, len(t.states))
	for id, s := range t.states {
		out = append(out, ProviderHealth{
			ProviderID: id,
			Healthy:    s.availableLocked(now) && s.failures < t.failMax,
			Failures:   s.failures,
			LastError:  s.lastErr,
			LastOKAt:   s.lastOK,
			LastFailAt: s.lastFail,
			DownUntil:  s.downUntil,
			LatencyMS:  s.latencyMS,
			Requests:   s.requests,
			Errors:     s.errors,
		})
	}
	return out
}

// Failures 返回 provider 当前的连续失败次数，用于判断是否处于半开探测。
func (t *Tracker) Failures(id string) int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if s, ok := t.states[id]; ok {
		return s.failures
	}
	return 0
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
