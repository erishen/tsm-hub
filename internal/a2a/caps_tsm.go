// caps_tsm.go — tsm-hub 的 tsm.* 能力实现（A2A 业务层）。
//
// 全部只读 + 目录查询，不暴露任何写入/执行能力（改路由/发 key/清用量仍只走
// 管理 API 的 admin token）。白名单语义与 lume-a2a 的 /peer/invoke 一致：
// 只认 Capabilities 声明过的名字。
//
// 脱敏边界：
//   - Provider 只出 id/name/models/enabled/weight/priority/protocol ——
//     不出 api_key / headers / proxy_url（它们是唯一能当凭据用的字段）；
//   - 路由表只出别名与候选（RouteTarget 无凭据字段）；
//   - 用量/健康是统计量；skill 正文是用户自己写的技能库内容。
package a2a

import (
	"sort"
	"time"

	"github.com/erishen/tsm-hub/internal/proxy"
	"github.com/erishen/tsm-hub/internal/quota"
	"github.com/erishen/tsm-hub/internal/router"
	"github.com/erishen/tsm-hub/internal/skills"
	"github.com/erishen/tsm-hub/internal/store"
)

// Deps 是 tsm.* 能力的数据来源（只读视角）。
type Deps struct {
	Store  *store.Store
	Health *router.Tracker
	Rec    *quota.Recorder
	Proxy  *proxy.Proxy
	Skills *skills.Library
	Tasks  *TaskStore
	// Uptime 返回服务已运行时长（调用方提供，便于测试注入）。
	Uptime func() time.Duration
}

// RegisterTSMCapabilities 把 tsm.* 能力挂到注册表。
// 顺序即 Agent Card 里 skills[] 的顺序。
func RegisterTSMCapabilities(c *Capabilities, d Deps) {
	c.Register(Capability{
		Name:  "tsm.overview",
		Title: "服务概况",
		Desc:  "tsm-hub 自检：运行时长、provider/路由/key/工具/技能/MCP 数量、A2A 活跃任务数。",
		Tag:   "general", ArgsExample: `{}`,
		WhenToUse: "想知道『tsm-hub 现在活着吗、挂了几个 provider、有没有 A2A 任务在跑』时用作存活自检。",
	}, capOverview(d))

	c.Register(Capability{
		Name:  "tsm.capabilities",
		Title: "能力目录",
		Desc:  "网关工具池（内置 + 条件 + MCP）与技能库目录。一次回答『我有什么能用』。",
		Tag:   "general", ArgsExample: `{}`,
		WhenToUse: "想知道『网关有哪些工具/技能/MCP 可调用』时先查这个目录，再决定调哪个 tsm.* 能力。",
	}, capCapabilities(d))

	c.Register(Capability{
		Name:  "tsm.skill",
		Title: "技能全文",
		Desc:  "取单个技能的完整 SKILL.md 正文（跨运行时复用）。缺 name 时回 input-required。",
		Tag:   "general", ArgsExample: `{"name":"code-review"}`,
		WhenToUse: "需要把某技能（如 code-review / rust-review）的指令说明取出来喂给模型时用。",
	}, capSkill(d))

	c.Register(Capability{
		Name:  "tsm.usage",
		Title: "用量统计",
		Desc:  "请求数/token/成本（今日 + 累计）。可指定 key 名（缺省为全部 key 汇总）。",
		Tag:   "general", ArgsExample: `{"key":"prod"}`,
		WhenToUse: "想知道『某个 key 或整体今天烧了多少 token/多少钱』时用。",
	}, capUsage(d))

	c.Register(Capability{
		Name:  "tsm.model_health",
		Title: "provider 健康",
		Desc:  "各上游 provider 的实时健康快照（熔断/冷却/半开/延迟 EWMA/成败计数）。",
		Tag:   "general", ArgsExample: `{}`,
		WhenToUse: "想知道『某个 provider 是不是挂了/在冷却/延迟多少』排障时用。",
	}, capModelHealth(d))

	c.Register(Capability{
		Name:  "tsm.routes",
		Title: "路由表",
		Desc:  "对外模型别名 → 候选 provider 列表（含策略与权重），不含任何凭据。",
		Tag:   "general", ArgsExample: `{}`,
		WhenToUse: "想知道『我对外暴露的模型别名会路由到哪些 provider、权重多少』时用。",
	}, capRoutes(d))
}

func argStr(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

// ── tsm.overview ────────────────────────────────────────────────────

func capOverview(d Deps) DispatchFunc {
	return func(args map[string]any) DispatchResult {
		var uptimeSecs int64
		if d.Uptime != nil {
			uptimeSecs = int64(d.Uptime().Seconds())
		}
		body := map[string]any{
			"node":        "tsm-hub",
			"uptime_secs": uptimeSecs,
		}
		if d.Store != nil {
			body["providers"] = len(d.Store.ListProviders())
			body["routes"] = len(d.Store.ListRoutes())
			body["keys"] = len(d.Store.ListKeys())
		}
		if d.Proxy != nil {
			body["tools"] = len(d.Proxy.ToolCatalog())
			mcps := d.Proxy.MCPStatuses()
			connected := 0
			for _, m := range mcps {
				if m.Connected {
					connected++
				}
			}
			body["mcps"] = map[string]any{"connected": connected, "total": len(mcps)}
		}
		if d.Skills != nil {
			body["skills"] = len(d.Skills.List())
		}
		if d.Tasks != nil {
			body["a2a_tasks_active"] = d.Tasks.ActiveCount()
		}
		body["boundary"] = "只读概况；写入/执行仍只走管理 API（admin token）"
		return completedArtifact(body)
	}
}

// ── tsm.capabilities ────────────────────────────────────────────────

func capCapabilities(d Deps) DispatchFunc {
	return func(args map[string]any) DispatchResult {
		body := map[string]any{}
		if d.Proxy != nil {
			tools := d.Proxy.ToolCatalog()
			toolNames := make([]string, 0, len(tools))
			for _, t := range tools {
				toolNames = append(toolNames, t.Name)
			}
			sort.Strings(toolNames)
			// 只出目录（名字 + 描述 + 来源），不出参数 schema 细节：
			// 对端要真正调用请走 tsm-hub 的 /v1 网关，而不是经 a2a 拿 schema 后直连。
			body["tools"] = toolNames
			body["mcps"] = mcpView(d.Proxy)
		}
		if d.Skills != nil {
			sums := d.Skills.List()
			names := make([]string, 0, len(sums))
			for _, s := range sums {
				names = append(names, s.Name)
			}
			sort.Strings(names)
			body["skills"] = names
			body["skill_hint"] = "技能全文用 tsm.skill {\"name\":\"<技能名>\"} 取"
		}
		body["boundary"] = "目录级；不含 MCP 配置与工具参数细节"
		return completedArtifact(body)
	}
}

// mcpView 出 MCP server 的连接状态与工具名（不出 command/args/env 配置——
// 那几样是本地路径与进程环境，出给对端没有意义且泄漏部署细节）。
func mcpView(p *proxy.Proxy) map[string]any {
	st := p.MCPStatuses()
	out := map[string]any{}
	for name, m := range st {
		v := map[string]any{
			"connected": m.Connected,
			"tools":     m.Tools,
		}
		if m.Err != "" {
			v["err"] = m.Err
		}
		out[name] = v
	}
	return out
}

// ── tsm.skill ───────────────────────────────────────────────────────

func capSkill(d Deps) DispatchFunc {
	if d.Skills == nil {
		return func(args map[string]any) DispatchResult {
			return DispatchResult{State: StateFailed, Note: "技能库未启用（settings.skills_dir 为空）"}
		}
	}
	return func(args map[string]any) DispatchResult {
		name := argStr(args, "name")
		if name == "" {
			sums := d.Skills.List()
			names := make([]string, 0, len(sums))
			for _, s := range sums {
				names = append(names, s.Name)
			}
			sort.Strings(names)
			// input-required 必须带 status.message：客户端要**知道缺什么**才能补，
			// 只改状态不写提问，客户端拿到的是"需要你补充，但不说什么"。
			return DispatchResult{State: StateInputRequired, Note: "请指定技能名 name: " + joinComma(names)}
		}
		detail, ok := d.Skills.Get(name)
		if !ok {
			sums := d.Skills.List()
			names := make([]string, 0, len(sums))
			for _, s := range sums {
				names = append(names, s.Name)
			}
			sort.Strings(names)
			return DispatchResult{State: StateFailed, Note: "技能 '" + name + "' 不存在；可用: " + joinComma(names)}
		}
		return completedArtifact(map[string]any{
			"name":        name,
			"description": detail.Description,
			"body":        detail.Body,
			"raw":         detail.Raw,
		})
	}
}

func joinComma(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	if out == "" {
		out = "(空)"
	}
	return out
}

// ── tsm.usage ───────────────────────────────────────────────────────

func capUsage(d Deps) DispatchFunc {
	return func(args map[string]any) DispatchResult {
		if d.Rec == nil || d.Store == nil {
			return DispatchResult{State: StateFailed, Note: "用量记录不可用"}
		}
		keyName := argStr(args, "key")
		keys := d.Store.ListKeys()

		aggToday := quota.Agg{}
		aggTotal := quota.Agg{}
		var keyID string
		if keyName == "" {
			// 缺省 = 全部 key 汇总。
			for _, k := range keys {
				if !k.Enabled {
					continue
				}
				aggToday = aggToday.Add(d.Rec.Today(k.ID))
				aggTotal = aggTotal.Add(d.Rec.Total(k.ID))
			}
		} else {
			found := false
			for _, k := range keys {
				if k.Name == keyName {
					keyID = k.ID
					found = true
					aggToday = d.Rec.Today(k.ID)
					aggTotal = d.Rec.Total(k.ID)
					break
				}
			}
			if !found {
				return DispatchResult{State: StateFailed, Note: "key '" + keyName + "' 不存在"}
			}
		}
		return completedArtifact(map[string]any{
			"scope": usageScopeName(keyName),
			"key":   keyID,
			"today": usageView(aggToday),
			"total": usageView(aggTotal),
			"note":  "cost 为按 settings.pricing 的估算值；streaming 取决于上游回不回 usage",
		})
	}
}

func usageScopeName(keyName string) string {
	if keyName == "" {
		return "all-keys"
	}
	return keyName
}

func usageView(a quota.Agg) map[string]any {
	avgLatency := int64(0)
	if a.Requests > 0 {
		avgLatency = a.LatencySumMS / int64(a.Requests)
	}
	return map[string]any{
		"requests":       a.Requests,
		"tokens":         a.TotalTokens,
		"cost_usd":       a.CostUSD,
		"errors":         a.Errors,
		"failovers":      a.Failovers,
		"avg_latency_ms": avgLatency,
	}
}

// ── tsm.model_health ────────────────────────────────────────────────

func capModelHealth(d Deps) DispatchFunc {
	return func(args map[string]any) DispatchResult {
		if d.Health == nil {
			return DispatchResult{State: StateFailed, Note: "健康追踪不可用"}
		}
		snap := d.Health.Snapshot()
		byID := map[string]map[string]any{}
		for _, h := range snap {
			v := map[string]any{
				"healthy":    h.Healthy,
				"half_open":  h.HalfOpen,
				"failures":   h.Failures,
				"requests":   h.Requests,
				"errors":     h.Errors,
				"latency_ms": h.LatencyMS,
			}
			if h.LastError != "" {
				v["last_error"] = h.LastError
			}
			if !h.DownUntil.IsZero() {
				v["down_until"] = h.DownUntil.Format(time.RFC3339)
			}
			byID[h.ProviderID] = v
		}
		// 没出现在快照里的 provider（从未调用过）也列出来 —— 缺省健康，
		// 否则对端会以为"没列 = 挂了"。
		if d.Store != nil {
			for _, p := range d.Store.ListProviders() {
				if _, ok := byID[p.ID]; ok {
					continue
				}
				byID[p.ID] = map[string]any{"healthy": true, "half_open": false, "note": "no traffic yet"}
			}
		}
		return completedArtifact(map[string]any{
			"providers": byID,
			"boundary":  "统计量；不含凭据与 base_url",
		})
	}
}

// ── tsm.routes ──────────────────────────────────────────────────────

func capRoutes(d Deps) DispatchFunc {
	return func(args map[string]any) DispatchResult {
		if d.Store == nil {
			return DispatchResult{State: StateFailed, Note: "配置存储不可用"}
		}
		routes := d.Store.ListRoutes()
		out := make([]map[string]any, 0, len(routes))
		for _, r := range routes {
			targets := make([]map[string]any, 0, len(r.Targets))
			for _, t := range r.Targets {
				targets = append(targets, map[string]any{
					"provider": t.ProviderID,
					"model":    t.Model,
					"weight":   t.Weight,
					"priority": t.Priority,
				})
			}
			view := map[string]any{
				"model":    r.Model,
				"strategy": r.Strategy,
				"targets":  targets,
			}
			if r.Remark != "" {
				view["remark"] = r.Remark
			}
			out = append(out, view)
		}
		return completedArtifact(map[string]any{
			"routes": out,
			"note":   "别名 → 候选；候选排序与降级发生在请求时（含健康过滤），此处只出静态表",
		})
	}
}
