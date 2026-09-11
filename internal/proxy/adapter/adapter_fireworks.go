package adapter

import (
	"github.com/erishen/tsm-hub/internal/store"
)

// FireworksAdapter 是 Fireworks AI API 协议的适配器。
//
// Fireworks AI 完全兼容 OpenAI 格式（/v1/chat/completions），
// 因此本适配器嵌入 OpenAIAdapter，只覆盖 Name() 方法。
//
// 用户配置：
// - Base URL：https://api.fireworks.ai/inference/v1
// - API Key：Fireworks AI API Key
// - 模型：accounts/fireworks/models/llama-v3-70b-instruct、accounts/fireworks/models/mixtral-8x7b-instruct 等
type FireworksAdapter struct {
	OpenAIAdapter
}

func (a *FireworksAdapter) Name() string { return "fireworks" }

func (a *FireworksAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	return a.OpenAIAdapter.UpstreamURL(provider, clientPath)
}

func (a *FireworksAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return a.OpenAIAdapter.AuthHeader(provider)
}
