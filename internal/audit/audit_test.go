package audit

import (
	"encoding/json"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, 90)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenDefaultRetention(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, 0) // 0 → default 90
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.retentionDays != 90 {
		t.Fatalf("default retention: %d", s.retentionDays)
	}
}

func TestRecordAndQuery(t *testing.T) {
	s := newTestStore(t)
	s.Record("create", "provider", "p1", "detail text", "admin", "1.2.3.4", "ua")
	logs, total, err := s.Query("", "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("total: %d", total)
	}
	if len(logs) != 1 {
		t.Fatalf("len: %d", len(logs))
	}
	l := logs[0]
	if l.Action != "create" || l.Object != "provider" || l.ObjectID != "p1" {
		t.Fatalf("fields: %+v", l)
	}
	if l.Detail != "detail text" {
		t.Fatalf("detail: %q", l.Detail)
	}
	if l.Operator != "admin" || l.ClientIP != "1.2.3.4" || l.UserAgent != "ua" {
		t.Fatalf("operator fields: %+v", l)
	}
	if l.TS.IsZero() {
		t.Fatal("ts zero")
	}
}

func TestRecordDetailTypes(t *testing.T) {
	s := newTestStore(t)
	s.Record("update", "key", "k1", map[string]any{"a": 1, "b": "x"}, "admin", "", "")
	s.Record("delete", "route", "r1", nil, "admin", "", "")

	logs, _, err := s.Query("key", "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("key logs: %d", len(logs))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(logs[0].Detail), &m); err != nil {
		t.Fatalf("detail not json: %v (%q)", err, logs[0].Detail)
	}
	if m["a"] != float64(1) || m["b"] != "x" {
		t.Fatalf("json body: %+v", m)
	}

	rlogs, _, err := s.Query("route", "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rlogs) != 1 {
		t.Fatalf("route logs: %d", len(rlogs))
	}
	if rlogs[0].Detail != "" {
		t.Fatalf("nil detail not empty: %q", rlogs[0].Detail)
	}
}

func TestRecordSensitiveMasked(t *testing.T) {
	s := newTestStore(t)
	s.Record("create", "provider", "p1",
		map[string]any{"api_key": "sk-secretvalue1234567890abcdef", "token": "abc"}, "admin", "", "")
	logs, _, err := s.Query("provider", "", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("logs: %d", len(logs))
	}
	if logs[0].Detail == "" {
		t.Fatal("empty detail")
	}
	if strings.Contains(logs[0].Detail, "sk-secretvalue1234567890abcdef") {
		t.Fatalf("api_key not masked: %q", logs[0].Detail)
	}
	if strings.Contains(logs[0].Detail, "abc") {
		t.Fatalf("token not masked: %q", logs[0].Detail)
	}
}

func TestQueryFilters(t *testing.T) {
	s := newTestStore(t)
	s.Record("create", "provider", "provA", "", "admin", "", "")
	s.Record("update", "provider", "provB", "", "admin", "", "")
	s.Record("create", "key", "keyX", "", "admin", "", "")

	if logs, total, _ := s.Query("provider", "", "", 10, 0); total != 2 {
		t.Fatalf("object filter total: %d", total)
	} else if len(logs) != 2 {
		t.Fatalf("object filter len: %d", len(logs))
	}

	if _, total, _ := s.Query("", "create", "", 10, 0); total != 2 {
		t.Fatalf("action filter total: %d", total)
	}

	if logs, total, _ := s.Query("", "", "provA", 10, 0); total != 1 {
		t.Fatalf("object_id filter total: %d", total)
	} else if logs[0].ObjectID != "provA" {
		t.Fatalf("object_id filter: %q", logs[0].ObjectID)
	}

	// limit clamping: <=0 or >500 → 100
	if logs, _, _ := s.Query("", "", "", 0, 0); len(logs) != 3 {
		t.Fatalf("limit 0 clamp: %d", len(logs))
	}
	if logs, _, _ := s.Query("", "", "", 1000, 0); len(logs) != 3 {
		t.Fatalf("limit >500 clamp: %d", len(logs))
	}
	if logs, _, _ := s.Query("", "", "", 2, 0); len(logs) != 2 {
		t.Fatalf("limit 2: %d", len(logs))
	}
	// offset (DESC by id: keyX, provB, provA → offset 1 drops keyX)
	if logs, _, _ := s.Query("", "", "", 10, 1); len(logs) != 2 {
		t.Fatalf("offset 1: %d", len(logs))
	}
}

func TestNilStoreSafety(t *testing.T) {
	var s *Store
	s.Record("create", "provider", "p1", "d", "op", "ip", "ua") // must not panic
	if _, _, err := s.Query("", "", "", 10, 0); err == nil {
		t.Fatal("expected error for nil store query")
	}
}

func TestSetRetentionDays(t *testing.T) {
	s := newTestStore(t)
	s.SetRetentionDays(30)
	if s.retentionDays != 30 {
		t.Fatalf("retention: %d", s.retentionDays)
	}
	s.SetRetentionDays(0) // ignored
	if s.retentionDays != 30 {
		t.Fatalf("retention 0 ignored: %d", s.retentionDays)
	}
	s.SetRetentionDays(-5) // ignored
	if s.retentionDays != 30 {
		t.Fatalf("retention neg ignored: %d", s.retentionDays)
	}
}

func TestRecordDetailTruncation(t *testing.T) {
	s := newTestStore(t)
	long := strings.Repeat("x", 2500)
	s.Record("create", "provider", "p1", long, "admin", "", "")
	logs, _, _ := s.Query("provider", "", "", 10, 0)
	if len(logs) != 1 {
		t.Fatalf("logs: %d", len(logs))
	}
	if len(logs[0].Detail) > 2003 {
		t.Fatalf("detail too long: %d", len(logs[0].Detail))
	}
	if !strings.HasSuffix(logs[0].Detail, "…") {
		t.Fatalf("detail not truncated: %q", logs[0].Detail)
	}
}

func TestRecordUserAgentTruncation(t *testing.T) {
	s := newTestStore(t)
	ua := strings.Repeat("a", 250)
	s.Record("create", "provider", "p1", "", "admin", "", "ua-long-"+ua)
	logs, _, _ := s.Query("provider", "", "", 10, 0)
	if len(logs) != 1 {
		t.Fatalf("logs: %d", len(logs))
	}
	if len(logs[0].UserAgent) != 200 {
		t.Fatalf("ua len: %d", len(logs[0].UserAgent))
	}
}

func TestMaskSensitive(t *testing.T) {
	cases := []struct {
		in             string
		mustNotContain string
	}{
		{`{"api_key":"sk-abcdefghijklmnopqrstuvwx","token":"t"}`, "sk-abcdefghijklmnopqrstuvwx"},
		{`{"password":"secret123","key":"v"}`, "secret123"},
		{`{"safe":"public"}`, ""},
	}
	for _, c := range cases {
		out := maskSensitive(c.in)
		if c.mustNotContain != "" && strings.Contains(out, c.mustNotContain) {
			t.Fatalf("not masked: in=%q out=%q", c.in, out)
		}
	}
	out := maskSensitive(`{"api_key":"sk-abcdefghijklmnopqrstuvwx"}`)
	if !strings.Contains(out, "***") {
		t.Fatalf("no mask marker: %q", out)
	}
	// bare sk- token outside quotes
	if out2 := maskSensitive("sk-abcdefghijklmnopqrstuvwx"); strings.Contains(out2, "abcdefghijklmnopqrstuvwx") {
		t.Fatalf("bare sk- not masked: %q", out2)
	}
}
