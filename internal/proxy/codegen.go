package proxy

// fastpath codegen + 晋升：当内置快路径未命中时，让 LLM 生成一个 JS 检测器
// （detect(text) -> string|null），用 goja 沙箱执行（无宿主 API、无 I/O、
// 无网络，纯计算），命中即持久化为插件，后续同类问题零模型直接命中。
// 管理台可将插件"晋升"为正式检测器。移植自 resolve-harness codegen.py。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"github.com/erishen/llm-router/internal/store"
)

// fastPlugin 是一个已加载的检测器插件。
type fastPlugin struct {
	Name     string
	Trigger  string
	Source   string
	Promoted bool
	// Mode 是晋升模式：fastpath（拦截）/ tool（注册为工具）/ both（两者）。
	Mode string
	mtime    int64
	size     int64
	vm       *goja.Runtime
	detect   goja.Callable
}

// fastPathMgr 管理插件目录的加载缓存（按文件 mtime/size 失效热重载）。
type fastPathMgr struct {
	mu      sync.Mutex
	plugins []*fastPlugin
	stats   map[string][2]int64
}

func (p *Proxy) fastPluginsDir() string {
	if d := p.store.Settings().Fastpath.PluginsDir; d != "" {
		return d
	}
	return filepath.Join(filepath.Dir(p.store.Path()), "fastpath_plugins")
}

func (p *Proxy) fastPromotedDir() string {
	return filepath.Join(filepath.Dir(p.store.Path()), "fastpath_promoted")
}

// fastModesFile 是晋升插件的模式元数据（name -> fastpath/tool/both）。
func (p *Proxy) fastModesFile() string {
	return filepath.Join(p.fastPromotedDir(), "modes.json")
}

func (p *Proxy) readFastModes() map[string]string {
	raw, err := os.ReadFile(p.fastModesFile())
	if err != nil {
		return map[string]string{}
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]string{}
	}
	return m
}

func (p *Proxy) writeFastMode(name, mode string) error {
	modes := p.readFastModes()
	if mode == "" || mode == "fastpath" {
		delete(modes, name)
	} else {
		modes[name] = mode
	}
	if len(modes) == 0 {
		_ = os.Remove(p.fastModesFile())
		return nil
	}
	return os.WriteFile(p.fastModesFile(), mustJSON(modes), 0o644)
}

// deleteFastMode 删除某插件的模式记录。
func (p *Proxy) deleteFastMode(name string) {
	modes := p.readFastModes()
	if _, ok := modes[name]; !ok {
		return
	}
	delete(modes, name)
	if len(modes) == 0 {
		_ = os.Remove(p.fastModesFile())
		return
	}
	_ = os.WriteFile(p.fastModesFile(), mustJSON(modes), 0o644)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

var jsFenceRe = regexp.MustCompile("(?s)```(?:js|javascript)?\\s*(.*?)```")

// extractJSDetector 从 LLM 输出提取 detect 函数源码。
func extractJSDetector(output string) string {
	text := strings.TrimSpace(output)
	if text == "" {
		return ""
	}
	// 显式拒绝
	if regexp.MustCompile(`(?i)^\s*(NONE|NO|无|无法|不能|不需要)\s*$`).MatchString(text) {
		return ""
	}
	if strings.HasPrefix(strings.ToUpper(text), "NONE") && !strings.Contains(text, "function detect") {
		return ""
	}
	if m := jsFenceRe.FindStringSubmatch(text); m != nil {
		return strings.TrimSpace(m[1])
	}
	if i := strings.Index(text, "function detect"); i >= 0 {
		return strings.TrimSpace(text[i:])
	}
	return ""
}

// runJSDetector 在 goja 沙箱中执行检测器（硬超时 3s，异常视为未命中）。
func runJSDetector(source, text string) (string, bool) {
	vm := goja.New()
	vm.SetFieldNameMapper(goja.UncapFieldNameMapper())
	// 禁用可能逃逸的宿主能力与动态执行（goja 默认无 require/process/global，
	// 再显式置空；eval/Function 覆盖为 undefined 后调用会抛错）
	_ = vm.Set("require", goja.Undefined())
	_ = vm.Set("process", goja.Undefined())
	_ = vm.Set("console", goja.Undefined())
	_ = vm.Set("eval", goja.Undefined())
	_ = vm.Set("Function", goja.Undefined())
	timer := time.AfterFunc(3*time.Second, func() { vm.Interrupt("detector timeout") })
	defer timer.Stop()
	if _, err := vm.RunString(source); err != nil {
		return "", false
	}
	v := vm.Get("detect")
	fn, ok := goja.AssertFunction(v)
	if !ok {
		return "", false
	}
	res, err := fn(goja.Undefined(), vm.ToValue(text))
	if err != nil {
		return "", false
	}
	if res == nil || goja.IsUndefined(res) || goja.IsNull(res) {
		return "", false
	}
	if s := res.String(); strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s), true
	}
	return "", false
}

// loadPlugins 加载晋升 + 普通插件（mtime/size 失效重载）。
func (p *Proxy) loadPlugins() []*fastPlugin {
	p.fastMgr.mu.Lock()
	defer p.fastMgr.mu.Unlock()
	dirs := []string{p.fastPromotedDir(), p.fastPluginsDir()}
	stats := map[string][2]int64{}
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			stats[e.Name()] = [2]int64{info.ModTime().Unix(), info.Size()}
		}
	}
	// 目录内容变化才重载
	if len(stats) == len(p.fastMgr.stats) {
		same := true
		for k, v := range stats {
			if p.fastMgr.stats[k] != v {
				same = false
				break
			}
		}
		if same {
			return p.fastMgr.plugins
		}
	}
	var out []*fastPlugin
	for _, d := range dirs {
		promoted := filepath.Clean(d) == filepath.Clean(p.fastPromotedDir())
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".js") {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, name := range names {
			full := filepath.Join(d, name)
			src, err := os.ReadFile(full)
			if err != nil {
				continue
			}
			info, _ := os.Stat(full)
			plugin := &fastPlugin{
				Name:     strings.TrimSuffix(name, ".js"),
				Source:   string(src),
				Promoted: promoted,
				Mode:     "fastpath",
			}
			if promoted {
				plugin.Mode = p.readFastModes()[plugin.Name]
				if plugin.Mode == "" {
					plugin.Mode = "fastpath"
				}
			}
			if info != nil {
				plugin.mtime = info.ModTime().Unix()
				plugin.size = info.Size()
			}
			// trigger 注释
			if m := regexp.MustCompile(`(?m)^//\s*trigger:\s*(.*)$`).FindStringSubmatch(plugin.Source); m != nil {
				plugin.Trigger = strings.TrimSpace(m[1])
			}
			vm := goja.New()
			if _, err := vm.RunString(plugin.Source); err != nil {
				continue // 坏插件跳过
			}
			if fn, ok := goja.AssertFunction(vm.Get("detect")); ok {
				plugin.vm = vm
				plugin.detect = fn
				out = append(out, plugin)
			}
		}
	}
	p.fastMgr.plugins = out
	p.fastMgr.stats = stats
	return out
}

// saveFastPlugin 持久化检测器源码（sha256 前缀命名去重）。
func (p *Proxy) saveFastPlugin(source, trigger string) (string, error) {
	// 先校验可执行
	if _, ok := runJSDetector(source, ""); !ok && !strings.Contains(source, "function detect") {
		return "", fmt.Errorf("invalid detector source")
	}
	digest := sha256.Sum256([]byte(source))
	name := "gen_" + hex.EncodeToString(digest[:])[:10]
	dir := p.fastPluginsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	full := filepath.Join(dir, name+".js")
	if _, err := os.Stat(full); err == nil {
		return name, nil
	}
	body := source
	if trigger != "" {
		tl := strings.Join(strings.Fields(trigger), " ")
		if len(tl) > 80 {
			tl = tl[:80]
		}
		body = "// trigger: " + tl + "\n" + body
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// FastPluginList 返回插件列表（管理台）。
func (p *Proxy) FastPluginList() []map[string]any {
	var out []map[string]any
	for _, pl := range p.loadPlugins() {
		out = append(out, map[string]any{
			"name":     pl.Name,
			"trigger":  pl.Trigger,
			"source":   pl.Source,
			"promoted": pl.Promoted,
			"mode":     pl.Mode,
			"mtime":    pl.mtime,
			"size":     pl.size,
		})
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out
}

// promoteFastPlugin 把插件晋升为正式检测器（移到 promoted 目录）。
func (p *Proxy) PromoteFastPlugin(name, mode string) error {
	if !regexp.MustCompile(`^gen_[0-9a-f]{10}$`).MatchString(name) {
		return fmt.Errorf("invalid plugin name")
	}
	if mode == "" {
		mode = "fastpath"
	}
	if mode != "fastpath" && mode != "tool" && mode != "both" {
		return fmt.Errorf("invalid mode: must be fastpath/tool/both")
	}
	src := filepath.Join(p.fastPluginsDir(), name+".js")
	dst := filepath.Join(p.fastPromotedDir(), name+".js")
	_, srcErr := os.Stat(src)
	_, dstErr := os.Stat(dst)
	if srcErr != nil && dstErr != nil {
		return fmt.Errorf("plugin not found")
	}
	// 未晋升：移入 promoted；已晋升：只改模式（保留原位）。
	if srcErr == nil {
		if err := os.MkdirAll(p.fastPromotedDir(), 0o755); err != nil {
			return err
		}
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	}
	if err := p.writeFastMode(name, mode); err != nil {
		return err
	}
	p.fastMgr.mu.Lock()
	p.fastMgr.stats = nil // 强制重载
	p.fastMgr.mu.Unlock()
	return nil
}

// deleteFastPlugin 删除插件（promoted 也允许删除）。
func (p *Proxy) DeleteFastPlugin(name string) error {
	if !regexp.MustCompile(`^gen_[0-9a-f]{10}$`).MatchString(name) {
		return fmt.Errorf("invalid plugin name")
	}
	removed := false
	for _, d := range []string{p.fastPluginsDir(), p.fastPromotedDir()} {
		full := filepath.Join(d, name+".js")
		if _, err := os.Stat(full); err == nil {
			if err := os.Remove(full); err != nil {
				return err
			}
			removed = true
		}
	}
	if !removed {
		return fmt.Errorf("plugin not found")
	}
	p.deleteFastMode(name)
	p.fastMgr.mu.Lock()
	p.fastMgr.stats = nil
	p.fastMgr.mu.Unlock()
	return nil
}

// ValidateJSDetector 校验一段 JS 是否是可执行的 detect(text) 检测器（外部工具录用时用）。
func (p *Proxy) ValidateJSDetector(source string) bool {
	return validateDetectorSource(source)
}

// validateDetectorSource 只验证 JS 能执行且定义了 detect 函数（不要求命中文本）。
func validateDetectorSource(source string) bool {
	vm := goja.New()
	vm.SetFieldNameMapper(goja.UncapFieldNameMapper())
	_ = vm.Set("require", goja.Undefined())
	_ = vm.Set("process", goja.Undefined())
	_ = vm.Set("console", goja.Undefined())
	_ = vm.Set("eval", goja.Undefined())
	_ = vm.Set("Function", goja.Undefined())
	timer := time.AfterFunc(3*time.Second, func() { vm.Interrupt("detector timeout") })
	defer timer.Stop()
	if _, err := vm.RunString(source); err != nil {
		return false
	}
	_, ok := goja.AssertFunction(vm.Get("detect"))
	return ok
}

// FastToolPlugins 返回晋升模式为 tool/both 的插件（注册进工具池、可被 LLM 调用）。
func (p *Proxy) FastToolPlugins() []*fastPlugin {
	out := make([]*fastPlugin, 0)
	for _, pl := range p.loadPlugins() {
		if !pl.Promoted || (pl.Mode != "tool" && pl.Mode != "both") {
			continue
		}
		out = append(out, pl)
	}
	return out
}

// -- LLM 生成 -----------------------------------------------------------------

var codeGenPrompt = `你是确定性快路径代码生成器。判断下面的用户问题能否用**纯 JS 函数**确定性解决（数学计算、格式转换、字符串/列表处理、日期计算等——不需要网络、文件或外部 API）。

问题：{query}

如果能解决，只输出一个 JS 函数（不要解释、不要多余文字）：

function detect(text) {
  // 从 text 中提取所需信息，返回完整自然的答案字符串（中文）；
  // 如果 text 不是这类问题，返回 null。
  ...
}

规则：
- 只能用 JS 内置能力：String/Number/RegExp/Math/Array/Object 的方法（match/replace/split、toUpperCase、parseInt、Math.round 等）。
- 禁止任何外部 API、fetch、require、process、setTimeout、eval、new Function、XMLHttpRequest、document/window/globalThis。
- 禁止访问 __proto__ / constructor / prototype。
- 函数必须健壮：对不相关输入返回 null，绝不抛异常。
- 正则用 /.../ 字面量即可。
- 答案要完整自然，例如「2 加 3 等于 5。」。

如果不能确定性解决（需要常识、写作、开放推理）→ 只输出 NONE。`

// codegenSolve 让 LLM 生成检测器并立即执行验证；命中则持久化插件并返回答案。
func (p *Proxy) codegenSolve(r *http.Request, key store.APIKey, path, text string) (string, bool, string) {
	if !p.store.Settings().Fastpath.Codegen {
		return "", false, "codegen 未启用（settings.fastpath.codegen=false）"
	}
	prompt := strings.Replace(codeGenPrompt, "{query}", text, 1)
	msgs := []chatMessage{{"role": "user", "content": prompt}}
	body, why := p.complete(r, key, path, msgs)
	if len(body) == 0 {
		if why == "" {
			why = "codegen 上游调用失败（所有候选不可用）"
		}
		return "", false, why
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Choices) == 0 {
		return "", false, "codegen 响应无 choices（上游异常）"
	}
	source := extractJSDetector(resp.Choices[0].Message.Content)
	if source == "" {
		return "", false, "codegen 未从模型输出中提取到 JS 检测器（模型没按格式返回代码）"
	}
	answer, hit := runJSDetector(source, text)
	if !hit {
		return "", false, "codegen 生成的检测器校验未命中（沙箱执行结果为空）"
	}
	p.saveFastPlugin(source, text)
	return answer, true, ""
}

// ChatMessage 是发送给上游的聊天消息（导出别名，供管理台复用补全通道）。
type ChatMessage = chatMessage

// Complete 走候选链做一次无工具的纯补全（管理台功能复用：如外部 MCP 接入建议）。
// 返回 (body, reason)：body 非空即成功；失败时 reason 描述原因。
func (p *Proxy) Complete(r *http.Request, key store.APIKey, path string, msgs []ChatMessage) ([]byte, string) {
	return p.complete(r, key, path, msgs)
}

// complete 走候选链做一次无工具的纯补全（codegen 专用：不带工具 schema，
// 避免模型返回 tool_calls 而非代码）。返回 (body, reason)：body 非空即成功；
// 失败时 reason 描述原因（可能为空表示无候选可试）。
func (p *Proxy) complete(r *http.Request, key store.APIKey, path string, msgs []chatMessage) ([]byte, string) {
	cands, err := p.router.Pick("chat")
	if err != nil {
		return nil, "codegen: 无可用的 chat 路由（" + err.Error() + "）"
	}
	var lastWhy string
	for _, c := range cands {
		reqBody := map[string]any{
			"model":    c.UpstreamModel,
			"messages": msgs,
			"stream":   false,
		}
		raw, err := json.Marshal(reqBody)
		if err != nil {
			lastWhy = "codegen: 请求编码失败"
			continue
		}
		timeout := time.Duration(c.Provider.TimeoutMS) * time.Millisecond
		if c.Provider.TimeoutMS <= 0 {
			timeout = time.Duration(p.store.Settings().DefaultTimeoutMS) * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL(c.Provider.BaseURL, path), bytes.NewReader(raw))
		if err != nil {
			cancel()
			lastWhy = "codegen: 构造请求失败"
			continue
		}
		upReq.Header.Set("Authorization", "Bearer "+c.Provider.ResolvedAPIKey())
		upReq.Header.Set("Content-Type", "application/json")
		for k, v := range c.Provider.Headers {
			upReq.Header.Set(k, v)
		}
		resp, err := p.client.Do(upReq)
		if err != nil {
			cancel()
			lastWhy = "codegen: 上游 " + c.ProviderID + " 不可用（" + err.Error() + "）"
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		cancel()
		if readErr != nil {
			lastWhy = "codegen: 读取上游响应失败"
			continue
		}
		if resp.StatusCode >= 400 {
			lastWhy = fmt.Sprintf("codegen: 上游 %s 返回 %d", c.ProviderID, resp.StatusCode)
			continue
		}
		return body, ""
	}
	return nil, lastWhy
}

// FastPathTry 完整快路径：内置 → 晋升/插件 → codegen。命中返回答案与方法名。
func (p *Proxy) FastPathTry(r *http.Request, key store.APIKey, path, text string) (string, string) {
	if !p.store.Settings().Fastpath.Enabled {
		return "", ""
	}
	if fast := tryFastAnswer(text); fast != nil {
		return fast.answer, fast.method
	}
	for _, pl := range p.loadPlugins() {
		if pl.Mode == "tool" {
			continue // 纯工具模式：不进请求前拦截链
		}
		if answer, hit := runJSDetector(pl.Source, text); hit {
			return answer, "plugin:" + pl.Name
		}
	}
	if answer, hit, _ := p.codegenSolve(r, key, path, text); hit {
		return answer, "codegen"
	}
	return "", ""
}

// FastPathProbe 记录快路径尝试链中的一步（管理台测试/生成用）。
type FastPathProbe struct {
	Stage  string `json:"stage"`            // builtin / plugin:name / codegen
	Hit    bool   `json:"hit"`
	Detail string `json:"detail,omitempty"` // 命中时答案；未命中时失败原因
}

// FastPathProbe 探测一条文本的完整快路径尝试链：内置 → 插件 → codegen，
// 返回最终答案、命中的方法名与每一步的详细结果。生产请求仍走 FastPathTry。
func (p *Proxy) FastPathProbe(r *http.Request, key store.APIKey, path, text string) (string, string, []FastPathProbe) {
	chain := make([]FastPathProbe, 0, 4)
	if !p.store.Settings().Fastpath.Enabled {
		chain = append(chain, FastPathProbe{Stage: "fastpath", Hit: false, Detail: "fastpath 未启用（settings.fastpath.enabled=false）"})
		return "", "", chain
	}
	if fast := tryFastAnswer(text); fast != nil {
		chain = append(chain, FastPathProbe{Stage: fast.method, Hit: true, Detail: fast.answer})
		return fast.answer, fast.method, chain
	}
	chain = append(chain, FastPathProbe{Stage: "builtin", Hit: false, Detail: "内置匹配器未命中"})
	for _, pl := range p.loadPlugins() {
		if answer, hit := runJSDetector(pl.Source, text); hit {
			chain = append(chain, FastPathProbe{Stage: "plugin:" + pl.Name, Hit: true, Detail: answer})
			return answer, "plugin:" + pl.Name, chain
		}
		chain = append(chain, FastPathProbe{Stage: "plugin:" + pl.Name, Hit: false, Detail: "插件检测器未命中"})
	}
	answer, hit, why := p.codegenSolve(r, key, path, text)
	detail := answer
	if !hit {
		detail = why
	}
	chain = append(chain, FastPathProbe{Stage: "codegen", Hit: hit, Detail: detail})
	if hit {
		return answer, "codegen", chain
	}
	return "", "", chain
}
