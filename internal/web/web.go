// Package web 把 Angular 构建产物嵌入二进制，并提供 SPA 回退。
//
// 构建产物目录（internal/web/dist/browser）只放 `ng build` 的输出，
// 未构建时目录里没有 index.html，此时由 placeholder() 返回提示页，
// 所以仓库里不需要维护一份"占位 index.html"去和产物互相覆盖。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// FS 返回前端文件系统（已定位到 dist/browser）。
func FS() (fs.FS, error) {
	sub, err := fs.Sub(distFS, "dist/browser")
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// Built 报告前端产物是否已经构建（存在 index.html）。
func Built() bool {
	root, err := FS()
	if err != nil {
		return false
	}
	info, err := fs.Stat(root, "index.html")
	return err == nil && !info.IsDir()
}

// Handler 返回静态文件处理器：找不到路径时回退到 index.html，交给前端路由。
func Handler() (http.Handler, error) {
	root, err := FS()
	if err != nil {
		return nil, err
	}
	if !Built() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(placeholder()))
		}), nil
	}
	fileServer := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只处理 GET/HEAD，其余交给上层。
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fileServer.ServeHTTP(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(root, path); err != nil {
			// SPA 回退
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/index.html"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	}), nil
}

// placeholder 是前端未构建时的提示页。
func placeholder() string {
	return `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>tsm-gateway 管理台</title>
  <style>
    body { margin: 0; font: 15px/1.7 -apple-system, "PingFang SC", "Microsoft YaHei", sans-serif;
           color: #1f2937; background: #f7f8fa; display: flex; align-items: center;
           justify-content: center; min-height: 100vh; }
    main { max-width: 560px; padding: 32px 36px; background: #fff; border: 1px solid #e5e7eb;
           border-radius: 12px; }
    h1 { margin: 0 0 12px; font-size: 18px; }
    p { margin: 8px 0; color: #4b5563; }
    code { background: #f3f4f6; padding: 2px 6px; border-radius: 4px; font-size: 13px; }
  </style>
</head>
<body>
  <main>
    <h1>管理台前端尚未构建</h1>
    <p>当前二进制里没有打包 Angular 产物，所以这里显示的是占位页。</p>
    <p>在项目根目录执行：</p>
    <p><code>make web-install &amp;&amp; make web-build</code></p>
    <p>然后重新编译并启动。API 不受影响，可直接用 <code>/healthz</code> 与 <code>/v1/*</code>。</p>
  </main>
</body>
</html>
`
}
