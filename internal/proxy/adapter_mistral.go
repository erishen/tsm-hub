package proxy

import (
	"github.com/erishen/tsm-hub/internal/store"
)

// MistralAdapter 是 Mistral AI API 协议的适配器。
//
// Mistral API 完全兼容 OpenAI 格式（/v1/chat/completions），
// 因此本适配器嵌入 OpenAIAdapter，只覆盖 Name() 方法。
//
// 用户配置：
// - Base URL：https://api.mistral.ai/v1
// - API Key：Mistral API Key
// - 模型：mistral-large-latest、mistral-medium-latest、mistral-small-latest 等
type MistralAdapter struct {
	OpenAIAdapter
}

func (a *MistralAdapter) Name() string { return "mistral" }

// UpstreamURL 覆盖 OpenAIAdapter 的方法，确保使用正确的 Base URL。
func (a *MistralAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	return a.OpenAIAdapter.UpstreamURL(provider, clientPath)
}

// AuthHeader 覆盖 OpenAIAdapter 的方法，使用 Mistral 的认证方式（与 OpenAI 相同）。
func (a *MistralAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return a.OpenAIAdapter.AuthHeader(provider)
}
