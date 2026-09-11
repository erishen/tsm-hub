package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erishen/tsm-hub/internal/store"
)

func sandboxStore(t *testing.T) *Proxy {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.Sandbox.Enabled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &Proxy{store: st}
}

// TestExecuteCodePython 在真实 Docker 沙箱执行 python（docker 不可用时跳过）。
func TestExecuteCodePython(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker 不可用，跳过沙箱测试")
	}
	p := sandboxStore(t)
	out := p.execTool("k1", "execute_code", toolArgs{
		"language": "python",
		"code":     "print(6 * 7)",
	})
	if strings.HasPrefix(out, "error") {
		t.Fatalf("execute_code error: %s", out)
	}
	if !strings.Contains(out, "42") {
		t.Fatalf("execute_code = %q, want containing 42", out)
	}
	if !strings.Contains(out, "[exit=0") {
		t.Fatalf("execute_code = %q, want exit=0 marker", out)
	}
}

// TestExecuteCodeShell 与错误路径。
func TestExecuteCodeShell(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker 不可用，跳过沙箱测试")
	}
	p := sandboxStore(t)
	out := p.execTool("k1", "execute_code", toolArgs{
		"language": "sh",
		"code":     "echo hello-sandbox",
	})
	if !strings.Contains(out, "hello-sandbox") {
		t.Fatalf("shell exec = %q", out)
	}
	// 非零退出码透传
	out = p.execTool("k1", "execute_code", toolArgs{
		"language": "sh",
		"code":     "exit 3",
	})
	if !strings.Contains(out, "[exit=3") {
		t.Fatalf("exit code = %q, want exit=3", out)
	}
	// 不支持的组合
	out = p.execTool("k1", "execute_code", toolArgs{"language": "brainfuck", "code": "x"})
	if !strings.HasPrefix(out, "error") {
		t.Fatalf("bad language should error, got %q", out)
	}
	// 未启用时报错
	st, _ := store.New(filepath.Join(t.TempDir(), "config.json"))
	p2 := &Proxy{store: st}
	out = p2.execTool("k1", "execute_code", toolArgs{"language": "python", "code": "print(1)"})
	if !strings.HasPrefix(out, "error") {
		t.Fatalf("disabled should error, got %q", out)
	}
}

// TestExecuteCodeTimeout 超时强制终止（挂起进程 3s，超时 2s）。
func TestExecuteCodeTimeout(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker 不可用，跳过沙箱测试")
	}
	p := sandboxStore(t)
	out := p.execTool("k1", "execute_code", toolArgs{
		"language": "sh",
		"code":     "sleep 10",
		"timeout":  2,
	})
	if !strings.Contains(out, "超时") {
		t.Fatalf("timeout exec = %q, want 超时 marker", out)
	}
}

var _ = os.Getenv
