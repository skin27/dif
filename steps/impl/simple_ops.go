package impl

import (
	"fmt"
	"regexp"
	"strings"

	"dif/message"
)

// cond is a compiled condition.
type cond func(e *env) (bool, error)

// binaryOperators are the operators of Camel's simple language between two
// values, with a space on both sides, longest first where one starts with another.
var binaryOperators = []string{
	"not contains", "not regex", "not range", "not in", "not is", "starts with", "ends with",
	"!startsWith", "!endsWith", "!contains", "!regex", "!range", "!is", "!in", "!=~", "!~~", "!=",
	"startsWith", "endsWith", "contains", "regex", "range", "==", "=~", ">=", "<=", "~~", ">", "<", "is", "in",
}

// findOperator returns the first binary operator in s that is outside quotes,
// with a space on both sides, and its index; -1 if there is none.
func findOperator(s string) (string, int) {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
			continue
		case c == '\'' || c == '"':
			quote = c
			continue
		case i == 0 || s[i-1] != ' ':
			continue
		}
		for _, op := range binaryOperators {
			if strings.HasPrefix(s[i:], op) && i+len(op) < len(s) && s[i+len(op)] == ' ' {
				return op, i
			}
		}
	}
	return "", -1
}

// splitLogical splits s at every op outside quotes.
func splitLogical(s, op string) []string {
	var parts []string
	var quote byte
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case strings.HasPrefix(s[i:], op):
			parts = append(parts, s[start:i])
			i += len(op) - 1
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

// pseudoBlock makes a block of the template s, so that conditions over the
// text of a block can be used for it: its blocks become placeholders.
func pseudoBlock(s string) (*block, error) {
	items, err := parseTemplate(s)
	if err != nil {
		return nil, err
	}
	b := &block{src: s}
	var body strings.Builder
	for _, it := range items {
		if it.blk != nil {
			b.kids = append(b.kids, it.blk)
			body.WriteString(placeholder(len(b.kids) - 1))
		} else {
			body.WriteString(it.text)
		}
	}
	b.body = body.String()
	return b, nil
}

// condition compiles s, text of b (see block), as a condition: values joined by
// && and ||, each one a comparison or a value that is true.
func (c *compiler) condition(b *block, s string) (cond, error) {
	s = strings.TrimSpace(s)
	for _, word := range []string{" and ", " or "} {
		if findOutsideQuotes(s, word) >= 0 {
			return nil, fmt.Errorf("simple predicate %q: combine conditions with && and ||", b.src)
		}
	}
	for _, join := range []struct {
		op  string
		and bool
	}{{"||", false}, {"&&", true}} {
		parts := splitLogical(s, join.op)
		if len(parts) == 1 {
			continue
		}
		conds := make([]cond, len(parts))
		for i, p := range parts {
			var err error
			if conds[i], err = c.condition(b, p); err != nil {
				return nil, err
			}
		}
		return func(e *env) (bool, error) {
			for _, f := range conds {
				ok, err := f(e)
				if err != nil {
					return false, err
				}
				if ok != join.and {
					return ok, nil // false for &&, true for ||
				}
			}
			return join.and, nil
		}, nil
	}
	return c.comparison(b, s)
}

// comparison compiles one condition: left operator right, or a value that must be true.
func (c *compiler) comparison(b *block, s string) (cond, error) {
	op, i := findOperator(s)
	if i < 0 {
		v, err := c.operand(b, s)
		if err != nil {
			return nil, err
		}
		return func(e *env) (bool, error) {
			x, err := v(e)
			if err != nil {
				return false, err
			}
			if t, ok := asBool(x); ok {
				return t, nil
			}
			return false, nil
		}, nil
	}

	left, err := c.operand(b, s[:i])
	if err != nil {
		return nil, err
	}
	rightText := strings.TrimSpace(s[i+len(op):])
	right, err := c.operand(b, rightText)
	if err != nil {
		return nil, err
	}
	test, err := operatorTest(op, rightText)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", b.src, err)
	}
	test = wrapNull(op, test)
	return func(e *env) (bool, error) {
		l, err := left(e)
		if err != nil {
			return false, err
		}
		r, err := right(e)
		if err != nil {
			return false, err
		}
		return test(l, r)
	}, nil
}

// wrapNull makes every operator but == and != see a null literal as nothing.
func wrapNull(op string, f func(l, r any) (bool, error)) func(l, r any) (bool, error) {
	if op == "==" || op == "!=" {
		return f
	}
	return func(l, r any) (bool, error) { return f(denull(l), denull(r)) }
}

// operand compiles one side of a comparison: quoted text, null, true, false, a
// number, or a template.
func (c *compiler) operand(b *block, s string) (evalFn, error) {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return c.template(b, s[1:len(s)-1])
	}
	if s == "null" {
		return func(*env) (any, error) { return nullLiteral{}, nil }, nil
	}
	if lit := literalValue(s); !isText(lit) {
		return func(*env) (any, error) { return lit, nil }, nil
	}
	return c.template(b, s)
}

// nullLiteral is the value of null written in a condition. Only nothing equals
// it, where nothing also equals the empty text.
type nullLiteral struct{}

// denull makes the value of a null literal nothing.
func denull(v any) any {
	if _, ok := v.(nullLiteral); ok {
		return nil
	}
	return v
}

// operatorTest returns the function that applies the operator to two values.
func operatorTest(op, rightText string) (func(l, r any) (bool, error), error) {
	not := func(f func(l, r any) (bool, error)) func(l, r any) (bool, error) {
		return func(l, r any) (bool, error) { ok, err := f(l, r); return !ok && err == nil, err }
	}
	switch op {
	case "==":
		return func(l, r any) (bool, error) { return valuesEqual(l, r), nil }, nil
	case "!=":
		return not(func(l, r any) (bool, error) { return valuesEqual(l, r), nil }), nil
	case "=~":
		return func(l, r any) (bool, error) { return strings.EqualFold(render(l), render(r)), nil }, nil
	case "!=~":
		return not(func(l, r any) (bool, error) { return strings.EqualFold(render(l), render(r)), nil }), nil
	case ">", ">=", "<", "<=":
		return func(l, r any) (bool, error) { return valuesOrdered(op, l, r), nil }, nil
	case "contains", "!contains", "not contains":
		f := func(l, r any) (bool, error) { return valueContains(l, r, false), nil }
		if op == "contains" {
			return f, nil
		}
		return not(f), nil
	case "~~", "!~~":
		f := func(l, r any) (bool, error) { return valueContains(l, r, true), nil }
		if op == "~~" {
			return f, nil
		}
		return not(f), nil
	case "regex", "!regex", "not regex":
		f := func(l, r any) (bool, error) {
			re, err := javaRegexp("^(?:" + render(r) + ")$")
			if err != nil {
				return false, err
			}
			return l != nil && re.MatchString(render(l)), nil
		}
		if op == "regex" {
			return f, nil
		}
		return not(f), nil
	case "in", "!in", "not in":
		f := func(l, r any) (bool, error) {
			for _, x := range elements(r) {
				if valuesEqual(l, x) {
					return true, nil
				}
			}
			return false, nil
		}
		if op == "in" {
			return f, nil
		}
		return not(f), nil
	case "is", "!is", "not is":
		f := func(l, r any) (bool, error) { return valueIs(l, strings.TrimSpace(render(r))) }
		if op == "is" {
			return f, nil
		}
		return not(f), nil
	case "range", "!range", "not range":
		f := func(l, r any) (bool, error) { return inRange(l, render(r)) }
		if op == "range" {
			return f, nil
		}
		return not(f), nil
	case "startsWith", "starts with", "!startsWith":
		f := func(l, r any) (bool, error) {
			return l != nil && r != nil && strings.HasPrefix(render(l), render(r)), nil
		}
		if op != "!startsWith" {
			return f, nil
		}
		return not(f), nil
	case "endsWith", "ends with", "!endsWith":
		f := func(l, r any) (bool, error) {
			return l != nil && r != nil && strings.HasSuffix(render(l), render(r)), nil
		}
		if op != "!endsWith" {
			return f, nil
		}
		return not(f), nil
	}
	return nil, fmt.Errorf("operator %s is not supported", op)
}

// valuesEqual compares two values as Camel does: numbers by value when both
// are numbers, otherwise as text. Nothing equals an empty text, as a missing
// header is not told from an empty one.
func valuesEqual(l, r any) bool {
	_, lnull := l.(nullLiteral)
	_, rnull := r.(nullLiteral)
	if lnull || rnull {
		return (l == nil || lnull) && (r == nil || rnull)
	}
	if l == nil || r == nil {
		return empty(l) && empty(r) || l == nil && r == nil
	}
	if ln, _, ok := asNumber(l); ok {
		if rn, _, ok := asNumber(r); ok {
			return ln == rn
		}
	}
	if lb, ok := l.(bool); ok {
		if rb, ok := asBool(r); ok {
			return lb == rb
		}
	}
	if rb, ok := r.(bool); ok {
		if lb, ok := asBool(l); ok {
			return lb == rb
		}
	}
	return render(l) == render(r)
}

// valuesOrdered applies > >= < <=: by value for numbers, else by text.
func valuesOrdered(op string, l, r any) bool {
	if l == nil || r == nil {
		return false
	}
	var cmp int
	ln, _, lok := asNumber(l)
	rn, _, rok := asNumber(r)
	if lok && rok {
		switch {
		case ln < rn:
			cmp = -1
		case ln > rn:
			cmp = 1
		}
	} else {
		cmp = strings.Compare(render(l), render(r))
	}
	switch op {
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	}
	return cmp <= 0
}

// valueContains is contains: an element of a list, or part of a text.
func valueContains(l, r any, ignoreCase bool) bool {
	if l == nil || r == nil {
		return false
	}
	switch x := l.(type) {
	case jlist:
		return listHas([]any(x), r, ignoreCase)
	case []any:
		return listHas(x, r, ignoreCase)
	}
	ls, rs := render(l), render(r)
	if ignoreCase {
		ls, rs = strings.ToLower(ls), strings.ToLower(rs)
	}
	return strings.Contains(ls, rs)
}

func listHas(l []any, v any, ignoreCase bool) bool {
	for _, e := range l {
		if ignoreCase && strings.EqualFold(render(e), render(v)) || !ignoreCase && valuesEqual(e, v) {
			return true
		}
	}
	return false
}

// valueIs is the instanceof operator for the types a flow can name.
func valueIs(v any, typ string) (bool, error) {
	typ = strings.TrimPrefix(strings.TrimSuffix(typ, ".class"), "java.lang.")
	typ = strings.TrimPrefix(typ, "java.util.")
	switch typ {
	case "Object":
		return v != nil, nil
	case "String", "CharSequence":
		_, ok := v.(string)
		return ok, nil
	case "Integer", "Long", "Short", "int", "long":
		switch x := v.(type) {
		case int, int64:
			return true, nil
		case float64:
			return false, nil
		default:
			_ = x
			return false, nil
		}
	case "Number", "Double", "double", "Float":
		_, _, ok := asNumber(v)
		return ok && !isText(v), nil
	case "Boolean", "boolean":
		_, ok := v.(bool)
		return ok, nil
	case "List", "ArrayList", "Collection":
		switch v.(type) {
		case jlist, []any:
			return true, nil
		}
		return false, nil
	case "Map", "LinkedHashMap", "HashMap":
		switch v.(type) {
		case map[string]any, jmap:
			return true, nil
		}
		return false, nil
	case "byte[]":
		_, ok := v.([]byte)
		return ok, nil
	}
	return false, fmt.Errorf("is operator cannot find class with name: %s", typ)
}

var rangePattern = regexp.MustCompile(`^\s*(-?\d+)\s*\.\.\s*(-?\d+)\s*$`)

// inRange is the range operator: a number from..to, both ends in.
func inRange(v any, spec string) (bool, error) {
	m := rangePattern.FindStringSubmatch(spec)
	if m == nil {
		return false, fmt.Errorf("range operator is not valid. Valid syntax:'from..to' (where from and to are numbers)")
	}
	n, _, ok := asNumber(v)
	if !ok {
		return false, nil
	}
	lo, _, _ := parseNumber(m[1])
	hi, _, _ := parseNumber(m[2])
	return n >= lo && n <= hi, nil
}

// compileSimplePredicate compiles expr as a condition of the simple language.
func compileSimplePredicate(flow *flowProperties, expr string) (predicate, error) {
	b, err := pseudoBlock(strings.TrimSpace(expr))
	if err != nil {
		return nil, err
	}
	f, err := (&compiler{flow: flow}).condition(b, b.body)
	if err != nil {
		return nil, err
	}
	return func(m message.Message) (bool, error) { return f(&env{m: m}) }, nil
}
