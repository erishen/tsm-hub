package quota

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/erishen/tsm-hub/internal/store"
)

// Limiter 基于滑动窗口做 RPM 限流，并结合 Recorder 的累计值做额度判断。
type Limiter struct {
	rec *Recorder

	mu    sync.Mutex
	hits  map[string][]time.Time // keyID -> 请求时间戳
	warn  map[string]string      // keyID -> 已告警的额度类型（避免每个请求刷日志）
	clock func() time.Time

	// lastSweep 是上次全量清理时间。清理要扫所有 key，故最多每分钟做一次，
	// 避免每个请求都 O(n)；调用方必须持有 mu。
	lastSweep time.Time
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

// WarnInfo 是某个 Key 的额度告警快照，供管理台红点展示。
type WarnInfo struct {
	KeyID string  `json:"key_id"`
	Name  string  `json:"name"`
	Kind  string  `json:"kind"` // max_tokens / max_cost_usd / daily_tokens
	Used  float64 `json:"used"`
	Limit float64 `json:"limit"`
	Ratio float64 `json:"ratio"` // used/limit
}

// SnapshotWarnings 主动计算并返回当前所有 key 中额度使用率越过 warnRatio 线（>=80%）的项，
// 供管理台红点展示。与 CheckWarn 的"跨线只提示一次、记日志"解耦：这里每次都实时计算，不改内部状态。
func (l *Limiter) SnapshotWarnings(keys []store.APIKey) []WarnInfo {
	out := make([]WarnInfo, 0, len(keys))
	for _, k := range keys {
		if !k.Enabled {
			continue
		}
		total := l.rec.Total(k.ID)
		today := l.rec.Today(k.ID)
		type cand struct {
			kind  string
			used  float64
			limit float64
		}
		cands := make([]cand, 0, 3)
		if k.Quota.MaxTokens > 0 {
			cands = append(cands, cand{"max_tokens", float64(total.TotalTokens), float64(k.Quota.MaxTokens)})
		}
		if k.Quota.MaxCostUSD > 0 {
			cands = append(cands, cand{"max_cost_usd", total.CostUSD, k.Quota.MaxCostUSD})
		}
		if k.Quota.DailyTokens > 0 {
			cands = append(cands, cand{"daily_tokens", float64(today.TotalTokens), float64(k.Quota.DailyTokens)})
		}
		for _, c := range cands {
			if c.limit <= 0 {
				continue
			}
			if r := c.used / c.limit; r >= warnRatio {
				out = append(out, WarnInfo{
					KeyID: k.ID,
					Name:  k.Name,
					Kind:  c.kind,
					Used:  c.used,
					Limit: c.limit,
					Ratio: r,
				})
			}
		}
	}
	return out
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

// sweepInterval 是惰性清理的最小间隔，与 RPM 滑动窗口同量级。
const sweepInterval = time.Minute

// sweepLocked 删除窗口内已无请求的 key 条目（Key 被删除 / 长期不用后不再常驻），
// 并压缩仍活跃 key 的时间戳切片，释放过期元素占用的底层数组空间。
// warn 与 hits 同步清理，避免额度告警状态也无限累积。
// 调用方必须持有 l.mu。
func (l *Limiter) sweepLocked(now time.Time) {
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = now
	cutoff := now.Add(-time.Minute)
	for k, ts := range l.hits {
		kept := ts[:0]
		for _, t := range ts {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(l.hits, k)
			delete(l.warn, k)
			continue
		}
		l.hits[k] = kept
	}
}

func (l *Limiter) allowRPM(keyID string, rpm int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock()
	// 任何一次请求都顺便清理其它已失效的 key（内部按分钟节流）。
	l.sweepLocked(now)
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
	l.sweepLocked(now)
	cutoff := now.Add(-time.Minute)
	out := make(map[string]int, len(l.hits))
	for k, ts := range l.hits {
		n := 0
		for _, t := range ts {
			if t.After(cutoff) {
				n++
			}
		}
		// 只报窗口内仍活跃的 key；管理台用 rpm[k.ID] 按当前 Key 列表取值，
		// 缺失即 0，无需为已清理的 key 保留幽灵条目。
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
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TotalTokens != out[j].TotalTokens {
			return out[i].TotalTokens > out[j].TotalTokens
		}
		return out[i].KeyID < out[j].KeyID
	})
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
