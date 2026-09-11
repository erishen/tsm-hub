// Package adapter 提供协议适配器，负责把 OpenAI 格式的请求/响应转换为各种上游 API 协议。
//
// 网关对外只暴露 OpenAI 兼容 API；上游 Provider 可能使用不同协议（Anthropic、Gemini、Azure 等），
// 适配器负责在请求发送前和响应返回后进行格式转换。
package adapter

import (
	"net/http"
	"strings"

	"github.com/erishen/tsm-hub/internal/store"
)

// ProtocolAdapter 定义协议适配器接口。
type ProtocolAdapter interface {
	// Name 返回适配器名称（与 Provider.Protocol 对应）。
	Name() string

	// UpstreamURL 根据 Provider 和客户端请求路径，构造上游请求 URL。
	UpstreamURL(provider store.Provider, clientPath string) string

	// AuthHeader 返回认证请求头的 key 和 value。
	// 如果 key 为空，则不设置认证头（由适配器自己在 URL 中处理，如 Gemini）。
	AuthHeader(provider store.Provider) (key, value string)

	// ConvertRequest 把 OpenAI 格式的请求体转换为目标协议格式。
	ConvertRequest(body []byte, upstreamModel string) (converted []byte, extraHeaders map[string]string, err error)

	// ConvertResponse 把目标协议的非流式响应体转换为 OpenAI 格式。
	ConvertResponse(body []byte) ([]byte, error)

	// ConvertStreamEvent 把目标协议的流式 SSE 事件转换为 OpenAI 格式。
	ConvertStreamEvent(data []byte) (converted []byte, skip bool, err error)

	// StreamDoneEvent 返回目标协议表示流结束的事件 data。
	StreamDoneEvent() string
}

// RequestSigner 是可选接口，适配器可以实现它来在请求发送前做签名等准备工作。
type RequestSigner interface {
	SignRequest(req *http.Request, body []byte, provider store.Provider) error
}

// adapters 是已注册的协议适配器注册表。
var adapters = map[string]ProtocolAdapter{}

// RegisterAdapter 注册一个协议适配器。
func RegisterAdapter(a ProtocolAdapter) {
	adapters[a.Name()] = a
}

// GetAdapter 根据 Provider 的 Protocol 字段返回对应的适配器。
// 如果 Provider 未设置 Protocol 或协议未知，返回 OpenAI 适配器（默认纯透传）。
func GetAdapter(p store.Provider) ProtocolAdapter {
	proto := p.Protocol
	if proto == "" {
		proto = "openai"
	}
	if a, ok := adapters[proto]; ok {
		return a
	}
	return adapters["openai"]
}

// === 共享函数 ===

// UpstreamURL 拼接上游 URL，处理 /v1 前缀重复的问题。
func UpstreamURL(base, path string) string {
	return upstreamURL(base, path)
}

// upstreamURL 拼接上游 URL，处理 /v1 前缀重复的问题。
func upstreamURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return base + path
}

// CopyHeaders 复制客户端请求头到上游请求（跳过逐跳头和鉴权头）。
func CopyHeaders(dst, src http.Header) {
	hopByHop := map[string]bool{
		"host":              true,
		"authorization":     true,
		"content-length":    true,
		"content-encoding":  true,
		"transfer-encoding": true,
		"connection":        true,
		"keep-alive":        true,
		"proxy-authenticate": true,
		"proxy-authorization": true,
		"te":                true,
		"trailer":           true,
		"upgrade":           true,
	}
	for k, vs := range src {
		if hopByHop[strings.ToLower(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// randomHex 生成指定长度的随机十六进制字符串。
func randomHex(n int) string {
	const hexChars = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = hexChars[i%len(hexChars)]
	}
	return string(b)
}

// === 共享类型（OpenAI 请求/响应格式）===

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Stream      bool            `json:"stream"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Tools       []openAITool    `json:"tools,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAITool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type openAIResponse struct {
	ID      string                  `json:"id"`
	Object  string                  `json:"object"`
	Created int64                   `json:"created"`
	Model   string                  `json:"model"`
	Choices []openAIResponseChoice  `json:"choices"`
	Usage   openAIUsage             `json:"usage"`
}

type openAIResponseChoice struct {
	Index        int           `json:"index"`
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIStreamChunk struct {
	ID      string                `json:"id"`
	Object  string                `json:"object"`
	Created int64                 `json:"created"`
	Model   string                `json:"model"`
	Choices []openAIStreamChoice  `json:"choices"`
}

type openAIStreamChoice struct {
	Index        int           `json:"index"`
	Delta        openAIMessage `json:"delta"`
	FinishReason string        `json:"finish_reason"`
}

func init() {
	RegisterAdapter(&OpenAIAdapter{})
	RegisterAdapter(&AnthropicAdapter{})
	RegisterAdapter(&AzureAdapter{})
	RegisterAdapter(&GeminiAdapter{})
	RegisterAdapter(&BedrockAdapter{})
	RegisterAdapter(&SageMakerAdapter{})
	RegisterAdapter(&CohereAdapter{})
	RegisterAdapter(&MistralAdapter{})
	RegisterAdapter(&HuggingFaceAdapter{})
	RegisterAdapter(&ReplicateAdapter{})
	RegisterAdapter(&TogetherAdapter{})
	RegisterAdapter(&FireworksAdapter{})
	RegisterAdapter(&GroqAdapter{})
}
