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

// TestInjectWarnBadge 验证告警红点脚本注入：必须在 </head> 前插入、不重复注入、无 </head> 时原样返回。
func TestInjectWarnBadge(t *testing.T) {
	withHead := "<html><head><title>x</title></head><body>hi</body></html>"
	out := injectWarnBadge(withHead)
	if !contains(out, warnBadgeScript) {
		t.Fatal("warn badge script not injected")
	}
	if idx := indexOf(out, warnBadgeScript); idx == -1 || !contains(out[idx:], "</head>") {
		t.Fatalf("script not placed before </head>: %q", out)
	}
	// 幂等：再注入一次不应翻倍。
	if twice := injectWarnBadge(out); countSub(twice, warnBadgeScript) != 1 {
		t.Fatalf("expected exactly 1 injection, got %d", countSub(twice, warnBadgeScript))
	}
	// 没有 </head> 时原样返回。
	noHead := "<html><body>hi</body></html>"
	if got := injectWarnBadge(noHead); got != noHead {
		t.Fatalf("no </head> should return unchanged, got %q", got)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func countSub(s, sub string) int {
	n := 0
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			n++
			i += len(sub) - 1
		}
	}
	return n
}
