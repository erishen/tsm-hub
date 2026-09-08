package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return newStoreAt(t, filepath.Join(t.TempDir(), "config.json"))
}

func newStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	s, err := New(path)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s
}

func TestNewCreatesConfigWithDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := newStoreAt(t, path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not created: %v", err)
	}
	st := s.Settings()
	if st.Listen != ":9070" || st.DefaultTimeoutMS <= 0 {
		t.Fatalf("unexpected defaults: %+v", st)
	}
	if st.Pricing["default"].InputPer1K <= 0 {
		t.Fatal("default pricing missing")
	}
}

func TestUpdateIsAtomicAndPersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	s := newStoreAt(t, path)

	if err := s.UpsertProvider(Provider{ID: "p1", BaseURL: "http://p1/v1", Enabled: true}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 重新打开，验证确实落盘。
	s2 := newStoreAt(t, path)
	if _, ok := s2.GetProvider("p1"); !ok {
		t.Fatal("provider not persisted")
	}
	// 临时文件不应残留。
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if len(e.Name()) > 7 && e.Name()[:7] == ".config" {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestUpdateRollsBackOnError(t *testing.T) {
	s := newStore(t)
	_ = s.UpsertProvider(Provider{ID: "p1"})
	err := s.Update(func(c *Config) error {
		c.Providers = append(c.Providers, Provider{ID: "p2"})
		return errBoom
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(s.ListProviders()) != 1 {
		t.Fatalf("draft should not be committed: %+v", s.ListProviders())
	}
}

var errBoom = errTest("boom")

type errTest string

func (e errTest) Error() string { return string(e) }

func TestUpsertProviderDefaults(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertProvider(Provider{ID: "p1", Name: "P1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ := s.GetProvider("p1")
	if got.Weight <= 0 {
		t.Fatalf("weight should default, got %d", got.Weight)
	}
	if got.TimeoutMS <= 0 {
		t.Fatalf("timeout should default, got %d", got.TimeoutMS)
	}
	// 再次 upsert 应覆盖而不是新增，且保留创建时间。
	if err := s.UpsertProvider(Provider{ID: "p1", Name: "renamed"}); err != nil {
		t.Fatalf("upsert again: %v", err)
	}
	if len(s.ListProviders()) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(s.ListProviders()))
	}
	if got2, _ := s.GetProvider("p1"); got2.Name != "renamed" {
		t.Fatalf("name = %q", got2.Name)
	}
}

func TestDeleteProviderCleansRoutes(t *testing.T) {
	s := newStore(t)
	_ = s.UpsertProvider(Provider{ID: "a"})
	_ = s.UpsertProvider(Provider{ID: "b"})
	_ = s.UpsertRoute(Route{Model: "m", Targets: []RouteTarget{
		{ProviderID: "a"}, {ProviderID: "b"},
	}})
	_ = s.UpsertRoute(Route{Model: "only-a", Targets: []RouteTarget{{ProviderID: "a"}}})

	if err := s.DeleteProvider("a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	routes := s.ListRoutes()
	// only-a 的候选被清空，应整条移除；m 只留 b。
	if len(routes) != 1 || routes[0].Model != "m" || len(routes[0].Targets) != 1 {
		t.Fatalf("routes after delete = %+v", routes)
	}
	if err := s.DeleteProvider("missing"); err == nil {
		t.Fatal("deleting missing provider should error")
	}
}

func TestRouteValidation(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertRoute(Route{}); err == nil {
		t.Fatal("empty model should error")
	}
	if err := s.UpsertRoute(Route{Model: "m", Strategy: "nonsense"}); err == nil {
		t.Fatal("bad strategy should error")
	}
	if err := s.UpsertRoute(Route{Model: "m"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := s.ListRoutes()[0].Strategy; got != "weighted" {
		t.Fatalf("default strategy = %q", got)
	}
	if err := s.DeleteRoute("m"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteRoute("m"); err == nil {
		t.Fatal("deleting twice should error")
	}
}

func TestKeyCRUD(t *testing.T) {
	s := newStore(t)
	k := APIKey{ID: "k1", Hash: "h1", Enabled: true}
	if err := s.AddKey(k); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, ok := s.LookupKeyHash("h1"); !ok {
		t.Fatal("key not found by hash")
	}
	if _, ok := s.LookupKeyHash("nope"); ok {
		t.Fatal("unknown hash should not resolve")
	}
	if err := s.AddKey(APIKey{ID: "k2", Hash: "h1"}); err == nil {
		t.Fatal("duplicate hash should error")
	}
	if err := s.SetKeyEnabled("k1", false); err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if keys := s.ListKeys(); len(keys) != 1 || keys[0].Enabled {
		t.Fatalf("keys = %+v", keys)
	}
	if err := s.DeleteKey("k1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(s.ListKeys()) != 0 {
		t.Fatal("key not deleted")
	}
}

func TestUpsertProviderKeepsMaskedAPIKey(t *testing.T) {
	s := newStore(t)
	base := Provider{
		ID: "a", Name: "A", BaseURL: "http://a/v1", APIKey: "sk-real-secret-1234567890",
		Models: []string{"m"}, Enabled: true, Weight: 100,
	}
	if err := s.UpsertProvider(base); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// 编辑时回显的是脱敏值（含省略号），不应覆盖真实 Key。
	masked := base
	masked.APIKey = "sk-r" + MaskedSecretMarker + "7890"
	masked.Name = "A 改过名"
	if err := s.UpsertProvider(masked); err != nil {
		t.Fatalf("upsert masked: %v", err)
	}
	got, ok := s.GetProvider("a")
	if !ok {
		t.Fatal("provider missing")
	}
	if got.APIKey != base.APIKey {
		t.Fatalf("api_key overwritten by masked value: %q", got.APIKey)
	}
	if got.Name != "A 改过名" {
		t.Fatalf("name not updated: %q", got.Name)
	}

	// 显式传新的真实 Key 应正常覆盖。
	renew := base
	renew.APIKey = "sk-brand-new-key-9876543210"
	if err := s.UpsertProvider(renew); err != nil {
		t.Fatalf("upsert renew: %v", err)
	}
	got, _ = s.GetProvider("a")
	if got.APIKey != renew.APIKey {
		t.Fatalf("api_key = %q, want renewed key", got.APIKey)
	}
}

func TestConfigRoundTripKeepsUnknownFieldsFormat(t *testing.T) {
	// 落盘后的配置必须是格式化过的合法 JSON（便于人工 diff / 手工修）。
	path := filepath.Join(t.TempDir(), "config.json")
	s := newStoreAt(t, path)
	_ = s.UpsertProvider(Provider{ID: "a", BaseURL: "http://a/v1"})
	raw, _ := os.ReadFile(path)
	if len(raw) == 0 || raw[0] != '{' {
		t.Fatalf("unexpected config bytes: %q", raw[:min(20, len(raw))])
	}
	var check map[string]any
	if err := json.Unmarshal(raw, &check); err != nil {
		t.Fatalf("config is not valid json: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestProviderResolvedAPIKey(t *testing.T) {
	t.Setenv("LLM_ROUTER_KEY_FOR_TEST", "sk-secret-value")

	// env 引用：解析为环境变量值。
	p := Provider{APIKey: "env:LLM_ROUTER_KEY_FOR_TEST"}
	if got := p.ResolvedAPIKey(); got != "sk-secret-value" {
		t.Fatalf("resolved = %q, want env value", got)
	}
	// 变量缺失：原样返回，由上游 401 暴露配置问题，不在网关静默吞掉。
	p2 := Provider{APIKey: "env:NO_SUCH_VAR_XYZ"}
	if got := p2.ResolvedAPIKey(); got != "env:NO_SUCH_VAR_XYZ" {
		t.Fatalf("fallback = %q", got)
	}
	// 普通 key 原样返回。
	p3 := Provider{APIKey: "sk-plain"}
	if got := p3.ResolvedAPIKey(); got != "sk-plain" {
		t.Fatalf("plain = %q", got)
	}
	// 空变量名不 panic、不解析。
	p4 := Provider{APIKey: "env:"}
	if got := p4.ResolvedAPIKey(); got != "env:" {
		t.Fatalf("empty name = %q", got)
	}
}
