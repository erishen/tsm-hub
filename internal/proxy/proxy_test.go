package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/erishen/tsm-hub/internal/proxy/adapter"
	"github.com/erishen/tsm-hub/internal/router"
)

func TestRewriteBodySetsModel(t *testing.T) {
	in := []byte(`{"model":"smart","messages":[{"role":"user","content":"hi"}],"temperature":0.2}`)
	out, err := rewriteBody(in, "gpt-4o", false)
	if err != nil {
		t.Fatalf("rewriteBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got["model"] != "gpt-4o" {
		t.Fatalf("model = %v, want gpt-4o", got["model"])
	}
	if got["temperature"] != 0.2 {
		t.Fatalf("temperature lost: %v", got["temperature"])
	}
	if _, has := got["stream_options"]; has {
		t.Fatalf("stream_options should not be injected for non-stream request")
	}
}

func TestRewriteBodyInjectsStreamOptions(t *testing.T) {
	in := []byte(`{"model":"smart","stream":true}`)
	out, err := rewriteBody(in, "gpt-4o-mini", true)
	if err != nil {
		t.Fatalf("rewriteBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	so, ok := got["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options missing: %s", out)
	}
	if so["include_usage"] != true {
		t.Fatalf("include_usage = %v, want true", so["include_usage"])
	}
}

func TestRewriteBodyKeepsExistingStreamOptions(t *testing.T) {
	in := []byte(`{"model":"m","stream":true,"stream_options":{"include_usage":false}}`)
	out, _ := rewriteBody(in, "m", true)
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	so := got["stream_options"].(map[string]any)
	if so["include_usage"] != false {
		t.Fatalf("existing stream_options must be preserved, got %v", so)
	}
}

func TestRewriteBodyRejectsInvalidJSON(t *testing.T) {
	if _, err := rewriteBody([]byte(`{"model":`), "m", false); err == nil {
		t.Fatal("expected error for invalid json body")
	}
	if _, err := rewriteBody([]byte(`{"model":"m"}{"x":1}`), "m", false); err == nil {
		t.Fatal("expected error for trailing garbage after json")
	}
}

func TestRewriteBodyAdaptsKimiTemperature(t *testing.T) {
	// kimi-k2.6 只接受 temperature=1，其他值应被钳到 1
	in := []byte(`{"model":"smart","temperature":0.7}`)
	out, err := rewriteBody(in, "kimi-k2.6", false)
	if err != nil {
		t.Fatalf("rewriteBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got["temperature"] != float64(1) {
		t.Fatalf("kimi temperature = %v, want 1", got["temperature"])
	}
	// 已经是 1 的不动
	in2 := []byte(`{"model":"smart","temperature":1}`)
	out2, _ := rewriteBody(in2, "kimi-k2.6", false)
	_ = json.Unmarshal(out2, &got)
	if got["temperature"] != float64(1) {
		t.Fatalf("temperature 1 must be preserved, got %v", got["temperature"])
	}
	// 非 kimi 模型不钳制
	in3 := []byte(`{"model":"smart","temperature":0.7}`)
	out3, _ := rewriteBody(in3, "gpt-4o", false)
	_ = json.Unmarshal(out3, &got)
	if got["temperature"] != 0.7 {
		t.Fatalf("non-kimi temperature = %v, want 0.7", got["temperature"])
	}
	// 未传 temperature 时不需要注入
	in4 := []byte(`{"model":"smart"}`)
	out4, _ := rewriteBody(in4, "kimi-k2.6", false)
	var got4 map[string]any
	_ = json.Unmarshal(out4, &got4)
	if _, has := got4["temperature"]; has {
		t.Fatalf("kimi without temperature must not get one injected")
	}
}

// alibailian 托管的 kimi-k3 完全不接受 temperature 参数（传任何值 400
// "Parameter 'temperature'=0.7 is not supported for kimi-k3 model"），应整个删掉。
func TestRewriteBodyStripsKimiK3Temperature(t *testing.T) {
	in := []byte(`{"model":"smart","temperature":0.7,"max_tokens":100}`)
	out, err := rewriteBody(in, "kimi-k3", false)
	if err != nil {
		t.Fatalf("rewriteBody: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if _, has := got["temperature"]; has {
		t.Fatalf("kimi-k3 temperature must be stripped, got %v", got["temperature"])
	}
	// 其他字段不受影响
	if got["max_tokens"] != float64(100) {
		t.Fatalf("max_tokens = %v, want 100", got["max_tokens"])
	}
	// 未传时也不注入
	out2, _ := rewriteBody([]byte(`{"model":"smart"}`), "kimi-k3", false)
	var got2 map[string]any
	_ = json.Unmarshal(out2, &got2)
	if _, has := got2["temperature"]; has {
		t.Fatalf("kimi-k3 without temperature must not get one injected")
	}
	// kimi-k2 老行为不变（钳到 1 而非删除）
	out3, _ := rewriteBody([]byte(`{"model":"smart","temperature":0.7}`), "kimi-k2.6", false)
	var got3 map[string]any
	_ = json.Unmarshal(out3, &got3)
	if got3["temperature"] != float64(1) {
		t.Fatalf("kimi-k2.6 temperature = %v, want clamped to 1", got3["temperature"])
	}
}

func TestParseSSEUsage(t *testing.T) {
	cases := []struct {
		line    string
		wantPT  int
		wantCT  int
		wantTot int
		wantOK  bool
	}{
		{`data: {"choices":[{"delta":{"content":"hi"}}]}`, 0, 0, 0, false},
		{`data: {"usage":null}`, 0, 0, 0, false},
		{`data: {"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`, 11, 7, 18, true},
		{`data: {"usage":{"prompt_tokens":11,"completion_tokens":7}}`, 11, 7, 18, true},
		{`data: [DONE]`, 0, 0, 0, false},
		{``, 0, 0, 0, false},
	}
	for _, c := range cases {
		pt, ct, tot, ok := parseSSEUsage(c.line)
		if ok != c.wantOK || pt != c.wantPT || ct != c.wantCT || tot != c.wantTot {
			t.Fatalf("parseSSEUsage(%q) = (%d,%d,%d,%v), want (%d,%d,%d,%v)",
				c.line, pt, ct, tot, ok, c.wantPT, c.wantCT, c.wantTot, c.wantOK)
		}
	}
}

func TestUpstreamURL(t *testing.T) {
	cases := []struct{ base, path, want string }{
		// base_url 已带 /v1：不能拼成 /v1/v1
		{"https://api.openai.com/v1", "/v1/chat/completions", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com/v1/", "/v1/chat/completions", "https://api.openai.com/v1/chat/completions"},
		// base_url 不带 /v1：直接拼
		{"https://api.openai.com", "/v1/models", "https://api.openai.com/v1/models"},
		// Ollama / 本地兼容服务
		{"http://localhost:11434/v1", "/v1/chat/completions", "http://localhost:11434/v1/chat/completions"},
		// OpenRouter 这类带子路径的
		{"https://openrouter.ai/api/v1", "/v1/chat/completions", "https://openrouter.ai/api/v1/chat/completions"},
	}
	for _, c := range cases {
		if got := adapter.UpstreamURL(c.base, c.path); got != c.want {
			t.Fatalf("upstreamURL(%q, %q) = %q, want %q", c.base, c.path, got, c.want)
		}
	}
}

func TestIsModelNotFound(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		// 真实上游响应体：裸 NOT_FOUND / Not Found（无 "model" 字样）。
		// 这是修复核心——sensenova 返回裸 NOT_FOUND、openrouter 返回裸 Not Found，
		// 旧实现因要求 "model" 关键词而漏判，导致模型从未被标记不可用、反复 404。
		{"NOT_FOUND", true},
		{"Not Found", true},
		{`{"error":{"message":"model route not found","type":"not_found_error","code":"5"}}`, true},
		{`{"error":{"message":"model is not found"}}`, true},
		{`{"error":{"message":"Model 'x' does not exist"}}`, true},
		{`{"error":{"message":"未找到模型"}}`, true},
		{`{"error":{"message":"not found"}}`, true}, // 放宽：404 下裸 not found 即视为模型不存在
		{`{"error":{"message":"insufficient permissions"}}`, false},
		{`{"error":{"message":"the model does not exist"}}`, true},
		{`{"error":{"message":"route not found"}}`, true},
		// 路径错误类 404：不应误判为模型不存在（不含 not found 关键词）。
		{"Invalid URL (GET /v1/chat/completions/tools)", false},
		{"Invalid URL (GET /v1/chat/completions/mcps)", false},
		{`{"error":"invalid api key"}`, false},
		{"", false},
	}
	for _, c := range cases {
		if got := isModelNotFound(c.body); got != c.want {
			t.Errorf("isModelNotFound(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}

func TestReportThrottleCoolsProvider(t *testing.T) {
	h := router.NewTracker(3, 60)
	h.ReportThrottle("p", "upstream 429", 5*time.Second)
	if !h.Throttled("p") {
		t.Fatalf("expected throttled")
	}
	if h.Available("p") {
		t.Fatalf("expected unavailable during throttle cooldown")
	}
	h.ReportSuccess("p", 10*time.Millisecond)
	if h.Throttled("p") {
		t.Fatalf("expected not throttled after success")
	}
	if !h.Available("p") {
		t.Fatalf("expected available after success")
	}
}

// 客户端断开/取消（context canceled）不是上游故障，不得计入健康熔断。
func TestIsClientAbort(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"canceled", context.Canceled, true},
		{"wrapped canceled", fmt.Errorf("dial: %w", context.Canceled), true},
		{"url.Error canceled", &url.Error{Op: "Post", URL: "https://x", Err: context.Canceled}, true},
		{"deadline exceeded", context.DeadlineExceeded, false},
		{"network", errors.New("connection refused"), false},
	}
	for _, c := range cases {
		if got := isClientAbort(c.err); got != c.want {
			t.Errorf("%s: isClientAbort = %v, want %v", c.name, got, c.want)
		}
	}
}
