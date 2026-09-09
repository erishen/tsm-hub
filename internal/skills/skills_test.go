package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSkill(t *testing.T, dir, name, fm, body string) {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(fm+body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListParsesFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "code-review", "---\nname: code-review\ndescription: Review code files and output structured report\n---\n\n# Instructions\n\nDo things.\n", "")
	if err := os.MkdirAll(filepath.Join(dir, "code-review", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "code-review", "scripts", "stats.py"), []byte("print(1)"), 0o644)
	writeSkill(t, dir, "rust-review", "---\ndescription: \"Rust conventions review\"\n---\n\nBody.\n", "")

	l := New(dir)
	list := l.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(list))
	}
	// 按 name 排序
	if list[0].Name != "code-review" || list[1].Name != "rust-review" {
		t.Fatalf("unexpected order: %+v", list)
	}
	if list[0].Description != "Review code files and output structured report" {
		t.Fatalf("desc = %q", list[0].Description)
	}
	if !list[0].HasScripts || len(list[0].Scripts) != 1 || list[0].Scripts[0] != "stats.py" {
		t.Fatalf("scripts = %+v", list[0].Scripts)
	}
	// 无 name frontmatter 时回退目录名
	if list[1].Name != "rust-review" {
		t.Fatalf("fallback name = %q", list[1].Name)
	}
}

func TestGetReturnsBody(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "weekly-investment",
		"---\nname: weekly-investment\ndescription: Portfolio weekly report\n---\n\n# Weekly\n\nStep 1.\n", "")
	l := New(dir)
	d, ok := l.Get("weekly-investment")
	if !ok {
		t.Fatal("get failed")
	}
	if d.Name != "weekly-investment" || d.Description != "Portfolio weekly report" {
		t.Fatalf("summary = %+v", d.Summary)
	}
	if !contains(d.Body, "# Weekly") || contains(d.Body, "description:") {
		t.Fatalf("body should exclude frontmatter: %q", d.Body)
	}
	if !contains(d.Raw, "---") {
		t.Fatal("raw should include frontmatter")
	}
}

func TestGetRejectsTraversal(t *testing.T) {
	l := New(t.TempDir())
	if _, ok := l.Get("../../etc/passwd"); ok {
		t.Fatal("path traversal should be rejected")
	}
}

func TestEmptyDir(t *testing.T) {
	l := New("")
	if got := l.List(); got != nil {
		t.Fatalf("expected nil list, got %v", got)
	}
	if _, ok := l.Get("x"); ok {
		t.Fatal("expected not found")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
