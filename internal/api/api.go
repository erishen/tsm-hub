// Package api 暴露 HTTP 接口：OpenAI 兼容代理（/v1/*）、管理 REST（/api/admin/*）与前端。
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/erishen/llm-router/internal/auth"
	"github.com/erishen/llm-router/internal/proxy"
	"github.com/erishen/llm-router/internal/quota"
	"github.com/erishen/llm-router/internal/router"
	"github.com/erishen/llm-router/internal/store"
	"github.com/erishen/llm-router/internal/web"
)

// Server 聚合所有依赖并注册路由。
type Server struct {
	store   *store.Store
	rec     *quota.Recorder
	limiter *quota.Limiter
	router  *router.Router
	health  *router.Tracker
	proxy   *proxy.Proxy
	sess    *auth.Session
	logger  *slog.Logger

	mu           sync.RWMutex
	startAt      time.Time
	requests     int64
	statusCounts map[string]int64 // "200" / "401" ... -> 计数（/metrics）
	denyCounts   map[string]int64 // 配额拒绝原因 -> 计数（/metrics）
	adminHandler http.Handler

	loginMu    sync.Mutex
	loginFails map[string]loginFail // ip -> 失败登录状态（暴力尝试防护）
}

// Options 构造 Server 的参数。
type Options struct {
	Store   *store.Store
	Rec     *quota.Recorder
	Limiter *quota.Limiter
	Router  *router.Router
	Health  *router.Tracker
	Proxy   *proxy.Proxy
	Logger  *slog.Logger
}

// New 创建 Server。
func New(o Options) *Server {
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	s := &Server{
		store:   o.Store,
		rec:     o.Rec,
		limiter: o.Limiter,
		router:  o.Router,
		health:  o.Health,
		proxy:   o.Proxy,
		sess:    auth.NewSession(12 * time.Hour),
		logger:  o.Logger,
		startAt:      time.Now(),
		statusCounts: map[string]int64{},
		denyCounts:   map[string]int64{},

		loginFails: map[string]loginFail{},
	}
	s.adminHandler = s.adminMux()
	return s
}

// Handler 返回根路由，最外层包 panic 兜底 + 请求 ID 中间件。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/metrics", s.handleMetrics)

	// OpenAI 兼容代理。
	mux.HandleFunc("/v1/", s.withLogging(s.handleOpenAI))
	mux.HandleFunc("/v1", s.withLogging(s.handleOpenAI))

	// 管理 API。
	mux.HandleFunc("/api/admin/login", s.withLogging(s.handleAdminLogin))
	mux.Handle("/api/admin/", s.withLogging(s.adminHandler.ServeHTTP))

	// 前端。
	if webHandler, err := web.Handler(); err == nil {
		mux.Handle("/", webHandler)
	} else {
		s.logger.Warn("web assets unavailable", "err", err)
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				writeJSON(w, http.StatusOK, map[string]any{
					"service": "llm-router",
					"docs":    "/api/admin/overview (需要管理口令)",
				})
				return
			}
			http.NotFound(w, r)
		})
	}
	return s.withRecovery(mux)
}

// ---------- 中间件 ----------

// ctxKey 是上下文键的唯一类型，避免与其他包/库的键冲突。
type ctxKey int

const reqIDKey ctxKey = iota

// requestID 生成短随机十六进制请求 ID，用于日志、响应头与错误响应关联。
func requestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("r%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// withRecovery 包裹整个 mux：给每个请求生成 X-Request-ID 并兜底 panic。
// 若 panic 时尚未写出响应头（非流式），回 500 JSON 并带上 request_id；
// 若已写出（如 SSE 流中途），无法改状态码，只记录错误与堆栈。
func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID()
		r = r.WithContext(context.WithValue(r.Context(), reqIDKey, id))
		w.Header().Set("X-Request-ID", id)
		g := &panicGuard{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("panic recovered",
					"request_id", id,
					"method", r.Method,
					"path", r.URL.Path,
					"panic", fmt.Sprintf("%v", rec),
					"stack", string(debug.Stack()),
				)
				if !g.wroteHeader {
					writeJSON(g, http.StatusInternalServerError, map[string]any{
						"error": map[string]any{
							"message":    "internal server error",
							"type":       "internal_error",
							"code":       "internal_error",
							"request_id": id,
						},
					})
				}
			}
		}()
		next.ServeHTTP(g, r)
	})
}

// panicGuard 记录是否已写出响应头，panic 兜底时据此决定能否回 500。
type panicGuard struct {
	http.ResponseWriter
	wroteHeader bool
}

func (g *panicGuard) WriteHeader(code int) {
	g.wroteHeader = true
	g.ResponseWriter.WriteHeader(code)
}

func (g *panicGuard) Write(b []byte) (int, error) {
	if !g.wroteHeader {
		g.wroteHeader = true
	}
	return g.ResponseWriter.Write(b)
}

// Flush 透传给底层，保证 SSE 不被中间件缓冲。
func (g *panicGuard) Flush() {
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) withLogging(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next(rec, r)
		s.mu.Lock()
		s.requests++
		s.statusCounts[fmt.Sprintf("%d", rec.code)]++
		s.mu.Unlock()
		id, _ := r.Context().Value(reqIDKey).(string)
		s.logger.Info("request",
			"request_id", id,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.code,
			"ms", time.Since(start).Milliseconds(),
		)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (sr *statusRecorder) WriteHeader(code int) { sr.code = code; sr.ResponseWriter.WriteHeader(code) }

// Flush 透传给底层，保证 SSE 不被中间件缓冲。
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	up := int64(time.Since(s.startAt).Seconds())
	reqs := s.requests
	s.mu.RUnlock()

	// liveness 语义：进程活着就 200；上游是否健康单独用 degraded + providers 表达，
	// 供监控区分「网关挂了」与「某个上游被摘除」两种告警级别。
	providers := []router.ProviderHealth(nil)
	degraded := false
	if s.health != nil {
		providers = s.health.Snapshot()
		for _, p := range providers {
			if !p.Healthy {
				degraded = true
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"degraded":  degraded,
		"uptime_s":  up,
		"requests":  reqs,
		"providers": providers,
	})
}

// ---------- OpenAI 兼容入口 ----------

func (s *Server) handleOpenAI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/models" && r.Method == http.MethodGet {
		s.requireKey(w, r, func(w http.ResponseWriter, r *http.Request, key store.APIKey) {
			s.handleModels(w, r, key)
		})
		return
	}

	key, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	if allowed, reason := s.limiter.Check(key); !allowed {
		code := "quota_exceeded"
		if reason != nil {
			code = reason.Code
		}
		status := http.StatusTooManyRequests
		if code == "quota_exhausted" || code == "daily_quota_exhausted" {
			status = http.StatusPaymentRequired
		}
		msg := "quota exceeded"
		if reason != nil {
			msg = reason.Message
		}
		s.mu.Lock()
		s.denyCounts[code]++
		s.mu.Unlock()
		s.logger.Warn("request denied", "key", key.Prefix, "reason", msg)
		writeError(w, status, code, msg)
		return
	}

	// 额度告警：接近上限（≥80%）时提示一次，用于成本管控。
	if kind, used, limit, hit := s.limiter.CheckWarn(key); hit {
		s.logger.Warn("key near quota limit",
			"key", key.Prefix, "kind", kind,
			"used", used, "limit", limit)
	}

	// 多读 1 字节以检测超限，超限直接 413，而不是截断后报误导性的解析错误。
	max := s.store.Settings().MaxBodyBytes
	body, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "cannot read request body")
		return
	}
	if int64(len(body)) > max {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large",
			fmt.Sprintf("request body exceeds %d bytes", max))
		return
	}
	path := r.URL.Path
	if path == "/v1" {
		path = "/v1/chat/completions"
	}
	s.proxy.Handle(w, r, key, path, body)
}

// authenticate 校验自制 Key，成功返回 Key 记录。
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (store.APIKey, bool) {
	raw := auth.Extract(r.Header.Get("Authorization"))
	if raw == "" {
		raw = auth.Extract(r.Header.Get("X-Api-Key"))
	}
	if raw == "" {
		writeError(w, http.StatusUnauthorized, "missing_key",
			"missing API key; send it as Authorization: Bearer sk-tr-…")
		return store.APIKey{}, false
	}
	if !strings.HasPrefix(raw, auth.Prefix) {
		writeError(w, http.StatusUnauthorized, "invalid_key",
			"invalid key format; expected prefix "+auth.Prefix)
		return store.APIKey{}, false
	}
	key, ok := s.store.LookupKeyHash(auth.Hash(raw))
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid_key", "unknown API key")
		return store.APIKey{}, false
	}
	if !key.Enabled {
		writeError(w, http.StatusUnauthorized, "key_disabled", "API key is disabled")
		return store.APIKey{}, false
	}
	if !key.ExpiresAt.IsZero() && time.Now().After(key.ExpiresAt) {
		writeError(w, http.StatusUnauthorized, "key_expired", "API key is expired")
		return store.APIKey{}, false
	}
	return key, true
}

// requireKey 是需要先鉴权再执行的小工具。
func (s *Server) requireKey(w http.ResponseWriter, r *http.Request, fn func(http.ResponseWriter, *http.Request, store.APIKey)) {
	key, ok := s.authenticate(w, r)
	if !ok {
		return
	}
	fn(w, r, key)
}

// handleModels 聚合所有对外可用的模型名。
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request, key store.APIKey) {
	type modelItem struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		OwnedBy string `json:"owned_by"`
		Created int64  `json:"created"`
	}
	seen := map[string]bool{}
	items := make([]modelItem, 0, 16)
	add := func(id, owner string) {
		if id == "" || seen[id] {
			return
		}
		if len(key.Models) > 0 && !contains(key.Models, id) && !contains(key.Models, "*") {
			return
		}
		seen[id] = true
		items = append(items, modelItem{ID: id, Object: "model", OwnedBy: owner, Created: s.startAt.Unix()})
	}
	for _, rt := range s.store.ListRoutes() {
		add(rt.Model, "llm-router")
	}
	for _, p := range s.store.ListProviders() {
		if !p.Enabled {
			continue
		}
		for _, m := range p.Models {
			if m == "*" {
				continue
			}
			add(m, p.ID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": items})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---------- 通用输出 ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"message": msg, "type": code, "code": code},
	})
}
