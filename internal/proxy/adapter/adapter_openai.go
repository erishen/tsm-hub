package adapter

import (
	"github.com/erishen/tsm-hub/internal/store"
)

// OpenAIAdapter 是 OpenAI 兼容协议的适配器，也是默认适配器。
// 它不做任何转换，直接透传请求和响应（只做 URL 拼接和认证头）。
// 注意：模型名替换和 stream_options 注入由 proxy.rewriteBody 统一处理，
// 这里不再重新序列化请求体，以保留 stream_options 等未知字段。
type OpenAIAdapter struct{}

func (a *OpenAIAdapter) Name() string { return "openai" }

func (a *OpenAIAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	return upstreamURL(provider.BaseURL, clientPath)
}

func (a *OpenAIAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return "Authorization", "Bearer " + provider.ResolvedAPIKey()
}

func (a *OpenAIAdapter) ConvertRequest(body []byte, upstreamModel string) ([]byte, map[string]string, error) {
	// OpenAI 适配器纯透传，不做请求体转换。
	// 模型名替换和 stream_options 注入已由 proxy.rewriteBody 处理。
	// 不重新序列化可以保留 stream_options 等所有原始字段。
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
