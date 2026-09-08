package router

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/erishen/llm-router/internal/store"
)

// Candidate 是一个可转发的上游目标。
type Candidate struct {
	ProviderID string
	Provider   store.Provider
	// UpstreamModel 是实际发给上游的模型名（可能被路由表改写）。
	UpstreamModel string
	// Priority 来自路由表 target，未配置时回落到 provider 的优先级。
	Priority int
	// HalfOpen 表示该 provider 刚过冷却期，本次是探测性放行。
	HalfOpen bool
}

// Router 依据路由表与健康度挑选上游。
type Router struct {
	store  *store.Store
	health *Tracker
	rnd    *rand.Rand
}

// New 创建路由器。
func New(s *store.Store, h *Tracker) *Router {
	return &Router{
		store:  s,
		health: h,
		rnd:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Pick 返回按优先级排好序的候选序列，调用方从头依次尝试即可实现 failover。
func (r *Router) Pick(model string) ([]Candidate, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, fmt.Errorf("model is required")
	}

	targets, strategy := r.resolveTargets(model)

	cands := make([]Candidate, 0, len(targets))
	for _, t := range targets {
		p, ok := r.store.GetProvider(t.ProviderID)
		if !ok || !p.Enabled {
			continue
		}
		up := t.Model
		if up == "" {
			up = model
		}
		// target 未显式配置优先级时，回落到 provider 自己的优先级。
		prio := t.Priority
		if prio == 0 {
			prio = p.Priority
		}
		cands = append(cands, Candidate{ProviderID: p.ID, Provider: p, UpstreamModel: up, Priority: prio})
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("no provider serves model %q", model)
	}

	// 健康过滤分三档：健康 / 刚过冷却的半开 / 仍在冷却中。
	healthy := make([]Candidate, 0, len(cands))
	halfOpen := make([]Candidate, 0, len(cands))
	cooling := make([]Candidate, 0, len(cands))
	for _, c := range cands {
		switch {
		case !r.health.Available(c.ProviderID):
			c.HalfOpen = true
			cooling = append(cooling, c)
		case r.health.Failures(c.ProviderID) > 0:
			c.HalfOpen = true
			halfOpen = append(halfOpen, c)
		default:
			healthy = append(healthy, c)
		}
	}

	usable := healthy
	if len(usable) == 0 {
		usable = halfOpen
	}
	if len(usable) == 0 && len(cooling) > 0 {
		// 全部仍在冷却：挑最快恢复的那个做一次探测，
		// 否则单 provider 场景会一直 502，永远等不到它恢复。
		sort.Slice(cooling, func(i, j int) bool {
			return r.health.DownUntil(cooling[i].ProviderID).
				Before(r.health.DownUntil(cooling[j].ProviderID))
		})
		usable = cooling[:1]
	}
	if len(usable) == 0 {
		return nil, fmt.Errorf("no healthy provider for model %q (all in cooldown)", model)
	}

	return r.order(usable, strategy), nil
}

// resolveTargets 先查显式路由表，再回退到「provider 声明支持该模型」的隐式路由。
func (r *Router) resolveTargets(model string) ([]store.RouteTarget, string) {
	for _, rt := range r.store.ListRoutes() {
		if rt.Model == model && len(rt.Targets) > 0 {
			return rt.Targets, rt.Strategy
		}
	}
	implicit := make([]store.RouteTarget, 0, 4)
	for _, p := range r.store.ListProviders() {
		if !p.Enabled {
			continue
		}
		if supportsModel(p, model) {
			w := p.Weight
			implicit = append(implicit, store.RouteTarget{
				ProviderID: p.ID,
				Weight:     w,
				Priority:   p.Priority,
			})
		}
	}
	return implicit, "weighted"
}

func supportsModel(p store.Provider, model string) bool {
	for _, m := range p.Models {
		if m == model || m == "*" {
			return true
		}
	}
	return false
}

// order 按策略排序候选：failover 严格按 priority 升序；weighted 按延迟加权打散。
func (r *Router) order(cands []Candidate, strategy string) []Candidate {
	out := append([]Candidate(nil), cands...)

	// 优先级分组（升序，数字越小越优先）。
	byPrio := make(map[int][]Candidate)
	priorities := make([]int, 0, len(out))
	for _, c := range out {
		prio := priorityOf(c)
		if _, ok := byPrio[prio]; !ok {
			priorities = append(priorities, prio)
		}
		byPrio[prio] = append(byPrio[prio], c)
	}
	sort.Ints(priorities)

	if strategy == "failover" {
		result := make([]Candidate, 0, len(out))
		for _, prio := range priorities {
			result = append(result, r.weightedShuffle(byPrio[prio])...)
		}
		return result
	}

	// weighted：所有候选同池，按「配置权重 × 延迟因子」加权随机排序。
	pool := make([]Candidate, 0, len(out))
	for _, prio := range priorities {
		pool = append(pool, byPrio[prio]...)
	}
	return r.weightedShuffle(pool)
}

func priorityOf(c Candidate) int {
	return c.Priority
}

// weightedShuffle 按权重做加权随机排序（权重高的更靠前）。
func (r *Router) weightedShuffle(in []Candidate) []Candidate {
	if len(in) <= 1 {
		return in
	}
	type item struct {
		c Candidate
		w float64
	}
	items := make([]item, 0, len(in))
	for _, c := range in {
		w := float64(normalizeWeight(c.Provider.Weight))
		// 延迟因子：EWMA 越高，权重越低（1000ms 时折半）。
		if lat := r.health.Latency(c.ProviderID); lat > 0 {
			w *= 1000.0 / (1000.0 + float64(lat))
		}
		if c.HalfOpen {
			w *= 0.1 // 半开探测降权，正常节点优先
		}
		if w <= 0 {
			w = 1
		}
		items = append(items, item{c: c, w: w})
	}
	out := make([]Candidate, 0, len(items))
	for len(items) > 0 {
		total := 0.0
		for _, it := range items {
			total += it.w
		}
		pick := r.rnd.Float64() * total
		idx := 0
		acc := 0.0
		for i, it := range items {
			acc += it.w
			if pick <= acc {
				idx = i
				break
			}
		}
		out = append(out, items[idx].c)
		items = append(items[:idx], items[idx+1:]...)
	}
	return out
}

func normalizeWeight(w int) int {
	if w <= 0 {
		return 100
	}
	return w
}
