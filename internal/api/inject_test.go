package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInjectSystemMessage(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"max_tokens":10}`)
	out, err := injectSystemMessage(body, "[技能] 清单")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Model    string          `json:"model"`
		MaxToken int             `json:"max_tokens"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m.Model != "m" || m.MaxToken != 10 {
		t.Fatalf("other fields lost: %s", out)
	}
	if len(m.Messages) != 2 {
		t.Fatalf("messages len = %d: %s", len(m.Messages), out)
	}
	first := m.Messages[0]
	if first["role"] != "system" || !strings.Contains(first["content"].(string), "技能") {
		t.Fatalf("first message = %+v", first)
	}
	if m.Messages[1]["role"] != "user" {
		t.Fatalf("original message lost: %+v", m.Messages[1])
	}
}

func TestInjectSystemMessageNoMessages(t *testing.T) {
	out, err := injectSystemMessage([]byte(`{"model":"m"}`), "x")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"model":"m"}` {
		t.Fatalf("expected unchanged, got %s", out)
	}
}

func TestInjectSystemMessageBadJSON(t *testing.T) {
	if _, err := injectSystemMessage([]byte(`{not json`), "x"); err == nil {
		t.Fatal("expected error on bad json")
	}
}
