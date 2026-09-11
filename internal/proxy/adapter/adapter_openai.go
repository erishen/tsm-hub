package adapter

import (
	"encoding/json"

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
	// OpenAI 适配器不做请求体转换，但需要替换模型名
	var req openAIRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return body, nil, nil // 解析失败就原样透传
	}
	if upstreamModel != "" && upstreamModel != "*" {
		req.Model = upstreamModel
		out, err := json.Marshal(req)
		if err != nil {
			return body, nil, nil
		}
		return out, nil, nil
	}
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
