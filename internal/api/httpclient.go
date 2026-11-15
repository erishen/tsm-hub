package api

import (
	"net/http"
	"time"
)

// adminHTTPClient 是管理台侧外部调用的共享 client。
// 显式设置 Timeout（20s）作为兜底，避免任何新增调用方忘记在 request 上
// 设置 context 超时时导致 goroutine 永久阻塞（http.DefaultClient 的 Timeout 为 0）。
// 各调用点仍按需在 request 上设置更紧的 context.WithTimeout，
// Client.Timeout 与 context 超时取更紧者生效。
var adminHTTPClient = &http.Client{Timeout: 20 * time.Second}
