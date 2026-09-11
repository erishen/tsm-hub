package api

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/erishen/tsm-hub/internal/store"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)


func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	for _, p := range s.store.ListProviders() {
		h := s.health.Health(p.ID)
		out = append(out, map[string]any{
			"id": p.ID, "name": p.Name, "base_url": p.BaseURL,
			"api_key": displaySecret(p.APIKey), "models": p.Models, "headers": p.Headers,
			"enabled": p.Enabled, "weight": p.Weight, "priority": p.Priority,
			"timeout_ms": p.TimeoutMS,
			"healthy":    h.Healthy,
			"latency_ms": h.LatencyMS,
			"health":     h,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// displaySecret 决定管理台回显 api_key 的方式：
// env: 引用本身不含密钥，原样回显方便查看配置；其余一律脱敏。

func (s *Server) handleProviderBalances(w http.ResponseWriter, r *http.Request) {
	provs := s.store.ListProviders()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	type item struct {
		ID      string         `json:"id"`
		Name    string         `json:"name"`
		ProbeAt string         `json:"probe_at,omitempty"`
		Balance map[string]any `json:"balance,omitempty"`
		Error   string         `json:"error,omitempty"`
	}
	out := make([]item, 0, len(provs))
	for _, p := range provs {
		if p.ID == "mock-local" {
			continue
		}
		it := item{ID: p.ID, Name: p.Name}
		if !p.ProbeAt.IsZero() {
			it.ProbeAt = p.ProbeAt.Format(time.RFC3339)
		}
		out = append(out, it)
	}
	var wg sync.WaitGroup
	for i := range out {
		p, ok := s.store.GetProvider(out[i].ID)
		if !ok {
			continue
		}
		wg.Add(1)
		go func(i int, p store.Provider) {
			defer wg.Done()
			key := p.ResolvedAPIKey()
			if key == "" {
				out[i].Error = "no_key"
				return
			}
			base := strings.TrimRight(p.BaseURL, "/")
			// 重试：最多 3 次尝试（首次 + 2 次重试），间隔 500ms；
			// 上游偶尔超时/502 时自动重试，避免瞬时故障误报 unavailable。
			const maxAttempts = 3
			var bal map[string]any
			for attempt := 0; attempt < maxAttempts; attempt++ {
				if attempt > 0 {
					select {
					case <-ctx.Done():
						out[i].Error = "timeout"
						return
					case <-time.After(500 * time.Millisecond):
					}
				}
				bal = s.probeBalance(ctx, base, key)
				if bal != nil {
					break
				}
			}
			if bal == nil {
				out[i].Error = "unavailable"
				return
			}
			out[i].Balance = bal
		}(i, p)
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, map[string]any{"balances": out})
}

// parseOpenRouterKey: {"data":{"label":"…","limit":null,"limit_remaining":null,"usage":0,
// "is_free_tier":true,"expires_at":"…","rate_limit":{…}}}
// OpenRouter 无充值余额概念，额度信息来自 auth/key：已用 usage、上限 limit（null=无上限）、免费层标记。

func parseOpenRouterKey(body []byte) map[string]any {
	var raw struct {
		Data struct {
			Usage         float64  `json:"usage"`
			Limit         *float64 `json:"limit"`
			LimitRemaining *float64 `json:"limit_remaining"`
			IsFreeTier    bool     `json:"is_free_tier"`
			ExpiresAt     string   `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	d := raw.Data
	if d.Usage == 0 && d.Limit == nil && d.LimitRemaining == nil && !d.IsFreeTier && d.ExpiresAt == "" {
		return nil
	}
	m := map[string]any{
		"kind":         "openrouter",
		"usage":        d.Usage,
		"is_free_tier": d.IsFreeTier,
		"url":          "https://openrouter.ai",
	}
	if d.Limit != nil {
		m["limit"] = *d.Limit
	}
	if d.LimitRemaining != nil {
		m["limit_remaining"] = *d.LimitRemaining
	}
	if d.ExpiresAt != "" {
		m["expires_at"] = d.ExpiresAt
	}
	return m
}

// parseOpenRouterCredits: {"data":{"total_credits":0,"total_usage":0}}
// OpenRouter 新接口兜底（与 /auth/key 二选一，命中即返回）。

func parseOpenRouterCredits(body []byte) map[string]any {
	var raw struct {
		Data struct {
			TotalCredits float64 `json:"total_credits"`
			TotalUsage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.Data.TotalCredits == 0 && raw.Data.TotalUsage == 0 {
		return nil
	}
	return map[string]any{
		"kind":          "openrouter",
		"total_credits": raw.Data.TotalCredits,
		"total_usage":   raw.Data.TotalUsage,
		"url":           "https://openrouter.ai",
	}
}

// probeAliBailianLimits 查询阿里云百炼限流配额（GET /api/v1/models/limits），
// 返回前 3 个模型配额摘要；免费额度剩余量无公开 API，由 note 指引控制台。

func (s *Server) probeAliBailianLimits(ctx context.Context, baseURL, key string) map[string]any {
	host := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/compatible-mode/v1")
	ctx2, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx2, http.MethodGet, host+"/api/v1/models/limits?page_size=100", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var raw struct {
		Output struct {
			Quotas []struct {
				Model      string `json:"model"`
				ModelLimit struct {
					UsageLimit       *int64 `json:"usage_limit"`
					UsageLimitPeriod *int64 `json:"usage_limit_period"`
				} `json:"model_limit"`
			} `json:"quotas"`
		} `json:"output"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	parts := make([]string, 0, 3)
	for _, q := range raw.Output.Quotas {
		if q.ModelLimit.UsageLimit != nil && q.ModelLimit.UsageLimitPeriod != nil {
			parts = append(parts, fmt.Sprintf("%s %d tokens/%d", q.Model, *q.ModelLimit.UsageLimit, *q.ModelLimit.UsageLimitPeriod))
		}
		if len(parts) >= 3 {
			break
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return map[string]any{
		"kind":  "platform_note",
		"plan":  "阿里云百炼",
		"quota": "限流配额：" + strings.Join(parts, " · "),
		"note":  "免费额度剩余量无公开 API，请在百炼控制台「免费额度」页查看；此为限流配额（每周期可调用 tokens 上限）",
		"url":   "https://bailian.console.aliyun.com",
	}
}

// isProbeRetryable 判断探测失败是否属于可重试的瞬时连接错误
//（unexpected EOF、连接重置、超时等），避免对 4xx/业务错误做无意义重试。

func (s *Server) probeBalance(ctx context.Context, baseURL, key string) map[string]any {
	if key == "" {
		return nil
	}
	// 无公开余额接口的平台（如 SenseNova Token Plan、TokenRouter）：
	// 实测常见余额端点全 404，额度只在各自控制台/Dashboard 查看。
	// 这里返回官方公开的配额/平台信息（非实时余额），让额度页不至于空白。
	type platformNote struct{ host, plan, quota, reset, note, url string }
	platforms := []platformNote{
		{"token.sensenova.cn", "免费公测",
			"每5小时 1500 次调用（自研）/ 500 次（DeepSeek V4 Flash）",
			"每5小时独立刷新，无一次性总量限制",
			"无公开余额接口，剩余次数请在商汤控制台查看",
			"https://platform.sensenova.cn"},
		{"tokenrouter.com", "免费/低价模型聚合", "", "",
			"无公开余额接口，余额请在 TokenRouter Dashboard 查看",
			"https://www.tokenrouter.com"},
		{"apihub.agnes-ai.com", "免费+付费（文本/图片/视频统一网关）",
			"文本模型免费调用 · 图片模型每月500次 · 视频模型首月100秒",
			"付费计划每5小时限流：Starter 1500次 / Plus 7500次 / Pro 30000次",
			"无公开余额API（/v1/account/balance 等均404）；免费额度政策如上，付费计划剩余次数请在 Agnes 控制台查看",
			"https://agnes-ai.com"},
		{"api.b.ai", "Credits 计费（登录送10万免费 credits）", "", "",
			"API 网关仅开放推理路径（/v1/chat/completions、/v1/models 等），余额/计费路径被 403 拒绝；credits 余额请在 B.AI 控制台 Top up 页面查看",
			"https://b.ai"},
	}
	for _, pn := range platforms {
		if strings.Contains(baseURL, pn.host) {
			return map[string]any{
				"kind":   "platform_note",
				"plan":   pn.plan,
				"quota":  pn.quota,
				"reset":  pn.reset,
				"note":   pn.note,
				"url":    pn.url,
			}
		}
	}
	// 阿里云百炼（专属 endpoint …maas.aliyuncs.com）：免费额度剩余量无公开 API（仅控制台「免费额度」页可见），
	// 唯一可自动获取的是 /api/v1/models/limits 限流配额（每周期可调用 tokens 上限）。
	if strings.Contains(baseURL, "maas.aliyuncs.com") {
		if m := s.probeAliBailianLimits(ctx, baseURL, key); m != nil {
			return m
		}
	}
	type probe struct {
		path  string
		parse func([]byte) map[string]any
	}
	probes := []probe{
		{"/users/me/balance", parseMoonshotBalance},
		{"/user/balance", parseDeepSeekBalance},
		{"/dashboard/billing/subscription", parseOpenAISubscription},
		{"/dashboard/billing/usage", parseOpenAIUsage},
		{"/auth/key", parseOpenRouterKey},
		{"/credits", parseOpenRouterCredits},
	}
	for _, p := range probes {
		ctx2, cancel := context.WithTimeout(ctx, 3*time.Second)
		upReq, err := http.NewRequestWithContext(ctx2, http.MethodGet, baseURL+p.path, nil)
		if err != nil {
			cancel()
			continue
		}
		upReq.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(upReq)
		cancel()
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if m := p.parse(body); m != nil {
			return m
		}
	}
	return nil
}

// parseMoonshotBalance: {"data":{"available_balance":14.99,"voucher_balance":14.99,"cash_balance":0}}

func parseMoonshotBalance(body []byte) map[string]any {
	var raw struct {
		Data struct {
			Available float64 `json:"available_balance"`
			Voucher   float64 `json:"voucher_balance"`
			Cash      float64 `json:"cash_balance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.Data.Available == 0 && raw.Data.Voucher == 0 && raw.Data.Cash == 0 {
		return nil
	}
	return map[string]any{
		"kind":     "moonshot",
		"available": raw.Data.Available,
		"voucher":   raw.Data.Voucher,
		"cash":      raw.Data.Cash,
		"url":       "https://platform.moonshot.cn",
	}
}

// flexFloat 兼容 JSON 字符串与数字两种表示（DeepSeek 余额字段是 "51.75" 字符串）。
type flexFloat float64


func parseDeepSeekBalance(body []byte) map[string]any {
	var raw struct {
		BalanceInfos []struct {
			Currency       string    `json:"currency"`
			TotalBalance   flexFloat `json:"total_balance"`
			GrantedBalance flexFloat `json:"granted_balance"`
			ToppedUp       flexFloat `json:"topped_up_balance"`
		} `json:"balance_infos"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || len(raw.BalanceInfos) == 0 {
		return nil
	}
	b := raw.BalanceInfos[0]
	if b.TotalBalance == 0 && b.GrantedBalance == 0 && b.ToppedUp == 0 {
		return nil
	}
	return map[string]any{
		"kind":      "deepseek",
		"currency":  b.Currency,
		"total":     float64(b.TotalBalance),
		"granted":   float64(b.GrantedBalance),
		"topped_up": float64(b.ToppedUp),
		"url":       "https://platform.deepseek.com",
	}
}

// parseOpenAISubscription: {"hard_limit_usd":120,"system_hard_limit_usd":120}
// 部分聚合平台（如 agnes）返回 1e8 量级占位值，视为未提供真实额度。

func parseOpenAISubscription(body []byte) map[string]any {
	var raw struct {
		HardLimitUSD float64 `json:"hard_limit_usd"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.HardLimitUSD <= 0 || raw.HardLimitUSD >= 1e7 {
		return nil
	}
	return map[string]any{"kind": "openai", "hard_limit_usd": raw.HardLimitUSD, "url": "https://platform.openai.com"}
}

// parseOpenAIUsage: {"total_usage":123.45}（单位 0.01 USD）

func parseOpenAIUsage(body []byte) map[string]any {
	var raw struct {
		TotalUsage float64 `json:"total_usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.TotalUsage == 0 {
		return nil
	}
	return map[string]any{"kind": "openai", "total_usage_usd": raw.TotalUsage / 100, "url": "https://platform.openai.com"}
}



func (s *Server) handleUpsertProvider(w http.ResponseWriter, r *http.Request) {
	var p store.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if p.ID == "" {
		p.ID = slugify(p.Name)
	}
	if p.ID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "id or name is required")
		return
	}
	if p.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "base_url is required")
		return
	}
	if err := s.store.UpsertProvider(p); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	s.recordAudit(r, "upsert", "provider", p.ID, map[string]any{"name": p.Name, "base_url": p.BaseURL, "models": p.Models})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": p.ID})
}



func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteProvider(id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.recordAudit(r, "delete", "provider", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- routes ----------

