package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

type parsedHistogram struct {
	buckets []float64
	inf     float64
	sum     float64
	count   float64
	seen    map[string]bool
}

// TestHTTPDurationHistogramFormat 校验输出是合法的 Prometheus histogram：
// 桶单调不减、le=+Inf 等于 _count、每个组合的 HELP/TYPE 只有一份。
func TestHTTPDurationHistogramFormat(t *testing.T) {
	d := newRequestDurations()
	d.observe("inference", "post", flowBuffered, 30*time.Millisecond)
	d.observe("inference", "post", flowBuffered, 3*time.Second)
	d.observe("inference", "post", flowStream, 45*time.Second)
	d.observe("admin", "get", flowBuffered, 250*time.Millisecond)

	var sb strings.Builder
	writeDurationMetrics(&sb, d)
	body := sb.String()

	if got := strings.Count(body, "# TYPE "+httpDurationMetric+" histogram"); got != 1 {
		t.Errorf("TYPE count = %d, want 1", got)
	}
	if got := strings.Count(body, "# HELP "+httpDurationMetric+" "); got != 1 {
		t.Errorf("HELP count = %d, want 1", got)
	}

	groups := map[string]*parsedHistogram{}
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		head, rawVal, ok := strings.Cut(line, " ")
		if !ok {
			t.Fatalf("malformed sample %q", line)
		}
		val, err := strconv.ParseFloat(rawVal, 64)
		if err != nil {
			t.Fatalf("non-numeric value in %q: %v", line, err)
		}
		name, labelPart, hasLabels := strings.Cut(head, "{")
		if !hasLabels || !strings.HasSuffix(labelPart, "}") {
			t.Fatalf("sample %q must carry labels", line)
		}
		labels := map[string]string{}
		for _, kv := range strings.Split(strings.TrimSuffix(labelPart, "}"), ",") {
			k, raw, ok := strings.Cut(kv, "=")
			if !ok {
				t.Fatalf("malformed label pair %q", kv)
			}
			v, err := strconv.Unquote(raw)
			if err != nil {
				t.Fatalf("label value %q is not quoted: %v", raw, err)
			}
			labels[k] = v
		}
		key := labels["route"] + "/" + labels["method"] + "/" + labels["flow"]
		g := groups[key]
		if g == nil {
			g = &parsedHistogram{seen: map[string]bool{}}
			groups[key] = g
		}
		switch {
		case strings.HasSuffix(name, "_bucket"):
			if labels["le"] == "+Inf" {
				g.inf = val
				g.seen["inf"] = true
			} else {
				g.buckets = append(g.buckets, val)
				g.seen["bucket"] = true
			}
		case strings.HasSuffix(name, "_sum"):
			g.sum = val
			g.seen["sum"] = true
		case strings.HasSuffix(name, "_count"):
			g.count = val
			g.seen["count"] = true
		default:
			t.Errorf("unexpected sample name %q", name)
		}
	}

	if len(groups) != 3 {
		t.Errorf("groups = %d, want 3 (stream/buffered must be separate series)", len(groups))
	}
	for key, g := range groups {
		if !g.seen["bucket"] || !g.seen["inf"] || !g.seen["sum"] || !g.seen["count"] {
			t.Errorf("%s missing one of bucket/inf/sum/count: %+v", key, g.seen)
		}
		if len(g.buckets) != len(httpDurationBuckets) {
			t.Errorf("%s buckets = %d, want %d", key, len(g.buckets), len(httpDurationBuckets))
		}
		for i := 1; i < len(g.buckets); i++ {
			if g.buckets[i] < g.buckets[i-1] {
				t.Errorf("%s bucket counts not cumulative at index %d", key, i)
			}
		}
		if g.inf != g.count {
			t.Errorf("%s le=+Inf (%v) != _count (%v)", key, g.inf, g.count)
		}
		if g.buckets[len(g.buckets)-1] > g.count {
			t.Errorf("%s largest finite bucket exceeds count", key)
		}
	}
	if got := groups["inference/post/buffered"].count; got != 2 {
		t.Errorf("inference/post/buffered count = %v, want 2", got)
	}
	// 30ms + 3s = 3.03s（含微小误差）
	if got := groups["inference/post/buffered"].sum; got < 3.02 || got > 3.04 {
		t.Errorf("inference/post/buffered sum = %v, want ~3.03", got)
	}
	if got := groups["inference/post/stream"].count; got != 1 {
		t.Errorf("inference/post/stream count = %v, want 1", got)
	}
	if got := groups["inference/post/stream"].sum; got < 44.9 || got > 45.1 {
		t.Errorf("inference/post/stream sum = %v, want ~45", got)
	}
}

// TestHTTPDurationClassification 是标签白名单的回归门禁：路径/方法里的
// 高基数或敏感片段必须被折叠成枚举，绝不能原样进入 /metrics。
func TestHTTPDurationClassification(t *testing.T) {
	for in, want := range map[string]string{
		"/healthz":                              "healthz",
		"/metrics":                              "metrics",
		"/.well-known/agent-card.json":          "agent_card",
		"/a2a":                                  "a2a",
		"/api/admin/login":                      "admin_login",
		"/api/admin/providers/sk-secret/reveal": "admin",
		"/api/admin/keys?id=abc":                "admin",
		"/v1":                                   "inference",
		"/v1/chat/completions":                  "inference",
		"/v1/chat/completions/tools":            "inference",
		"/v1/skills/private-skill-name?token=xxxx": "inference",
		"/peer/invoke":                "peer",
		"/totally/unknown/path/12345": "other",
		"":                            "other",
	} {
		if got := metricRoute(in); got != want {
			t.Errorf("metricRoute(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		http.MethodGet:     "get",
		http.MethodPost:    "post",
		http.MethodPut:     "put",
		http.MethodPatch:   "patch",
		http.MethodDelete:  "delete",
		http.MethodHead:    "head",
		http.MethodOptions: "options",
		"PROPFIND":         "other",
		"sk-live-secret":   "other",
		"":                 "other",
	} {
		if got := metricMethod(in); got != want {
			t.Errorf("metricMethod(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHTTPDurationNilSafe 覆盖最小 Server（&Server{}）与空观测两种情况。
func TestHTTPDurationNilSafe(t *testing.T) {
	var nilD *requestDurations
	nilD.observe("other", "other", flowStream, time.Second)
	if got := nilD.snapshot(); len(got) != 0 {
		t.Errorf("nil snapshot length = %d, want 0", len(got))
	}
	var sb strings.Builder
	writeDurationMetrics(&sb, nilD)
	if got := strings.Count(sb.String(), "# TYPE "+httpDurationMetric+" histogram"); got != 1 {
		t.Errorf("empty histogram must still declare TYPE once, got %d", got)
	}
	if got := strings.Count(sb.String(), "_bucket{"); got != 0 {
		t.Errorf("empty histogram must not emit bucket samples, got %d", got)
	}
}

// 首字节直方图是独立 family：只有流式样本，且不带 flow 标签（避免无意义分裂）。
func TestFirstByteHistogramSeparateFamily(t *testing.T) {
	d := newFirstByteDurations()
	d.observe("inference", "post", flowStream, 120*time.Millisecond)
	d.observe("inference", "post", flowStream, 900*time.Millisecond)

	var sb strings.Builder
	writeDurationMetrics(&sb, d)
	body := sb.String()
	if strings.Contains(body, httpDurationMetric) {
		t.Error("first-byte histogram must not emit the total-duration metric name")
	}
	if got := strings.Count(body, "# TYPE "+firstByteMetric+" histogram"); got != 1 {
		t.Errorf("TYPE count = %d, want 1", got)
	}
	if strings.Contains(body, "flow=") {
		t.Error("first-byte histogram must not carry a flow label")
	}
	if got := strings.Count(body, firstByteMetric+"_count{"); got != 1 {
		t.Errorf("count series = %d, want 1 (flow must not split the sample)", got)
	}
	if !strings.Contains(body, firstByteMetric+`_count{route="inference",method="post"} 2`) {
		t.Error("missing expected first-byte count sample")
	}
	if !strings.Contains(body, firstByteMetric+`_bucket{route="inference",method="post",le="+Inf"} 2`) {
		t.Error("le=+Inf bucket must equal count")
	}
}

// 走完整 withLogging：SSE 请求应同时产出 flow=stream 的总耗时样本和一条
// 首字节样本；普通请求只产出 flow=buffered，且不污染首字节直方图。
func TestWithLoggingFlowAndFirstByte(t *testing.T) {
	s := &Server{
		startAt:      time.Now(),
		statusCounts: map[string]int64{},
		denyCounts:   map[string]int64{},
		durations:    newRequestDurations(),
		firstBytes:   newFirstByteDurations(),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	streamHandler := s.withLogging(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		time.Sleep(5 * time.Millisecond)
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("data: first\n\n"))
		flusher.Flush()
		flusher.Flush() // 多次 Flush 只算一次首字节
	})
	bufferedHandler := s.withLogging(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	req := func() *http.Request { return httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil) }
	streamHandler(httptest.NewRecorder(), req())
	bufferedHandler(httptest.NewRecorder(), req())

	dSnap := s.durations.snapshot()
	if len(dSnap) != 2 {
		t.Fatalf("duration series = %d, want 2 (stream + buffered)", len(dSnap))
	}
	streamKey := durationKey{route: "inference", method: "post", flow: flowStream}
	bufferedKey := durationKey{route: "inference", method: "post", flow: flowBuffered}
	if dSnap[streamKey].count != 1 || dSnap[bufferedKey].count != 1 {
		t.Errorf("flow split wrong: stream=%d buffered=%d", dSnap[streamKey].count, dSnap[bufferedKey].count)
	}

	fSnap := s.firstBytes.snapshot()
	if len(fSnap) != 1 {
		t.Fatalf("first-byte series = %d, want 1", len(fSnap))
	}
	fb := fSnap[durationKey{route: "inference", method: "post"}]
	if fb.count != 1 {
		t.Errorf("first-byte count = %d, want 1", fb.count)
	}
	if fb.sum < 0 || fb.sum > float64(dSnap[streamKey].sum) {
		t.Errorf("first-byte sum %v out of [0, total %v]", fb.sum, dSnap[streamKey].sum)
	}
}

// TestMetricsHistogramEndpointPrivacy 走完整 /metrics，确认真实端点里
// 不出现原始路径、密钥样式片段或客户端自定义方法。
func TestMetricsHistogramEndpointPrivacy(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	d := newRequestDurations()
	d.observe(metricRoute("/api/admin/providers/sk-secret/reveal"), metricMethod("PROPFIND"), flowBuffered, time.Second)
	s := &Server{
		store:        e.store,
		startAt:      time.Now(),
		statusCounts: map[string]int64{},
		denyCounts:   map[string]int64{},
		durations:    d,
	}
	w := httptest.NewRecorder()
	s.handleMetrics(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "tsm_hub_http_request_duration_seconds_bucket{route=\"admin\",method=\"other\"") {
		t.Error("histogram sample with normalized labels missing")
	}
	for _, forbidden := range []string{"sk-secret", "PROPFIND", "/api/admin/", "reveal", "token="} {
		if strings.Contains(body, forbidden) {
			t.Errorf("/metrics leaked %q from raw path/method", forbidden)
		}
	}
}
