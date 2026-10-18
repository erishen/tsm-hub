package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/erishen/tsm-hub/internal/store"
)

// waitProviderName 轮询等待 store 内某个 provider 的 Name 变为 want。
func waitProviderName(t *testing.T, st *store.Store, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, p := range st.ListProviders() {
			if p.ID == id && p.Name == want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("hot reload did not pick up provider %q name %q", id, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

const cfgWithProviderName = `{
  "version": 1,
  "settings": {"listen": ":9070", "admin_token": "test-admin-token"},
  "providers": [{"id": "p1", "name": "%s", "base_url": "http://127.0.0.1:1", "enabled": true}]
}`

// TestWatchConfigHotReloadsOnAtomicReplace 覆盖热加载的核心场景：外部用
// 「临时文件 + rename」原子替换 config.json（store.saveLocked 与多数编辑器
// 的保存方式）后，变更必须生效。早期实现盯文件 inode，第一次替换后 watcher
// 静默失效（inotify IN_IGNORED），本测试守住修复后的行为。
func TestWatchConfigHotReloadsOnAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(replaceName(cfgWithProviderName, "p1-old")), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go watchConfig(st, logger)
	// 等 watcher Add 就绪再发事件，避免竞态。
	time.Sleep(200 * time.Millisecond)

	// 原子替换（与 saveLocked 同手法）：新 inode 改名覆盖旧文件。
	tmp := filepath.Join(dir, ".config-atomic.json")
	if err := os.WriteFile(tmp, []byte(replaceName(cfgWithProviderName, "p1-new")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	waitProviderName(t, st, "p1", "p1-new")
}

// TestWatchConfigIgnoresTempFileNoise 覆盖同目录临时文件（.config-*.json）
// 与派生文件（unavailable.json）的写入不得触发重载：否则一次 store 保存
// 会引发「刚存盘的内容被外部事件再读一遍」的冗余 reload 风暴。
func TestWatchConfigIgnoresTempFileNoise(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(replaceName(cfgWithProviderName, "p1-old")), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go watchConfig(st, logger)
	time.Sleep(200 * time.Millisecond)

	// 同目录写一堆噪声文件（模拟 saveLocked 的临时文件与派生数据文件）。
	for _, name := range []string{".config-abc123.json", "unavailable.json", "memory.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(600 * time.Millisecond)
	// 噪声不应改变内存配置（仍为旧值，且没有崩溃/泄漏 goroutine）。
	for _, p := range st.ListProviders() {
		if p.ID == "p1" && p.Name != "p1-old" {
			t.Fatalf("noise file triggered reload: name = %q, want p1-old", p.Name)
		}
	}

	// 真正的 config.json 原地写仍可触发重载（回归「原地写」路径）。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(replaceName(cfgWithProviderName, "p1-inplace")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	waitProviderName(t, st, "p1", "p1-inplace")
}

func replaceName(tmpl, name string) string {
	return fmt.Sprintf(tmpl, name)
}
