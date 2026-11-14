package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HTTP 请求耗时直方图。withLogging 已经算过一次耗时，这里只复用它，
// 不再新增任何业务埋点。LLM 网关的 SSE 长连接可能持续数分钟，所以桶
// 一直铺到 120s；p95/p99 由 Prometheus 的 histogram_quantile() 计算。
//
// ⚠️ /metrics 不鉴权：route / method 一律经白名单折叠成有限枚举，
// 绝不把原始 URL（含 query、技能名、session id）或客户端自定义方法写进标签。
var httpDurationBuckets = []float64{
	0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120,
}

const (
	httpDurationMetric = "tsm_hub_http_request_duration_seconds"
	httpDurationHelp   = "HTTP request duration in seconds (normalized route, method and flow)."
	// firstByteMetric 只统计「首次 Flush」的耗时：SSE 流式响应在写出响应头后
	// 立刻 Flush，此值即用户感知的首字节时间（含上游排队 + 首 token 前等待）。
	// 非流式请求不产生样本，所以它天然就是流式请求的计数器。
	firstByteMetric = "tsm_hub_http_first_byte_seconds"
	firstByteHelp   = "Time to the first flushed response byte in seconds (streaming requests only)."
)

// flow 是代码内二值枚举：stream=响应过程中调用过 Flush（SSE/长连接），
// buffered=一次性返回。绝不把 Content-Type、模型名或客户端标记写进标签。
const (
	flowStream   = "stream"
	flowBuffered = "buffered"
)

type durationKey struct {
	route  string
	method string
	flow   string
}

type durationSample struct {
	buckets []uint64 // 与 httpDurationBuckets 等长，累计计数（le=upper 的样本数）
	sum     float64  // 秒
	count   uint64
}

// requestDurations 用独立锁，避免与 Server.mu（status/deny 计数）互相竞争。
// 每个 (route, method, flow) 组合一行状态；由于标签是白名单枚举，map 规模有上界。
type requestDurations struct {
	mu       sync.Mutex
	samples  map[durationKey]*durationSample
	metric   string // 导出的 family 名
	help     string // # HELP 文本
	withFlow bool   // 是否输出 flow 标签（首字节直方图不需要，它只有流式样本）
}

func newHistogram(metric, help string, withFlow bool) *requestDurations {
	return &requestDurations{
		samples:  make(map[durationKey]*durationSample),
		metric:   metric,
		help:     help,
		withFlow: withFlow,
	}
}

func newRequestDurations() *requestDurations {
	return newHistogram(httpDurationMetric, httpDurationHelp, true)
}

func newFirstByteDurations() *requestDurations {
	return newHistogram(firstByteMetric, firstByteHelp, false)
}

// observe 记录一次请求耗时。nil receiver 安全（最小 Server / 单测可直接用 &Server{}）。
// flow 只在 withFlow 的直方图里生效，避免首字节直方图被拆成无意义的子序列。
func (d *requestDurations) observe(route, method, flow string, elapsed time.Duration) {
	if d == nil {
		return
	}
	sec := elapsed.Seconds()
	if !d.withFlow {
		flow = ""
	}
	k := durationKey{route: route, method: method, flow: flow}
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.samples[k]
	if s == nil {
		s = &durationSample{buckets: make([]uint64, len(httpDurationBuckets))}
		d.samples[k] = s
	}
	s.count++
	s.sum += sec
	for i, upper := range httpDurationBuckets {
		if sec <= upper {
			s.buckets[i]++
		}
	}
}

// snapshot 在锁内做浅拷贝，导出时不持有锁，避免慢抓取阻塞请求路径。
func (d *requestDurations) snapshot() map[durationKey]durationSample {
	out := make(map[durationKey]durationSample)
	if d == nil {
		return out
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for k, s := range d.samples {
		cp := durationSample{
			buckets: make([]uint64, len(s.buckets)),
			sum:     s.sum,
			count:   s.count,
		}
		copy(cp.buckets, s.buckets)
		out[k] = cp
	}
	return out
}

// metricRoute 把任意 URL path 折叠成固定枚举。这里必须"先精确匹配、后前缀"，
// 且所有子路径一律合并（/api/admin/providers/xxx 之类不能各占一条序列）。
func metricRoute(path string) string {
	switch {
	case path == "/healthz":
		return "healthz"
	case path == "/metrics":
		return "metrics"
	case path == "/.well-known/agent-card.json":
		return "agent_card"
	case path == "/a2a":
		return "a2a"
	case path == "/api/admin/login":
		return "admin_login"
	case strings.HasPrefix(path, "/api/admin/"):
		return "admin"
	case path == "/v1" || strings.HasPrefix(path, "/v1/"):
		// normalizeInferencePath 处理的是转发路径，这里只做标签归一化；
		// /v1/chat/completions/tools 之类误拼也归入同一个 route。
		return "inference"
	case strings.HasPrefix(path, "/peer/"):
		return "peer"
	default:
		return "other"
	}
}

// metricMethod 只放行标准方法并转小写（标签值门禁要求 [a-z0-9_-]+）。
// 客户端可发任意自定义 method，未命中一律 other，防止标签基数被外部输入撑爆。
func metricMethod(method string) string {
	switch method {
	case http.MethodGet:
		return "get"
	case http.MethodPost:
		return "post"
	case http.MethodPut:
		return "put"
	case http.MethodPatch:
		return "patch"
	case http.MethodDelete:
		return "delete"
	case http.MethodHead:
		return "head"
	case http.MethodOptions:
		return "options"
	default:
		return "other"
	}
}

// writeDurationMetrics 输出标准 Prometheus histogram（bucket/sum/count 三个样本族，
// 共用一份 HELP/TYPE）。没有观测值时仍输出 family 声明，与 http_requests_total
// 在冷启动时的行为保持一致。
func writeDurationMetrics(b *strings.Builder, d *requestDurations) {
	// nil / 零值 receiver 仍按「总耗时直方图」输出 HELP/TYPE，保持冷启动行为不变。
	metric, help, withFlow := httpDurationMetric, httpDurationHelp, true
	if d != nil && d.metric != "" {
		metric, help, withFlow = d.metric, d.help, d.withFlow
	}
	snap := d.snapshot()
	fmt.Fprintf(b, "# HELP %s %s\n", metric, help)
	fmt.Fprintf(b, "# TYPE %s histogram\n", metric)

	keys := make([]durationKey, 0, len(snap))
	for k := range snap {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].flow < keys[j].flow
	})

	for _, k := range keys {
		s := snap[k]
		labels := fmt.Sprintf("route=%q,method=%q,", k.route, k.method)
		if withFlow {
			labels += fmt.Sprintf("flow=%q,", k.flow)
		}
		sumLabels := strings.TrimSuffix(labels, ",")
		for i, upper := range httpDurationBuckets {
			fmt.Fprintf(b, "%s_bucket{%sle=%q} %d\n",
				metric, labels, strconv.FormatFloat(upper, 'g', -1, 64), s.buckets[i])
		}
		fmt.Fprintf(b, "%s_bucket{%sle=\"+Inf\"} %d\n", metric, labels, s.count)
		fmt.Fprintf(b, "%s_sum{%s} %s\n", metric, sumLabels, strconv.FormatFloat(s.sum, 'g', -1, 64))
		fmt.Fprintf(b, "%s_count{%s} %d\n", metric, sumLabels, s.count)
	}
}
