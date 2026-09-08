package proxy

import (
	"encoding/json"
	"testing"
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
		if got := upstreamURL(c.base, c.path); got != c.want {
			t.Fatalf("upstreamURL(%q, %q) = %q, want %q", c.base, c.path, got, c.want)
		}
	}
}
