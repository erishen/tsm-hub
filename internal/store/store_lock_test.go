package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSaveLockedWritesAndLockFile 验证 saveLocked 在加跨进程文件锁后仍能正常落盘，
// 且生成了 .lock 文件（多实例并发写保护）。无主密钥时密钥保持明文（向后兼容）。
func TestSaveLockedWritesAndLockFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	st, err := New(p)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := st.Update(func(c *Config) error {
		c.Providers = append(c.Providers, Provider{ID: "p1", APIKey: "sk-test-plain", BaseURL: "http://x/v1"})
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), "sk-test-plain") {
		t.Fatalf("config not persisted; got: %s", raw)
	}
	if _, err := os.Stat(p + ".lock"); err != nil {
		t.Fatalf("lock file should be created alongside config: %v", err)
	}
}

// TestReloadPreservesPlaintext 验证无主密钥时 Reload 不破坏明文配置，
// 且内存态 Provider.APIKey 为明文（proxy 需明文鉴权）。
func TestReloadPreservesPlaintext(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	st, err := New(p)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := st.Update(func(c *Config) error {
		c.Providers = append(c.Providers, Provider{ID: "p1", APIKey: "sk-reload-plain", BaseURL: "http://x/v1"})
		return nil
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := st.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	found := false
	for _, pr := range st.ListProviders() {
		if pr.ID == "p1" {
			found = true
			if pr.APIKey != "sk-reload-plain" {
				t.Fatalf("APIKey should stay plaintext in memory, got %q", pr.APIKey)
			}
		}
	}
	if !found {
		t.Fatal("provider p1 missing after reload")
	}
}
