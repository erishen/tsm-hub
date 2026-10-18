package api

import (
	"testing"

	"github.com/erishen/tsm-hub/internal/store"
)

func TestCORSWildcardWithCredentialsRejected(t *testing.T) {
	// allow_credentials + "*" 不得回显任意源（否则任意站点可发带凭证请求）。
	m := newCORSMiddleware(store.CORSCfg{AllowCredentials: true, AllowedOrigins: []string{"*"}})
	if m.originAllowed("https://evil.example") {
		t.Fatal("wildcard + credentials must not allow an arbitrary origin")
	}
	if len(m.cfg.AllowedOrigins) != 0 {
		t.Fatalf("wildcard should be stripped, got %v", m.cfg.AllowedOrigins)
	}
}

func TestCORSWildcardWithoutCredentialsAllowed(t *testing.T) {
	m := newCORSMiddleware(store.CORSCfg{AllowedOrigins: []string{"*"}})
	if !m.originAllowed("https://any.example") {
		t.Fatal("wildcard without credentials should allow any origin")
	}
}

func TestCORSExplicitOriginWithCredentialsAllowed(t *testing.T) {
	m := newCORSMiddleware(store.CORSCfg{
		AllowCredentials: true,
		AllowedOrigins:   []string{"https://app.example.com", "*"},
	})
	if !m.originAllowed("https://app.example.com") {
		t.Fatal("explicit origin should be allowed with credentials")
	}
	if m.originAllowed("https://evil.example") {
		t.Fatal("non-listed origin must be rejected")
	}
}
