package mapping

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type token struct {
	kind, text string
	line, col  int
}
type expr struct {
	op       string
	tok      token
	value    any
	names    []string
	args     []*expr
	depth    int
	function *Function
}
type parser struct {
	tokens      []token
	at, nesting int
	limits      Limits
	scope       map[string]bool
	resolve     Resolver
}

func syntax(t token, s string) error { return fmt.Errorf("line %d column %d: %s", t.line, t.col, s) }
func lex(src string, l Limits) ([]token, error) {
	if len(src) > l.SourceBytes {
		return nil, fmt.Errorf("mapping source size limit exceeded")
	}
	if !utf8.ValidString(src) {
		return nil, fmt.Errorf("mapping source is not UTF-8")
	}
	out := []token{}
	i, line, col := 0, 1, 1
	advance := func() {
		r, n := utf8.DecodeRuneInString(src[i:])
		i += n
		if r == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	for i < len(src) {
		r, _ := utf8.DecodeRuneInString(src[i:])
		if unicode.IsSpace(r) {
			advance()
			continue
		}
		t := token{line: line, col: col}
		start := i
		if strings.HasPrefix(src[i:], "//") {
			for i < len(src) && src[i] != '\n' {
				advance()
			}
			continue
		}
		if strings.HasPrefix(src[i:], "/*") {
			advance()
			advance()
			for i < len(src) && !strings.HasPrefix(src[i:], "*/") {
				advance()
			}
			if i == len(src) {
				return nil, syntax(t, "unterminated comment")
			}
			advance()
			advance()
			continue
		}
		switch {
		case r == '"':
			advance()
			escaped := false
			closed := false
			for i < len(src) {
				ch := src[i]
				advance()
				if ch == '"' && !escaped {
					closed = true
					break
				}
				if ch == '\\' && !escaped {
					escaped = true
				} else {
					escaped = false
				}
			}
			if !closed {
				return nil, syntax(t, "unterminated string")
			}
			s, e := strconv.Unquote(src[start:i])
			if e != nil {
				return nil, syntax(t, "invalid string")
			}
			t.kind = "str"
			t.text = s
		case r >= '0' && r <= '9':
			for i < len(src) && src[i] >= '0' && src[i] <= '9' {
				advance()
			}
			if i < len(src) && src[i] == '.' {
				advance()
				for i < len(src) && src[i] >= '0' && src[i] <= '9' {
					advance()
				}
			}
			if i < len(src) && (src[i] == 'e' || src[i] == 'E') {
				advance()
				if i < len(src) && (src[i] == '+' || src[i] == '-') {
					advance()
				}
				for i < len(src) && src[i] >= '0' && src[i] <= '9' {
					advance()
				}
			}
			t.kind = "num"
			t.text = src[start:i]
		case unicode.IsLetter(r) || r == '_':
			for i < len(src) {
				c, _ := utf8.DecodeRuneInString(src[i:])
				if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
					break
				}
				advance()
			}
			t.text = src[start:i]
			t.kind = "id"
			if strings.Contains(" let if else map filter default and or not true false null ", " "+t.text+" ") {
				t.kind = t.text
			}
		default:
			advance()
			t.kind = src[start:i]
			if i < len(src) {
				pair := src[start : i+1]
				if pair == "->" || pair == "==" || pair == "!=" || pair == "<=" || pair == ">=" || pair == "++" {
					advance()
					t.kind = pair
				}
			}
			if !strings.Contains("{}[](),;:.=+-*/<>!", string(r)) {
				return nil, syntax(t, "unexpected character")
			}
		}
		out = append(out, t)
		if len(out) > l.Operations {
			return nil, fmt.Errorf("mapping token limit exceeded")
		}
	}
	return append(out, token{kind: "eof", line: line, col: col}), nil
}
func compileExpression(src string, l Limits) (*expr, error) {
	return compileScoped(src, l, map[string]bool{"payload": true, "headers": true}, nil)
}
func compileScoped(src string, l Limits, scope map[string]bool, resolve Resolver) (*expr, error) {
	ts, e := lex(src, l)
	if e != nil {
		return nil, e
	}
	p := parser{tokens: ts, limits: l, scope: scope, resolve: resolve}
	x, e := p.expression(0)
	if e == nil && p.peek().kind != "eof" {
		e = syntax(p.peek(), "unexpected token after expression")
	}
	return x, e
}
func (p *parser) peek() token { return p.tokens[p.at] }
func (p *parser) take() token { t := p.peek(); p.at++; return t }
func (p *parser) accept(k string) bool {
	if p.peek().kind == k {
		p.at++
		return true
	}
	return false
}
func (p *parser) want(k string) (token, error) {
	t := p.peek()
	if !p.accept(k) {
		return t, syntax(t, "expected "+k)
	}
	return t, nil
}

func propertyToken(t token) bool {
	return t.kind == "id" || strings.Contains(" let if else map filter default and or not true false null ", " "+t.kind+" ")
}
func (p *parser) node(t token, op string, args ...*expr) (*expr, error) {
	d := 1
	for _, a := range args {
		d = max(d, a.depth+1)
	}
	if d > p.limits.Depth {
		return nil, syntax(t, "AST depth limit exceeded")
	}
	return &expr{op: op, tok: t, args: args, depth: d}, nil
}

var precedence = map[string]int{"map": 1, "filter": 1, "default": 2, "or": 3, "and": 4, "==": 5, "!=": 5, "<": 6, ">": 6, "<=": 6, ">=": 6, "++": 7, "+": 8, "-": 8, "*": 9, "/": 9}

func (p *parser) expression(min int) (*expr, error) {
	p.nesting++
	defer func() { p.nesting-- }()
	if p.nesting > p.limits.Depth {
		return nil, syntax(p.peek(), "parse depth limit exceeded")
	}
	left, err := p.prefix()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind == "." {
			p.take()
			k := p.take()
			if !propertyToken(k) {
				return nil, syntax(k, "expected property name")
			}
			key, _ := p.node(k, "literal")
			key.value = k.text
			left, err = p.node(t, "select", left, key)
			if err != nil {
				return nil, err
			}
			continue
		}
		if t.kind == "[" {
			p.take()
			key, e := p.expression(0)
			if e != nil {
				return nil, e
			}
			if _, e = p.want("]"); e != nil {
				return nil, e
			}
			left, err = p.node(t, "select", left, key)
			if err != nil {
				return nil, err
			}
			continue
		}
		prec, ok := precedence[t.kind]
		if !ok || prec < min {
			break
		}
		p.take()
		var right *expr
		var names []string
		if t.kind == "map" || t.kind == "filter" {
			if _, err = p.want("("); err != nil {
				return nil, err
			}
			n, e := p.want("id")
			if e != nil {
				return nil, e
			}
			names = append(names, n.text)
			if p.accept(",") {
				n, e = p.want("id")
				if e != nil {
					return nil, e
				}
				if n.text == names[0] {
					return nil, syntax(n, "duplicate lambda parameter")
				}
				names = append(names, n.text)
			}
			if _, err = p.want(")"); err != nil {
				return nil, err
			}
			if _, err = p.want("->"); err != nil {
				return nil, err
			}
			old := p.scope
			p.scope = copyScope(old)
			for _, name := range names {
				if name == "payload" || name == "headers" {
					return nil, syntax(t, "reserved lambda parameter")
				}
				p.scope[name] = true
			}
			right, err = p.expression(prec + 1)
			p.scope = old
		} else {
			right, err = p.expression(prec + 1)
		}
		if err != nil {
			return nil, err
		}
		left, err = p.node(t, t.kind, left, right)
		if err != nil {
			return nil, err
		}
		left.names = names
	}
	return left, nil
}
func copyScope(s map[string]bool) map[string]bool {
	o := map[string]bool{}
	for k, v := range s {
		o[k] = v
	}
	return o
}
func (p *parser) prefix() (*expr, error) {
	t := p.take()
	switch t.kind {
	case "str", "num", "true", "false", "null":
		n, _ := p.node(t, "literal")
		switch t.kind {
		case "str":
			n.value = t.text
		case "num":
			v, e := number(t.text, p.limits)
			if e != nil {
				return nil, syntax(t, e.Error())
			}
			n.value = v
		case "true":
			n.value = true
		case "false":
			n.value = false
		}
		return n, nil
	case "id":
		if p.accept("(") {
			args := []*expr{}
			if !p.accept(")") {
				for {
					a, e := p.expression(0)
					if e != nil {
						return nil, e
					}
					args = append(args, a)
					if p.accept(")") {
						break
					}
					if _, e = p.want(","); e != nil {
						return nil, e
					}
				}
			}
			if t.text == "function" {
				if len(args) == 0 || args[0].op != "literal" {
					return nil, syntax(t, "function requires a literal name")
				}
				name, ok := args[0].value.(string)
				if !ok || p.resolve == nil {
					return nil, syntax(t, "unknown function")
				}
				f, err := p.resolve(name)
				if err != nil {
					return nil, syntax(t, err.Error())
				}
				if err = f.CheckArity(len(args) - 1); err != nil {
					return nil, syntax(t, err.Error())
				}
				for i, a := range args[1:] {
					if a.op == "literal" && !matches(f.parameters[i].Type, a.value) {
						return nil, syntax(t, "function argument type mismatch")
					}
				}
				n, err := p.node(t, "function", args[1:]...)
				if err == nil {
					n.function = f
				}
				return n, err
			}
			if t.text == "coalesce" {
				if len(args) < 2 {
					return nil, syntax(t, "coalesce requires at least two arguments")
				}
				return p.node(t, "coalesce", args...)
			}
			arity, ok := builtins[t.text]
			if !ok || len(args) != arity {
				return nil, syntax(t, "unknown function or invalid arity")
			}
			n, e := p.node(t, "call", args...)
			if e == nil {
				n.names = []string{t.text}
			}
			return n, e
		}
		if !p.scope[t.text] {
			return nil, syntax(t, "unknown variable")
		}
		n, _ := p.node(t, "variable")
		n.names = []string{t.text}
		return n, nil
	case "-", "not", "!":
		a, e := p.expression(10)
		if e != nil {
			return nil, e
		}
		return p.node(t, "unary"+t.kind, a)
	case "(":
		a, e := p.expression(0)
		if e != nil {
			return nil, e
		}
		_, e = p.want(")")
		return a, e
	case "[":
		args := []*expr{}
		if !p.accept("]") {
			for {
				a, e := p.expression(0)
				if e != nil {
					return nil, e
				}
				args = append(args, a)
				if p.accept("]") {
					break
				}
				if _, e = p.want(","); e != nil {
					return nil, e
				}
			}
		}
		return p.node(t, "array", args...)
	case "{":
		names := []string{}
		seen := map[string]bool{}
		args := []*expr{}
		if !p.accept("}") {
			for {
				k := p.take()
				if !propertyToken(k) && k.kind != "str" {
					return nil, syntax(k, "expected object key")
				}
				if seen[k.text] {
					return nil, syntax(k, "duplicate object key")
				}
				seen[k.text] = true
				names = append(names, k.text)
				if _, e := p.want(":"); e != nil {
					return nil, e
				}
				a, e := p.expression(0)
				if e != nil {
					return nil, e
				}
				args = append(args, a)
				if p.accept("}") {
					break
				}
				if _, e = p.want(","); e != nil {
					return nil, e
				}
			}
		}
		n, e := p.node(t, "object", args...)
		if e == nil {
			n.names = names
		}
		return n, e
	case "if":
		if _, e := p.want("("); e != nil {
			return nil, e
		}
		c, e := p.expression(0)
		if e != nil {
			return nil, e
		}
		if _, e = p.want(")"); e != nil {
			return nil, e
		}
		a, e := p.expression(0)
		if e != nil {
			return nil, e
		}
		if _, e = p.want("else"); e != nil {
			return nil, e
		}
		b, e := p.expression(0)
		if e != nil {
			return nil, e
		}
		return p.node(t, "if", c, a, b)
	case "let":
		name, e := p.want("id")
		if e != nil {
			return nil, e
		}
		if p.scope[name.text] {
			return nil, syntax(name, "binding already exists")
		}
		if _, e = p.want("="); e != nil {
			return nil, e
		}
		v, e := p.expression(0)
		if e != nil {
			return nil, e
		}
		if _, e = p.want(";"); e != nil {
			return nil, e
		}
		old := p.scope
		p.scope = copyScope(old)
		p.scope[name.text] = true
		body, e := p.expression(0)
		p.scope = old
		if e != nil {
			return nil, e
		}
		n, e := p.node(t, "let", v, body)
		if e == nil {
			n.names = []string{name.text}
		}
		return n, e
	default:
		return nil, syntax(t, "expected expression")
	}
}
