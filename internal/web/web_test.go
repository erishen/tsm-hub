package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSPADeepLinks 锁住 SPA 深链行为：未知路径（前端路由）必须 200 回 index.html，
// 不允许被 http.FileServer 的路径规范化 301 弹回根目录。
// 背景：此前 GET /usage 等深链 → 301 → "./" → 根 dashboard，直接访问 URL 全部失效。
func TestSPADeepLinks(t *testing.T) {
	if !Built() {
		t.Skip("frontend not built")
	}
	h, err := Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", "/index.html", "/usage", "/a2a-panel", "/no-such-page"} {
		r := httptest.NewRequest("GET", p, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s -> %d (Location=%q), want 200 (SPA fallback serves index.html)",
				p, w.Code, w.Header().Get("Location"))
		}
		if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Errorf("GET %s content-type = %q, want text/html", p, ct)
		}
	}
}

// TestServesRealAsset 带哈希的静态资源仍正常 200（不能全被 SPA 回退吞掉）。
func TestServesRealAsset(t *testing.T) {
	if !Built() {
		t.Skip("frontend not built")
	}
	h, err := Handler()
	if err != nil {
		t.Fatal(err)
	}
	root, err := FS()
	if err != nil {
		t.Fatal(err)
	}
	// 找一个真实存在的带哈希 JS 资源（Angular 产物文件名含内容哈希）。
	var found string
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 12 && e.Name()[:3] != "in." &&
			!contains(e.Name(), ".html") && !contains(e.Name(), ".txt") {
			found = e.Name()
			break
		}
	}
	if found == "" {
		t.Skip("no hashed asset found")
	}
	r := httptest.NewRequest("GET", "/"+found, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("GET /%s -> %d, want 200", found, w.Code)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
