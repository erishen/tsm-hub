package proxy

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/erishen/tsm-hub/internal/store"
)

func newDynamicTestProxy(t *testing.T, mutate func(*store.Config)) *Proxy {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.Agent.ReadRoot = t.TempDir()
		if mutate != nil {
			mutate(c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &Proxy{store: st, mcps: newMCPManager()}
}

func schemaNames(schemas []map[string]any) map[string]bool {
	m := map[string]bool{}
	for _, s := range schemas {
		if fn, ok := s["function"].(map[string]any); ok {
			if n, ok := fn["name"].(string); ok {
				m[n] = true
			}
		}
	}
	return m
}

// TestToolScore 验证打分：拉丁 token 与中文 bigram 都能命中，
// 无关 query 得 0。
func TestToolScore(t *testing.T) {
	if toolScore("帮我读取一下文件", "read_file", "读取本地文件内容") == 0 {
		t.Fatal("cjk bigram should match read_file")
	}
	if toolScore("query exchange rate please", "query_exchange_rate", "查询实时汇率") == 0 {
		t.Fatal("latin tokens should match tool name")
	}
	if toolScore("完全无关的的内容", "calc", "计算数学表达式") != 0 {
		t.Fatal("unrelated query must score 0")
	}
	if toolScore("", "calc", "计算") != 0 {
		t.Fatal("empty query must score 0")
	}
}

// TestDynamicToolSelection 验证动态注入：核心常驻、相关工具入选、
// 无关工具被裁掉、tool_search 始终在、小池子直接全量。
func TestDynamicToolSelection(t *testing.T) {
	// 核心集收窄到 2 个，让选择逻辑在小目录下也能生效。
	p := newDynamicTestProxy(t, func(c *store.Config) {
		c.Settings.Agent.DynamicTools = true
		c.Settings.Agent.DynamicTopK = 2
		c.Settings.Agent.CoreTools = []string{"calc", "get_time"}
	})

	// 1) 相关 query：csv_analyze（csv/读取/分析命中）入选，无关的 echo/system_info 出局。
	schemas := p.dynamicToolSchemas("帮我把 data/report.csv 读取并分析一下", nil, nil)
	names := schemaNames(schemas)
	if !names["calc"] || !names["get_time"] {
		t.Fatal("core tools must stay resident")
	}
	if !names["tool_search"] {
		t.Fatal("tool_search meta tool must always be injected")
	}
	if !names["csv_analyze"] {
		t.Fatalf("relevant csv_analyze missing: %v", names)
	}
	if names["echo"] || names["system_info"] {
		t.Fatalf("irrelevant tools leaked: %v", names)
	}

	// 2) 无关 query：只剩核心 + tool_search。
	onlyCore := schemaNames(p.dynamicToolSchemas("随便聊聊今天的心情", nil, nil))
	if onlyCore["echo"] || onlyCore["csv_analyze"] {
		t.Fatalf("no-match query should not add tools: %v", onlyCore)
	}
	if !onlyCore["tool_search"] {
		t.Fatal("tool_search missing on no-match query")
	}

	// 3) 小池子（核心收窄到 2 个、topK 用默认 8 → 阈值 14 > 目录 11）：
	//    直接全量 + tool_search。
	p2 := newDynamicTestProxy(t, func(c *store.Config) {
		c.Settings.Agent.DynamicTools = true
		c.Settings.Agent.CoreTools = []string{"calc", "get_time"}
	})
	full := schemaNames(p2.dynamicToolSchemas("随便聊聊", nil, nil))
	if !full["echo"] || !full["query_exchange_rate"] {
		t.Fatalf("small pool should fall back to full injection: %v", full)
	}
	if !full["tool_search"] {
		t.Fatal("tool_search missing on small pool fallback")
	}
}

// TestToolSearchText 验证元工具执行：命中返回名称+参数、无命中退回
// 全量名单、per-key toolAllow 仍然生效。
func TestToolSearchText(t *testing.T) {
	p := newDynamicTestProxy(t, nil)

	got := p.toolSearchText("读取文件 read", nil, nil)
	if !strings.Contains(got, "read_file") || !strings.Contains(got, "path") {
		t.Fatalf("expected read_file with params, got: %s", got)
	}

	none := p.toolSearchText("zzzqqqxyz", nil, nil)
	if !strings.Contains(none, "没有匹配") || !strings.Contains(none, "query_exchange_rate") {
		t.Fatalf("no-match should list catalog names, got: %s", none)
	}

	filtered := p.toolSearchText("读取文件", nil, map[string]bool{"calc": true})
	if strings.Contains(filtered, "read_file") {
		t.Fatalf("toolAllow=calc should exclude read_file, got: %s", filtered)
	}

	if out := p.toolSearchText("", nil, nil); !strings.HasPrefix(out, "error:") {
		t.Fatalf("empty query must error, got: %s", out)
	}
}

// TestExecToolSearch 验证 execTool 直调 tool_search（管理台路径）。
func TestExecToolSearch(t *testing.T) {
	p := newDynamicTestProxy(t, nil)
	out := p.execTool("admin", "tool_search", toolArgs{"query": "汇率 exchange"})
	if !strings.Contains(out, "query_exchange_rate") {
		t.Fatalf("execTool tool_search failed: %s", out)
	}
}
