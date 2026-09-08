package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
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
	m.HandleFunc("POST /api/admin/providers", s.admin(s.handleUpsertProvider))
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
