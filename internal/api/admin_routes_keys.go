package api

import (
	"encoding/json"
	"github.com/erishen/tsm-hub/internal/auth"
	"github.com/erishen/tsm-hub/internal/store"
	"net/http"
	"sort"
	"time"
)


func (s *Server) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"routes": s.store.ListRoutes()})
}



func (s *Server) handleUpsertRoute(w http.ResponseWriter, r *http.Request) {
	var rt store.Route
	if err := json.NewDecoder(r.Body).Decode(&rt); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if err := s.store.UpsertRoute(rt); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	s.recordAudit(r, "upsert", "route", rt.Model, map[string]any{"targets": rt.Targets, "strategy": rt.Strategy})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": rt.Model})
}



func (s *Server) handleDeleteRoute(w http.ResponseWriter, r *http.Request) {
	model := r.PathValue("model")
	if model == "_default" {
		model = "" // 通配兜底路由（空 model）用 _default 哨兵标识
	}
	if err := s.store.DeleteRoute(model); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.recordAudit(r, "delete", "route", model, nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- keys ----------

// topTools 把工具计数 map 转成按次数降序的名字列表（前 n 个）。

func topTools(m map[string]int, n int) []string {
	type kv struct{ k string; v int }
	all := make([]kv, 0, len(m))
	for k, v := range m {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v })
	out := make([]string, 0, min(n, len(all)))
	for i, it := range all {
		if i >= n {
			break
		}
		out = append(out, it.k)
	}
	return out
}



func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	rpm := s.limiter.Snapshot()
	per := s.rec.PerKey()
	out := make([]map[string]any, 0)
	for _, k := range s.store.ListKeys() {
		agg := per[k.ID]
		out = append(out, map[string]any{
			"id": k.ID, "name": k.Name, "prefix": k.Prefix, "enabled": k.Enabled,
			"models": k.Models, "quota": k.Quota,
			"created_at":    k.CreatedAt,
			"expires_at":    k.ExpiresAt,
			"inject_skills": k.InjectSkills,
			"usage":         agg,
			"rpm_current":   rpm[k.ID],
			// 新建 key 的 2 分钟窗口内为 true，前端展示「补看」入口。
			"revealable":    s.revealable(k.ID),
			// 工具归因：该调用方实际执行过 + 声明过的工具（按次数降序）。
			"tools_used":     topTools(s.rec.KeyTools(k.ID), 5),
			"tools_declared": topTools(s.rec.KeyClientTools(k.ID), 5),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}



func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string      `json:"name"`
		Models       []string    `json:"models"`
		Quota        store.Quota `json:"quota"`
		ExpiresIn    int64       `json:"expires_in_seconds"`
		InjectSkills string      `json:"inject_skills"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	plaintext, hash, display, err := auth.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	// 默认注入技能清单：新签发的 key 天然带技能库上下文（可后续通过重新签发调整）。
	if req.InjectSkills == "" {
		req.InjectSkills = "list"
	}
	k := store.APIKey{
		ID:           newID(),
		Name:         req.Name,
		Prefix:       display,
		Hash:         hash,
		Enabled:      true,
		Models:       req.Models,
		Quota:        req.Quota,
		CreatedAt:    time.Now(),
		InjectSkills: req.InjectSkills,
	}
	if req.ExpiresIn > 0 {
		k.ExpiresAt = time.Now().Add(time.Duration(req.ExpiresIn) * time.Second)
	}
	if k.Name == "" {
		k.Name = "key-" + k.ID
	}
	if err := s.store.AddKey(k); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// ⚠️ 明文只在创建响应与短窗口内可重看，之后只能靠哈希校验。
	s.revealMu.Lock()
	s.revealMap[k.ID] = keyReveal{plain: plaintext, until: time.Now().Add(keyRevealWindow)}
	s.revealMu.Unlock()
	s.recordAudit(r, "create", "key", k.ID, map[string]any{"name": k.Name, "models": k.Models, "quota": k.Quota})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": k.ID, "key": plaintext, "prefix": display,
		"warning": "请立即保存该 key；创建后 2 分钟内可从管理台补看，超窗后服务端只保存哈希",
	})
}

// revealable 报告某个 key 是否仍在新签发的明文可重看窗口内。

func (s *Server) revealable(id string) bool {
	s.revealMu.Lock()
	defer s.revealMu.Unlock()
	rv, ok := s.revealMap[id]
	if !ok {
		return false
	}
	if time.Now().After(rv.until) {
		delete(s.revealMap, id)
		return false
	}
	return true
}

// handleRevealKey 在短窗口内重看新建 key 的明文（误关页面兜底）。
// 只对创建后未过窗口的 key 生效；已查看/已过期/老 key 一律 404。

func (s *Server) handleRevealKey(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.revealMu.Lock()
	rv, ok := s.revealMap[id]
	if ok && time.Now().After(rv.until) {
		delete(s.revealMap, id)
		ok = false
	}
	s.revealMu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "not_found",
			"key 明文仅创建后 2 分钟内可重看，且只显示一次；服务端只保存哈希")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "key": rv.plain})
}



func (s *Server) handleToggleKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if err := s.store.SetKeyEnabled(r.PathValue("id"), req.Enabled); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.recordAudit(r, "toggle", "key", r.PathValue("id"), map[string]any{"enabled": req.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": req.Enabled})
}



func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteKey(r.PathValue("id")); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.recordAudit(r, "delete", "key", r.PathValue("id"), nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}



func (s *Server) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string      `json:"name"`
		Models       []string    `json:"models"`
		Quota        store.Quota `json:"quota"`
		InjectSkills string      `json:"inject_skills"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid json: "+err.Error())
		return
	}
	if err := s.store.UpdateKey(r.PathValue("id"), req.Name, req.Models, req.Quota, req.InjectSkills); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.recordAudit(r, "update", "key", r.PathValue("id"), map[string]any{"name": req.Name, "models": req.Models})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- usage ----------

