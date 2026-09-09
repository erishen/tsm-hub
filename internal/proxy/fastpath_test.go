package proxy

import (
	"strings"
	"testing"
)

func TestFastArithmetic(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2+3 等于多少", "5"},
		{"计算 12×34", "408"},
		{"(23+45)*2", "136"},
		{"100 / 4 是多少", "25"},
		{"2^10 等于几", "1024"},
		{"7*6", "42"},
		{"23 加 45 等于", "68"},
		{"100 减 37", "63"},
		{"8 乘 9", "72"},
		{"81 除以 9", "9"},
		{"3.5 + 2.5", "6"},
	}
	for _, c := range cases {
		r := tryFastAnswer(c.in)
		if r == nil || r.method != "arithmetic" {
			t.Errorf("%q: no arithmetic hit (%+v)", c.in, r)
			continue
		}
		if !strings.Contains(r.answer, c.want) {
			t.Errorf("%q = %q, want containing %q", c.in, r.answer, c.want)
		}
	}
	// 比较意图
	r := tryFastAnswer("23+45 和 10*3 哪个更大")
	if r == nil || !strings.Contains(r.answer, "更大") {
		t.Errorf("compare = %+v", r)
	}
	// 不安全的表达式不应命中
	if r := tryFastAnswer("帮我算一下人生意义"); r != nil {
		t.Errorf("non-math should miss, got %+v", r)
	}
}

func TestFastTimeAndDate(t *testing.T) {
	if r := tryFastAnswer("现在几点"); r == nil || r.method != "time" || !strings.Contains(r.answer, "现在是") {
		t.Errorf("time = %+v", r)
	}
	if r := tryFastAnswer("明天是几号"); r == nil || r.method != "date_math" || !strings.Contains(r.answer, "明天是") {
		t.Errorf("date = %+v", r)
	}
	if r := tryFastAnswer("3天后是什么日期"); r == nil || !strings.Contains(r.answer, "3 天后是") {
		t.Errorf("days later = %+v", r)
	}
	if r := tryFastAnswer("2026-09-01 和 2026-09-09 相差几天"); r == nil || !strings.Contains(r.answer, "相差 8 天") {
		t.Errorf("date diff = %+v", r)
	}
}

func TestFastStatsConvertBase(t *testing.T) {
	if r := tryFastAnswer("3、5、8、2 的最大值"); r == nil || !strings.Contains(r.answer, "8") {
		t.Errorf("max = %+v", r)
	}
	if r := tryFastAnswer("3、5、8、2 的平均值"); r == nil || !strings.Contains(r.answer, "4.5") {
		t.Errorf("avg = %+v", r)
	}
	if r := tryFastAnswer("100 摄氏度等于多少华氏度"); r == nil || !strings.Contains(r.answer, "212") {
		t.Errorf("c2f = %+v", r)
	}
	if r := tryFastAnswer("5 公里等于多少英里"); r == nil || !strings.Contains(r.answer, "3.11") {
		t.Errorf("km2mi = %+v", r)
	}
	if r := tryFastAnswer("255 的十六进制"); r == nil || !strings.Contains(r.answer, "ff") {
		t.Errorf("hex = %+v", r)
	}
	if r := tryFastAnswer("你好世界 几个字"); r == nil || !strings.Contains(r.answer, "4 个字") {
		t.Errorf("text stats = %+v", r)
	}
}
