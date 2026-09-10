package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/erishen/tsm-gateway/internal/store"
)

func TestSystemInfoTool(t *testing.T) {
	out := systemInfo()
	for _, want := range []string{"os=", "arch=", "cpu_cores=", "uptime="} {
		if !strings.Contains(out, want) {
			t.Fatalf("system_info = %q, want containing %q", out, want)
		}
	}
}

func TestCSVAnalyzeTool(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "data.csv")
	content := "name,age,score\nAlice,25,88.5\nBob,30,92\nCarol,28,\n"
	if err := os.WriteFile(csvPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.Agent.ReadRoot = dir
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := &Proxy{store: st}

	out := p.execTool("k1", "csv_analyze", toolArgs{"path": "data.csv"})
	if strings.HasPrefix(out, "error") {
		t.Fatalf("csv_analyze error: %s", out)
	}
	for _, want := range []string{"3 行数据", "3 列", "name", "age", "score", "数值列", "Alice"} {
		if !strings.Contains(out, want) {
			t.Fatalf("csv_analyze = %q, want containing %q", out, want)
		}
	}
	// 白名单外路径拒绝
	out = p.execTool("k1", "csv_analyze", toolArgs{"path": "../outside.csv"})
	if !strings.HasPrefix(out, "error") {
		t.Fatalf("outside path should error, got %q", out)
	}
	// 无 read_root 时报错
	st2, _ := store.New(filepath.Join(t.TempDir(), "config.json"))
	p2 := &Proxy{store: st2}
	out = p2.execTool("k1", "csv_analyze", toolArgs{"path": "x.csv"})
	if !strings.HasPrefix(out, "error") {
		t.Fatalf("no read_root should error, got %q", out)
	}
}

func TestExchangeRateBadArgs(t *testing.T) {
	st, _ := store.New(filepath.Join(t.TempDir(), "config.json"))
	p := &Proxy{store: st}
	out := p.execTool("k1", "query_exchange_rate", toolArgs{})
	if !strings.HasPrefix(out, "error") {
		t.Fatalf("empty from should error, got %q", out)
	}
}
