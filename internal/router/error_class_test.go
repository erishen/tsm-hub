package router

import (
	"reflect"
	"regexp"
	"sort"
	"testing"
	"time"
)

// 分类只看固定前缀；错误正文里即使混入 URL / Key 片段也不会影响结果，
// 更不会被写进标签（/metrics 未鉴权）。
func TestClassifyUpstreamError(t *testing.T) {
	cases := map[string]string{
		"upstream 402: insufficient balance sk-live-xxxx":       errClassQuota,
		"upstream 401: invalid api key":                         errClassAuth,
		"upstream 403: forbidden by https://api.example.com/v1": errClassAuth,
		"upstream 429: free tier rpm exceeded":                  errClassRateLimit,
		"upstream 404 (model unavailable): model not found":     errClassModelNotFound,
		"upstream 400 (tools unsupported, failover): bad tools": errClassBadRequest,
		"upstream 405 (method not allowed, failover): nope":     errClassBadRequest,
		"upstream 500: internal error":                          errClassServerError,
		"upstream 502 in stream":                                errClassServerError,
		"upstream 200: should not happen":                       errClassOther,
		"context deadline exceeded (Client.Timeout exceeded)":   errClassNetwork,
		"dial tcp: lookup api.example.com: no such host":        errClassNetwork,
		"unexpected EOF":                errClassNetwork,
		"":                              errClassOther,
		"upstream 42: malformed status": errClassOther,
		"UPSTREAM 429: wrong case":      errClassNetwork,
		"upstream 429x: not a three digit status after prefix": errClassRateLimit,
	}
	for in, want := range cases {
		if got := classifyUpstreamError(in); got != want {
			t.Errorf("classifyUpstreamError(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUpstreamErrorClassesEnum(t *testing.T) {
	got := UpstreamErrorClasses()
	if len(got) != 8 {
		t.Fatalf("class enum length = %d, want 8", len(got))
	}
	sorted := append([]string(nil), got...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(got, sorted) {
		t.Errorf("class enum is not sorted: %v", got)
	}
	slug := regexp.MustCompile(`^[a-z0-9_]+$`)
	seen := map[string]bool{}
	for _, c := range got {
		if !slug.MatchString(c) {
			t.Errorf("class %q is not a safe slug label value", c)
		}
		if seen[c] {
			t.Errorf("duplicate class %q", c)
		}
		seen[c] = true
	}
	// 返回值必须是拷贝，调用方改动不能污染导出顺序。
	got[0] = "tampered"
	if UpstreamErrorClasses()[0] == "tampered" {
		t.Error("UpstreamErrorClasses must return a copy")
	}
}

func TestTrackerErrorClasses(t *testing.T) {
	tr := NewTracker(3, 60)
	tr.ReportFailure("alpha", "upstream 402: balance exhausted")
	tr.ReportFailure("alpha", "upstream 500: boom")
	tr.ReportFailure("beta", "upstream 429: rate limited")
	tr.ReportFailure("beta", "dial tcp: timeout")
	tr.ReportThrottle("beta", "upstream 429: throttled", 30*time.Second)

	classes := tr.ErrorClasses()
	if got := classes["alpha"][errClassQuota]; got != 1 {
		t.Errorf("alpha quota = %d, want 1", got)
	}
	if got := classes["alpha"][errClassServerError]; got != 1 {
		t.Errorf("alpha server_error = %d, want 1", got)
	}
	if got := classes["beta"][errClassRateLimit]; got != 2 {
		t.Errorf("beta rate_limit = %d, want 2 (failure + throttle)", got)
	}
	if got := classes["beta"][errClassNetwork]; got != 1 {
		t.Errorf("beta network = %d, want 1", got)
	}
	if _, ok := classes["alpha"][errClassRateLimit]; ok {
		t.Error("unobserved class must be absent from internal map")
	}

	// 深拷贝：外部改动不能改到 Tracker 内部。
	classes["alpha"][errClassQuota] = 999
	if got := tr.ErrorClasses()["alpha"][errClassQuota]; got != 1 {
		t.Errorf("ErrorClasses must return a deep copy, got %d", got)
	}

	// 类别总数必须与 errors 完全一致（同源计数，不重不漏）。
	total := uint64(0)
	for _, m := range tr.ErrorClasses() {
		for _, n := range m {
			total += n
		}
	}
	if total != 5 {
		t.Errorf("class total = %d, want 5", total)
	}
	var errs int64
	for _, ph := range tr.Snapshot() {
		errs += ph.Errors
	}
	if errs != int64(total) {
		t.Errorf("class total %d != upstream errors %d", total, errs)
	}

	// 成功上报不产生类别计数；Restore 也不该把历史类别塞回来（counter 重启归零）。
	tr.ReportSuccess("alpha", 10*time.Millisecond)
	if got := len(tr.ErrorClasses()["alpha"]); got != 2 {
		t.Errorf("success must not add class counts, got %d entries", got)
	}
	tr.Restore([]ProviderHealth{{ProviderID: "alpha", Errors: 42}})
	if _, ok := tr.ErrorClasses()["alpha"][errClassQuota]; !ok {
		t.Error("Restore must not wipe in-process class counters")
	}
}
