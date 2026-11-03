package adapter

import (
	"net/http"
	"testing"
)

func TestCopyHeadersFiltersInternalAndSensitive(t *testing.T) {
	src := http.Header{}
	src.Set("Authorization", "Bearer user-key")
	src.Set("X-Admin-Token", "admin-secret")
	src.Set("X-Session-Token", "session-secret")
	src.Set("Cookie", "session=abc")
	src.Set("X-Llm-Router-Agent", "browser")
	src.Set("X-Request-ID", "trace-123")
	src.Set("User-Agent", "curl/8")
	src.Set("X-Custom", "keep-me")

	dst := http.Header{}
	CopyHeaders(dst, src)

	for _, h := range []string{"Authorization", "X-Admin-Token", "X-Session-Token", "Cookie", "X-Llm-Router-Agent"} {
		if dst.Get(h) != "" {
			t.Errorf("header %s should be filtered, got %q", h, dst.Get(h))
		}
	}
	for _, h := range []string{"X-Request-ID", "User-Agent", "X-Custom"} {
		if dst.Get(h) == "" {
			t.Errorf("header %s should be copied", h)
		}
	}
}

func TestUpstreamURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		path string
		want string
	}{
		{"v1 base dedups client v1", "https://api.deepseek.com/v1", "/v1/chat/completions", "https://api.deepseek.com/v1/chat/completions"},
		{"v4 base (bigmodel) dedups client v1", "https://open.bigmodel.cn/api/paas/v4/", "/v1/chat/completions", "https://open.bigmodel.cn/api/paas/v4/chat/completions"},
		{"v1beta segment counts as version", "https://example.com/api/v1beta", "/v1/chat/completions", "https://example.com/api/v1beta/chat/completions"},
		{"non-version last segment keeps v1", "https://router.example.com/api", "/v1/chat/completions", "https://router.example.com/api/v1/chat/completions"},
		{"vendor-named segment keeps v1", "https://x.example.com/openai", "/v1/chat/completions", "https://x.example.com/openai/v1/chat/completions"},
		{"non-chat path untouched", "https://api.deepseek.com/v1", "/v1/models", "https://api.deepseek.com/v1/models"},
		{"trailing slash trimmed", "https://api.siliconflow.cn/v1/", "/v1/chat/completions", "https://api.siliconflow.cn/v1/chat/completions"},
	}
	for _, tc := range cases {
		if got := upstreamURL(tc.base, tc.path); got != tc.want {
			t.Errorf("%s: upstreamURL(%q,%q) = %q, want %q", tc.name, tc.base, tc.path, got, tc.want)
		}
	}
}
