package adapter

import (
	"github.com/erishen/tsm-hub/internal/store"
)

// GroqAdapter 是 Groq API 协议的适配器。
//
// Groq 完全兼容 OpenAI 格式（/v1/chat/completions），
// 因此本适配器嵌入 OpenAIAdapter，只覆盖 Name() 方法。
//
// Groq 以超快推理速度著称（LPU 推理引擎），适合低延迟场景。
//
// 用户配置：
// - Base URL：https://api.groq.com/openai/v1
// - API Key：Groq API Key
// - 模型：llama-3.3-70b-versatile、llama-3.1-8b-instant、mixtral-8x7b-32768 等
type GroqAdapter struct {
	OpenAIAdapter
}

func (a *GroqAdapter) Name() string { return "groq" }

func (a *GroqAdapter) UpstreamURL(provider store.Provider, clientPath string) string {
	return a.OpenAIAdapter.UpstreamURL(provider, clientPath)
}

func (a *GroqAdapter) AuthHeader(provider store.Provider) (key, value string) {
	return a.OpenAIAdapter.AuthHeader(provider)
}
