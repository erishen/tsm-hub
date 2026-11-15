package adapter

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/erishen/tsm-hub/internal/store"
)

func TestCopyHeadersFiltersInternalAndSensitive(t *testing.T) {
	src := http.Header{}
	src.Set("Authorization", "Bearer user-key")
	src.Set("X-Admin-Token", "admin-secret")
	src.Set("X-Session-Token", "session-secret")
	src.Set("Cookie", "session=abc")
	src.Set("X-Llm-Router-Agent", "browser")
	src.Set("X-Request-ID", "trace-123")
	src.Set("User-Agent", "curl/8")
	src.Set("X-Custom", "keep-me")

	dst := http.Header{}
	CopyHeaders(dst, src)

	for _, h := range []string{"Authorization", "X-Admin-Token", "X-Session-Token", "Cookie", "X-Llm-Router-Agent"} {
		if dst.Get(h) != "" {
			t.Errorf("header %s should be filtered, got %q", h, dst.Get(h))
		}
	}
	for _, h := range []string{"X-Request-ID", "User-Agent", "X-Custom"} {
		if dst.Get(h) == "" {
			t.Errorf("header %s should be copied", h)
		}
	}
}

func TestUpstreamURL(t *testing.T) {
	cases := []struct {
		name string
		base string
		path string
		want string
	}{
		{"v1 base dedups client v1", "https://api.deepseek.com/v1", "/v1/chat/completions", "https://api.deepseek.com/v1/chat/completions"},
		{"v4 base (bigmodel) dedups client v1", "https://open.bigmodel.cn/api/paas/v4/", "/v1/chat/completions", "https://open.bigmodel.cn/api/paas/v4/chat/completions"},
		{"v1beta segment counts as version", "https://example.com/api/v1beta", "/v1/chat/completions", "https://example.com/api/v1beta/chat/completions"},
		{"non-version last segment keeps v1", "https://router.example.com/api", "/v1/chat/completions", "https://router.example.com/api/v1/chat/completions"},
		{"vendor-named segment keeps v1", "https://x.example.com/openai", "/v1/chat/completions", "https://x.example.com/openai/v1/chat/completions"},
		{"non-chat path untouched", "https://api.deepseek.com/v1", "/v1/models", "https://api.deepseek.com/v1/models"},
		{"trailing slash trimmed", "https://api.siliconflow.cn/v1/", "/v1/chat/completions", "https://api.siliconflow.cn/v1/chat/completions"},
	}
	for _, tc := range cases {
		if got := upstreamURL(tc.base, tc.path); got != tc.want {
			t.Errorf("%s: upstreamURL(%q,%q) = %q, want %q", tc.name, tc.base, tc.path, got, tc.want)
		}
	}
}

// === 以下为本轮补充的适配器契约测试 ===
// 目标：把「25 个平台 = 13 个协议适配器 + 12 个 OpenAI 兼容平台」的
// 注册表 / URL / 认证 / 请求响应转换 / 流事件 / AWS 签名全部纳入回归保护，
// 全部使用表驱动 + 包内类型，不发起任何真实网络请求。

const (
	contractBase = "https://upstream.example.com/v1"
	contractPath = "/v1/chat/completions"
	contractKey  = "sk-contract-test"
)

func contractProvider(proto string) store.Provider {
	return store.Provider{
		ID: "p", Name: "p", BaseURL: contractBase, APIKey: contractKey,
		Protocol: proto, Enabled: true,
	}
}

// openAICompatProtocols 是纯透传的适配器（含嵌入 OpenAIAdapter 的兼容平台）。
var openAICompatProtocols = []string{"openai", "mistral", "together", "fireworks", "groq", "azure", "sagemaker"}

func TestAdapterRegistryURLsAndAuth(t *testing.T) {
	bearer := "Bearer " + contractKey
	cases := []struct {
		proto       string
		wantURL     string
		wantAuthKey string
		wantAuthVal string
	}{
		{"openai", contractBase + "/chat/completions", "Authorization", bearer},
		{"anthropic", contractBase + "/messages", "x-api-key", contractKey},
		{"azure", contractBase + "/chat/completions?api-version=2024-02-15-preview", "api-key", contractKey},
		{"gemini", contractBase + "/v1beta/models/gemini-pro:generateContent", "x-goog-api-key", contractKey},
		{"bedrock", contractBase + "/model/PLACEHOLDER/invoke", "", ""},
		{"sagemaker", contractBase + "/invocations", "", ""},
		{"cohere", contractBase + "/chat", "Authorization", bearer},
		{"mistral", contractBase + "/chat/completions", "Authorization", bearer},
		{"huggingface", contractBase + "/models/PLACEHOLDER", "Authorization", bearer},
		{"replicate", contractBase + "/v1/predictions?wait=true", "Authorization", bearer},
		{"together", contractBase + "/chat/completions", "Authorization", bearer},
		{"fireworks", contractBase + "/chat/completions", "Authorization", bearer},
		{"groq", contractBase + "/chat/completions", "Authorization", bearer},
	}
	if len(cases) != len(adapters) {
		t.Fatalf("registry has %d adapters, test covers %d (keep them in sync)", len(adapters), len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.proto, func(t *testing.T) {
			p := contractProvider(tc.proto)
			a := GetAdapter(p)
			if a.Name() != tc.proto {
				t.Errorf("Name() = %q, want %q", a.Name(), tc.proto)
			}
			if got := a.UpstreamURL(p, contractPath); got != tc.wantURL {
				t.Errorf("UpstreamURL = %q, want %q", got, tc.wantURL)
			}
			k, v := a.AuthHeader(p)
			if k != tc.wantAuthKey || v != tc.wantAuthVal {
				t.Errorf("AuthHeader = (%q,%q), want (%q,%q)", k, v, tc.wantAuthKey, tc.wantAuthVal)
			}
			if a.StreamDoneEvent() == "" {
				t.Error("StreamDoneEvent() must not be empty")
			}
		})
	}

	// 未设置 / 未知协议一律回落到 OpenAI 纯透传，绝不返回 nil 让调用方 panic。
	for _, proto := range []string{"", "some-future-protocol"} {
		a := GetAdapter(contractProvider(proto))
		if a == nil || a.Name() != "openai" {
			t.Errorf("protocol %q should fall back to openai, got %+v", proto, a)
		}
	}
}

func TestAdapterRegistryOverride(t *testing.T) {
	const name = "test-only-protocol"
	defer delete(adapters, name)
	RegisterAdapter(&fakeAdapter{name: name})
	if got := GetAdapter(contractProvider(name)).Name(); got != name {
		t.Fatalf("registered adapter not found, got %q", got)
	}
}

type fakeAdapter struct {
	OpenAIAdapter
	name string
}

func (f *fakeAdapter) Name() string { return f.name }

// sampleOpenAIRequest 覆盖 system / user / assistant / tool 四种角色、
// temperature / max_tokens / tools / 未知字段（stream_options）。
const sampleOpenAIRequest = `{"model":"alias","messages":[` +
	`{"role":"system","content":"be concise"},` +
	`{"role":"user","content":"hi"},` +
	`{"role":"assistant","content":"yo"},` +
	`{"role":"tool","content":"tool result"}],` +
	`"stream":true,"temperature":0.7,"max_tokens":100,` +
	`"stream_options":{"include_usage":true},` +
	`"tools":[{"type":"function","function":{"name":"get_time","description":"clock","parameters":{"type":"object"}}}]}`

func assertContains(t *testing.T, out []byte, keys ...string) {
	t.Helper()
	s := string(out)
	for _, k := range keys {
		if !strings.Contains(s, k) {
			t.Errorf("converted body missing %q: %s", k, s)
		}
	}
}

func TestAdapterConvertRequestContracts(t *testing.T) {
	body := []byte(sampleOpenAIRequest)
	passthrough := map[string]bool{}
	for _, p := range openAICompatProtocols {
		passthrough[p] = true
	}

	cases := []struct {
		proto       string
		wantKeys    []string
		wantHeader  string
		passthrough bool
	}{
		{proto: "openai", passthrough: true},
		{proto: "mistral", passthrough: true},
		{proto: "together", passthrough: true},
		{proto: "fireworks", passthrough: true},
		{proto: "groq", passthrough: true},
		{proto: "azure", passthrough: true},
		{proto: "sagemaker", passthrough: true},
		{proto: "anthropic", wantKeys: []string{`"model":"real-model"`, `"system":"be concise"`, `"max_tokens":4096`, `"messages"`, `"get_time"`}, wantHeader: "anthropic-version"},
		{proto: "bedrock", wantKeys: []string{`"system":"be concise"`, `"max_tokens":4096`}, wantHeader: "X-Bedrock-Model"},
		{proto: "gemini", wantKeys: []string{`"contents"`, `"systemInstruction"`, `"role":"model"`, `"generationConfig"`, `"functionDeclarations"`}},
		{proto: "cohere", wantKeys: []string{`"message"`, `"chat_history"`, `"model":"real-model"`, `"max_tokens":100`}},
		// JSON 字符串里的换行被转义为 \n，因此断言前缀/角色文本而非整段带引号的字符串。
		{proto: "huggingface", wantKeys: []string{`"inputs"`, `System: be concise`, `User: hi`, `tool: tool result`, `\n\nAssistant: `, `"max_new_tokens":100`, `"temperature":0.7`}, wantHeader: "X-HF-Model"},
		{proto: "replicate", wantKeys: []string{`"model":"real-model"`, `"prompt"`, `System: be concise`, `\n\nAssistant: `, `"max_tokens":100`, `"temperature":0.7`}},
	}
	for _, tc := range cases {
		t.Run(tc.proto, func(t *testing.T) {
			a := GetAdapter(contractProvider(tc.proto))
			out, headers, err := a.ConvertRequest(body, "real-model")
			if err != nil {
				t.Fatalf("ConvertRequest: %v", err)
			}
			if tc.passthrough {
				if string(out) != string(body) {
					t.Errorf("openai-compatible adapter must pass body through unchanged (stream_options lost?)")
				}
			} else {
				assertContains(t, out, tc.wantKeys...)
			}
			if tc.wantHeader != "" {
				found := false
				for k := range headers {
					if strings.EqualFold(k, tc.wantHeader) {
						found = true
					}
				}
				if !found {
					t.Errorf("missing extra header %q, got %v", tc.wantHeader, headers)
				}
			}
			if _, ok := passthrough[tc.proto]; ok != tc.passthrough {
				t.Fatalf("passthrough classification out of sync for %q", tc.proto)
			}
		})
	}

	// 非法 JSON：纯透传协议必须原样返回；转换类只要求不 panic（各协议容错策略不同）。
	bad := []byte(`{not json`)
	for _, tc := range cases {
		if !tc.passthrough {
			_, _, _ = GetAdapter(contractProvider(tc.proto)).ConvertRequest(bad, "m")
			continue
		}
		out, _, err := GetAdapter(contractProvider(tc.proto)).ConvertRequest(bad, "m")
		if err != nil || string(out) != string(bad) {
			t.Errorf("%s: passthrough of invalid JSON = (%q, %v)", tc.proto, out, err)
		}
	}
}

const sampleOpenAIResponse = `{"id":"x","object":"chat.completion","model":"m",` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],` +
	`"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8}}`

func TestAdapterConvertResponseContracts(t *testing.T) {
	cases := []struct {
		proto string
		in    string
	}{
		{"openai", sampleOpenAIResponse},
		{"azure", sampleOpenAIResponse},
		{"sagemaker", sampleOpenAIResponse},
		{"mistral", sampleOpenAIResponse},
		{"together", sampleOpenAIResponse},
		{"fireworks", sampleOpenAIResponse},
		{"groq", sampleOpenAIResponse},
		{"anthropic", `{"id":"msg_1","model":"claude","content":[{"type":"text","text":"hello"}],"stop_reason":"max_tokens","usage":{"input_tokens":3,"output_tokens":5}}`},
		{"bedrock", `{"id":"msg_1","model":"claude","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":5}}`},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":5,"totalTokenCount":8}}`},
		{"cohere", `{"text":"hello","generation_id":"g1","finish_reason":"MAX_TOKENS","meta":{"tokens":{"input_tokens":3,"output_tokens":5}}}`},
		{"huggingface", `[{"generated_text":"hello"}]`},
		{"replicate", `{"id":"r1","status":"succeeded","output":["he","llo"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.proto, func(t *testing.T) {
			a := GetAdapter(contractProvider(tc.proto))
			out, err := a.ConvertResponse([]byte(tc.in))
			if err != nil {
				t.Fatalf("ConvertResponse: %v", err)
			}
			var resp openAIResponse
			if err := json.Unmarshal(out, &resp); err != nil {
				t.Fatalf("output is not OpenAI JSON: %v (%s)", err, out)
			}
			if len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "hello" {
				t.Fatalf("content = %+v, want hello; body=%s", resp.Choices, out)
			}
			if resp.Object != "chat.completion" {
				t.Errorf("object = %q", resp.Object)
			}
		})
	}

	// finish_reason 映射：各协议的长度终止原因都要归一到 OpenAI 的 "length"。
	lengthCases := []struct{ proto, in string }{
		{"anthropic", `{"content":[{"type":"text","text":"hello"}],"stop_reason":"max_tokens"}`},
		{"gemini", `{"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"MAX_TOKENS"}]}`},
		{"cohere", `{"text":"hello","finish_reason":"MAX_TOKENS"}`},
	}
	for _, tc := range lengthCases {
		out, err := GetAdapter(contractProvider(tc.proto)).ConvertResponse([]byte(tc.in))
		if err != nil {
			t.Fatalf("%s: %v", tc.proto, err)
		}
		var resp openAIResponse
		_ = json.Unmarshal(out, &resp)
		if len(resp.Choices) != 1 || resp.Choices[0].FinishReason != "length" {
			t.Errorf("%s finish_reason = %+v, want length", tc.proto, resp.Choices)
		}
	}
}

func TestAdapterConvertStreamEventContracts(t *testing.T) {
	// [DONE] 是 OpenAI 客户端的终止信号，任何适配器都不能吞掉或标记 skip。
	for proto := range adapters {
		out, skip, err := GetAdapter(contractProvider(proto)).ConvertStreamEvent([]byte("[DONE]"))
		if err != nil || skip || string(out) != "[DONE]" {
			t.Errorf("%s: [DONE] = (%q, skip=%v, %v)", proto, out, skip, err)
		}
	}

	cases := []struct {
		name     string
		proto    string
		in       string
		wantText string
		wantDone bool
		wantSkip bool
	}{
		{"anthropic delta", "anthropic", `{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hi"}}`, "Hi", false, false},
		{"anthropic start skipped", "anthropic", `{"type":"message_start"}`, "", false, true},
		{"anthropic stop", "anthropic", `{"type":"message_stop"}`, "", true, false},
		{"anthropic delta finish", "anthropic", `{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`, "", false, false},
		{"gemini delta", "gemini", `{"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}`, "Hi", false, false},
		{"gemini empty skipped", "gemini", `{"candidates":[]}`, "", false, true},
		{"cohere delta", "cohere", `{"event_type":"text-generation","text":"Hi"}`, "Hi", false, false},
		{"cohere finished", "cohere", `{"is_finished":true}`, "", true, false},
		{"cohere other skipped", "cohere", `{"event_type":"stream-start"}`, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := GetAdapter(contractProvider(tc.proto))
			out, skip, err := a.ConvertStreamEvent([]byte(tc.in))
			if err != nil {
				t.Fatalf("ConvertStreamEvent: %v", err)
			}
			if skip != tc.wantSkip {
				t.Errorf("skip = %v, want %v (out=%s)", skip, tc.wantSkip, out)
			}
			if tc.wantDone {
				if string(out) != "[DONE]" {
					t.Errorf("out = %q, want [DONE]", out)
				}
				return
			}
			if tc.wantSkip {
				return
			}
			var chunk openAIStreamChunk
			if err := json.Unmarshal(out, &chunk); err != nil {
				t.Fatalf("not an OpenAI chunk: %v (%s)", err, out)
			}
			if len(chunk.Choices) != 1 {
				t.Fatalf("choices = %+v", chunk.Choices)
			}
			if chunk.Choices[0].Delta.Content != tc.wantText {
				t.Errorf("delta = %q, want %q", chunk.Choices[0].Delta.Content, tc.wantText)
			}
			if strings.Contains(tc.name, "finish") && chunk.Choices[0].FinishReason != "length" {
				t.Errorf("finish_reason = %q, want length", chunk.Choices[0].FinishReason)
			}
		})
	}

	// 无法解析的帧只能透传或跳过，不能 panic / 返回假错误。
	for proto := range adapters {
		_, _, err := GetAdapter(contractProvider(proto)).ConvertStreamEvent([]byte(`{broken`))
		if err != nil {
			t.Errorf("%s: malformed frame returned error: %v", proto, err)
		}
	}
}

func TestAWSSigV4Helpers(t *testing.T) {
	if got := sha256Hex([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("sha256Hex(abc) = %s", got)
	}
	key, data := []byte("secret"), []byte("payload")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	if got := hex.EncodeToString(hmacSHA256(key, data)); got != hex.EncodeToString(mac.Sum(nil)) {
		t.Errorf("hmacSHA256 mismatch")
	}
	if got := len(awsSignatureKey("s", "20261115", "us-east-1", "bedrock")); got != sha256.Size {
		t.Errorf("signing key length = %d, want %d", got, sha256.Size)
	}

	if got := awsRegion("https://bedrock-runtime.eu-west-1.amazonaws.com"); got != "eu-west-1" {
		t.Errorf("awsRegion = %q, want eu-west-1", got)
	}
	if got := awsRegion("https://runtime.sagemaker.eu-west-1.amazonaws.com/endpoints/x"); got != "us-east-1" {
		t.Errorf("awsRegion sagemaker fallback = %q, want us-east-1", got)
	}
	if got := awsRegion("://bad url"); got != "us-east-1" {
		t.Errorf("awsRegion malformed = %q, want us-east-1", got)
	}

	ak, sk, err := awsCredentials(store.Provider{APIKey: "AKIDEXAMPLE:secret"})
	if err != nil || ak != "AKIDEXAMPLE" || sk != "secret" {
		t.Errorf("awsCredentials = (%q,%q,%v)", ak, sk, err)
	}
	if _, _, err := awsCredentials(store.Provider{APIKey: "no-colon"}); err == nil {
		t.Error("awsCredentials should reject malformed credentials")
	}

	u, _ := url.Parse("https://bedrock-runtime.us-east-1.amazonaws.com/model/m/invoke?b=2&a=1")
	if got := canonicalURI(u); got != "/model/m/invoke" {
		t.Errorf("canonicalURI = %q", got)
	}
	if got := canonicalURI(&url.URL{}); got != "/" {
		t.Errorf("canonicalURI empty = %q, want /", got)
	}
	if got := canonicalQuery(u); !strings.HasPrefix(got, "a=1") || !strings.HasSuffix(got, "b=2") {
		t.Errorf("canonicalQuery = %q, want sorted a before b", got)
	}
	if got := canonicalQuery(&url.URL{}); got != "" {
		t.Errorf("canonicalQuery empty = %q", got)
	}

	req, _ := http.NewRequest(http.MethodPost, u.String(), strings.NewReader("body"))
	req.Header.Set("Z-Custom", " last ")
	canonical, signed := canonicalHeaders(req)
	if !strings.Contains(canonical, "host:bedrock-runtime.us-east-1.amazonaws.com") ||
		!strings.Contains(canonical, "z-custom:last") ||
		!strings.HasSuffix(canonical, "\n") {
		t.Errorf("canonicalHeaders = %q", canonical)
	}
	if !strings.Contains(signed, "host") || !strings.Contains(signed, "z-custom") {
		t.Errorf("signedHeaders = %q", signed)
	}

	req2, _ := http.NewRequest(http.MethodPost, u.String(), strings.NewReader("body"))
	if err := signAWSRequest(req2, []byte("body"), "AKIDEXAMPLE", "secret", "us-east-1", "bedrock"); err != nil {
		t.Fatalf("signAWSRequest: %v", err)
	}
	auth := req2.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") ||
		!strings.Contains(auth, "/us-east-1/bedrock/aws4_request") ||
		!strings.Contains(auth, "SignedHeaders=") || !strings.Contains(auth, "Signature=") {
		t.Errorf("Authorization = %q", auth)
	}
	if req2.Header.Get("X-Amz-Date") == "" || req2.Header.Get("Host") == "" {
		t.Errorf("missing X-Amz-Date / Host: %v", req2.Header)
	}

	// readRequestBody 必须把 body 放回去，否则签名后真实请求会丢 payload。
	req3, _ := http.NewRequest(http.MethodPost, u.String(), strings.NewReader("payload"))
	b, err := readRequestBody(req3)
	if err != nil || string(b) != "payload" {
		t.Fatalf("readRequestBody = (%q, %v)", b, err)
	}
	again, _ := io.ReadAll(req3.Body)
	if string(again) != "payload" {
		t.Errorf("body not restored, got %q", again)
	}
}

func TestListUpstreamPlatforms(t *testing.T) {
	platforms := ListUpstreamPlatforms()
	if len(platforms) != 25 {
		t.Fatalf("platforms = %d, want 25", len(platforms))
	}
	seen := map[string]bool{}
	for _, p := range platforms {
		if seen[p.ID] {
			t.Errorf("duplicate platform ID %q", p.ID)
		}
		seen[p.ID] = true
		for field, value := range map[string]string{
			"id": p.ID, "name": p.Name, "protocol": p.Protocol,
			"base_url": p.BaseURL, "docs_url": p.DocsURL,
		} {
			if value == "" {
				t.Errorf("platform %q has empty %s", p.ID, field)
			}
		}
		if len(p.Models) == 0 {
			t.Errorf("platform %q has no models", p.ID)
		}
	}
}

func TestVersionedBaseEdges(t *testing.T) {
	cases := []struct {
		base string
		want bool
	}{
		{"https://x/v1", true},
		{"https://x/api/paas/v4", true},
		{"https://x/api/v1beta", true},
		{"https://x/api/v2free", true},
		{"https://x/openai", false},
		{"https://x/api/vendor", false},
		{"https://x/v", false},
		{"https://x/", false},
		{"v1", true},
	}
	for _, tc := range cases {
		if got := versionedBase(tc.base); got != tc.want {
			t.Errorf("versionedBase(%q) = %v, want %v", tc.base, got, tc.want)
		}
	}
	if got := UpstreamURL("https://x/v1/", "/v1/models"); got != "https://x/v1/models" {
		t.Errorf("exported UpstreamURL = %q", got)
	}
}

func TestRandomHex(t *testing.T) {
	const alphabet = "0123456789abcdef"
	for _, n := range []int{0, 1, 8, 32} {
		got := randomHex(n)
		if len(got) != n {
			t.Errorf("randomHex(%d) length = %d", n, len(got))
		}
		for _, c := range got {
			if !strings.ContainsRune(alphabet, c) {
				t.Errorf("randomHex(%d) has non-hex char %q", n, c)
			}
		}
	}
}
