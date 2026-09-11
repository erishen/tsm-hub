package proxy

// fastpath: 确定性快路径——能用纯代码回答的问题（算术/时间/日期/换算/
// 统计/进制/字数）直接返回，零模型调用。移植自 resolve-harness fastpath.py：
// 在 agent 循环前先尝试匹配，命中即构造答案，不消耗任何上游 token。

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// mathExp / mathLog 占位，直接使用 math 包函数（见 powFloat）。
var mathExp = math.Exp
var mathLog = math.Log

type fastAnswer struct {
	query  string
	method string
	answer string
	detail string
}

// -- 安全算术求值（递归下降，仅数字 + - * / % ^ ( )，无变量无函数） ----------

type mathParser struct {
	s   string
	pos int
}

func (mp *mathParser) peek() byte {
	if mp.pos < len(mp.s) {
		return mp.s[mp.pos]
	}
	return 0
}

func (mp *mathParser) skipSpace() {
	for mp.pos < len(mp.s) && (mp.s[mp.pos] == ' ' || mp.s[mp.pos] == '\t') {
		mp.pos++
	}
}

func (mp *mathParser) parseExpr() (float64, error) {
	v, err := mp.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		mp.skipSpace()
		switch mp.peek() {
		case '+':
			mp.pos++
			r, err := mp.parseTerm()
			if err != nil {
				return 0, err
			}
			v += r
		case '-':
			mp.pos++
			r, err := mp.parseTerm()
			if err != nil {
				return 0, err
			}
			v -= r
		default:
			return v, nil
		}
	}
}

func (mp *mathParser) parseTerm() (float64, error) {
	v, err := mp.parseFactor()
	if err != nil {
		return 0, err
	}
	for {
		mp.skipSpace()
		switch mp.peek() {
		case '*':
			mp.pos++
			r, err := mp.parseFactor()
			if err != nil {
				return 0, err
			}
			v *= r
		case '/':
			mp.pos++
			r, err := mp.parseFactor()
			if err != nil {
				return 0, err
			}
			if r == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			v /= r
		case '%':
			mp.pos++
			r, err := mp.parseFactor()
			if err != nil {
				return 0, err
			}
			v = float64(int64(v) % int64(r))
		default:
			return v, nil
		}
	}
}

func (mp *mathParser) parseFactor() (float64, error) {
	mp.skipSpace()
	c := mp.peek()
	if c == '-' {
		mp.pos++
		v, err := mp.parseFactor()
		return -v, err
	}
	if c == '+' {
		mp.pos++
		return mp.parseFactor()
	}
	if c == '(' {
		mp.pos++
		v, err := mp.parseExpr()
		if err != nil {
			return 0, err
		}
		mp.skipSpace()
		if mp.peek() != ')' {
			return 0, fmt.Errorf("expected )")
		}
		mp.pos++
		// 括号后可能接幂
		mp.skipSpace()
		if mp.peek() == '^' {
			mp.pos++
			r, err := mp.parseFactor()
			if err != nil {
				return 0, err
			}
			return powFloat(v, r), nil
		}
		return v, nil
	}
	// 数字
	start := mp.pos
	for mp.pos < len(mp.s) && (isDigit(mp.s[mp.pos]) || mp.s[mp.pos] == '.') {
		mp.pos++
	}
	if start == mp.pos {
		return 0, fmt.Errorf("expected number at %d", mp.pos)
	}
	v, err := strconv.ParseFloat(mp.s[start:mp.pos], 64)
	if err != nil {
		return 0, err
	}
	// 数字后可能接幂（右结合：2^3^2 = 2^(3^2)）
	mp.skipSpace()
	if mp.peek() == '^' {
		mp.pos++
		r, err := mp.parseFactor()
		if err != nil {
			return 0, err
		}
		return powFloat(v, r), nil
	}
	return v, nil
}

func powFloat(base, exp float64) float64 {
	if exp == float64(int64(exp)) {
		r := 1.0
		for i := int64(0); i < int64(exp); i++ {
			r *= base
		}
		return r
	}
	// 非整数指数：用指数对数近似（fastpath 仅兜底，整数幂已覆盖常见场景）
	if base > 0 {
		return mathExp(exp * mathLog(base))
	}
	return 0
}

// normalizeCNMath 把中文运算符词转成符号（仅算术提取用）。
func normalizeCNMath(text string) string {
	for _, p := range [][2]string{
		{"乘以", "*"}, {"乘", "*"}, {"除以", "/"}, {"除", "/"},
		{"加上", "+"}, {"加", "+"}, {"减去", "-"}, {"减", "-"},
	} {
		text = strings.ReplaceAll(text, p[0], p[1])
	}
	return text
}

// evalMath 安全求值纯数学表达式；失败返回 error。
func evalMath(expr string) (float64, error) {
	expr = strings.ReplaceAll(expr, "**", "^")
	expr = strings.ReplaceAll(expr, "×", "*")
	expr = strings.ReplaceAll(expr, "÷", "/")
	mp := &mathParser{s: expr}
	v, err := mp.parseExpr()
	if err != nil {
		return 0, err
	}
	mp.skipSpace()
	if mp.pos != len(mp.s) {
		return 0, fmt.Errorf("trailing chars at %d", mp.pos)
	}
	return v, nil
}

// 提取文本中可安全求值的算术表达式链：首项（数字或括号）后跟至少一个运算符+项。
var exprRe = regexp.MustCompile(`(?:\([^()]*\d[\d+\-*/×x÷%^().\s]*\)|-?\d+(?:\.\d+)?)(?:\s*[+\-*/×x÷%^]\s*(?:\([^()]*\d[\d+\-*/×x÷%^().\s]*\)|-?\d+(?:\.\d+)?))+`)

func formatNum(v float64) string {
	if v == float64(int64(v)) && v < 1e15 && v > -1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 6, 64), "0"), ".")
}

func tryArithmetic(text string) *fastAnswer {
	runeLen := len([]rune(text))
	// 防误匹配：算术问题通常很短（<100 字符），长文本（文章写作/代码生成等）不进算术快路径。
	if runeLen > 100 {
		return nil
	}
	// 中等长度文本（50-100 字符）需要明确的算术关键词，避免章节编号/列表项被误识别。
	// 短文本（<50 字符）很可能是纯算术问题（如 "2+3"、"7*6"），直接匹配。
	if runeLen >= 50 {
		arithmeticHintRe := regexp.MustCompile(`(?i)(等于|计算|算一下|算算|多少|几|=|what\s+is|how\s+much|compute|calculate)`)
		if !arithmeticHintRe.MatchString(text) {
			return nil
		}
	}
	norm := normalizeCNMath(text)
	var exprs []string
	for _, m := range exprRe.FindAllString(norm, -1) {
		e := strings.TrimSpace(m)
		if matched, _ := regexp.MatchString(`^-?\d+(?:\.\d+)?$`, e); matched {
			continue // 单个数字不是表达式
		}
		if _, err := evalMath(e); err != nil {
			continue
		}
		if !containsStr(exprs, e) {
			exprs = append(exprs, e)
		}
	}
	if len(exprs) == 0 {
		return nil
	}
	lines := make([]string, 0, len(exprs))
	vals := make([]float64, 0, len(exprs))
	for _, e := range exprs {
		v, err := evalMath(e)
		if err != nil {
			continue
		}
		vals = append(vals, v)
		lines = append(lines, fmt.Sprintf("%s = %s", e, formatNum(v)))
	}
	if len(lines) == 0 {
		return nil
	}
	answer := strings.Join(lines, "，") + "。"
	if len(vals) >= 2 && (strings.Contains(text, "哪个") || strings.Contains(text, "更大") || strings.Contains(text, "更小") || strings.Contains(text, "比较大") || strings.Contains(text, "比一比") || strings.Contains(text, "compare")) {
		maxV := vals[0]
		maxI := 0
		allEq := true
		for i, v := range vals {
			if v != vals[0] {
				allEq = false
			}
			if v > maxV {
				maxV = v
				maxI = i
			}
		}
		if allEq {
			answer += "两个结果相等。"
		} else {
			answer += fmt.Sprintf("其中 %s = %s 更大。", exprs[maxI], formatNum(maxV))
		}
	}
	return &fastAnswer{query: text, method: "arithmetic", answer: answer, detail: strings.Join(lines, "\n")}
}

// -- 时间 ----------------------------------------------------------------------

var timeRe = regexp.MustCompile(`(?i)现在几点|几点了|当前时间|现在时间|什么时间|what\s*(?:is\s*)?time|current\s*time`)

var weekdays = []string{"星期一", "星期二", "星期三", "星期四", "星期五", "星期六", "星期日"}

func tryTime(text string) *fastAnswer {
	if !timeRe.MatchString(text) {
		return nil
	}
	now := time.Now()
	wd := weekdays[int(now.Weekday()+6)%7]
	answer := fmt.Sprintf("现在是 %s（%s）。", now.Format("2006-01-02 15:04:05"), wd)
	return &fastAnswer{query: text, method: "time", answer: answer, detail: now.Format(time.RFC3339)}
}

// -- 统计 / 排序 ----------------------------------------------------------------

var numListRe = regexp.MustCompile(`-?\d+(?:\.\d+)?`)

var statIntents = []struct {
	kw    string
	kind  string
	label string
}{
	{"最大数", "max", "最大值为"},
	{"最大", "max", "最大值为"},
	{"最小", "min", "最小值为"},
	{"平均", "avg", "平均值为"},
	{"总和", "sum", "总和为"},
	{"求和", "sum", "总和为"},
	{"排序", "sort", "从小到大为"},
	{"从小到大", "sort", "从小到大为"},
	{"从大到小", "sort_desc", "从大到小为"},
}

func tryStatistics(text string) *fastAnswer {
	kind, label := "", ""
	for _, it := range statIntents {
		if strings.Contains(text, it.kw) {
			kind, label = it.kind, it.label
			break
		}
	}
	if kind == "" {
		return nil
	}
	var nums []float64
	for _, m := range numListRe.FindAllString(text, -1) {
		v, err := strconv.ParseFloat(m, 64)
		if err == nil {
			nums = append(nums, v)
		}
	}
	if len(nums) < 2 {
		return nil
	}
	switch kind {
	case "max":
		v := nums[0]
		for _, n := range nums[1:] {
			if n > v {
				v = n
			}
		}
		return &fastAnswer{text, "statistics", fmt.Sprintf("%s %s。", label, formatNum(v)), formatNum(v)}
	case "min":
		v := nums[0]
		for _, n := range nums[1:] {
			if n < v {
				v = n
			}
		}
		return &fastAnswer{text, "statistics", fmt.Sprintf("%s %s。", label, formatNum(v)), formatNum(v)}
	case "avg":
		sum := 0.0
		for _, n := range nums {
			sum += n
		}
		return &fastAnswer{text, "statistics", fmt.Sprintf("%s %s。", label, formatNum(sum/float64(len(nums)))), formatNum(sum / float64(len(nums)))}
	case "sum":
		sum := 0.0
		for _, n := range nums {
			sum += n
		}
		return &fastAnswer{text, "statistics", fmt.Sprintf("%s %s。", label, formatNum(sum)), formatNum(sum)}
	case "sort":
		sorted := append([]float64(nil), nums...)
		sort.Float64s(sorted)
		parts := make([]string, len(sorted))
		for i, v := range sorted {
			parts[i] = formatNum(v)
		}
		return &fastAnswer{text, "statistics", fmt.Sprintf("%s %s。", label, strings.Join(parts, "、")), strings.Join(parts, "、")}
	default: // sort_desc
		sorted := append([]float64(nil), nums...)
		sort.Sort(sort.Reverse(sort.Float64Slice(sorted)))
		parts := make([]string, len(sorted))
		for i, v := range sorted {
			parts[i] = formatNum(v)
		}
		return &fastAnswer{text, "statistics", fmt.Sprintf("%s %s。", label, strings.Join(parts, "、")), strings.Join(parts, "、")}
	}
}

// -- 单位换算 --------------------------------------------------------------------

type conversion struct {
	pat     *regexp.Regexp
	label   string
	target  string
	factor  float64 // 0 表示用 kind 特殊处理
	kind    string  // c2f / f2c / ""
}

var conversions = []conversion{
	{regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(?:摄氏)?度\s*(?:等于|换算成|是|转|到|多少|几)*\s*(?:华氏|fahrenheit)`), "摄氏→华氏", "华氏度", 0, "c2f"},
	{regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(?:华氏)度\s*(?:等于|换算成|是|转|到|多少|几)*\s*(?:摄氏|celsius)`), "华氏→摄氏", "摄氏度", 0, "f2c"},
	{regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(?:km|公里)\s*(?:等于|换算成|是|转|到|多少|几)*\s*(?:mi|英里)`), "公里→英里", "英里", 0.621371, ""},
	{regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(?:mi|英里)\s*(?:等于|换算成|是|转|到|多少|几)*\s*(?:km|公里)`), "英里→公里", "公里", 1.609344, ""},
	{regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(?:kg|千克|公斤)\s*(?:等于|换算成|是|转|到|多少|几)*\s*(?:lb|磅)`), "千克→磅", "磅", 2.204623, ""},
	{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*(?:斤)\s*(?:等于|换算成|是|转|到|多少|几)*\s*(?:kg|千克|公斤)`), "斤→千克", "千克", 0.5, ""},
	{regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(?:小时|h)\s*(?:等于|换算成|是|转|到|多少|几)*\s*(?:分钟|min)`), "小时→分钟", "分钟", 60.0, ""},
	{regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(?:分钟|min)\s*(?:等于|换算成|是|转|到|多少|几)*\s*(?:小时|h)`), "分钟→小时", "小时", 1.0 / 60.0, ""},
}

func tryUnitConvert(text string) *fastAnswer {
	for _, c := range conversions {
		m := c.pat.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			continue
		}
		var result float64
		switch c.kind {
		case "c2f":
			result = v*9/5 + 32
		case "f2c":
			result = (v - 32) * 5 / 9
		default:
			result = v * c.factor
		}
		display := formatNum(round2(result))
		return &fastAnswer{text, "unit_convert",
			fmt.Sprintf("%s 换算成 %s 为 %s。", formatNum(v), c.target, display),
			fmt.Sprintf("%v %s = %v", formatNum(v), c.label, result)}
	}
	return nil
}

func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }

// -- 日期 ------------------------------------------------------------------------

var dateMathRe = regexp.MustCompile(`(明天|昨天|后天|前天)`)
var daysLaterRe = regexp.MustCompile(`(\d+)\s*天(?:之|以)?(?:后|前)`)
var dateDiffRe = regexp.MustCompile(`(\d{4}[-/.]\d{1,2}[-/.]\d{1,2})\s*(?:和|与|到|跟)\s*(\d{4}[-/.]\d{1,2}[-/.]\d{1,2})\s*(?:相差|差|相隔)?\s*几天`)

func parseDate(s string) (time.Time, error) {
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	return time.ParseInLocation("2006-1-2", s, time.Local)
}

func tryDateMath(text string) *fastAnswer {
	today := time.Now()
	if m := dateDiffRe.FindStringSubmatch(text); m != nil {
		d1, err1 := parseDate(m[1])
		d2, err2 := parseDate(m[2])
		if err1 == nil && err2 == nil {
			days := int(d2.Sub(d1).Hours() / 24)
			if days < 0 {
				days = -days
			}
			return &fastAnswer{text, "date_math", fmt.Sprintf("%s 和 %s 相差 %d 天。", m[1], m[2], days), strconv.Itoa(days)}
		}
	}
	if m := daysLaterRe.FindStringSubmatch(text); m != nil {
		days, _ := strconv.Atoi(m[1])
		dir := 1
		if strings.Contains(m[0], "前") {
			dir = -1
		}
		target := today.AddDate(0, 0, days*dir)
		kw := "后"
		if dir < 0 {
			kw = "前"
		}
		return &fastAnswer{text, "date_math", fmt.Sprintf("%d 天%s是 %s（%s）。", days, kw, target.Format("2006-01-02"), weekdays[int(target.Weekday()+6)%7]), target.Format("2006-01-02")}
	}
	if m := dateMathRe.FindStringSubmatch(text); m != nil {
		offset := map[string]int{"明天": 1, "后天": 2, "昨天": -1, "前天": -2}[m[1]]
		target := today.AddDate(0, 0, offset)
		return &fastAnswer{text, "date_math", fmt.Sprintf("%s是 %s（%s）。", m[1], target.Format("2006-01-02"), weekdays[int(target.Weekday()+6)%7]), target.Format("2006-01-02")}
	}
	return nil
}

// -- 进制转换 ----------------------------------------------------------------------

var baseRe = regexp.MustCompile(`(?i)(\d+)\s*的\s*(二进制|八进制|十六进制|二进制数|八进制数|十六进制数)|(二进制|八进制|十六进制)\s*(?:的|表示|形式)?\s*(\d+)`)
var baseMap = map[string]int{"二进制": 2, "八进制": 8, "十六进制": 16}

func tryBaseConvert(text string) *fastAnswer {
	m := baseRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	numStr, baseName := m[1], m[2]
	if numStr == "" {
		numStr, baseName = m[4], m[3]
	}
	if numStr == "" || baseName == "" {
		return nil
	}
	baseName = strings.ReplaceAll(baseName, "数", "")
	base, ok := baseMap[baseName]
	if !ok {
		return nil
	}
	v, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return nil
	}
	var result string
	switch base {
	case 2:
		result = strconv.FormatInt(v, 2)
	case 8:
		result = strconv.FormatInt(v, 8)
	case 16:
		result = strconv.FormatInt(v, 16)
	}
	return &fastAnswer{text, "base_convert", fmt.Sprintf("%s 的%s是 %s。", numStr, baseName, result), result}
}

// -- 字数统计 ----------------------------------------------------------------------

var charCountRe = regexp.MustCompile(`(?:有|一共|统计|数一数)?\s*几个(?:字|字符)|多少(?:个)?(?:字|字符)`)

func tryTextStats(text string) *fastAnswer {
	if !charCountRe.MatchString(text) {
		return nil
	}
	quoted := quotedRe.FindAllString(text, -1)
	body := ""
	if len(quoted) > 0 {
		body = quoted[0]
	} else {
		body = charCountRe.ReplaceAllString(text, "")
	}
	body = strings.Trim(body, " \"“”「」'‘’")
	if body == "" {
		return nil
	}
	n := utf8.RuneCountInString(body)
	return &fastAnswer{text, "text_stats", fmt.Sprintf("「%s」共 %d 个字（含标点）。", body, n), strconv.Itoa(n)}
}

var quotedRe = regexp.MustCompile(`[\"“「]([^\"”」]+)[\"”」]`)

// -- 公共入口 ----------------------------------------------------------------------

var fastChecks = []func(string) *fastAnswer{
	tryDateMath, // 日期模式最具体，放最前避免被算术抢先（如 2026-09-01 里的减号）
	tryArithmetic,
	tryStatistics,
	tryUnitConvert,
	tryBaseConvert,
	tryTextStats,
	tryTime,
}

// taskKeywords 是任务型关键词黑名单：包含这些词的请求很可能是写作/代码/分析等任务，
// 不是纯算术/统计/换算查询，直接跳过 fastpath 避免误匹配。
var taskKeywords = []string{
	// 写作类
	"写一篇", "写个", "写一", "文章", "作文", "故事", "小说", "介绍", "总结", "报告", "文档",
	"撰写", "起草", "润色", "改写", "续写",
	// 代码类
	"代码", "函数", "编程", "开发", "实现", "程序", "脚本", "bug", "调试", "修复", "重构",
	"编译", "部署", "接口", "api", "API", "数据库", "sql", "SQL",
	// 分析类
	"分析", "评估", "对比", "比较", "研究", "调研", "审查", "评审", "诊断", "排查",
	// 生成/创建类
	"生成", "创建", "设计", "规划", "制作", "构建", "搭建", "配置", "安装",
	// 翻译类
	"翻译", "译成", "翻译成", "转译",
	// 项目/方案类
	"项目", "方案", "计划", "建议", "意见", "想法", "思路", "策略", "架构",
	// 问答/解释类（长文本解释，不是简单查询）
	"为什么", "怎么理解", "如何理解", "详细说明", "详细解释", "原理", "机制",
}

// queryKeywords 是查询型关键词白名单：中等长度文本（50-200字符）必须包含这些词之一
// 才进入 fastpath，避免包含数字/运算符的任务型请求被误匹配。
var queryKeywords = []string{
	// 算术
	"等于", "计算", "算一下", "算算", "多少", "几", "=?", "=？",
	// 时间
	"现在几点", "几点了", "当前时间", "现在时间", "什么时间",
	// 换算
	"换算", "等于多少", "转成", "转换成", "是多少", "相当于",
	// 统计
	"平均值", "最大值", "最小值", "求和", "排序", "平均", "最大", "最小",
	// 日期
	"明天", "昨天", "今天", "几号", "星期", "相差几天", "几天后",
	// 进制
	"十六进制", "二进制", "八进制", "进制", "转十六", "转二",
	// 字数
	"多少字", "几个字", "字数", "字符数",
}

// containsAny 检查 text 是否包含 list 中任意一个关键词。
func containsAny(text string, list []string) bool {
	for _, kw := range list {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// tryFastAnswer 返回可确定性回答的结果，否则 nil。
// 采用三层防护：
//  1. 任务型关键词黑名单：包含写作/代码/分析等任务关键词的请求直接跳过
//  2. 长度限制：>200 字符直接跳过（长文本不可能是纯查询）
//  3. 查询型关键词白名单：50-200 字符的中等长度文本必须包含明确的查询关键词才进入匹配器
func tryFastAnswer(text string) *fastAnswer {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	// 第一层：任务型关键词黑名单——包含这些词的请求很可能是复杂任务，不是纯查询。
	if containsAny(text, taskKeywords) {
		return nil
	}
	runeLen := len([]rune(text))
	// 第二层：全局长度限制——>200 字符的长文本不可能是纯算术/统计/换算查询。
	if runeLen > 200 {
		return nil
	}
	// 第三层：中等长度文本（50-200字符）需要明确的查询关键词，避免包含数字/运算符的
	// 任务型请求（如章节编号、列表项、参数说明）被误匹配为算术/统计查询。
	// 短文本（<50字符）很可能是纯查询（如 "2+3"、"现在几点"），直接进入匹配器。
	if runeLen >= 50 && !containsAny(text, queryKeywords) {
		return nil
	}
	for _, check := range fastChecks {
		if r := check(text); r != nil {
			return r
		}
	}
	return nil
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// fastMatchersDesc 内置匹配器清单（管理台展示）。
var fastMatchersDesc = []struct{ Name, Trigger, Desc string }{
	{"fastpath.arithmetic", "计算 2+3 / 12×34 / 23 加 45 等于多少", "算术表达式安全求值（递归下降解析器，零模型）"},
	{"fastpath.statistics", "…的平均值 / 总和 / 最大最小 / 排序（数字列表）", "数字列表统计与排序"},
	{"fastpath.unit_convert", "100 摄氏度是多少华氏度 / 5 公里等于多少英里", "常用单位换算（温度/长度/重量/时间）"},
	{"fastpath.date_math", "明天是几号 / N 天后的日期 / 两个日期相差几天", "日期计算"},
	{"fastpath.base_convert", "255 的十六进制 / 十进制转二进制", "进制转换（2/8/16）"},
	{"fastpath.text_stats", "这段文字有多少字", "文本字数统计"},
	{"fastpath.time", "现在几点 / 当前时间", "当前本地时间"},
}

func (p *Proxy) FastMatchers() []map[string]any {
	out := make([]map[string]any, 0, len(fastMatchersDesc))
	for _, m := range fastMatchersDesc {
		out = append(out, map[string]any{
			"name":    m.Name,
			"trigger": m.Trigger,
			"desc":    m.Desc,
			"builtin": true,
		})
	}
	return out
}
