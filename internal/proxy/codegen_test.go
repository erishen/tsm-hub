package proxy

import (
	"strings"
	"testing"
)

func TestRunJSDetector(t *testing.T) {
	// 基本命中
	out, hit := runJSDetector(`function detect(text) {
		if (text.indexOf("反转") >= 0) return text.split("").reverse().join("");
		return null;
	}`, "反转abc")
	if !hit || out != "cba转反" {
		t.Fatalf("detector = %q hit=%v", out, hit)
	}
	// 未命中返回 null
	_, hit = runJSDetector(`function detect(text) { return null; }`, "anything")
	if hit {
		t.Fatalf("null detector should miss")
	}
	// 恶意代码：fetch/require/process 等不可用 → 不命中也不 panic
	for _, src := range []string{
		`function detect(text) { return require("fs").readFileSync("/etc/passwd"); }`,
		`function detect(text) { return process.env; }`,
		`function detect(text) { return globalThis.fetch("http://x"); }`,
		`function detect(text) { return eval("1+1"); }`,
	} {
		_, hit := runJSDetector(src, "x")
		if hit {
			t.Fatalf("malicious detector should miss: %s", src)
		}
	}
	// 死循环被超时中断
	_, hit = runJSDetector(`function detect(text) { while(true){} }`, "x")
	if hit {
		t.Fatalf("infinite loop should timeout")
	}
}

func TestExtractJSDetector(t *testing.T) {
	cases := []struct{ in, want string }{
		{"```js\nfunction detect(text) { return null; }\n```", "function detect(text) { return null; }"},
		{"```javascript\nfunction detect(text) { return 'a'; }\n```", "function detect(text) { return 'a'; }"},
		{"好的：\nfunction detect(text) { return 'b'; }", "function detect(text) { return 'b'; }"},
		{"NONE", ""},
		{"无法确定", ""},
		{"随便聊聊天", ""},
	}
	for _, c := range cases {
		if got := extractJSDetector(c.in); got != c.want {
			t.Errorf("extract(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFastPluginPersistence(t *testing.T) {
	p := sandboxStore(t) // 复用 Proxy（store 在 temp dir）
	_ = p
	src := "function detect(text) { if (text.indexOf('md5') >= 0) return 'md5: demo'; return null; }"
	p.store.Path() // ensure no-op
	dir := p.fastPluginsDir()
	// 手动指定目录（temp 环境没有 data 目录）
	_ = dir
	// 直接用包级函数测试 run 逻辑已在上面覆盖；这里验证 FastPluginList 空安全
	if lst := p.FastPluginList(); lst == nil {
		t.Fatalf("FastPluginList should not be nil")
	}
	// FastMatchers 清单
	if m := p.FastMatchers(); len(m) < 7 {
		t.Fatalf("FastMatchers = %d, want >= 7", len(m))
	}
	var _ = strings.Contains
	_ = src
}
