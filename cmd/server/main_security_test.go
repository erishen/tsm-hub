package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erishen/tsm-hub/internal/store"
)

func TestConsoleURL(t *testing.T) {
	for _, tc := range []struct {
		listen, want string
		tls          bool
	}{
		{":9070", "http://localhost:9070", false},
		{"  :9070  ", "http://localhost:9070", false},
		{"0.0.0.0:9070", "http://localhost:9070", false},
		{"0.0.0.0:9070", "https://localhost:9070", true},
		{"127.0.0.1:9070", "https://127.0.0.1:9070", true},
		{"http://example.com:9070", "http://example.com:9070", false},
		{"https://example.com", "https://example.com", false},
	} {
		if got := consoleURL(tc.listen, tc.tls); got != tc.want {
			t.Errorf("consoleURL(%q, %v) = %q, want %q", tc.listen, tc.tls, got, tc.want)
		}
	}
}

func TestNewLoggerLevels(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		level                     string
		debugEnabled, warnEnabled bool
	}{
		{"debug", true, true},
		{"info", false, true},
		{"warn", false, true},
		{"error", false, false},
		{"unknown", false, true}, // 未知级别回退到 info，不能变成静默
	} {
		l := newLogger(tc.level)
		if got := l.Enabled(ctx, slog.LevelDebug); got != tc.debugEnabled {
			t.Errorf("newLogger(%q) Debug enabled = %v, want %v", tc.level, got, tc.debugEnabled)
		}
		if got := l.Enabled(ctx, slog.LevelWarn); got != tc.warnEnabled {
			t.Errorf("newLogger(%q) Warn enabled = %v, want %v", tc.level, got, tc.warnEnabled)
		}
	}
}

// checkConfigSecurity 是启动时唯一的「配置可能泄露密钥」告警路径，此前 0% 覆盖。
// 分别验证：权限过松、明文 key、env 引用与加密密文都不该被误报。
const securityCfg = `{
  "version": 1,
  "settings": {"listen": ":9070", "admin_token": "test-admin-token"},
  "providers": [
    {"id": "plain", "name": "plain", "base_url": "http://127.0.0.1:1", "api_key": "plain-key-for-test", "enabled": true},
    {"id": "env", "name": "env", "base_url": "http://127.0.0.1:1", "api_key": "env:DEMO_KEY", "enabled": true},
    {"id": "empty", "name": "empty", "base_url": "http://127.0.0.1:1", "enabled": true}
  ]
}`

// newSecurityStore 建好配置后再插入一个「已加密」provider，覆盖
// IsEncryptedSecret 分支。不能把假密文直接写进配置文件：store.New 会尝试
// 解密落盘的 enc:v1: 字段，缺少主密钥时直接拒绝加载。
func newSecurityStore(t *testing.T, cfg string, perm os.FileMode) (string, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProvider(store.Provider{
		ID: "enc", Name: "enc", APIKey: "enc:v1:AAAA",
		BaseURL: "http://127.0.0.1:1", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	// Upsert 会原子重写 config.json，权限可能被重置；显式恢复后再做权限检查。
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
	return path, st
}

func securityWarnings(path string, st *store.Store) string {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	checkConfigSecurity(path, st, logger)
	return buf.String()
}

func TestCheckConfigSecurity(t *testing.T) {
	// 明文模式：fixture 不依赖运行测试那台机器是否配置了主密钥。
	t.Setenv("TSM_HUB_MASTER_KEY", "")

	for _, tc := range []struct {
		name         string
		perm         os.FileMode
		wantPermWarn bool
	}{
		{"0600 with plaintext key", 0o600, false},
		{"0644 with plaintext key", 0o644, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, st := newSecurityStore(t, securityCfg, tc.perm)
			out := securityWarnings(path, st)

			if got := strings.Contains(out, "readable by group/others"); got != tc.wantPermWarn {
				t.Errorf("permission warning present = %v, want %v; log = %s", got, tc.wantPermWarn, out)
			}
			// 只有 plain 一个明文 key；env 引用、加密密文、空 key 都不得计入。
			if !strings.Contains(out, "plaintext_count=1") {
				t.Errorf("plaintext warning missing; log = %s", out)
			}
			if strings.Contains(out, "plaintext_count=2") || strings.Contains(out, "plaintext_count=3") {
				t.Errorf("env/encrypted/empty keys were counted as plaintext; log = %s", out)
			}
			if !strings.Contains(out, "env_reference_count=1") {
				t.Errorf("env reference count missing; log = %s", out)
			}
		})
	}

	// 权限正确且无明文 key 时必须完全静默，避免运维被反复噪声淹没。
	t.Run("clean config stays silent", func(t *testing.T) {
		clean := strings.Replace(securityCfg, `"api_key": "plain-key-for-test", `, "", 1)
		path, st := newSecurityStore(t, clean, 0o600)
		if out := securityWarnings(path, st); out != "" {
			t.Fatalf("clean config produced warnings: %s", out)
		}
	})
}

// 确认测试所用的加密前缀确实被 store 判定为密文，避免用例因常量改名而失真。
func TestEncryptedSecretFixture(t *testing.T) {
	if !store.IsEncryptedSecret("enc:v1:AAAA") {
		t.Fatal("test fixture no longer matches store's encrypted-secret prefix")
	}
}
