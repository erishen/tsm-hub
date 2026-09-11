package proxy

import (
	"net/http"

	"github.com/erishen/tsm-hub/internal/store"
)

// ProtocolAdapter 定义协议适配器接口。
// 网关对外只暴露 OpenAI 兼容 API；上游 Provider 可能使用不同协议，
// 适配器负责把 OpenAI 格式的请求/响应转换为目标协议格式。
type ProtocolAdapter interface {
	// Name 返回适配器名称（与 Provider.Protocol 对应）。
	Name() string

	// UpstreamURL 根据 Provider 的 base_url 和客户端请求路径，构造上游请求 URL。
	UpstreamURL(baseURL, clientPath string) string

	// ConvertRequest 把 OpenAI 格式的请求体转换为目标协议格式。
	// 返回转换后的请求体和需要额外设置的请求头（如 anthropic-version）。
	ConvertRequest(body []byte, upstreamModel string) (converted []byte, extraHeaders map[string]string, err error)

	// ConvertResponse 把目标协议的非流式响应体转换为 OpenAI 格式。
	ConvertResponse(body []byte) ([]byte, error)

	// ConvertStreamEvent 把目标协议的流式 SSE 事件转换为 OpenAI 格式。
	// 输入是单个 SSE 事件的 data 部分（不含 "data: " 前缀和结尾的换行）。
	// 返回转换后的 data 部分；如果该事件应被跳过（不转发给客户端），返回 skip=true。
	ConvertStreamEvent(data []byte) (converted []byte, skip bool, err error)

	// StreamDoneEvent 返回目标协议表示流结束的事件 data（用于识别流结束）。
	StreamDoneEvent() string
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

func init() {
	RegisterAdapter(&OpenAIAdapter{})
	RegisterAdapter(&AnthropicAdapter{})
}

// copyHeaders 复制客户端请求头到上游请求（跳过逐跳头和鉴权头）。
func copyHeaders(dst, src http.Header) {
	hopByHop := map[string]bool{
		"Host": true, "Connection": true, "Keep-Alive": true,
		"Proxy-Authenticate": true, "Proxy-Authorization": true,
		"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
		"Content-Length": true, "Authorization": true, "X-Admin-Token": true,
	}
	for k, vv := range src {
		if hopByHop[k] {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}
