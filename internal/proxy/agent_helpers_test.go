package proxy

import (
	"math"
	"strings"
	"testing"

	"github.com/erishen/tsm-hub/internal/store"
)

// agent 链路里这几个小工具此前 0% 覆盖：它们负责从不可信请求体提取
// 分类对象、模型名与白名单，边界写错会直接影响 fastpath 判定与权限。

func TestLastUserText(t *testing.T) {
	msgs := []chatMessage{
		{"role": "system", "content": "sys"},
		{"role": "user", "content": "first question"},
		{"role": "assistant", "content": "answer"},
		{"role": "user", "content": "last question"},
	}
	if got := lastUserText(msgs); got != "last question" {
		t.Errorf("lastUserText = %q, want last question", got)
	}
	if got := lastUserText(nil); got != "" {
		t.Errorf("lastUserText(nil) = %q, want empty", got)
	}
	// content 为多模态数组时不能 panic，也不能误取 system/assistant。
	nonText := []chatMessage{
		{"role": "user", "content": []any{map[string]any{"type": "image_url"}}},
		{"role": "assistant", "content": "not a user message"},
	}
	if got := lastUserText(nonText); got != "" {
		t.Errorf("lastUserText on non-string content = %q, want empty", got)
	}
	// 最后一条 user 无文本时应继续往前找，而不是直接返回空。
	mixed := []chatMessage{
		{"role": "user", "content": "earlier text"},
		{"role": "user", "content": 42},
	}
	if got := lastUserText(mixed); got != "earlier text" {
		t.Errorf("lastUserText should skip non-text user messages, got %q", got)
	}
}

func TestParseMessages(t *testing.T) {
	msgs, err := parseMessages([]byte(`{"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("parseMessages error: %v", err)
	}
	if len(msgs) != 1 || msgs[0]["role"] != "user" {
		t.Fatalf("parseMessages = %+v", msgs)
	}
	for _, body := range []string{
		`{"messages":[]}`,
		`{}`,
		`not json`,
	} {
		if _, err := parseMessages([]byte(body)); err == nil {
			t.Errorf("parseMessages(%s) should fail", body)
		}
	}
}

func TestUpstreamModelOf(t *testing.T) {
	for _, tc := range []struct {
		body, want string
	}{
		{`{"model":"gpt-4o","messages":[]}`, "gpt-4o"},
		{`{"messages":[]}`, ""},
		{`{"model":42}`, ""}, // 类型不符时不能 panic
		{`not json`, ""},
	} {
		if got := upstreamModelOf([]byte(tc.body)); got != tc.want {
			t.Errorf("upstreamModelOf(%s) = %q, want %q", tc.body, got, tc.want)
		}
	}
}

func TestNewID(t *testing.T) {
	id := newID("chatcmpl")
	if !strings.HasPrefix(id, "chatcmpl-") {
		t.Fatalf("newID = %q, want chatcmpl- prefix", id)
	}
	if newID("") == "" {
		t.Fatal("newID must still return a non-empty id for an empty prefix")
	}
}

func TestKeyAllowSets(t *testing.T) {
	srv, tool := keyAllowSets(store.APIKey{})
	if srv != nil || tool != nil {
		t.Fatalf("empty allowlists must stay nil (= unrestricted), got %v / %v", srv, tool)
	}
	srv, tool = keyAllowSets(store.APIKey{
		McpsAllow:  []string{"fs", "memory"},
		ToolsAllow: []string{"web_search"},
	})
	if !srv["fs"] || !srv["memory"] || srv["other"] {
		t.Errorf("server allowlist wrong: %v", srv)
	}
	if !tool["web_search"] || tool["other"] {
		t.Errorf("tool allowlist wrong: %v", tool)
	}
}

func TestCalcFunc1(t *testing.T) {
	got, err := calcFunc1("sqrt", []float64{9})
	if err != nil || math.Abs(got-3) > 1e-9 {
		t.Errorf("calcFunc1(sqrt, 9) = (%v, %v), want 3", got, err)
	}
	// 单参数函数收到多参数必须报错，不能静默取第一个。
	if _, err := calcFunc1("abs", []float64{-1, -2}); err == nil {
		t.Error("calcFunc1 must reject extra arguments")
	}
	if _, err := calcFunc1("unknown_fn", []float64{1}); err == nil {
		t.Error("calcFunc1 must reject unknown function names")
	}
}
