package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnvOrFallback(t *testing.T) {
	t.Setenv("TSM_HUB_X1", "hub")
	t.Setenv("TSM_HUB_X2", "gw")
	t.Setenv("LLM_ROUTER_X3", "rt")

	if v := envOrFallback("TSM_HUB_X1", "TSM_HUB_X2", "LLM_ROUTER_X3", "def"); v != "hub" {
		t.Fatalf("priority1: got %q", v)
	}
	if v := envOrFallback("TSM_HUB_NO1", "TSM_HUB_X2", "LLM_ROUTER_X3", "def"); v != "gw" {
		t.Fatalf("priority2: got %q", v)
	}
	if v := envOrFallback("TSM_HUB_NO2", "TSM_HUB_NO3", "LLM_ROUTER_X3", "def"); v != "rt" {
		t.Fatalf("priority3: got %q", v)
	}
	if v := envOrFallback("A", "B", "C", "def"); v != "def" {
		t.Fatalf("default: got %q", v)
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("TSM_ENVOR_KEY", "v")
	if got := envOr("TSM_ENVOR_KEY", "d"); got != "v" {
		t.Fatalf("set: got %q", got)
	}
	if got := envOr("TSM_ENVOR_MISSING", "d"); got != "d" {
		t.Fatalf("unset: got %q", got)
	}
}

func TestDefaultDataDir(t *testing.T) {
	if got := DefaultDataDir(); got != "data" {
		t.Fatalf("default: got %q", got)
	}
	t.Setenv("TSM_HUB_DATA_DIR", "/fromhub")
	if got := DefaultDataDir(); got != "/fromhub" {
		t.Fatalf("hub: got %q", got)
	}
}

func TestDefaultDataDirRouterFallback(t *testing.T) {
	// hub/gateway key left unset (restored from prior tests); only router key set.
	t.Setenv("LLM_ROUTER_DATA_DIR", "/fromrouter")
	if got := DefaultDataDir(); got != "/fromrouter" {
		t.Fatalf("router: got %q", got)
	}
}

func TestParseDefaults(t *testing.T) {
	c, err := Parse([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.DataDir == "" {
		t.Fatal("empty datadir")
	}
	if !filepath.IsAbs(c.DataDir) {
		t.Fatalf("datadir not absolute: %q", c.DataDir)
	}
	if c.LogLevel != "info" {
		t.Fatalf("loglevel default: %q", c.LogLevel)
	}
	if c.AdminToken != "" {
		t.Fatalf("admin default should be empty: %q", c.AdminToken)
	}
}

func TestParseFlagsOverride(t *testing.T) {
	c, err := Parse([]string{"-addr", ":9999", "-admin-token", "secret", "-log-level", "debug", "-data", "/tmp/x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Listen != ":9999" {
		t.Fatalf("addr: %q", c.Listen)
	}
	if c.AdminToken != "secret" {
		t.Fatalf("admin: %q", c.AdminToken)
	}
	if c.LogLevel != "debug" {
		t.Fatalf("loglevel: %q", c.LogLevel)
	}
	if c.DataDir != "/tmp/x" {
		t.Fatalf("data: %q", c.DataDir)
	}
}

func TestParseEnvOverride(t *testing.T) {
	t.Setenv("TSM_HUB_ADDR", ":8888")
	t.Setenv("TSM_HUB_ADMIN_TOKEN", "envtok")
	t.Setenv("TSM_HUB_LOG_LEVEL", "warn")
	c, err := Parse([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Listen != ":8888" {
		t.Fatalf("addr env: %q", c.Listen)
	}
	if c.AdminToken != "envtok" {
		t.Fatalf("admin env: %q", c.AdminToken)
	}
	if c.LogLevel != "warn" {
		t.Fatalf("loglevel env: %q", c.LogLevel)
	}
}

func TestParseEmptyDataDir(t *testing.T) {
	if _, err := Parse([]string{"-data", ""}); err == nil {
		t.Fatal("expected error for empty data dir")
	}
}

func TestParseBadFlag(t *testing.T) {
	if _, err := Parse([]string{"-nope"}); err == nil {
		t.Fatal("expected error for unknown flag")
	}
}

func TestConfigPaths(t *testing.T) {
	c := Config{DataDir: "/tmp/foo"}
	if got := c.ConfigFile(); got != "/tmp/foo/config.json" {
		t.Fatalf("configfile: %q", got)
	}
	if got := c.UsageDir(); got != "/tmp/foo/usage" {
		t.Fatalf("usagedir: %q", got)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	content := "FOO=bar\n# comment line\n\nBAZ=\"hello world\"\nQUX='single quoted'\nBADLINE\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(p); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer os.Unsetenv("FOO")
	defer os.Unsetenv("BAZ")
	defer os.Unsetenv("QUX")
	if got := os.Getenv("FOO"); got != "bar" {
		t.Fatalf("FOO: %q", got)
	}
	if got := os.Getenv("BAZ"); got != "hello world" {
		t.Fatalf("BAZ quoted: %q", got)
	}
	if got := os.Getenv("QUX"); got != "single quoted" {
		t.Fatalf("QUX single-quoted: %q", got)
	}
}

func TestLoadDotEnvMissingFile(t *testing.T) {
	if err := LoadDotEnv("/nonexistent/path/.env"); err != nil {
		t.Fatalf("missing file should be nil, got %v", err)
	}
}

func TestLoadDotEnvNoOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, ".env")
	if err := os.WriteFile(p, []byte("KEEP=fromfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// KEEP pre-set via t.Setenv; LoadDotEnv must NOT os.Setenv it (guard skips), so no panic.
	t.Setenv("KEEP", "fromenv")
	if err := LoadDotEnv(p); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := os.Getenv("KEEP"); got != "fromenv" {
		t.Fatalf("should not override existing: %q", got)
	}
}
