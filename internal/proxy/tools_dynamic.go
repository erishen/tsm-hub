package proxy

// 动态工具注入（P2）+ tool_search 元工具（P3）：
//
//   - P2：agent 不再把全量工具 schema 发给上游，而是「常驻核心工具
//     （core_tools）+ 按用户请求文本检索出的 top-K 相关工具」。
//   - P3：动态注入有漏选风险，因此同时注入 tool_search 元工具——模型发现
//     需要的工具不在本轮 schema 里时，先用它按关键词检索完整工具目录，
//     拿到名称/说明/参数后直接按名调用（execTool 按名字分发，天然支持）。
//
// 检索是纯关键词打分（英文名词 token + 中文 bigram 对工具名/描述匹配），
// 零外部依赖、确定性可测试。工具池很小（<= 核心数+topK+4）时直接全量，
// 此时动态裁剪没有收益，反而徒增漏选面。

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	toolSearchName      = "tool_search"
	defaultDynamicTopK  = 8
	maxDynamicTopK      = 20
	searchResultLimit   = 8
	searchResultMaxByte = 8 << 10
	dynamicQueryMaxRune = 2000
)

// defaultCoreTools 是动态模式的默认常驻核心集：高频通用工具，
// 几乎所有任务都用得上，不值得每次按相关度竞争。
var defaultCoreTools = []string{"calc", "fetch_url", "get_time", "remember", "recall", "skill-run"}

// toolEntry 是工具目录项（从 OpenAI tool schema 反解）。
type toolEntry struct {
	name   string
	desc   string
	schema map[string]any
}

// catalogEntries 把 tool schemas 反解成目录项。
func catalogEntries(schemas []map[string]any) []toolEntry {
	out := make([]toolEntry, 0, len(schemas))
	for _, s := range schemas {
		fn, _ := s["function"].(map[string]any)
		if fn == nil {
			continue
		}
		name, _ := fn["name"].(string)
		if name == "" {
			continue
		}
		desc, _ := fn["description"].(string)
		out = append(out, toolEntry{name: name, desc: desc, schema: s})
	}
	return out
}

// toolScore 对 (query, 工具) 打相关度分：拉丁 token 命中工具名 +5、描述 +2；
// 中文 bigram 命中工具名 +6、描述 +2。0 = 无关。
func toolScore(query, name, desc string) int {
	if strings.TrimSpace(query) == "" {
		return 0
	}
	n := strings.ToLower(name)
	d := strings.ToLower(desc)
	score := 0
	for _, tok := range tokenizeLatin(query) {
		if len(tok) < 2 {
			continue // 单字符噪声太强
		}
		if strings.Contains(n, tok) {
			score += 5
		}
		if strings.Contains(d, tok) {
			score += 2
		}
	}
	for _, g := range cjkBigrams(query) {
		if strings.Contains(n, g) {
			score += 6
		}
		if strings.Contains(d, g) {
			score += 2
		}
	}
	return score
}

// tokenizeLatin 切出查询里的拉丁/数字 token（含 _ -，便于匹配工具名片段）。
func tokenizeLatin(q string) []string {
	var toks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	for _, r := range q {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			cur.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return toks
}

// cjkBigrams 抽取查询里的中文 2-gram（去重）；孤立单字原样保留。
func cjkBigrams(q string) []string {
	seen := map[string]bool{}
	var out []string
	var run []rune
	flush := func() {
		for i := 0; i < len(run); i++ {
			g := ""
			if i+1 < len(run) {
				g = string(run[i : i+2])
			} else if len(run) == 1 {
				g = string(run)
			}
			if g != "" && !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
		run = run[:0]
	}
	for _, r := range q {
		if r >= 0x4E00 && r <= 0x9FFF {
			run = append(run, r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// coreToolSet 返回动态模式的常驻核心工具集合。
func (p *Proxy) coreToolSet() map[string]bool {
	names := p.store.Settings().Agent.CoreTools
	if len(names) == 0 {
		names = defaultCoreTools
	}
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// dynamicTopK 返回动态注入的相关工具上限（默认 8，封顶 20）。
func (p *Proxy) dynamicTopK() int {
	k := p.store.Settings().Agent.DynamicTopK
	if k <= 0 {
		k = defaultDynamicTopK
	}
	if k > maxDynamicTopK {
		k = maxDynamicTopK
	}
	return k
}

// dynamicToolSchemas 构造动态工具 schema 集：常驻核心 + top-K 相关 +
// tool_search。池子太小时直接全量（动态裁剪无收益）。
// 入参复用 P1 的白名单语义（nil = 不限制），核心/相关工具都只从
// 白名单过滤后的池子里选，两层裁剪天然可组合。
func (p *Proxy) dynamicToolSchemas(query string, serverAllow, toolAllow map[string]bool) []map[string]any {
	schemas := p.toolSchemasAllow(serverAllow, toolAllow)
	entries := catalogEntries(schemas)
	core := p.coreToolSet()
	topK := p.dynamicTopK()
	if len(entries) <= len(core)+topK+4 {
		// 小池子：裁不出什么，全量更稳。
		return append(schemas, toolSearchSchema())
	}
	var out []map[string]any
	type scored struct {
		e toolEntry
		s int
	}
	rest := make([]scored, 0, len(entries))
	for _, e := range entries {
		if core[e.name] {
			out = append(out, e.schema)
			continue
		}
		rest = append(rest, scored{e, toolScore(query, e.name, e.desc)})
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].s > rest[j].s })
	added := 0
	for _, r := range rest {
		if r.s <= 0 || added >= topK {
			break
		}
		out = append(out, r.e.schema)
		added++
	}
	return append(out, toolSearchSchema())
}

// toolSearchSchema 是 tool_search 元工具的 OpenAI tool schema。
func toolSearchSchema() map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": toolSearchName,
			"description": "搜索网关的完整工具目录（含当前未注入的工具）。返回匹配工具的名称、说明与参数定义；" +
				"拿到结果后可直接按该名称调用对应工具。当你需要的工具不在本轮工具列表里时，先用本工具检索。",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "检索关键词（中英文均可，匹配工具名与功能描述），如 文件/汇率/exchange",
					},
					"limit": map[string]any{
						"type":        "number",
						"description": "最多返回条数（默认 8，最大 8）",
					},
				},
				"required": []string{"query"},
			},
		},
	}
}

// toolSearchText 执行 tool_search：按关键词检索完整工具目录（同样遵守
// per-key 白名单），返回可读的匹配清单（名称 + 说明 + 参数 JSON）。
// 全部无命中时退回「全量工具名列表 + 换关键词提示」，避免模型死胡同。
func (p *Proxy) toolSearchText(query string, serverAllow, toolAllow map[string]bool) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return "error: missing 'query'"
	}
	entries := catalogEntries(p.toolSchemasAllow(serverAllow, toolAllow))
	type hit struct {
		e toolEntry
		s int
	}
	var hits []hit
	for _, e := range entries {
		if s := toolScore(query, e.name, e.desc); s > 0 {
			hits = append(hits, hit{e, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].s > hits[j].s })
	if len(hits) == 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.name)
		}
		return fmt.Sprintf("没有匹配 %q 的工具。当前目录共 %d 个工具：\n%s\n请换更具体的中英文关键词重试。",
			query, len(entries), strings.Join(names, ", "))
	}
	limit := searchResultLimit
	if len(hits) < limit {
		limit = len(hits)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "匹配到 %d 个工具（按相关度排序，展示前 %d 个）:\n", len(hits), limit)
	total := 0
	cut := false
	for i := 0; i < limit; i++ {
		e := hits[i].e
		params, _ := json.Marshal(e.schema["function"].(map[string]any)["parameters"])
		block := fmt.Sprintf("\n[%d] %s\n  说明: %s\n  参数: %s\n", i+1, e.name, e.desc, params)
		if total+len(block) > searchResultMaxByte {
			cut = true
			break
		}
		total += len(block)
		sb.WriteString(block)
	}
	if cut {
		sb.WriteString("\n…(结果过长已截断，请用更精确的关键词缩小范围)\n")
	}
	// 始终附上全量工具名（仅名字，开销极小）：目录里有英文描述的 MCP 工具，
	// 中文关键词可能打 0 分而漏掉；名字（如 mcp_fs_list_directory）通常
	// 足够让模型推断出该不该调。
	if len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.name)
		}
		fmt.Fprintf(&sb, "\n目录全部工具名: %s\n", strings.Join(names, ", "))
	}
	sb.WriteString("\n拿到需要的工具后，直接按上面的名称与参数定义调用即可。")
	return sb.String()
}

// userContextText 汇总消息列表里的用户文本（动态检索的 query 源），
// 截到 dynamicQueryMaxRune，防止超长对话拖慢打分。
func userContextText(msgs []chatMessage) string {
	var parts []string
	for _, m := range msgs {
		if m["role"] != "user" {
			continue
		}
		if s, ok := m["content"].(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	s := strings.Join(parts, "\n")
	if r := []rune(s); len(r) > dynamicQueryMaxRune {
		s = string(r[:dynamicQueryMaxRune])
	}
	return s
}
