// Package skills 挂载外部 Agent Skills 技能库（Agent Skills 标准：每个技能
// 一个目录，含 SKILL.md frontmatter + 可选 scripts/references/assets）。
// 只读浏览：tsm-gateway 管理台通过 /api/admin/skills 展示技能清单与全文。
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Summary 是技能列表条目（frontmatter 摘要 + 结构）。
type Summary struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	HasScripts  bool     `json:"has_scripts"`
	Scripts     []string `json:"scripts"`
	HasRefs     bool     `json:"has_refs"`
	HasAssets   bool     `json:"has_assets"`
	MDFile      string   `json:"-"`
}

// Detail 是单个技能详情：frontmatter + 正文全文。
type Detail struct {
	Summary
	Body    string `json:"body"`              // SKILL.md frontmatter 之后的正文
	Raw     string `json:"raw"`               // SKILL.md 全文（含 frontmatter）
	Size    int64  `json:"size"`
	Updated string `json:"updated"`
}

// Library 是技能库加载器。
type Library struct {
	dir string
}

// New 创建技能库；dir 为空或不存在时返回空库（不报错，接口返回空列表）。
func New(dir string) *Library {
	return &Library{dir: dir}
}

// Dir 返回配置的技能目录。
func (l *Library) Dir() string { return l.dir }

// List 扫描技能库，返回按名称排序的摘要列表。
func (l *Library) List() []Summary {
	if l.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil
	}
	out := make([]Summary, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		sum, ok := l.loadSummary(e.Name())
		if !ok {
			continue
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get 返回单个技能详情；技能不存在或解析失败时 ok=false。
func (l *Library) Get(name string) (Detail, bool) {
	if l.dir == "" || name == "" || filepath.Base(name) != name {
		return Detail{}, false
	}
	md := filepath.Join(l.dir, name, "SKILL.md")
	raw, err := os.ReadFile(md)
	if err != nil {
		return Detail{}, false
	}
	sum, ok := l.loadSummary(name)
	if !ok {
		sum = Summary{Name: name}
	}
	info, _ := os.Stat(md)
	d := Detail{
		Summary: sum,
		Raw:     string(raw),
		Size:    int64(len(raw)),
	}
	if info != nil {
		d.Updated = info.ModTime().Format("2006-01-02 15:04")
	}
	// 去掉 frontmatter 取正文
	body := string(raw)
	if fm := parseFrontmatter(body); fm != nil {
		if idx := strings.Index(body, "---\n"); idx >= 0 {
			rest := body[idx+4:]
			if i2 := strings.Index(rest, "---"); i2 >= 0 {
				body = rest[i2+3:]
				body = strings.TrimPrefix(body, "\n")
			}
		}
	}
	d.Body = body
	return d, true
}

// loadSummary 解析单个技能的 frontmatter 与目录结构。
func (l *Library) loadSummary(name string) (Summary, bool) {
	md := filepath.Join(l.dir, name, "SKILL.md")
	raw, err := os.ReadFile(md)
	if err != nil {
		return Summary{}, false
	}
	sum := Summary{
		Name:   name,
		MDFile: md,
	}
	fm := parseFrontmatter(string(raw))
	if fm != nil {
		if v, ok := fm["name"]; ok && v != "" {
			sum.Name = strings.TrimSpace(v)
		}
		if v, ok := fm["description"]; ok {
			sum.Description = strings.TrimSpace(v)
		}
	}
	if st, err := os.Stat(filepath.Join(l.dir, name, "scripts")); err == nil && st.IsDir() {
		sum.HasScripts = true
		if files, err := os.ReadDir(filepath.Join(l.dir, name, "scripts")); err == nil {
			for _, f := range files {
				if !f.IsDir() {
					sum.Scripts = append(sum.Scripts, f.Name())
				}
			}
			sort.Strings(sum.Scripts)
		}
	}
	if st, err := os.Stat(filepath.Join(l.dir, name, "references")); err == nil && st.IsDir() {
		sum.HasRefs = true
	}
	if st, err := os.Stat(filepath.Join(l.dir, name, "assets")); err == nil && st.IsDir() {
		sum.HasAssets = true
	}
	return sum, true
}

// Render 构造注入 system prompt 的技能文本。mode 取值：
//   "list" —— 技能清单（名称 + 描述），轻量，适合"你有什么技能"类问答；
//   "all"  —— 全部技能的 SKILL.md 全文；
//   其他   —— 单个技能名，注入该技能的 SKILL.md 全文。
// 技能库为空、技能不存在或 mode 为空时返回 ""（调用方不注入）。
func (l *Library) Render(mode string) string {
	if l.dir == "" || mode == "" {
		return ""
	}
	switch mode {
	case "list":
		list := l.List()
		if len(list) == 0 {
			return ""
		}
		var b strings.Builder
		b.WriteString("[tsm-gateway 技能库] 本网关挂载了以下 Agent Skills（技能名: 用途）：\n")
		for _, s := range list {
			fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
		}
		b.WriteString("\n技能说明已内联在上下文中，不要尝试读取任何文件路径（不存在）；当用户要求执行某个技能时，直接依据上面的说明执行；不确定用哪个技能时先列出清单询问。")
		return b.String()
	case "all":
		list := l.List()
		if len(list) == 0 {
			return ""
		}
		var b strings.Builder
		b.WriteString("[tsm-gateway 技能库] 以下是网关挂载的全部技能（SKILL.md 全文已内联，按此执行；不要尝试读取任何文件路径）：\n")
		for _, s := range list {
			d, ok := l.Get(s.Name)
			if !ok {
				continue
			}
			fmt.Fprintf(&b, "\n===== 技能 %s =====\n%s\n", d.Name, d.Raw)
		}
		return b.String()
	default:
		d, ok := l.Get(mode)
		if !ok {
			return ""
		}
		return fmt.Sprintf("[tsm-gateway 技能库] 技能 %s 的 SKILL.md（全文已内联，按此执行；不要尝试读取任何文件路径）：\n%s", d.Name, d.Raw)
	}
}

// parseFrontmatter 解析 YAML frontmatter 的 name/description（Agent Skills 标准最小集）。
func parseFrontmatter(md string) map[string]string {
	if !strings.HasPrefix(md, "---\n") {
		return nil
	}
	rest := md[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil
	}
	fm := make(map[string]string)
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key != "name" && key != "description" {
			continue
		}
		val = strings.Trim(val, "\"'")
		if key == "description" {
			// 多行描述折叠为单行
			val = strings.Join(strings.Fields(val), " ")
		}
		fm[key] = val
	}
	if len(fm) == 0 {
		return nil
	}
	return fm
}
