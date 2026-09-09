package router

import (
	"encoding/json"
	"strings"
)

// 场景路由：外部调用方无需选择具体路由，model 传 "auto"（或不传）时，
// 网关根据请求内容自动判断走 chat / reason / code / fast 中的一条场景链。
// 判断为规则引擎：零外部依赖、零额外延迟、可解释（rule 字段返回命中信号）。

// 推理/分析信号：需求是"想清楚"而非"快速响应"，优先于代码与快速信号。
var reasonHints = []string{
	"分析", "推理", "为什么", "原理", "解释", "对比", "权衡", "论证", "推导",
	"证明", "设计", "规划", "架构", "方案", "深入", "研究", "评估", "利弊",
	"机制", "因果", "决策", "策略",
}

// 代码信号：生成/修改/修复代码类需求。
var codeHints = []string{
	"代码", "脚本", "函数", "写个", "写一个", "编写", "修复", "bug", "重构",
	"正则", "sql", "shell", "python", "typescript", "javascript", "golang",
	"rust", "html", "css", "json", "yaml", "接口", "命令", "````",
}

// 快速信号：要求简短/快速响应。
var fastHints = []string{
	"一句话", "简短", "快点", "快速", "概括", "摘要", "总结一下", "简述",
}

// ClassifyScene 根据请求体自动选择场景路由。
// 返回 (scene, rule)：scene ∈ {chat, reason, code, fast}；rule 为命中信号（可观测/排障）。
func ClassifyScene(body []byte) (scene, rule string) {
	joined := joinMessages(body)

	// 1) 显式声明工具调用 → 工具场景：agent 循环在服务端执行，需要稳定模型跑循环。
	var req struct {
		Tools []any `json:"tools"`
	}
	if json.Unmarshal(body, &req) == nil && len(req.Tools) > 0 {
		return "chat", "tools"
	}
	// 2) 推理/分析需求（含代码讨论时也优先：分析优先级高于生成）。
	for _, h := range reasonHints {
		if strings.Contains(joined, h) {
			return "reason", "reason:" + h
		}
	}
	// 3) 代码生成/修改需求。
	for _, h := range codeHints {
		if strings.Contains(joined, h) {
			return "code", "code:" + h
		}
	}
	// 4) 快速简短需求。
	for _, h := range fastHints {
		if strings.Contains(joined, h) {
			return "fast", "fast:" + h
		}
	}
	return "chat", "default"
}

// joinMessages 提取请求里所有 user 消息文本（支持 string 与多模态 parts 两种 content 形态）。
// 只取 role=user：system 可能是网关注入的技能库/系统提示，不代表用户意图，不能参与场景分类。
func joinMessages(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, m := range req.Messages {
		if m.Role != "user" {
			continue
		}
		switch c := m.Content.(type) {
		case string:
			sb.WriteString(c)
			sb.WriteByte('\n')
		case []any:
			for _, part := range c {
				if p, ok := part.(map[string]any); ok {
					if s, ok := p["text"].(string); ok {
						sb.WriteString(s)
						sb.WriteByte('\n')
					}
				}
			}
		}
	}
	return strings.ToLower(sb.String())
}
