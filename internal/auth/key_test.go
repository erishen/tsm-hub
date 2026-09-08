package auth

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateFormat(t *testing.T) {
	plain, hash, display, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(plain, Prefix) {
		t.Fatalf("plain = %q, want prefix %q", plain, Prefix)
	}
	if len(plain) != len(Prefix)+48 {
		t.Fatalf("plain length = %d", len(plain))
	}
	if hash != Hash(plain) {
		t.Fatal("hash mismatch")
	}
	if len(hash) != 64 {
		t.Fatalf("hash length = %d, want 64", len(hash))
	}
	if !strings.Contains(display, "…") || !strings.HasPrefix(display, Prefix) {
		t.Fatalf("display = %q", display)
	}
	// 两次生成不能相同。
	other, _, _, _ := Generate()
	if other == plain {
		t.Fatal("duplicate key generated")
	}
}

func TestExtract(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Bearer sk-tr-abc", "sk-tr-abc"},
		{"bearer sk-tr-abc", "sk-tr-abc"},
		{"sk-tr-abc", "sk-tr-abc"},
		{"  sk-tr-abc  ", "sk-tr-abc"},
		{"", ""},
		{"Bearer ", ""},
	}
	for _, c := range cases {
		if got := Extract(c.in); got != c.want {
			t.Fatalf("Extract(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !ConstantTimeEqual("abc", "abc") {
		t.Fatal("equal strings should match")
	}
	if ConstantTimeEqual("abc", "abd") {
		t.Fatal("different strings should not match")
	}
	if ConstantTimeEqual("abc", "abcd") {
		t.Fatal("different length should not match")
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := NewSession(50 * time.Millisecond)
	tok := s.Issue("secret")
	if !s.Valid(tok) {
		t.Fatal("fresh session should be valid")
	}
	if s.Valid("nope") {
		t.Fatal("unknown token should be invalid")
	}
	s.Revoke(tok)
	if s.Valid(tok) {
		t.Fatal("revoked session should be invalid")
	}

	// 过期后应失效。
	tok2 := s.Issue("secret")
	time.Sleep(60 * time.Millisecond)
	if s.Valid(tok2) {
		t.Fatal("expired session should be invalid")
	}
}

func TestAdminTokenEnv(t *testing.T) {
	t.Setenv("LLM_ROUTER_ADMIN_TOKEN", "from-env")
	if got := AdminToken(""); got != "from-env" {
		t.Fatalf("got %q", got)
	}
	if got := AdminToken("explicit"); got != "explicit" {
		t.Fatalf("explicit should win, got %q", got)
	}
}
