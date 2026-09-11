package adapter

import (
	"github.com/erishen/tsm-hub/internal/store"
)

// TogetherAdapter 是 Together AI API 协议的适配器。
//
// Together AI 完全兼容 OpenAI 格式（/v1/chat/completions），
// 因此本适配器嵌入 OpenAIAdapter，只覆盖 Name() 方法。
//
// 用户配置：
// - Base URL：https://api.together.xyz/v1
// - API Key：Together AI API Key
// - 模型：meta-llama/Llama-3-70B-chat-hf、mistralai/Mixtral-8x7B-Instruct-v0.1 等
type TogetherAdapter struct {
	OpenAIAdapter
}

func (a *TogetherAdapter) Name() string { return "together" }

func (a *TogetherAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	return a.OpenAIAdapter.UpstreamURL(provider, clientPath)
}

func (a *TogetherAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return a.OpenAIAdapter.AuthHeader(provider)
}
