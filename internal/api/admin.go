package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/erishen/llm-router/internal/auth"
	"github.com/erishen/llm-router/internal/quota"
	"github.com/erishen/llm-router/internal/store"
)

// adminMux 注册管理 API（Go 1.22 起 net/http 支持方法与路径参数）。
func (s *Server) adminMux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/admin/overview", s.admin(s.handleOverview))
	m.HandleFunc("GET /api/admin/providers", s.admin(s.handleListProviders))
	m.HandleFunc("GET /api/admin/providers/balances", s.admin(s.handleProviderBalances))
	m.HandleFunc("POST /api/admin/providers", s.admin(s.handleUpsertProvider))
	m.HandleFunc("POST /api/admin/providers/probe", s.admin(s.handleProbeModels))
	m.HandleFunc("DELETE /api/admin/providers/{id}", s.admin(s.handleDeleteProvider))
	m.HandleFunc("GET /api/admin/routes", s.admin(s.handleListRoutes))
	m.HandleFunc("POST /api/admin/routes", s.admin(s.handleUpsertRoute))
	m.HandleFunc("DELETE /api/admin/routes/{model}", s.admin(s.handleDeleteRoute))
	m.HandleFunc("GET /api/admin/keys", s.admin(s.handleListKeys))
	m.HandleFunc("POST /api/admin/keys", s.admin(s.handleCreateKey))
	m.HandleFunc("POST /api/admin/keys/{id}/toggle", s.admin(s.handleToggleKey))
	m.HandleFunc("DELETE /api/admin/keys/{id}", s.admin(s.handleDeleteKey))
	m.HandleFunc("GET /api/admin/usage", s.admin(s.handleUsage))
	m.HandleFunc("GET /api/admin/health", s.admin(s.handleAdminHealth))
	m.HandleFunc("GET /api/admin/settings", s.admin(s.handleGetSettings))
	m.HandleFunc("POST /api/admin/settings", s.admin(s.handleUpdateSettings))
	return m
}

// admin 校验管理口令（X-Admin-Token 或登录会话 X-Session-Token）。
func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		expected := s.store.Settings().AdminToken
		if tok := r.Header.Get("X-Admin-Token"); tok != "" && auth.ConstantTimeEqual(tok, expected) {
			next(w, r)
			return
		}
		if sess := r.Header.Get("X-Session-Token"); sess != "" && s.sess.Valid(sess) {
			next(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", "admin token required")
	}
}

// loginFail 记录某个来源 IP 的失败登录状态，用于防暴力尝试。
type loginFail struct {
	count int
	until time.Time // 超过阈值后在此时间前拒绝该 IP
}

const (
	loginMaxFails = 5
	loginLockDur  = 10 * time.Minute
)

// handleAdminLogin 用管理口令换取会话 token。
func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	ip := clientIP(r)
	if !s.loginAllowed(ip) {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts",
			"too many failed login attempts, try again later")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Token == "" {
		req.Token = r.Header.Get("X-Admin-Token")
	}
	if !auth.ConstantTimeEqual(req.Token, s.store.Settings().AdminToken) {
		s.loginFailed(ip)
		writeError(w, http.StatusUnauthorized, "unauthorized", "wrong admin token")
		return
	}
	s.loginOK(ip)
	writeJSON(w, http.StatusOK, map[string]any{
		"session_token": s.sess.Issue(req.Token),
		"expires_in":    int64((12 * time.Hour).Seconds()),
	})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) loginAllowed(ip string) bool {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	f, ok := s.loginFails[ip]
	if !ok {
		return true
	}
	if time.Now().Before(f.until) {
		return false
	}
	if f.count >= loginMaxFails {
		// 锁定期已过：复位，允许重试。
		delete(s.loginFails, ip)
	}
	return true
}

func (s *Server) loginFailed(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	f := s.loginFails[ip]
	f.count++
	if f.count >= loginMaxFails {
		f.until = time.Now().Add(loginLockDur)
	}
	s.loginFails[ip] = f
}

func (s *Server) loginOK(ip string) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	delete(s.loginFails, ip)
}

// newID 生成短随机 ID。
func newID() string {
	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// ---------- overview ----------

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	providers := s.store.ListProviders()
	healthy := 0
	for _, p := range providers {
		if p.Enabled && s.health.Available(p.ID) {
			healthy++
		}
	}
	var today quota.Agg
	for _, a := range s.rec.Daily(1) {
		today = a.Agg
	}
	var total quota.Agg
	for _, a := range s.rec.PerKey() {
		total = total.Add(a)
	}
	s.mu.RLock()
	reqs := s.requests
	s.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"version":        "1",
		"providers":      len(providers),
		"healthy":        healthy,
		"routes":         len(s.store.ListRoutes()),
		"keys":           len(s.store.ListKeys()),
		"requests_total": reqs,
		"today":          today,
		"total":          total,
	})
}

// ---------- providers ----------

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	out := make([]map[string]any, 0)
	for _, p := range s.store.ListProviders() {
		out = append(out, map[string]any{
			"id": p.ID, "name": p.Name, "base_url": p.BaseURL,
			"api_key": displaySecret(p.APIKey), "models": p.Models, "headers": p.Headers,
			"enabled": p.Enabled, "weight": p.Weight, "priority": p.Priority,
			"timeout_ms": p.TimeoutMS,
			"healthy":    s.health.Available(p.ID),
			"latency_ms": s.health.Latency(p.ID),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// displaySecret 决定管理台回显 api_key 的方式：
// env: 引用本身不含密钥，原样回显方便查看配置；其余一律脱敏。
func displaySecret(k string) string {
	if strings.HasPrefix(k, store.EnvKeyPrefix) {
		return k
	}
	return maskSecret(k)
}

// compact 压缩一段文本用于错误消息：去换行、截断。
func compact(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// handleProviderBalances 并行查询所有 Provider 已存 Key 的账户余额/额度，
// 供独立额度页面使用；无 Key 或查询失败不阻塞其余项。
// mock-local（一键 Mock 联调）无真实额度概念，直接排除。
func (s *Server) handleProviderBalances(w http.ResponseWriter, r *http.Request) {
	provs := s.store.ListProviders()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	type item struct {
		ID      string         `json:"id"`
		Name    string         `json:"name"`
		Balance map[string]any `json:"balance,omitempty"`
		Error   string         `json:"error,omitempty"`
	}
	out := make([]item, 0, len(provs))
	for _, p := range provs {
		if p.ID == "mock-local" {
			continue
		}
		out = append(out, item{ID: p.ID, Name: p.Name})
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
			bal := s.probeBalance(ctx, strings.TrimRight(p.BaseURL, "/"), key)
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

// isProbeRetryable 判断探测失败是否属于可重试的瞬时连接错误
//（unexpected EOF、连接重置、超时等），避免对 4xx/业务错误做无意义重试。
func isProbeRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, kw := range []string{"eof", "connection reset", "broken pipe", "connection refused"} {
		if strings.Contains(msg, kw) {
			return true
		}
	}
	return false
}

// handleProbeModels 用给定的 base_url + API Key 探测上游 /v1/models，返回模型 id 列表（去重排序）。
// 用于管理台「按 Key 查询模型」：Key 支持 env: 引用，脱敏回显值（含省略号）不参与探测。
func (s *Server) handleProbeModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	req.BaseURL = strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if req.BaseURL == "" || (!strings.HasPrefix(req.BaseURL, "http://") && !strings.HasPrefix(req.BaseURL, "https://")) {
		writeError(w, http.StatusBadRequest, "bad_request", "base_url 必须是合法的 http(s) 地址")
		return
	}
	if strings.Contains(req.APIKey, store.MaskedSecretMarker) {
		writeError(w, http.StatusBadRequest, "bad_request", "API Key 是脱敏回显值，无法用于探测；请重新输入真实 Key（或填 env: 引用）")
		return
	}
	key := store.Provider{APIKey: req.APIKey}.ResolvedAPIKey()

	// 探测是交互操作，固定 12s 单次超时（不随 default_timeout_ms 漂移）；
	// 连接类瞬断（unexpected EOF / reset / 超时）自动重试 1 次。
	probeOnce := func() (*http.Response, error) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		upReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.BaseURL+"/models", nil)
		if err != nil {
			return nil, err
		}
		if key != "" {
			upReq.Header.Set("Authorization", "Bearer "+key)
		}
		return http.DefaultClient.Do(upReq)
	}
	resp, err := probeOnce()
	if err != nil && isProbeRetryable(err) {
		resp, err = probeOnce()
	}
	if err != nil {
		note := ""
		if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "reset") {
			note = "（上游连接被中断，已自动重试 1 次仍失败；请检查网络或本地代理 127.0.0.1:7897 是否稳定）"
		}
		writeError(w, http.StatusBadGateway, "upstream_unavailable", "无法连接上游: "+err.Error()+note)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized {
			if key == "" {
				writeError(w, http.StatusBadRequest, "probe_failed",
					"上游要求鉴权但本次探测未携带 API Key（编辑已有 Provider 时 Key 为脱敏值，需重新输入完整 Key 或 env: 引用）；上游返回: "+compact(string(body)))
			} else {
				writeError(w, http.StatusBadRequest, "probe_failed",
					"上游拒绝了该 API Key（401），请检查 Key 是否完整有效；上游返回: "+compact(string(body)))
			}
			return
		}
		writeError(w, http.StatusBadRequest, "probe_failed",
			fmt.Sprintf("上游返回 %d: %s", resp.StatusCode, compact(string(body))))
		return
	}
	var payload struct {
		Data []struct {
			ID             string `json:"id"`
			ContextLength  int64  `json:"context_length"`
			IsFree         *bool  `json:"is_free"`
			Free           *bool  `json:"free"`
			Pricing        *struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
			} `json:"pricing"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadGateway, "upstream_read_error", "上游响应不是有效的模型列表: "+err.Error())
		return
	}
	seen := map[string]bool{}
	out := make([]map[string]any, 0, len(payload.Data))
	for _, m := range payload.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		info := map[string]any{"id": id}
		if m.ContextLength > 0 {
			info["context_length"] = m.ContextLength
		}
		// 免费判定：显式 is_free/free 字段，或 pricing 全 0。
		free := false
		switch {
		case m.IsFree != nil:
			free = *m.IsFree
		case m.Free != nil:
			free = *m.Free
		case m.Pricing != nil:
			free = m.Pricing.Prompt == "0" && m.Pricing.Completion == "0"
		}
		if free {
			info["free"] = true
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["id"].(string) < out[j]["id"].(string) })
	result := map[string]any{"models": out}
	if bal := s.probeBalance(r.Context(), req.BaseURL, key); bal != nil {
		result["balance"] = bal
	}
	writeJSON(w, http.StatusOK, result)
}

// probeBalance 尝试用同一 Key 查询上游账户余额/额度（token 可使用总量），
// 依次探测 Moonshot / DeepSeek / OpenAI 三种格式，命中即返回；失败静默。
func (s *Server) probeBalance(ctx context.Context, baseURL, key string) map[string]any {
	if key == "" {
		return nil
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
	}
}

// flexFloat 兼容 JSON 字符串与数字两种表示（DeepSeek 余额字段是 "51.75" 字符串）。
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return err
		}
		*f = flexFloat(v)
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexFloat(n)
	return nil
}

// parseDeepSeekBalance: {"balance_infos":[{"currency":"CNY","total_balance":"51.75","granted_balance":"0","topped_up_balance":"51.75"}]}
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
	return map[string]any{"kind": "openai", "hard_limit_usd": raw.HardLimitUSD}
}

// parseOpenAIUsage: {"total_usage":123.45}（单位 0.01 USD）
func parseOpenAIUsage(body []byte) map[string]any {
	var raw struct {
		TotalUsage float64 `json:"total_usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil || raw.TotalUsage == 0 {
		return nil
	}
	return map[string]any{"kind": "openai", "total_usage_usd": raw.TotalUsage / 100}
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": p.ID})
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteProvider(id); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- routes ----------

func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"routes": s.store.ListRoutes()})
}

func (s *Server) handleUpsertRoute(w http.ResponseWriter, r *http.Request) {
	var rt store.Route
	if err := json.NewDecoder(r.Body).Decode(&rt); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if err := s.store.UpsertRoute(rt); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": rt.Model})
}

func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	model := r.PathValue("model")
	if err := s.store.DeleteRoute(model); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- keys ----------

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	rpm := s.limiter.Snapshot()
	per := s.rec.PerKey()
	out := make([]map[string]any, 0)
	for _, k := range s.store.ListKeys() {
		agg := per[k.ID]
		out = append(out, map[string]any{
			"id": k.ID, "name": k.Name, "prefix": k.Prefix, "enabled": k.Enabled,
			"models": k.Models, "quota": k.Quota,
			"created_at":  k.CreatedAt,
			"expires_at":  k.ExpiresAt,
			"usage":       agg,
			"rpm_current": rpm[k.ID],
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string      `json:"name"`
		Models    []string    `json:"models"`
		Quota     store.Quota `json:"quota"`
		ExpiresIn int64       `json:"expires_in_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	plaintext, hash, display, err := auth.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	k := store.APIKey{
		ID:        newID(),
		Name:      req.Name,
		Prefix:    display,
		Hash:      hash,
		Enabled:   true,
		Models:    req.Models,
		Quota:     req.Quota,
		CreatedAt: time.Now(),
	}
	if req.ExpiresIn > 0 {
		k.ExpiresAt = time.Now().Add(time.Duration(req.ExpiresIn) * time.Second)
	}
	if k.Name == "" {
		k.Name = "key-" + k.ID
	}
	if err := s.store.AddKey(k); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// ⚠️ 明文只在这一次响应里出现，之后只能靠哈希校验。
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": k.ID, "key": plaintext, "prefix": display,
		"warning": "请立即保存该 key，服务端只保存哈希，遗失无法找回",
	})
}

func (s *Server) handleToggleKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := s.store.SetKeyEnabled(r.PathValue("id"), req.Enabled); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": req.Enabled})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteKey(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- usage ----------

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v := r.URL.Query().Get("days"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &days); err != nil || days <= 0 {
			days = 7
		}
		if days > 90 {
			days = 90
		}
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &limit); err != nil || limit <= 0 {
			limit = 50
		}
		if limit > 500 {
			limit = 500
		}
	}
	byModel := s.rec.ByModel()
	models := make([]map[string]any, 0, len(byModel))
	for m, a := range byModel {
		models = append(models, map[string]any{"model": m, "usage": a})
	}
	perKey := s.rec.PerKey()
	keys := make([]map[string]any, 0, len(perKey))
	for k, a := range perKey {
		keys = append(keys, map[string]any{"key_id": k, "usage": a})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days":   s.rec.Daily(days),
		"models": models,
		"keys":   keys,
		"recent": s.rec.Recent(limit),
	})
}

// ---------- health ----------

func (s *Server) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": s.health.Snapshot()})
}

// ---------- settings ----------

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st := s.store.Settings()
	writeJSON(w, http.StatusOK, map[string]any{
		"listen":             st.Listen,
		"default_timeout_ms": st.DefaultTimeoutMS,
		"max_body_bytes":     st.MaxBodyBytes,
		"fail_threshold":     st.FailThreshold,
		"cooldown_sec":       st.CooldownSec,
		"pricing":            st.Pricing,
		"data_file":          s.store.Path(),
		"admin_token_set":    st.AdminToken != "",
	})
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		DefaultTimeoutMS int                    `json:"default_timeout_ms"`
		MaxBodyBytes     int64                  `json:"max_body_bytes"`
		FailThreshold    int                    `json:"fail_threshold"`
		CooldownSec      int                    `json:"cooldown_sec"`
		Pricing          map[string]store.Price `json:"pricing"`
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	err := s.store.Update(func(c *store.Config) error {
		if patch.DefaultTimeoutMS > 0 {
			c.Settings.DefaultTimeoutMS = patch.DefaultTimeoutMS
		}
		if patch.MaxBodyBytes > 0 {
			c.Settings.MaxBodyBytes = patch.MaxBodyBytes
		}
		if patch.FailThreshold > 0 {
			c.Settings.FailThreshold = patch.FailThreshold
		}
		if patch.CooldownSec > 0 {
			c.Settings.CooldownSec = patch.CooldownSec
		}
		if len(patch.Pricing) > 0 {
			if c.Settings.Pricing == nil {
				c.Settings.Pricing = map[string]store.Price{}
			}
			for k, v := range patch.Pricing {
				c.Settings.Pricing[k] = v
			}
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 小工具 ----------

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + store.MaskedSecretMarker + s[len(s)-4:]
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return out
}
