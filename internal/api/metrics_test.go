package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/erishen/tsm-hub/internal/router"
)

// 扩展指标必须全部出现，且每个 family 的 HELP/TYPE 只能有一份。
var extendedMetricFamilies = []string{
	"tsm_hub_upstream_half_open",
	"tsm_hub_upstream_consecutive_failures",
	"tsm_hub_upstream_cooldown_remaining_seconds",
	"tsm_hub_upstream_last_success_timestamp_seconds",
	"tsm_hub_upstream_error_classes_total",
	"tsm_hub_usage_requests_total",
	"tsm_hub_usage_tokens_total",
	"tsm_hub_usage_cost_usd_total",
	"tsm_hub_usage_errors_total",
	"tsm_hub_usage_failovers_total",
	"tsm_hub_usage_avg_latency_ms",
	"tsm_hub_usage_scene_requests_total",
	"tsm_hub_usage_scene_tokens_total",
	"tsm_hub_failover_candidates_total",
	"tsm_hub_tool_calls_total",
	"tsm_hub_mcp_servers",
	"tsm_hub_mcp_tools",
	"tsm_hub_a2a_tasks_active",
	"tsm_hub_a2a_capabilities",
	"tsm_hub_a2a_peer_mode",
	"tsm_hub_config_entities",
	"tsm_hub_runtime_goroutines",
	"tsm_hub_runtime_heap_alloc_bytes",
	"tsm_hub_runtime_cpu_cores",
}

// /metrics 不鉴权：标签名只能是这张白名单，标签值必须是 slug 风格。
// 任何密钥、URL、路径、ID、错误文本都会在这里被拒绝。
var allowedMetricLabels = map[string]bool{
	"provider": true, "status": true, "code": true, "kind": true,
	"scene": true, "source": true, "state": true, "mode": true, "entity": true,
	// class 是 router.UpstreamErrorClasses() 的固定枚举；flow 是
	// stream/buffered 二值；route/method 由白名单折叠；le 是直方图固有标签。
	"class": true, "flow": true,
	"route": true, "method": true, "le": true,
}

var slugLabelValue = regexp.MustCompile(`^[a-z0-9_-]+$`)
var leLabelValue = regexp.MustCompile(`^([0-9]+(\.[0-9]+)?|\+Inf)$`)

func TestMetricsExtendedFamilies(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	resp := e.do(t, http.MethodGet, "/metrics", "", nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	lines := strings.Split(strings.TrimSpace(body), "\n")

	for _, name := range extendedMetricFamilies {
		if strings.Count(body, "# TYPE "+name+" ") != 1 {
			t.Errorf("family %q must have exactly one # TYPE", name)
		}
		if strings.Count(body, "# HELP "+name+" ") != 1 {
			t.Errorf("family %q must have exactly one # HELP", name)
		}
		found := false
		for _, line := range lines {
			if strings.HasPrefix(line, name+" ") || strings.HasPrefix(line, name+"{") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("family %q has no sample", name)
		}
	}

	// 两个直方图必须是各自独立的 family，HELP/TYPE 各一份；最小 Server
	// 没初始化 firstBytes 时也不能重复输出总耗时 family（见 handleMetrics 兜底）。
	for _, name := range []string{httpDurationMetric, firstByteMetric} {
		if got := strings.Count(body, "# TYPE "+name+" histogram"); got != 1 {
			t.Errorf("histogram %q TYPE count = %d, want 1", name, got)
		}
		if got := strings.Count(body, "# HELP "+name+" "); got != 1 {
			t.Errorf("histogram %q HELP count = %d, want 1", name, got)
		}
	}

	// 每一行要么是指令，要么是合法样本；标签与取值都要过白名单。
	providerSeen := map[string]bool{}
	coreProviders := map[string]bool{}
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := line
		if i := strings.IndexAny(line, "{ "); i > 0 {
			name = line[:i]
		}
		open, closeIdx := strings.Index(line, "{"), strings.Index(line, "}")
		if open >= 0 {
			if closeIdx < open {
				t.Fatalf("malformed labels: %q", line)
			}
			for _, kv := range strings.Split(line[open+1:closeIdx], ",") {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) != 2 {
					t.Fatalf("malformed label pair %q in %q", kv, line)
				}
				key, rawVal := parts[0], parts[1]
				if !allowedMetricLabels[key] {
					t.Errorf("label %q on %q is not in the privacy whitelist", key, name)
				}
				val, err := strconv.Unquote(rawVal)
				if err != nil {
					t.Errorf("label value %q is not a quoted Prometheus label: %v", rawVal, err)
					continue
				}
				if key == "le" {
					if !leLabelValue.MatchString(val) {
						t.Errorf("le value %q is not a numeric bucket or +Inf", val)
					}
				} else if !slugLabelValue.MatchString(val) {
					t.Errorf("label value %q (key %s) is not a safe slug; possible secret/path/ID leak", val, key)
				}
				if key == "provider" {
					providerSeen[val] = true
					if strings.HasPrefix(name, "tsm_hub_upstream_healthy") {
						coreProviders[val] = true
					}
				}
			}
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Errorf("sample line has no value: %q", line)
			continue
		}
		if _, err := strconv.ParseFloat(fields[len(fields)-1], 64); err != nil {
			t.Errorf("sample value %q not numeric: %v", fields[len(fields)-1], err)
		}
	}

	// 扩展指标的 provider 标签必须与原有 7 指标同源（不导出历史 JSONL 里的残留名字）。
	for p := range providerSeen {
		if !coreProviders[p] {
			t.Errorf("provider %q appears in extended metrics but not in tsm_hub_upstream_healthy", p)
		}
	}
}

func TestMetricsExtendedPrivacyAndNilDeps(t *testing.T) {
	e := newEnv(t, "ok")
	defer e.server.Close()

	resp := e.do(t, http.MethodGet, "/metrics", "", nil)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(raw)
	for _, forbidden := range []string{"api_key", "key_id", "Bearer", "sk-", "session_id", "http://", "https://"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("/metrics leaked %q", forbidden)
		}
	}

	// 只带 store 的最小 Server：rec/proxy/a2a/skills 全为 nil 时不得 panic，
	// 且仍要输出核心 7 指标与不依赖子系统的运行时/配置指标。
	minimal := &Server{
		store:        e.store,
		startAt:      time.Now(),
		statusCounts: map[string]int64{},
		denyCounts:   map[string]int64{},
	}
	w := httptest.NewRecorder()
	minimal.handleMetrics(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("minimal server /metrics status = %d", w.Code)
	}
	for _, want := range []string{
		"# TYPE tsm_hub_uptime_seconds gauge",
		"# TYPE tsm_hub_config_entities gauge",
		"# TYPE tsm_hub_runtime_goroutines gauge",
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("minimal server missing %q", want)
		}
	}
	// 用量类 family 依赖 rec，nil 时应整族跳过而不是输出零值误导查询。
	if strings.Contains(w.Body.String(), "tsm_hub_usage_requests_total") {
		t.Error("usage family must be skipped when Recorder is nil")
	}
}

func TestMetricsHelperClassification(t *testing.T) {
	for in, want := range map[string]string{
		"chat": "chat", "reason": "reason", "code": "code", "fast": "fast",
		"unknown-scene": "other", "sk-secret": "other", "/v1/chat": "other", "": "other",
	} {
		if got := safeScene(in); got != want {
			t.Errorf("safeScene(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"off": "off", "shared": "shared", "derived": "derived", "evil": "other", "": "other",
	} {
		if got := safePeerMode(in); got != want {
			t.Errorf("safePeerMode(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"mcp_git_status": "mcp", "skill:review": "skill", "read_file": "builtin", "": "builtin",
	} {
		if got := toolSource(in); got != want {
			t.Errorf("toolSource(%q) = %q, want %q", in, got, want)
		}
	}
}

// 错误分类必须是「固定枚举 × 当前配置 provider」，且绝不能把错误正文带出来。
func TestMetricsErrorClasses(t *testing.T) {
	tr := router.NewTracker(3, 60)
	tr.ReportFailure("alpha", "upstream 402: sk-live-secret https://upstream.example.com/v1")
	tr.ReportThrottle("alpha", "upstream 429: throttled", time.Second)
	tr.ReportFailure("deleted-provider", "upstream 500: boom")

	s := &Server{health: tr}
	var sb strings.Builder
	// 白名单只有 alpha：deleted-provider 的计数不得出现。
	s.writeErrorClassMetrics(&sb, []string{"alpha"})
	body := sb.String()

	enums := router.UpstreamErrorClasses()
	if got := strings.Count(body, "tsm_hub_upstream_error_classes_total{"); got != len(enums) {
		t.Errorf("samples = %d, want %d (one per fixed class)", got, len(enums))
	}
	for _, c := range enums {
		if !strings.Contains(body, `class="`+c+`"`) {
			t.Errorf("missing zero/complete series for class %q", c)
		}
	}
	for _, want := range []string{
		`tsm_hub_upstream_error_classes_total{provider="alpha",class="quota"} 1`,
		`tsm_hub_upstream_error_classes_total{provider="alpha",class="rate_limit"} 1`,
		`tsm_hub_upstream_error_classes_total{provider="alpha",class="server_error"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing expected sample %q", want)
		}
	}
	for _, forbidden := range []string{"sk-live-secret", "https://", "deleted-provider", "boom", "throttled"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("error class metrics leaked %q", forbidden)
		}
	}
	if got := strings.Count(body, "# TYPE tsm_hub_upstream_error_classes_total counter"); got != 1 {
		t.Errorf("TYPE count = %d, want 1", got)
	}

	// health 为 nil 时仍要输出完整 0 值矩阵，不能 panic 或整族消失。
	var empty strings.Builder
	(&Server{}).writeErrorClassMetrics(&empty, []string{"alpha"})
	if got := strings.Count(empty.String(), "tsm_hub_upstream_error_classes_total{"); got != len(enums) {
		t.Errorf("nil tracker samples = %d, want %d", got, len(enums))
	}
}

func TestMetricsCooldownHelper(t *testing.T) {
	now := time.Now()
	zero := router.ProviderHealth{}
	if got := cooldownRemainingSeconds(zero, now); got != 0 {
		t.Errorf("zero state cooldown = %d, want 0", got)
	}
	ph := router.ProviderHealth{DownUntil: now.Add(10 * time.Second), ThrottleUntil: now.Add(20 * time.Second)}
	if got := cooldownRemainingSeconds(ph, now); got < 19 || got > 21 {
		t.Errorf("cooldown should use the later throttle deadline, got %d", got)
	}
	expired := router.ProviderHealth{DownUntil: now.Add(-time.Hour)}
	if got := cooldownRemainingSeconds(expired, now); got != 0 {
		t.Errorf("expired cooldown = %d, want 0", got)
	}
	if got := unixOrZero(time.Time{}); got != 0 {
		t.Errorf("unixOrZero(zero) = %d, want 0", got)
	}
	if got := unixOrZero(now); got != now.Unix() {
		t.Errorf("unixOrZero(now) = %d, want %d", got, now.Unix())
	}
}
