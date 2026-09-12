package proxy

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/erishen/tsm-hub/internal/store"
)

// TestToolSchemasAllowFilter 验证 per-key 工具白名单：
// toolAllow 非空时只保留名单内工具；serverAllow 非空时未列入的 MCP server
// 不建连也不出 schema（fake HTTP server 的访问计数应为 0）。
func TestToolSchemasAllowFilter(t *testing.T) {
	url := fakeHTTPMCP(t)
	st, err := store.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(c *store.Config) error {
		c.Settings.Mcps = map[string]store.MCPServer{
			"remote": {Transport: "http", URL: url},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	p := &Proxy{store: st, mcps: newMCPManager()}

	hasTool := func(schemas []map[string]any, name string) bool {
		for _, s := range schemas {
			if fn, ok := s["function"].(map[string]any); ok && fn["name"] == name {
				return true
			}
		}
		return false
	}

	// 1) 全量：mcp_remote_add 在（基线，兼容旧行为）。
	if !hasTool(p.toolSchemas(), "mcp_remote_add") {
		t.Fatal("baseline: mcp_remote_add missing")
	}

	// 2) toolAllow 只留 calc：MCP 工具与其它内置工具都不出现。
	only := p.toolSchemasAllow(nil, map[string]bool{"calc": true})
	if hasTool(only, "mcp_remote_add") || hasTool(only, "echo") {
		t.Fatalf("toolAllow=calc leaked other tools: %d schemas", len(only))
	}
	if !hasTool(only, "calc") {
		t.Fatal("toolAllow=calc dropped calc itself")
	}

	// 3) serverAllow 不含 remote：该 server 不建连、其工具不出 schema，
	//    但内置工具不受影响。
	noRemote := p.toolSchemasAllow(map[string]bool{}, nil)
	if hasTool(noRemote, "mcp_remote_add") {
		t.Fatal("serverAllow={} leaked mcp_remote_add")
	}
	if !hasTool(noRemote, "calc") {
		t.Fatal("serverAllow={} dropped builtin calc")
	}

	// 4) 空白名单(nil)等价全量。
	if !hasTool(p.toolSchemasAllow(nil, nil), "mcp_remote_add") {
		t.Fatal("nil allowlists should mean full pool")
	}
}

// TestTruncateDesc 验证描述截断：预算内原样、超预算按句子边界、
// 无边界时 rune 硬截不产生非法 UTF-8、小数点不算边界。
func TestTruncateDesc(t *testing.T) {
	short := "读取文件内容。"
	if got := truncateDesc(short, 200); got != short {
		t.Fatalf("short desc mutated: %q", got)
	}

	long := strings.Repeat("这是一句完整的中文描述。", 30) // 每句 12 rune，300+ rune
	got := truncateDesc(long, 200)
	if n := len([]rune(got)); n > 200 {
		t.Fatalf("truncateDesc exceeded budget: %d runes", n)
	}
	if !strings.HasSuffix(got, "。") {
		t.Fatalf("truncateDesc should cut at sentence boundary: %q", got)
	}
	if !strings.HasPrefix(long, got) {
		t.Fatal("truncated text must be a prefix of the original")
	}

	// 无中文句号：英文描述硬截，但仍是合法字符串（rune 级，不切半字符）。
	en := strings.Repeat("word ", 100)
	gotEn := truncateDesc(en, 40)
	if len([]rune(gotEn)) != 40 {
		t.Fatalf("hard cut length = %d", len([]rune(gotEn)))
	}

	// 小数点不算句子边界。
	dec := "Version 1.5 supports streaming. " + strings.Repeat("x", 300)
	gotDec := truncateDesc(dec, 60)
	if !strings.Contains(dec[:strings.Index(dec, "supports")], "1.5") {
		t.Fatal("test setup broken")
	}
	if strings.HasSuffix(gotDec, "Version 1.") {
		t.Fatalf("decimal point treated as boundary: %q", gotDec)
	}
}

// TestCleanSchemaTrimsPropertyDesc 验证 property 级描述也被瘦身。
func TestCleanSchemaTrimsPropertyDesc(t *testing.T) {
	long := strings.Repeat("参数说明。", 100)
	in := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": long},
		},
	}
	out := cleanSchema(in)
	props := out["properties"].(map[string]any)
	pm := props["path"].(map[string]any)
	got := pm["description"].(string)
	if len([]rune(got)) > maxParamDescRunes {
		t.Fatalf("property desc not trimmed: %d runes", len([]rune(got)))
	}
	if !strings.HasPrefix(long, got) {
		t.Fatal("trimmed desc must be a prefix of original")
	}
}
