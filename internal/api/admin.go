package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/erishen/tsm-hub/internal/auth"
	"github.com/erishen/tsm-hub/internal/proxy/adapter"
	"github.com/erishen/tsm-hub/internal/quota"
	"github.com/erishen/tsm-hub/internal/router"
	"github.com/erishen/tsm-hub/internal/store"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)


func (s *Server) adminMux() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/admin/overview", s.admin(s.handleOverview))
	m.HandleFunc("GET /api/admin/providers", s.admin(s.handleListProviders))
	m.HandleFunc("GET /api/admin/providers/balances", s.admin(s.handleProviderBalances))
	m.HandleFunc("GET /api/admin/models/catalog", s.admin(s.handleModelsCatalog))
	m.HandleFunc("POST /api/admin/models/refresh", s.admin(s.handleRefreshModels))
	m.HandleFunc("GET /api/admin/models/recommendations", s.admin(s.handleModelRecommendations))
	m.HandleFunc("POST /api/admin/providers", s.admin(s.handleUpsertProvider))
	m.HandleFunc("POST /api/admin/providers/probe", s.admin(s.handleProbeModels))
	m.HandleFunc("DELETE /api/admin/providers/{id}", s.admin(s.handleDeleteProvider))
	m.HandleFunc("GET /api/admin/routes", s.admin(s.handleListRoutes))
	m.HandleFunc("POST /api/admin/routes", s.admin(s.handleUpsertRoute))
	m.HandleFunc("DELETE /api/admin/routes/{model}", s.admin(s.handleDeleteRoute))
	m.HandleFunc("GET /api/admin/keys", s.admin(s.handleListKeys))
	m.HandleFunc("POST /api/admin/keys", s.admin(s.handleCreateKey))
	m.HandleFunc("POST /api/admin/keys/{id}/toggle", s.admin(s.handleToggleKey))
	m.HandleFunc("GET /api/admin/keys/{id}/plaintext", s.admin(s.handleRevealKey))
	m.HandleFunc("PATCH /api/admin/keys/{id}", s.admin(s.handleUpdateKey))
	m.HandleFunc("DELETE /api/admin/keys/{id}", s.admin(s.handleDeleteKey))
	m.HandleFunc("GET /api/admin/usage", s.admin(s.handleUsage))
	m.HandleFunc("POST /api/admin/usage/clear", s.admin(s.handleClearUsage))
	m.HandleFunc("GET /api/admin/observability/overview", s.admin(s.handleObservability))
	m.HandleFunc("GET /api/admin/health", s.admin(s.handleAdminHealth))
	m.HandleFunc("GET /api/admin/settings", s.admin(s.handleGetSettings))
	m.HandleFunc("POST /api/admin/settings", s.admin(s.handleUpdateSettings))
	m.HandleFunc("GET /api/admin/skills", s.admin(s.handleListSkills))
	m.HandleFunc("GET /api/admin/skills/{name}", s.admin(s.handleGetSkill))
	m.HandleFunc("GET /api/admin/mcps", s.admin(s.handleListMcps))
	m.HandleFunc("POST /api/admin/mcps/{name}", s.admin(s.handleUpsertMcp))
	m.HandleFunc("DELETE /api/admin/mcps/{name}", s.admin(s.handleDeleteMcp))
	m.HandleFunc("GET /api/admin/tools", s.admin(s.handleListTools))
	m.HandleFunc("POST /api/admin/tools/invoke", s.admin(s.handleInvokeTool))
	m.HandleFunc("GET /api/admin/fastpath", s.admin(s.handleListFastpath))
	m.HandleFunc("POST /api/admin/fastpath/{name}/promote", s.admin(s.handlePromoteFastpath))
	m.HandleFunc("DELETE /api/admin/fastpath/{name}", s.admin(s.handleDeleteFastpath))
	m.HandleFunc("POST /api/admin/fastpath/generate", s.admin(s.handleFastpathGenerate))
	m.HandleFunc("GET /api/admin/external-tools", s.admin(s.handleListExternalTools))
	m.HandleFunc("POST /api/admin/external-tools/{name}/adopt", s.admin(s.handleAdoptExternalTool))
	m.HandleFunc("DELETE /api/admin/external-tools/{name}", s.admin(s.handleDeleteExternalTool))
	m.HandleFunc("GET /api/admin/external-skills/candidates", s.admin(s.handleListExternalSkillCandidates))
	m.HandleFunc("GET /api/admin/external-mcps/candidates", s.admin(s.handleListExternalMcpCandidates))
	m.HandleFunc("POST /api/admin/external-mcps/suggest", s.admin(s.handleSuggestExternalMcp))
	m.HandleFunc("POST /api/admin/external-mcps/{server}/adopt", s.admin(s.handleAdoptExternalMcp))
	m.HandleFunc("DELETE /api/admin/external-mcps/{server}", s.admin(s.handleIgnoreExternalMcp))
	m.HandleFunc("GET /api/admin/sandbox/status", s.admin(s.handleSandboxStatus))
	m.HandleFunc("GET /api/admin/memory", s.admin(s.handleListMemory))
	m.HandleFunc("DELETE /api/admin/memory", s.admin(s.handleClearMemory))
	m.HandleFunc("GET /api/admin/audit-logs", s.admin(s.handleAuditLogs))
	m.HandleFunc("GET /api/admin/upstream-platforms", s.admin(s.handleUpstreamPlatforms))
	return m
}

// recordAudit 记录一条审计日志，自动从请求中获取 operator/clientIP/userAgent。

func (s *Server) recordAudit(r *http.Request, action, object, objectID string, detail any) {
	if s.audit == nil {
		return
	}
	operator := "admin"
	if tok := r.Header.Get("X-Admin-Token"); tok != "" {
		operator = "token:" + maskToken(tok)
	} else if sess := r.Header.Get("X-Session-Token"); sess != "" {
		operator = "session:" + sess[:8]
	}
	clientIP := r.RemoteAddr
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		clientIP = xff
	}
	userAgent := r.Header.Get("User-Agent")
	s.audit.Record(action, object, objectID, detail, operator, clientIP, userAgent)
}

// maskToken 脱敏 admin token（前4位+***）。

func maskToken(t string) string {
	if len(t) <= 8 {
		return "***"
	}
	return t[:4] + "***"
}

// handleAuditLogs 查询审计日志，支持 object/action/object_id 过滤和分页。

func (s *Server) handleAuditLogs(w http.ResponseWriter, r *http.Request) {
	if s.audit == nil {
		writeJSON(w, http.StatusOK, map[string]any{"logs": []any{}, "total": 0, "enabled": false})
		return
	}
	object := r.URL.Query().Get("object")
	action := r.URL.Query().Get("action")
	objectID := r.URL.Query().Get("object_id")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	logs, total, err := s.audit.Query(object, action, objectID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "query audit logs failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs, "total": total, "enabled": true, "limit": limit, "offset": offset})
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

// keyRevealWindow 是新建 key 明文可重看的短窗口：创建后立即展示一次，
// 若页面被误关，窗口内可从管理台补看；超窗后明文彻底消失（服务端只存哈希）。
const keyRevealWindow = 2 * time.Minute

type keyReveal struct {
	plain string
	until time.Time
}

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


func (s *Server) handleAdminHealth(w http.ResponseWriter, r *http.Request) {
	// 遍历全部 provider：无请求记录的也展示（默认健康），不依赖 Tracker 快照。
	providers := s.store.ListProviders()
	out := make([]router.ProviderHealth, 0, len(providers))
	for _, p := range providers {
		out = append(out, s.health.Health(p.ID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": out})
}

// ---------- settings ----------

// ---------- skills ----------



func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st := s.store.Settings()
	writeJSON(w, http.StatusOK, map[string]any{		"listen":             st.Listen,
		"default_timeout_ms": st.DefaultTimeoutMS,
		"max_body_bytes":     st.MaxBodyBytes,
		"fail_threshold":     st.FailThreshold,
		"cooldown_sec":       st.CooldownSec,
		"pricing":            st.Pricing,
		"smart":              st.Smart,
		"data_file":          s.store.Path(),
		"admin_token_set":    st.AdminToken != "",
	})
}



func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		DefaultTimeoutMS   int                    `json:"default_timeout_ms"`
		MaxBodyBytes       int64                  `json:"max_body_bytes"`
		FailThreshold      int                    `json:"fail_threshold"`
		CooldownSec        int                    `json:"cooldown_sec"`
		Pricing            map[string]store.Price `json:"pricing"`
		Smart              *store.SmartScoreCfg   `json:"smart"`
		AuditRetentionDays int                    `json:"audit_retention_days"`
		UsageRetentionDays int                    `json:"usage_retention_days"`
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
		if patch.Smart != nil {
			c.Settings.Smart = *patch.Smart
		}
		if patch.AuditRetentionDays > 0 {
			c.Settings.AuditRetentionDays = patch.AuditRetentionDays
		}
		if patch.UsageRetentionDays > 0 {
			c.Settings.UsageRetentionDays = patch.UsageRetentionDays
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 更新审计日志保留天数（运行时生效）
	if s.audit != nil && patch.AuditRetentionDays > 0 {
		s.audit.SetRetentionDays(patch.AuditRetentionDays)
	}
	s.recordAudit(r, "update", "settings", "", map[string]any{
		"default_timeout_ms":  patch.DefaultTimeoutMS,
		"max_body_bytes":      patch.MaxBodyBytes,
		"audit_retention_days": patch.AuditRetentionDays,
	})
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

// handleUpstreamPlatforms 返回所有支持的上游平台信息，用于管理台展示和快速接入。
func (s *Server) handleUpstreamPlatforms(w http.ResponseWriter, r *http.Request) {
	platforms := adapter.ListUpstreamPlatforms()
	writeJSON(w, http.StatusOK, map[string]any{
		"platforms": platforms,
		"total":     len(platforms),
	})
}

