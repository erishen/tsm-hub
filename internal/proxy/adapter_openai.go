package proxy

import (
	"strings"

	"github.com/erishen/tsm-hub/internal/store"
)

// OpenAIAdapter 是 OpenAI 兼容协议的适配器，也是默认适配器。
// 它不做任何转换，直接透传请求和响应（只做 URL 拼接和模型名替换）。
type OpenAIAdapter struct{}

func (a *OpenAIAdapter) Name() string { return "openai" }

func (a *OpenAIAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	return upstreamURL(provider.BaseURL, clientPath)
}

func (a *OpenAIAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return "Authorization", "Bearer " + provider.ResolvedAPIKey()
}

func (a *OpenAIAdapter) ConvertRequest(body []byte, upstreamModel string) ([]byte, map[string]string, error) {
	// OpenAI 协议不需要转换请求体（模型名替换由 rewriteBody 统一处理）
	return body, nil, nil
}

func (a *OpenAIAdapter) ConvertResponse(body []byte) ([]byte, error) {
	return body, nil
}

func (a *OpenAIAdapter) ConvertStreamEvent(data []byte) ([]byte, bool, error) {
	return data, false, nil
}

func (a *OpenAIAdapter) StreamDoneEvent() string {
	return "[DONE]"
}

// upstreamURL 拼接上游地址。
//
// 上游 base_url 常见两种写法：https://api.openai.com/v1 或 https://api.openai.com，
// 而客户端请求的路径固定带 /v1 前缀（/v1/chat/completions），
// 因此当 base 已经以 /v1 结尾时，要把 path 的 /v1 前缀去掉，避免拼成 /v1/v1/...。
func upstreamURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return base + path
}
