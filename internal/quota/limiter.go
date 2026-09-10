package quota

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/erishen/tsm-gateway/internal/store"
)

// Limiter 基于滑动窗口做 RPM 限流，并结合 Recorder 的累计值做额度判断。
type Limiter struct {
	rec *Recorder

	mu    sync.Mutex
	hits  map[string][]time.Time // keyID -> 请求时间戳
	warn  map[string]string      // keyID -> 已告警的额度类型（避免每个请求刷日志）
	clock func() time.Time
}

// NewLimiter 创建限流器。
func NewLimiter(rec *Recorder) *Limiter {
	return &Limiter{rec: rec, hits: map[string][]time.Time{}, warn: map[string]string{}, clock: time.Now}
}

// warnRatio 是额度使用率告警阈值：达到 80% 时提示一次。
const warnRatio = 0.8

// CheckWarn 检查 Key 是否「刚」跨越任一额度上限的 80% 线（跨线只提示一次，
// 用量回落到线下后重置，可再次提示）。请求通过 Check 后调用，返回 ok=true 时应记 warn 日志。
// 返回的 kind 取值：max_tokens / max_cost_usd / daily_tokens。
func (l *Limiter) CheckWarn(key store.APIKey) (kind string, used, limit float64, ok bool) {
	total := l.rec.Total(key.ID)
	today := l.rec.Today(key.ID)

	type cand struct {
		kind  string
		used  float64
		limit float64
	}
	cands := make([]cand, 0, 3)
	if key.Quota.MaxTokens > 0 {
		cands = append(cands, cand{"max_tokens", float64(total.TotalTokens), float64(key.Quota.MaxTokens)})
	}
	if key.Quota.MaxCostUSD > 0 {
		cands = append(cands, cand{"max_cost_usd", total.CostUSD, key.Quota.MaxCostUSD})
	}
	if key.Quota.DailyTokens > 0 {
		cands = append(cands, cand{"daily_tokens", float64(today.TotalTokens), float64(key.Quota.DailyTokens)})
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	prev := l.warn[key.ID]
	for _, it := range cands {
		if it.limit <= 0 || it.used/it.limit < warnRatio {
			continue
		}
		if prev != it.kind {
			l.warn[key.ID] = it.kind
			return it.kind, it.used, it.limit, true
		}
		return "", 0, 0, false // 已提示过该类型
	}
	delete(l.warn, key.ID) // 全部回落到线下，重置告警状态
	return "", 0, 0, false
}

// DenyReason 描述拒绝原因。
type DenyReason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Check 在请求进入前检查 RPM 与额度。allowed 为 false 时返回原因。
func (l *Limiter) Check(key store.APIKey) (bool, *DenyReason) {
	if !key.Enabled {
		return false, &DenyReason{Code: "key_disabled", Message: "key is disabled"}
	}
	if !key.ExpiresAt.IsZero() && time.Now().After(key.ExpiresAt) {
		return false, &DenyReason{Code: "key_expired", Message: "key is expired"}
	}

	// 1) RPM 滑动窗口
	if key.Quota.RPM > 0 && !l.allowRPM(key.ID, key.Quota.RPM) {
		return false, &DenyReason{
			Code:    "rate_limited",
			Message: fmt.Sprintf("rpm limit exceeded (max %d/min)", key.Quota.RPM),
		}
	}

	// 2) 总 token 额度
	if key.Quota.MaxTokens > 0 && l.rec.Total(key.ID).TotalTokens >= key.Quota.MaxTokens {
		return false, &DenyReason{
			Code:    "quota_exhausted",
			Message: fmt.Sprintf("token quota exhausted (max %d)", key.Quota.MaxTokens),
		}
	}

	// 3) 总金额额度
	if key.Quota.MaxCostUSD > 0 && l.rec.Total(key.ID).CostUSD >= key.Quota.MaxCostUSD {
		return false, &DenyReason{
			Code:    "quota_exhausted",
			Message: fmt.Sprintf("cost quota exhausted (max $%.4f)", key.Quota.MaxCostUSD),
		}
	}

	// 4) 每日 token 额度
	if key.Quota.DailyTokens > 0 && l.rec.Today(key.ID).TotalTokens >= key.Quota.DailyTokens {
		return false, &DenyReason{
			Code:    "daily_quota_exhausted",
			Message: fmt.Sprintf("daily token quota exhausted (max %d)", key.Quota.DailyTokens),
		}
	}
	return true, nil
}

func (l *Limiter) allowRPM(keyID string, rpm int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	cutoff := now.Add(-time.Minute)
	kept := l.hits[keyID][:0]
	for _, t := range l.hits[keyID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= rpm {
		l.hits[keyID] = kept
		return false
	}
	l.hits[keyID] = append(kept, now)
	return true
}

// Snapshot 返回各 Key 当前窗口内的请求数，供管理台展示。
func (l *Limiter) Snapshot() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	cutoff := now.Add(-time.Minute)
	out := make(map[string]int, len(l.hits))
	for k, ts := range l.hits {
		n := 0
		for _, t := range ts {
			if t.After(cutoff) {
				n++
			}
		}
		out[k] = n
	}
	return out
}

// TopKeys 按累计 token 降序返回前 n 个 Key。
func (l *Limiter) TopKeys(n int) []KeyUsage {
	per := l.rec.PerKey()
	out := make([]KeyUsage, 0, len(per))
	for k, v := range per {
		out = append(out, KeyUsage{KeyID: k, Agg: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TotalTokens > out[j].TotalTokens })
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

// KeyUsage 是某个 Key 的用量条目。
type KeyUsage struct {
	KeyID string `json:"key_id"`
	Agg
}
