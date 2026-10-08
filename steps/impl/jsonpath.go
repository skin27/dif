package impl

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// JSONPath as Jayway reads it, which is what Camel's jsonpath language uses:
//
//	$ or @           the document (a filter's @ is the element it tests)
//	.name  ['name']  a member; ['a','b'] two members
//	.*  [*]          every member or element
//	[n]  [-n]  [a,b] elements by index, from the end when negative
//	[a:b]  [a:]  [:b]  [a:b:c]   a slice of an array
//	..name  ..[...]  the selector on the node and all its descendants
//	[?(filter)]      the elements for which the filter holds
//	.length() .size() .min() .max() .avg() .sum() .first() .last() .keys() .index(n)
//
// A filter compares paths and literals with == != < <= > >= =~ in nin subsetof
// anyof noneof size empty contains, joined by && and || and negated with !; a
// path alone holds when it selects something.

type jsonPath struct {
	src   string
	steps []jsonStep
}

type selKind int

const (
	selName selKind = iota
	selWild
	selIndex
	selSlice
	selFilter
	selFunc
)

type jsonStep struct {
	deep   bool // the selector applies to the node and everything below it
	kind   selKind
	names  []string // selName: the members
	idx    []int    // selIndex: the elements
	slice  [3]*int  // selSlice: from, to, step
	filter jfilter  // selFilter
	fn     string   // selFunc
	args   []string
}

func compileJSONPath(expr string) (jsonPath, error) {
	s := strings.TrimSpace(expr)
	p, end, err := parseJSONPathAt(s, 0)
	if err != nil {
		return jsonPath{}, fmt.Errorf("jsonpath %q: %w", expr, err)
	}
	if end != len(s) {
		return jsonPath{}, fmt.Errorf("jsonpath %q: unexpected %q at %d", expr, s[end:], end)
	}
	return p, nil
}

// definite reports whether the path selects at most one value: it has only
// names and indexes, no wildcard, deep scan, union, slice or filter.
func (p jsonPath) definite() bool {
	for _, st := range p.steps {
		if st.deep || st.kind == selWild || st.kind == selSlice || st.kind == selFilter || len(st.names) > 1 || len(st.idx) > 1 {
			return false
		}
	}
	return true
}

func isNameByte(c byte) bool {
	return !strings.ContainsRune(".[]()'\" \t\r\n,=!<>&|~?@+/*", rune(c))
}

// parseJSONPathAt reads the path that starts at s[i], which is $ or @, and
// returns where it ends.
func parseJSONPathAt(s string, i int) (jsonPath, int, error) {
	if i >= len(s) || (s[i] != '$' && s[i] != '@') {
		return jsonPath{}, i, fmt.Errorf("a path starts with $ or @")
	}
	p := jsonPath{src: s}
	i++
	for i < len(s) {
		var st jsonStep
		switch {
		case strings.HasPrefix(s[i:], ".."):
			st.deep = true
			i += 2
			if i >= len(s) {
				return p, i, fmt.Errorf(".. needs a name, * or [ after it")
			}
			if s[i] == '[' {
				var err error
				if st, i, err = parseBracket(s, i, st); err != nil {
					return p, i, err
				}
			} else if s[i] == '*' {
				st.kind = selWild
				i++
			} else {
				j := i
				for j < len(s) && isNameByte(s[j]) {
					j++
				}
				if j == i {
					return p, i, fmt.Errorf(".. needs a name, * or [ after it")
				}
				st.kind, st.names = selName, []string{s[i:j]}
				i = j
			}
		case s[i] == '.':
			i++
			switch {
			case i < len(s) && s[i] == '[': // $.a.[0] is $.a[0]
				continue
			case i < len(s) && s[i] == '*':
				st.kind = selWild
				i++
			default:
				j := i
				for j < len(s) && isNameByte(s[j]) {
					j++
				}
				if j == i {
					return p, i, fmt.Errorf("a name is missing after the dot")
				}
				name := s[i:j]
				i = j
				if i < len(s) && s[i] == '(' { // .length()
					end := strings.IndexByte(s[i:], ')')
					if end < 0 {
						return p, i, fmt.Errorf("the function %s( is not closed", name)
					}
					st.kind, st.fn = selFunc, name
					if a := strings.TrimSpace(s[i+1 : i+end]); a != "" {
						for _, x := range strings.Split(a, ",") {
							st.args = append(st.args, strings.TrimSpace(x))
						}
					}
					if !jsonPathFuncs[name] {
						return p, i, fmt.Errorf("the function %s() is not supported", name)
					}
					i += end + 1
				} else {
					st.kind, st.names = selName, []string{name}
				}
			}
		case s[i] == '[':
			var err error
			if st, i, err = parseBracket(s, i, st); err != nil {
				return p, i, err
			}
		default:
			return p, i, nil // the end of the path inside a filter
		}
		p.steps = append(p.steps, st)
	}
	return p, i, nil
}

var jsonPathFuncs = map[string]bool{"length": true, "size": true, "min": true, "max": true, "avg": true, "sum": true,
	"first": true, "last": true, "keys": true, "index": true, "stddev": true}

// parseBracket reads the selector in the brackets at s[i].
func parseBracket(s string, i int, st jsonStep) (jsonStep, int, error) {
	end := matchingBracket(s, i)
	if end < 0 {
		return st, i, fmt.Errorf("[ is not closed")
	}
	in := strings.TrimSpace(s[i+1 : end])
	i = end + 1
	switch {
	case in == "*":
		st.kind = selWild
	case strings.HasPrefix(in, "?"):
		f, err := parseFilter(strings.TrimSpace(strings.TrimPrefix(in, "?")))
		if err != nil {
			return st, i, err
		}
		st.kind, st.filter = selFilter, f
	case in != "" && (in[0] == '\'' || in[0] == '"'):
		st.kind = selName
		for len(in) > 0 {
			name, rest, err := quotedString(in)
			if err != nil {
				return st, i, err
			}
			st.names = append(st.names, name)
			rest = strings.TrimSpace(rest)
			if rest == "" {
				break
			}
			if rest[0] != ',' {
				return st, i, fmt.Errorf("expected , between the names in [%s]", in)
			}
			in = strings.TrimSpace(rest[1:])
		}
	case strings.Contains(in, ":"):
		st.kind = selSlice
		parts := strings.Split(in, ":")
		if len(parts) > 3 {
			return st, i, fmt.Errorf("a slice has at most three parts: [%s]", in)
		}
		for k, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				n, err := strconv.Atoi(part)
				if err != nil {
					return st, i, fmt.Errorf("[%s] is no slice", in)
				}
				st.slice[k] = &n
			}
		}
	default:
		st.kind = selIndex
		for _, part := range strings.Split(in, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return st, i, fmt.Errorf("[%s] is no index, name, slice or filter", in)
			}
			st.idx = append(st.idx, n)
		}
	}
	return st, i, nil
}

// matchingBracket returns the index of the ] that closes the [ at s[i], or -1.
func matchingBracket(s string, i int) int {
	depth := 0
	var quote byte
	for j := i; j < len(s); j++ {
		c := s[j]
		switch {
		case quote != 0:
			if c == '\\' {
				j++
			} else if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// quotedString reads the quoted text at the start of s and returns it and the rest.
func quotedString(s string) (text, rest string, err error) {
	q := s[0]
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(s[i])
			}
		case c == q:
			return b.String(), s[i+1:], nil
		default:
			b.WriteByte(c)
		}
	}
	return "", "", fmt.Errorf("the text %s is not closed", s)
}

// eval returns the values the path selects in v, a decoded JSON document.
// Members of an object are visited in key order.
func (p jsonPath) eval(v any) []any { return p.evalAt(v, v) }

// evalAt is eval where $ is root and @ is cur.
func (p jsonPath) evalAt(root, cur any) []any {
	nodes := []any{cur}
	for i, st := range p.steps {
		if st.kind == selFunc {
			// A function takes the array a definite path selects, else all the
			// values a path selects.
			arg := any(nodes)
			if len(nodes) == 1 && i > 0 && p.steps[i-1].kind != selFilter && p.steps[i-1].kind != selWild && !st.deep {
				if a, ok := nodes[0].([]any); ok {
					arg = a
				}
			} else if len(nodes) == 1 && i == 0 {
				if a, ok := nodes[0].([]any); ok {
					arg = a
				}
			}
			r, ok := pathFunc(st, arg)
			if !ok {
				return nil
			}
			nodes = []any{r}
			continue
		}
		var next []any
		for _, n := range nodes {
			targets := []any{n}
			if st.deep {
				targets = descendantsAndSelf(n, nil)
			}
			for _, t := range targets {
				next = append(next, selectFrom(t, st, root)...)
			}
		}
		nodes = next
		if len(nodes) == 0 {
			return nil
		}
	}
	return nodes
}

// descendantsAndSelf returns v and everything below it, parents first.
func descendantsAndSelf(v any, out []any) []any {
	out = append(out, v)
	switch x := v.(type) {
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			out = descendantsAndSelf(x[k], out)
		}
	case []any:
		for _, e := range x {
			out = descendantsAndSelf(e, out)
		}
	}
	return out
}

func selectFrom(t any, st jsonStep, root any) []any {
	var out []any
	switch x := t.(type) {
	case map[string]any:
		switch st.kind {
		case selName:
			for _, n := range st.names {
				if v, ok := x[n]; ok {
					out = append(out, v)
				}
			}
		case selWild:
			for _, k := range slices.Sorted(maps.Keys(x)) {
				out = append(out, x[k])
			}
		case selFilter:
			if st.filter.test(root, x) {
				out = append(out, x)
			}
		}
	case []any:
		switch st.kind {
		case selWild:
			out = append(out, x...)
		case selIndex:
			for _, i := range st.idx {
				if i < 0 {
					i += len(x)
				}
				if i >= 0 && i < len(x) {
					out = append(out, x[i])
				}
			}
		case selSlice:
			from, to := sliceBounds(st.slice, len(x))
			for i := from; i < to; i++ {
				out = append(out, x[i])
			}
		case selFilter:
			for _, e := range x {
				if st.filter.test(root, e) {
					out = append(out, e)
				}
			}
		}
	}
	return out
}

// sliceBounds are the first element and the end of the slice [from:to] of n
// elements (a step other than 1 is not supported).
func sliceBounds(s [3]*int, n int) (from, to int) {
	from, to = 0, n
	if s[0] != nil {
		from = *s[0]
		if from < 0 {
			from += n
		}
	}
	if s[1] != nil {
		to = *s[1]
		if to < 0 {
			to += n
		}
	}
	from, to = max(0, min(from, n)), max(0, min(to, n))
	return from, max(from, to)
}

// pathFunc applies .length(), .max() and the other functions to a list of values
// (or to an array, whose elements they are).
func pathFunc(st jsonStep, arg any) (any, bool) {
	var vals []any
	switch x := arg.(type) {
	case []any:
		vals = x
	default:
		vals = []any{arg}
	}
	nums := func() []float64 {
		var f []float64
		for _, v := range vals {
			if n, ok := v.(float64); ok {
				f = append(f, n)
			}
		}
		return f
	}
	switch st.fn {
	case "length", "size":
		if len(vals) == 1 {
			switch x := vals[0].(type) {
			case string:
				return float64(utf8.RuneCountInString(x)), true
			case map[string]any:
				return float64(len(x)), true
			}
		}
		return float64(len(vals)), true
	case "min", "max", "avg", "sum", "stddev":
		f := nums()
		if len(f) == 0 {
			return nil, false
		}
		sum := 0.0
		lo, hi := f[0], f[0]
		for _, n := range f {
			sum += n
			lo, hi = math.Min(lo, n), math.Max(hi, n)
		}
		mean := sum / float64(len(f))
		switch st.fn {
		case "min":
			return lo, true
		case "max":
			return hi, true
		case "sum":
			return sum, true
		case "avg":
			return mean, true
		}
		v := 0.0
		for _, n := range f {
			v += (n - mean) * (n - mean)
		}
		return math.Sqrt(v / float64(len(f))), true
	case "first":
		if len(vals) > 0 {
			return vals[0], true
		}
	case "last":
		if len(vals) > 0 {
			return vals[len(vals)-1], true
		}
	case "index":
		if len(st.args) == 1 {
			if i, err := strconv.Atoi(st.args[0]); err == nil {
				if i < 0 {
					i += len(vals)
				}
				if i >= 0 && i < len(vals) {
					return vals[i], true
				}
			}
		}
	case "keys":
		if len(vals) == 1 {
			if m, ok := vals[0].(map[string]any); ok {
				keys := make([]any, 0, len(m))
				for _, k := range slices.Sorted(maps.Keys(m)) {
					keys = append(keys, k)
				}
				return keys, true
			}
		}
	}
	return nil, false
}

// decodeJSON returns body as decoded JSON: as is when it already is, else
// parsed from its text.
func decodeJSON(body any) (any, error) {
	switch body.(type) {
	case map[string]any, []any:
		return body, nil
	}
	var v any
	if err := json.Unmarshal(bytesOf(body), &v); err != nil {
		return nil, fmt.Errorf("body is not JSON: %w", err)
	}
	return v, nil
}

// matchJSON reports whether p selects a value in body other than null or
// false. A body that is not JSON matches nothing.
func (p jsonPath) matchJSON(body any) bool {
	v, err := decodeJSON(body)
	if err != nil {
		return false
	}
	for _, x := range p.eval(v) {
		if x != nil && x != false {
			return true
		}
	}
	return false
}

// ---- filters ----

type jfilter interface{ test(root, cur any) bool }

type (
	jOr  []jfilter
	jAnd []jfilter
	jNot struct{ f jfilter }
	jCmp struct {
		op          string
		left, right jOperand
	}
	jExists struct{ p jsonPath }
)

// jOperand yields the value of one side of a comparison; ok is false when a path selects nothing.
type jOperand func(root, cur any) (v any, ok bool)

func (f jOr) test(root, cur any) bool {
	for _, x := range f {
		if x.test(root, cur) {
			return true
		}
	}
	return false
}

func (f jAnd) test(root, cur any) bool {
	for _, x := range f {
		if !x.test(root, cur) {
			return false
		}
	}
	return true
}

func (f jNot) test(root, cur any) bool { return !f.f.test(root, cur) }

func (f jExists) test(root, cur any) bool {
	if f.p.src != "" && f.p.src[0] == '$' {
		return len(f.p.eval(root)) > 0
	}
	return len(f.p.evalAt(root, cur)) > 0
}

func (f jCmp) test(root, cur any) bool {
	l, lok := f.left(root, cur)
	r, rok := f.right(root, cur)
	return compareJSON(f.op, l, lok, r, rok)
}

// parseFilter reads the filter that is in the parentheses of [?( )].
func parseFilter(s string) (jfilter, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "(") || !strings.HasSuffix(s, ")") {
		return nil, fmt.Errorf("a filter is written [?(...)]")
	}
	fp := &filterParser{s: s[1 : len(s)-1]}
	f, err := fp.or()
	if err != nil {
		return nil, err
	}
	if fp.skip(); fp.i != len(fp.s) {
		return nil, fmt.Errorf("unexpected %q in the filter", fp.s[fp.i:])
	}
	return f, nil
}

type filterParser struct {
	s string
	i int
}

func (p *filterParser) skip() {
	for p.i < len(p.s) && strings.IndexByte(" \t\r\n", p.s[p.i]) >= 0 {
		p.i++
	}
}

func (p *filterParser) has(t string) bool {
	p.skip()
	return strings.HasPrefix(p.s[p.i:], t)
}

func (p *filterParser) or() (jfilter, error) {
	var parts jOr
	for {
		f, err := p.and()
		if err != nil {
			return nil, err
		}
		parts = append(parts, f)
		if !p.has("||") {
			break
		}
		p.i += 2
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return parts, nil
}

func (p *filterParser) and() (jfilter, error) {
	var parts jAnd
	for {
		f, err := p.not()
		if err != nil {
			return nil, err
		}
		parts = append(parts, f)
		if !p.has("&&") {
			break
		}
		p.i += 2
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return parts, nil
}

func (p *filterParser) not() (jfilter, error) {
	if p.has("!") && !p.has("!=") {
		p.i++
		f, err := p.not()
		return jNot{f}, err
	}
	if p.has("(") {
		p.i++
		f, err := p.or()
		if err != nil {
			return nil, err
		}
		if !p.has(")") {
			return nil, fmt.Errorf("( is not closed in the filter")
		}
		p.i++
		return f, nil
	}
	return p.comparison()
}

var filterOps = []string{"==", "!=", "<>", "<=", ">=", "=~", "<", ">", " nin ", " in ", " subsetof ", " anyof ", " noneof ", " size ", " empty ", " contains "}

func (p *filterParser) comparison() (jfilter, error) {
	left, lpath, err := p.operand()
	if err != nil {
		return nil, err
	}
	p.skip()
	rest := p.s[p.i:]
	// an operator word needs the spaces around it; skip() ate those before it
	spaced := " " + rest
	for _, op := range filterOps {
		var matched bool
		if op[0] == ' ' {
			matched = strings.HasPrefix(spaced, op)
		} else {
			matched = strings.HasPrefix(rest, op)
		}
		if !matched {
			continue
		}
		if op[0] == ' ' {
			p.i += len(op) - 1
		} else {
			p.i += len(op)
		}
		right, _, err := p.operand()
		if err != nil {
			return nil, err
		}
		return jCmp{strings.TrimSpace(strings.Replace(op, "<>", "!=", 1)), left, right}, nil
	}
	if lpath != nil {
		return jExists{*lpath}, nil
	}
	return jCmp{"truthy", left, left}, nil
}

// operand reads a path, a literal or an array; for a path it returns it too.
func (p *filterParser) operand() (jOperand, *jsonPath, error) {
	p.skip()
	if p.i >= len(p.s) {
		return nil, nil, fmt.Errorf("the filter ends where a value is expected")
	}
	switch c := p.s[p.i]; {
	case c == '@' || c == '$':
		path, end, err := parseJSONPathAt(p.s, p.i)
		if err != nil {
			return nil, nil, err
		}
		p.i = end
		abs := c == '$'
		op := func(root, cur any) (any, bool) {
			var res []any
			if abs {
				res = path.eval(root)
			} else {
				res = path.evalAt(root, cur)
			}
			switch {
			case len(res) == 0:
				return nil, false
			case path.definite() || len(res) == 1 && path.steps[len(path.steps)-1].kind == selFunc:
				return res[0], true
			}
			return res, true
		}
		return op, &path, nil
	case c == '\'' || c == '"':
		text, rest, err := quotedString(p.s[p.i:])
		if err != nil {
			return nil, nil, err
		}
		p.i = len(p.s) - len(rest)
		return func(_, _ any) (any, bool) { return text, true }, nil, nil
	case c == '/':
		end := p.i + 1
		for end < len(p.s) && p.s[end] != '/' {
			if p.s[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(p.s) {
			return nil, nil, fmt.Errorf("the regular expression %s is not closed", p.s[p.i:])
		}
		pattern := p.s[p.i+1 : end]
		end++
		flags := ""
		for end < len(p.s) && strings.IndexByte("imsx", p.s[end]) >= 0 {
			flags += string(p.s[end])
			end++
		}
		p.i = end
		prefix := ""
		if f := strings.ReplaceAll(strings.ReplaceAll(flags, "x", ""), "i", ""); f != "" || strings.Contains(flags, "i") {
			prefix = "(?" + strings.ReplaceAll(flags, "x", "") + ")"
		}
		re, err := regexp.Compile(prefix + pattern)
		if err != nil {
			return nil, nil, fmt.Errorf("regular expression %q: %w", pattern, err)
		}
		return func(_, _ any) (any, bool) { return re, true }, nil, nil
	case c == '[':
		end := matchingBracket(p.s, p.i)
		if end < 0 {
			return nil, nil, fmt.Errorf("[ is not closed in the filter")
		}
		var arr []any
		if err := json.Unmarshal([]byte(strings.ReplaceAll(p.s[p.i:end+1], "'", `"`)), &arr); err != nil {
			return nil, nil, fmt.Errorf("the array %s is no JSON: %w", p.s[p.i:end+1], err)
		}
		p.i = end + 1
		return func(_, _ any) (any, bool) { return arr, true }, nil, nil
	}
	// a number, true, false or null
	j := p.i
	for j < len(p.s) && strings.IndexByte(" \t\r\n)=!<>&|", p.s[j]) < 0 {
		j++
	}
	word := p.s[p.i:j]
	p.i = j
	var v any
	switch word {
	case "true":
		v = true
	case "false":
		v = false
	case "null":
		v = nil
	default:
		n, err := strconv.ParseFloat(word, 64)
		if err != nil {
			return nil, nil, fmt.Errorf("%q is no value in the filter", word)
		}
		v = n
	}
	return func(_, _ any) (any, bool) { return v, true }, nil, nil
}

// compareJSON applies a filter operator to two values.
func compareJSON(op string, l any, lok bool, r any, rok bool) bool {
	switch op {
	case "truthy":
		return lok && l != nil && l != false
	case "==":
		return lok && rok && jsonEqual(l, r)
	case "!=":
		return !(lok && rok && jsonEqual(l, r))
	case "<", "<=", ">", ">=":
		if !lok || !rok {
			return false
		}
		var c int
		switch a := l.(type) {
		case float64:
			b, ok := r.(float64)
			if !ok {
				return false
			}
			switch {
			case a < b:
				c = -1
			case a > b:
				c = 1
			}
		case string:
			b, ok := r.(string)
			if !ok {
				return false
			}
			c = strings.Compare(a, b)
		default:
			return false
		}
		switch op {
		case "<":
			return c < 0
		case "<=":
			return c <= 0
		case ">":
			return c > 0
		}
		return c >= 0
	case "=~":
		re, ok := r.(*regexp.Regexp)
		s, isStr := l.(string)
		if !lok || !ok || !isStr {
			return false
		}
		loc := re.FindStringIndex(s)
		return loc != nil && loc[0] == 0 && loc[1] == len(s) // a regular expression must match all of it
	case "in", "nin":
		arr, ok := r.([]any)
		if !lok || !ok {
			return false
		}
		found := slices.ContainsFunc(arr, func(e any) bool { return jsonEqual(l, e) })
		return found == (op == "in")
	case "subsetof", "anyof", "noneof":
		la, lis := l.([]any)
		ra, ris := r.([]any)
		if !lok || !lis || !ris {
			return false
		}
		n := 0
		for _, e := range la {
			if slices.ContainsFunc(ra, func(x any) bool { return jsonEqual(e, x) }) {
				n++
			}
		}
		switch op {
		case "subsetof":
			return n == len(la)
		case "anyof":
			return n > 0
		}
		return n == 0
	case "size":
		want, ok := r.(float64)
		if !lok || !ok {
			return false
		}
		switch x := l.(type) {
		case []any:
			return float64(len(x)) == want
		case string:
			return float64(utf8.RuneCountInString(x)) == want
		}
		return false
	case "empty":
		want, ok := r.(bool)
		if !lok || !ok {
			return false
		}
		switch x := l.(type) {
		case []any:
			return (len(x) == 0) == want
		case string:
			return (x == "") == want
		}
		return false
	case "contains":
		switch x := l.(type) {
		case string:
			s, ok := r.(string)
			return ok && strings.Contains(x, s)
		case []any:
			return slices.ContainsFunc(x, func(e any) bool { return jsonEqual(e, r) })
		}
		return false
	}
	return false
}

func jsonEqual(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case []any:
		y, ok := b.([]any)
		return ok && slices.EqualFunc(x, y, jsonEqual)
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, v := range x {
			if w, ok := y[k]; !ok || !jsonEqual(v, w) {
				return false
			}
		}
		return true
	}
	return false
}
