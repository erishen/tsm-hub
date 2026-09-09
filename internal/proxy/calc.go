package proxy

// 简易表达式求值（calc 工具）：四则运算 + 括号 + 常用函数，纯标准库。
// 语法：
//   expr   := add
//   add    := mul (('+'|'-') mul)*
//   mul    := unary (('*'|'/'|'%') unary)*
//   unary  := ('-'|'+') unary | pow
//   pow    := atom ('^' atom)*   （右结合）
//   atom   := number | ident '(' expr ')' | '(' expr ')'
// 支持函数：sqrt pow abs min max round floor ceil sin cos tan log ln exp

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type calcParser struct {
	s   string
	pos int
}

func evalExpr(s string) (float64, error) {
	p := &calcParser{s: strings.TrimSpace(s)}
	v, err := p.parseAdd()
	if err != nil {
		return 0, err
	}
	p.skipWS()
	if p.pos < len(p.s) {
		return 0, fmt.Errorf("unexpected %q at %d", p.s[p.pos:], p.pos)
	}
	return v, nil
}

func (p *calcParser) skipWS() {
	for p.pos < len(p.s) && (p.s[p.pos] == ' ' || p.s[p.pos] == '\t') {
		p.pos++
	}
}

func (p *calcParser) parseAdd() (float64, error) {
	l, err := p.parseMul()
	if err != nil {
		return 0, err
	}
	for {
		p.skipWS()
		if p.pos >= len(p.s) {
			return l, nil
		}
		switch p.s[p.pos] {
		case '+':
			p.pos++
			r, err := p.parseMul()
			if err != nil {
				return 0, err
			}
			l += r
		case '-':
			p.pos++
			r, err := p.parseMul()
			if err != nil {
				return 0, err
			}
			l -= r
		default:
			return l, nil
		}
	}
}

func (p *calcParser) parseMul() (float64, error) {
	l, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		p.skipWS()
		if p.pos >= len(p.s) {
			return l, nil
		}
		switch p.s[p.pos] {
		case '*':
			p.pos++
			r, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			l *= r
		case '/':
			p.pos++
			r, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			if r == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			l /= r
		case '%':
			p.pos++
			r, err := p.parseUnary()
			if err != nil {
				return 0, err
			}
			if r == 0 {
				return 0, fmt.Errorf("modulo by zero")
			}
			l = math.Mod(l, r)
		default:
			return l, nil
		}
	}
}

func (p *calcParser) parseUnary() (float64, error) {
	p.skipWS()
	if p.pos >= len(p.s) {
		return 0, fmt.Errorf("unexpected end of expression")
	}
	if p.s[p.pos] == '-' {
		p.pos++
		v, err := p.parseUnary()
		return -v, err
	}
	if p.s[p.pos] == '+' {
		p.pos++
		return p.parseUnary()
	}
	return p.parsePow()
}

func (p *calcParser) parsePow() (float64, error) {
	base, err := p.parseAtom()
	if err != nil {
		return 0, err
	}
	p.skipWS()
	if p.pos < len(p.s) && p.s[p.pos] == '^' {
		p.pos++
		exp, err := p.parsePow() // 右结合
		if err != nil {
			return 0, err
		}
		return math.Pow(base, exp), nil
	}
	return base, nil
}

func (p *calcParser) parseAtom() (float64, error) {
	p.skipWS()
	if p.pos >= len(p.s) {
		return 0, fmt.Errorf("unexpected end of expression")
	}
	c := p.s[p.pos]
	if c == '(' {
		p.pos++
		v, err := p.parseAdd()
		if err != nil {
			return 0, err
		}
		p.skipWS()
		if p.pos >= len(p.s) || p.s[p.pos] != ')' {
			return 0, fmt.Errorf("missing ')'")
		}
		p.pos++
		return v, nil
	}
	if c >= '0' && c <= '9' || c == '.' {
		start := p.pos
		for p.pos < len(p.s) && (isDigit(p.s[p.pos]) || p.s[p.pos] == '.' || p.s[p.pos] == 'e' || p.s[p.pos] == 'E' || (p.s[p.pos] == '+' || p.s[p.pos] == '-') && p.pos > start && (p.s[p.pos-1] == 'e' || p.s[p.pos-1] == 'E')) {
			p.pos++
		}
		v, err := strconv.ParseFloat(p.s[start:p.pos], 64)
		if err != nil {
			return 0, err
		}
		return v, nil
	}
	if isAlpha(c) {
		start := p.pos
		for p.pos < len(p.s) && isAlpha(p.s[p.pos]) {
			p.pos++
		}
		name := p.s[start:p.pos]
		p.skipWS()
		if p.pos >= len(p.s) || p.s[p.pos] != '(' {
			return 0, fmt.Errorf("unknown function %q", name)
		}
		p.pos++ // '('
		args := []float64{}
		p.skipWS()
		if p.pos < len(p.s) && p.s[p.pos] == ')' {
			p.pos++
			return calcFunc1(name, args)
		}
		for {
			a, err := p.parseAdd()
			if err != nil {
				return 0, err
			}
			args = append(args, a)
			p.skipWS()
			if p.pos >= len(p.s) {
				return 0, fmt.Errorf("missing ')' in %q", name)
			}
			if p.s[p.pos] == ',' {
				p.pos++
				continue
			}
			if p.s[p.pos] == ')' {
				p.pos++
				break
			}
			return 0, fmt.Errorf("expected ',' or ')' in %q", name)
		}
		return calcFuncN(name, args)
	}
	return 0, fmt.Errorf("unexpected char %q", string(c))
}

func calcFunc1(name string, args []float64) (float64, error) {
	return calcFuncN(name, args)
}

func calcFuncN(name string, args []float64) (float64, error) {
	one := func(f func(float64) float64) (float64, error) {
		if len(args) != 1 {
			return 0, fmt.Errorf("%s expects 1 argument, got %d", name, len(args))
		}
		return f(args[0]), nil
	}
	switch strings.ToLower(name) {
	case "sqrt":
		return one(math.Sqrt)
	case "abs":
		return one(math.Abs)
	case "floor":
		return one(math.Floor)
	case "ceil":
		return one(math.Ceil)
	case "round":
		return one(math.Round)
	case "sin":
		return one(math.Sin)
	case "cos":
		return one(math.Cos)
	case "tan":
		return one(math.Tan)
	case "log", "ln":
		return one(math.Log)
	case "log10":
		return one(math.Log10)
	case "exp":
		return one(math.Exp)
	case "pow":
		if len(args) != 2 {
			return 0, fmt.Errorf("pow expects 2 arguments, got %d", len(args))
		}
		return math.Pow(args[0], args[1]), nil
	case "min":
		if len(args) == 0 {
			return 0, fmt.Errorf("min needs at least 1 argument")
		}
		m := args[0]
		for _, a := range args[1:] {
			if a < m {
				m = a
			}
		}
		return m, nil
	case "max":
		if len(args) == 0 {
			return 0, fmt.Errorf("max needs at least 1 argument")
		}
		m := args[0]
		for _, a := range args[1:] {
			if a > m {
				m = a
			}
		}
		return m, nil
	}
	return 0, fmt.Errorf("unknown function %q", name)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}
