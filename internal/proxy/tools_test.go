package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestToolHTTPClientConfigured(t *testing.T) {
	if toolHTTPClient == nil {
		t.Fatal("toolHTTPClient must not be nil")
	}
	if toolHTTPClient.Timeout != 15*time.Second {
		t.Fatalf("toolHTTPClient.Timeout = %v, want 15s", toolHTTPClient.Timeout)
	}
}

// TestToolHTTPClientEnforcesTimeout 验证 toolHTTPClient 自带整体超时兜底。
func TestToolHTTPClientEnforcesTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	orig := toolHTTPClient.Timeout
	toolHTTPClient.Timeout = 100 * time.Millisecond
	t.Cleanup(func() { toolHTTPClient.Timeout = orig })

	_, err := toolHTTPClient.Get(srv.URL)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(strings.ToLower(err.Error()), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}
