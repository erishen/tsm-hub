package router

import (
	"sort"
	"strings"
)

// 上游失败类别是「代码内有限枚举」，供 /metrics 未鉴权端点导出。
// ⚠️ 只允许类别名进指标标签：errMsg 里可能含上游错误正文、URL 或 Key 片段，
// 一律不参与导出；分类函数只看 "upstream <3 位状态码>" 这个固定前缀。
const (
	errClassQuota         = "quota"           // 402：额度/余额耗尽
	errClassAuth          = "auth"            // 401/403：凭据失效或被上游拒绝
	errClassRateLimit     = "rate_limit"      // 429：限流（含免费池分钟配额）
	errClassModelNotFound = "model_not_found" // 404：模型不存在（若计入健康）
	errClassBadRequest    = "bad_request"     // 其它 4xx：请求/工具/方法不被支持
	errClassServerError   = "server_error"    // 5xx：上游服务端故障
	errClassNetwork       = "network"         // 无上游状态码：超时/连接失败/断流
	errClassOther         = "other"           // 兜底
)

var upstreamErrorClassNames = []string{
	errClassQuota, errClassAuth, errClassRateLimit, errClassModelNotFound,
	errClassBadRequest, errClassServerError, errClassNetwork, errClassOther,
}

// UpstreamErrorClasses 返回固定类别枚举（已排序、与内部实现同源）。
// /metrics 用它为每个 provider × 每个类别补 0 值序列，保证 rate()/increase()
// 在冷启动时也有干净的基线，且标签基数严格有上界。
func UpstreamErrorClasses() []string {
	out := append([]string(nil), upstreamErrorClassNames...)
	sort.Strings(out)
	return out
}

// classifyUpstreamError 从 ReportFailure 收到的 errMsg 提取失败类别。
//
// 只解析 proxy 生成的固定前缀 "upstream <3 位数字>"（例如
// "upstream 402: ..." / "upstream 404 (model unavailable): ..." /
// "upstream 500 in stream"）。正文从不参与判断，更不会被写进任何标签。
// 没有该前缀的通常是 context deadline exceeded / dial / EOF 等传输层错误，
// 归入 network；空字符串归 other，避免把「未知」误报成网络故障。
func classifyUpstreamError(errMsg string) string {
	const prefix = "upstream "
	if !hasUpstreamStatusPrefix(errMsg) {
		// 有 upstream 前缀却不是 3 位状态码，说明是未知格式，别硬猜成网络故障。
		if errMsg == "" || strings.HasPrefix(errMsg, prefix) {
			return errClassOther
		}
		return errClassNetwork
	}
	rest := errMsg[len(prefix):]
	code := int(rest[0]-'0')*100 + int(rest[1]-'0')*10 + int(rest[2]-'0')
	switch {
	case code == 402:
		return errClassQuota
	case code == 401, code == 403:
		return errClassAuth
	case code == 429:
		return errClassRateLimit
	case code == 404:
		return errClassModelNotFound
	case code >= 500:
		return errClassServerError
	case code >= 400:
		return errClassBadRequest
	default:
		return errClassOther
	}
}

func hasUpstreamStatusPrefix(errMsg string) bool {
	const prefix = "upstream "
	if len(errMsg) < len(prefix)+3 || errMsg[:len(prefix)] != prefix {
		return false
	}
	for i := len(prefix); i < len(prefix)+3; i++ {
		if errMsg[i] < '0' || errMsg[i] > '9' {
			return false
		}
	}
	return true
}
