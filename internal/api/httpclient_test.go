package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminHTTPClientConfigured(t *testing.T) {
	if adminHTTPClient == nil {
		t.Fatal("adminHTTPClient must not be nil")
	}
	if adminHTTPClient.Timeout != 20*time.Second {
		t.Fatalf("adminHTTPClient.Timeout = %v, want 20s", adminHTTPClient.Timeout)
	}
}

// TestAdminHTTPClientEnforcesTimeout 验证 adminHTTPClient 自带整体超时：
// 即使调用方忘记在 request 上设置 context 超时，client 也会在 Timeout 内中断，
// 避免 goroutine 永久阻塞（这正是替换 http.DefaultClient 的兜底价值）。
func TestAdminHTTPClientEnforcesTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	orig := adminHTTPClient.Timeout
	adminHTTPClient.Timeout = 100 * time.Millisecond
	t.Cleanup(func() { adminHTTPClient.Timeout = orig })

	_, err := adminHTTPClient.Get(srv.URL)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}
