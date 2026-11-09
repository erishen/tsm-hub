package api

// admin_a2a.go — 管理台 A2A 能力盘的 REST 出口。
//
// 任务落在 data/a2a-tasks/<node>/，前端不能直读文件（也在容器里），
// 统一走这里：admin 鉴权 + 只读快照 + 同步调用。

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/erishen/tsm-hub/internal/a2a"
)

// handleA2AOverview 返回节点名 / 能力数 / peer 模式 / 活跃任务数 / uptime。
func (s *Server) handleA2AOverview(w http.ResponseWriter, r *http.Request) {
	if s.a2a == nil {
		writeError(w, http.StatusServiceUnavailable, "a2a_disabled", "A2A 面未启用")
		return
	}
	writeJSON(w, http.StatusOK, s.a2a.AdminOverview())
}

// handleA2ATasks 返回最近 n 条任务（?n= 缺省 20，上限 200）。
func (s *Server) handleA2ATasks(w http.ResponseWriter, r *http.Request) {
	if s.a2a == nil {
		writeError(w, http.StatusServiceUnavailable, "a2a_disabled", "A2A 面未启用")
		return
	}
	n := 20
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			n = parsed
		}
	}
	if n <= 0 {
		n = 20
	}
	if n > 200 {
		n = 200
	}
	tasks := s.a2a.RecentTasks(n)
	if tasks == nil {
		tasks = []*a2a.Task{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"node": s.a2a.Node, "tasks": tasks})
}

// handleA2AInvoke 同步执行一个已登记能力（管理台"快速调用"入口）。
// body: {"tool":"tsm.skill","arguments":{"name":"code-review"}}
func (s *Server) handleA2AInvoke(w http.ResponseWriter, r *http.Request) {
	if s.a2a == nil {
		writeError(w, http.StatusServiceUnavailable, "a2a_disabled", "A2A 面未启用")
		return
	}
	var body struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "body 不是合法 JSON")
		return
	}
	if body.Tool == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "缺少 tool 字段")
		return
	}
	// 未登记能力 → 404（区别于"执行失败"，前端可提示"该能力不存在"）。
	res := s.a2a.InvokeCapability(body.Tool, body.Arguments)
	if errMsg, ok := res["error"].(string); ok && errMsg != "" {
		writeJSON(w, http.StatusNotFound, res)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
