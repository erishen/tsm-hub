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
