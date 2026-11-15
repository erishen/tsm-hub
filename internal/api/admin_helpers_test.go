package api

import (
	"math"
	"strings"
	"testing"

	"github.com/erishen/tsm-hub/internal/quota"
	"github.com/erishen/tsm-hub/internal/store"
)

// 管理台辅助函数此前大面积 0% 覆盖，其中 maskToken/maskSecret/validProviderID
// 是脱敏与数据归因的安全边界，解析器则负责处理不可信的上游响应与模型输出。
// 这里用表驱动把边界钉住，只测纯函数，不构造 Server / HTTP / 数据库。

func TestMaskToken(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"", "***"},
		{"short", "***"},
		{"12345678", "***"},      // len == 8 仍算短，整串盖掉
		{"123456789", "1234***"}, // 刚过阈值，只暴露前 4 位
		{"sk-live-abcd1234", "sk-l***"},
	} {
		if got := maskToken(tc.in); got != tc.want {
			t.Errorf("maskToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// 脱敏结果不得回显原文中段。
	secret := "abcdefgh-SECRET-MIDDLE-1234"
	if got := maskToken(secret); strings.Contains(got, "SECRET-MIDDLE") {
		t.Fatalf("maskToken leaked the middle of the secret: %q", got)
	}
}

func TestMaskSecret(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"", ""},
		{"abc", "***"},
		{"12345678", "********"},
		{"123456789", "1234" + store.MaskedSecretMarker + "6789"},
	} {
		if got := maskSecret(tc.in); got != tc.want {
			t.Errorf("maskSecret(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	secret := "abcdKEEP-THIS-PRIVATEwxyz"
	got := maskSecret(secret)
	if strings.Contains(got, "KEEP-THIS-PRIVATE") || !strings.Contains(got, store.MaskedSecretMarker) {
		t.Fatalf("maskSecret did not redact the middle: %q", got)
	}
}

func TestSlugify(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"Hello World", "hello-world"},
		{"  Foo   Bar!! ", "foo-bar"},
		{"v1.2 Beta", "v1-2-beta"},
		{"a__b", "a-b"},
		{"---", ""},
		{"中文", ""}, // 非 ASCII 逐 rune 变 '-'，Trim 后为空
		{"A", "a"},
	} {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidProviderID(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", false},
		{"abc", true},
		{"a-b_c.d", true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), false},
		{"has space", false},
		{"../etc/passwd", false},
		{"上游<html>", false},
		// 历史 bug：上游错误体被写进 provider_id，聚合展示必须跳过这类记录。
		{"upstream error: 502 Bad Gateway", false},
	} {
		if got := validProviderID(tc.in); got != tc.want {
			t.Errorf("validProviderID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestToAggView(t *testing.T) {
	a := quota.Agg{Requests: 10, Errors: 2, LatencySumMS: 200, CostUSD: 1.25}
	v := toAggView(a)
	if v.AvgLatencyMS != 20 {
		t.Errorf("AvgLatencyMS = %d, want 20", v.AvgLatencyMS)
	}
	if v.ErrorRate != 0.2 {
		t.Errorf("ErrorRate = %v, want 0.2", v.ErrorRate)
	}
	if v.Agg.CostUSD != a.CostUSD || v.Agg.Requests != a.Requests {
		t.Fatalf("toAggView lost the embedded Agg: %+v", v.Agg)
	}
	if empty := toAggView(quota.Agg{}); empty.AvgLatencyMS != 0 || empty.ErrorRate != 0 {
		t.Fatalf("zero-request aggregate must not divide by zero: %+v", empty)
	}
}

func TestGetStrGetBool(t *testing.T) {
	m := map[string]any{
		"name":    "demo",
		"enabled": true,
		"count":   3,
		"flag":    "true",
		"missing": nil,
	}
	if got := getStr(m, "name"); got != "demo" {
		t.Errorf("getStr(name) = %q, want demo", got)
	}
	if got := getStr(m, "count"); got != "" {
		t.Errorf("getStr on non-string = %q, want empty", got)
	}
	if got := getStr(m, "missing"); got != "" {
		t.Errorf("getStr(nil) = %q, want empty", got)
	}
	if !getBool(m, "enabled") {
		t.Error("getBool(enabled) = false, want true")
	}
	if getBool(m, "flag") || getBool(m, "count") || getBool(m, "absent") {
		t.Error("getBool must only accept bool values")
	}
}

func TestGeminiFreeModel(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"gemini-2.5-flash", true},
		{"GEMINI-2.5-FLASH-LITE", true},
		{"gemini-2.5-pro", false},
		{"gemini-2.0-flash-image", false},
		{"gemini-2.5-flash-live", false},
		{"gemini-2.5-flash-tts", false},
		{"gemini-embedding-001", false},
		{"text-embedding-004", false},
	} {
		if got := geminiFreeModel(tc.in); got != tc.want {
			t.Errorf("geminiFreeModel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseSuggestJSON(t *testing.T) {
	// 模型输出常带 ```json 围栏与前后杂文，这里必须提取出中间的对象。
	fenced := "Sure, here is the config:\n```json\n{\"command\": \"npx\", \"transport\": \"http\", \"args\": [\"-y\", \"demo\"]}\n```\nHope it helps."
	out, err := parseSuggestJSON(fenced)
	if err != nil {
		t.Fatalf("parseSuggestJSON(fenced) error: %v", err)
	}
	if out["command"] != "npx" || out["transport"] != "http" {
		t.Fatalf("fenced fields not preserved: %+v", out)
	}
	if args, ok := out["args"].([]any); !ok || len(args) != 2 {
		t.Fatalf("args not preserved: %#v", out["args"])
	}

	// 归一化：缺字段/错类型都要补成安全默认值，不能让 nil 流到调用方。
	bare, err := parseSuggestJSON("prefix {\"command\": 42, \"transport\": \"grpc\", \"url\": 7} suffix")
	if err != nil {
		t.Fatalf("parseSuggestJSON(bare) error: %v", err)
	}
	if bare["command"] != "" || bare["transport"] != "stdio" || bare["url"] != "" || bare["notes"] != "" {
		t.Fatalf("normalization failed: %+v", bare)
	}
	if a, ok := bare["args"].([]any); !ok || len(a) != 0 {
		t.Fatalf("args should default to an empty slice: %#v", bare["args"])
	}
	if _, err := parseSuggestJSON("no json here at all"); err == nil {
		t.Fatal("parseSuggestJSON must reject non-JSON input")
	}
}

func TestExtractMcpServer(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"fs__read_file", "fs", true},
		{"fs:read", "fs", true},
		{"fs/read", "fs", true},
		{"a__b:read", "a", true}, // 双下划线优先
		{"__tool", "", false},    // 分隔符在开头
		{"fs__", "", false},      // 工具名为空
		{"bad server__tool", "", false},
		{"fs/read__x", "fs", true}, // __ 的 server 非法时回退到 /
	} {
		got, ok := extractMcpServer(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("extractMcpServer(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestKnownToolName(t *testing.T) {
	known := map[string]bool{"mcp_fs_read_file": true, "web_search": true}
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"mcp_fs_read_file", true},
		{"web_search", true},
		{"fs__read_file", true}, // MCP 命名风格映射到网关能力名
		{"fs__write_file", false},
		{"other__x", false},
		{"__read_file", false},
	} {
		if got := knownToolName(tc.in, known); got != tc.want {
			t.Errorf("knownToolName(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseOpenRouterCredits(t *testing.T) {
	m := parseOpenRouterCredits([]byte(`{"data":{"total_credits":10.5,"total_usage":3.25}}`))
	if m == nil || m["kind"] != "openrouter" || m["total_credits"] != 10.5 || m["total_usage"] != 3.25 {
		t.Fatalf("credits not parsed: %+v", m)
	}
	// && 优先于 ||：只有两个计数同时为 0 才算「无数据」。
	if m = parseOpenRouterCredits([]byte(`{"data":{"total_credits":0,"total_usage":3.25}}`)); m == nil {
		t.Fatal("usage-only payload must still be accepted")
	}
	if m = parseOpenRouterCredits([]byte(`{"data":{"total_credits":0,"total_usage":0}}`)); m != nil {
		t.Fatalf("all-zero payload should be rejected: %+v", m)
	}
	if m = parseOpenRouterCredits([]byte(`not json`)); m != nil {
		t.Fatalf("invalid JSON should be rejected: %+v", m)
	}
}

func TestParseOpenAISubscription(t *testing.T) {
	m := parseOpenAISubscription([]byte(`{"hard_limit_usd":120}`))
	if m == nil || m["hard_limit_usd"] != 120.0 || m["kind"] != "openai" {
		t.Fatalf("subscription not parsed: %+v", m)
	}
	for _, body := range []string{
		`{"hard_limit_usd":0}`,
		`{"hard_limit_usd":-1}`,
		`{"hard_limit_usd":100000000}`, // 聚合平台 1e8 占位值
		`{"hard_limit_usd":10000000}`,  // 边界：>= 1e7 视为占位
		`not json`,
	} {
		if got := parseOpenAISubscription([]byte(body)); got != nil {
			t.Errorf("parseOpenAISubscription(%s) = %+v, want nil", body, got)
		}
	}
}

func TestParseOpenAIUsage(t *testing.T) {
	m := parseOpenAIUsage([]byte(`{"total_usage":12345}`))
	if m == nil || m["kind"] != "openai" {
		t.Fatalf("usage not parsed: %+v", m)
	}
	if got, ok := m["total_usage_usd"].(float64); !ok || math.Abs(got-123.45) > 1e-9 {
		t.Errorf("total_usage_usd = %v, want 123.45 (cents -> USD)", m["total_usage_usd"])
	}
	for _, body := range []string{`{"total_usage":0}`, `not json`} {
		if got := parseOpenAIUsage([]byte(body)); got != nil {
			t.Errorf("parseOpenAIUsage(%s) = %+v, want nil", body, got)
		}
	}
}
