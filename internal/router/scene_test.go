package router

import "testing"

func TestClassifyScene(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"默认对话", `{"model":"auto","messages":[{"role":"user","content":"你好"}]}`, "chat"},
		{"显式工具声明", `{"model":"auto","tools":[{"type":"function","function":{"name":"calc"}}],"messages":[{"role":"user","content":"1+1"}]}`, "chat"},
		{"写代码", `{"messages":[{"role":"user","content":"帮我写个 python 脚本计算斐波那契"}]}`, "code"},
		{"修bug", `{"messages":[{"role":"user","content":"这个函数有 bug，帮我修复"}]}`, "code"},
		{"分析优先于代码", `{"messages":[{"role":"user","content":"分析一下这段代码的性能瓶颈"}]}`, "reason"},
		{"为什么", `{"messages":[{"role":"user","content":"为什么天空是蓝色的？"}]}`, "reason"},
		{"快速概括", `{"messages":[{"role":"user","content":"用一句话概括这篇文章"}]}`, "fast"},
		{"多模态 content", `{"messages":[{"role":"user","content":[{"type":"text","text":"帮我设计一个系统架构"}]}]}`, "reason"},
		{"注入 system 不污染分类", `{"messages":[
			{"role":"system","content":"技能库：代码审查、投资分析、新闻营销内容生成"},
			{"role":"user","content":"你好"}]}`, "chat"},
	}
	for _, c := range cases {
		got, rule := ClassifyScene([]byte(c.body))
		if got != c.want {
			t.Errorf("%s: ClassifyScene = %q (rule=%s), want %q", c.name, got, rule, c.want)
		}
	}
}
