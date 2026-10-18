package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erishen/tsm-hub/internal/store"
)

// 私网目标（回环）在拨号层被拦截，即便域名解析与连接分两次发生也无法绕过。
func TestGuardedFetchClientBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer srv.Close()

	client := guardedFetchClient(false)
	defer client.CloseIdleConnections()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("expected loopback to be blocked, got status %d", resp.StatusCode)
	}
	if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// allowPrivate=true（本地联调）时放行回环地址。
func TestGuardedFetchClientAllowsPrivateWhenEnabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := guardedFetchClient(true)
	defer client.CloseIdleConnections()
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("allowPrivate should permit loopback: %v", err)
	}
	resp.Body.Close()
}

// read_file 不能通过 root 内的符号链接读到 root 之外的文件。
func TestReadFileRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "ok.txt")
	if err := os.WriteFile(inside, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}

	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.Agent.ReadRoot = root
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := &Proxy{store: st}

	if out := p.execTool("k1", "read_file", toolArgs{"path": "ok.txt"}); !strings.Contains(out, "hello") {
		t.Fatalf("normal file should read, got %q", out)
	}
	if out := p.execTool("k1", "read_file", toolArgs{"path": "link.txt"}); !strings.HasPrefix(out, "error") {
		t.Fatalf("symlink escape should be rejected, got %q", out)
	}
}
