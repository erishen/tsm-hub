package api

import (
	"net/http"
	"strings"

	"github.com/erishen/tsm-hub/internal/store"
)

// corsMiddleware 根据配置处理跨域资源共享（CORS）。
//
// 安全注意事项：
//   - 生产环境不要使用 AllowedOrigins: ["*"] + AllowCredentials: true（浏览器会拒绝）
//   - 建议明确列出允许的源，如 ["https://app.example.com"]
//   - 仅在需要浏览器端直接调用网关时启用 CORS
type corsMiddleware struct {
	cfg store.CORSCfg
}

func newCORSMiddleware(cfg store.CORSCfg) *corsMiddleware {
	if len(cfg.AllowedMethods) == 0 {
		cfg.AllowedMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions}
	}
	if len(cfg.AllowedHeaders) == 0 {
		cfg.AllowedHeaders = []string{"Content-Type", "Authorization", "X-Admin-Token", "X-Session-Token", "X-Llm-Router-Agent"}
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 86400 // 24 小时
	}
	return &corsMiddleware{cfg: cfg}
}

// Wrap 返回一个处理 CORS 的 http.Handler 包装器。
func (m *corsMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// 非跨域请求，直接放行
			next.ServeHTTP(w, r)
			return
		}

		// 检查源是否允许
		if !m.originAllowed(origin) {
			// 源不允许，不发送 CORS 头，浏览器会拒绝响应
			next.ServeHTTP(w, r)
			return
		}

		// 设置 CORS 响应头
		w.Header().Set("Access-Control-Allow-Origin", origin)
		if m.cfg.AllowCredentials {
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		w.Header().Set("Access-Control-Allow-Methods", strings.Join(m.cfg.AllowedMethods, ", "))
		w.Header().Set("Access-Control-Allow-Headers", strings.Join(m.cfg.AllowedHeaders, ", "))
		w.Header().Set("Access-Control-Max-Age", itoa(m.cfg.MaxAge))
		// 暴露给客户端的响应头
		w.Header().Set("Access-Control-Expose-Headers", "Content-Type, X-Request-Id")

		// 处理预检请求（OPTIONS）
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// originAllowed 检查请求源是否在允许列表中。
func (m *corsMiddleware) originAllowed(origin string) bool {
	for _, allowed := range m.cfg.AllowedOrigins {
		if allowed == "*" {
			return true
		}
		if strings.EqualFold(allowed, origin) {
			return true
		}
	}
	return false
}

// itoa 是 strconv.Itoa 的轻量替代，避免额外导入。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
