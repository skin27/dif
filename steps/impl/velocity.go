package impl

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// A subset of the Velocity Template Language, which is what Camel's velocity
// component runs. It is the part that templates of flows use to build a text
// out of the body and the headers; the rest of Velocity is refused when the
// template is compiled, never run differently:
//
//   - comments: ## to the end of the line, #* ... *#, and #[[ ... ]]# for text
//     that is not parsed
//   - references: $name, ${name}, $!name (nothing if undefined), followed by
//     .property, [index] and .method(arguments) for the methods in velocityMethods
//   - #set($name = expression)
//   - #if(expression) ... #elseif(expression) ... #else ... #end
//   - #foreach($name in expression) ... #end, with $foreach.index, .count,
//     .hasNext, .first, .last and $velocityCount
//   - expressions: strings ("..." with references in it, '...' as it is),
//     numbers, true and false, references, lists [a, b] and ranges [1..3],
//     parentheses, ! - * / % + - < <= > >= == != && ||
//
// #macro, #parse, #include, #define, #evaluate, #break and #stop are refused.
// As in Velocity, #name for any other name is plain text, a reference that has
// no value is written as it stands in the template ($name), and a line that
// holds only a directive leaves no empty line behind. A backslash before $ or
// # is dropped and the character written as text.

type vNode interface{}

type (
	vText string
	vRef  struct {
		src    string // the reference as written, for when it has no value
		quiet  bool   // $!name
		target *vPath
	}
	vSet struct {
		name string
		expr vExpr
	}
	vIf struct {
		conds  []vExpr   // the #if and each #elseif
		blocks [][]vNode // the block after each
		other  []vNode   // the #else block
	}
	vForeach struct {
		name string
		over vExpr
		body []vNode
	}
)

// vHeaders is the map of $headers. Its names are not told apart by case, as
// the headers of Camel (and headerValue of the simple language): the name as it
// is wins, else the smallest name that matches.
type vHeaders map[string]any

// unmap is the map in v, of any kind.
func unmap(v any) (m map[string]any, ok bool) {
	switch x := v.(type) {
	case map[string]any:
		return x, true
	case vHeaders:
		return x, true
	}
	return nil, false
}

// vGet is the member key of m, if m is a map.
func vGet(m any, key string) any {
	x, ok := unmap(m)
	if !ok {
		return nil
	}
	if v, ok := x[key]; ok {
		return v
	}
	if _, caseless := m.(vHeaders); caseless {
		best, found := "", false
		for k := range x {
			if strings.EqualFold(k, key) && (!found || k < best) {
				best, found = k, true
			}
		}
		return x[best]
	}
	return nil
}

// vPath is a reference: a variable and what follows it.
type vPath struct {
	name  string
	steps []vStep
}

type vStep struct {
	prop  string // .prop, or the method name when call is true
	call  bool
	args  []vExpr
	index vExpr // [index]
}

// A Velocity template compiled.
type velocityTemplate struct {
	nodes []vNode
}

const (
	maxVelocityOutput = 64 << 20
	maxVelocityRange  = 1 << 20
	maxVelocityDepth  = 64
)

// compileVelocity compiles a template, or says what in it is not supported.
func compileVelocity(src string) (*velocityTemplate, error) {
	p := &vParser{s: src}
	nodes, term, err := p.block(false)
	if err != nil {
		return nil, err
	}
	if term != "" {
		return nil, p.errf("#%s without #if", term)
	}
	return &velocityTemplate{nodes}, nil
}

type vParser struct {
	s     string
	i     int
	depth int

	// termLineStart tells if the #else, #elseif or #end that ended the last
	// block stood at the start of its line.
	termLineStart bool
}

func (p *vParser) errf(format string, args ...any) error {
	line := 1 + strings.Count(p.s[:min(p.i, len(p.s))], "\n")
	return fmt.Errorf("velocity: line %d: %s", line, fmt.Sprintf(format, args...))
}

func isVAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isVIdent(c byte) bool { return isVAlpha(c) || c >= '0' && c <= '9' || c == '_' || c == '-' }

// block parses until the end of the template or a directive that ends the
// block: it returns "else", "elseif" or "end", with the parser after the
// name (the condition of #elseif is left to the caller).
func (p *vParser) block(nested bool) (nodes []vNode, term string, err error) {
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			nodes = append(nodes, vText(text.String()))
			text.Reset()
		}
	}
	// atLineStart tells if only blanks come before p.i on its line, and cuts
	// them off the text: a directive alone on its line leaves nothing of it.
	atLineStart := func() bool {
		j := p.i
		for j > 0 && (p.s[j-1] == ' ' || p.s[j-1] == '\t') {
			j--
		}
		if j > 0 && p.s[j-1] != '\n' {
			return false
		}
		if cur, n := text.String(), p.i-j; n <= len(cur) {
			text.Reset()
			text.WriteString(cur[:len(cur)-n])
		}
		return true
	}
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '\\' && p.i+1 < len(p.s) && (p.s[p.i+1] == '$' || p.s[p.i+1] == '#'):
			text.WriteByte(p.s[p.i+1])
			p.i += 2

		case c == '#' && strings.HasPrefix(p.s[p.i:], "##"):
			// The comment takes its line break with it.
			atLineStart()
			if end := strings.IndexByte(p.s[p.i:], '\n'); end < 0 {
				p.i = len(p.s)
			} else {
				p.i += end + 1
			}

		case c == '#' && strings.HasPrefix(p.s[p.i:], "#*"):
			end := strings.Index(p.s[p.i+2:], "*#")
			if end < 0 {
				return nil, "", p.errf("comment #* is not closed")
			}
			lineStart := atLineStart()
			p.i += 2 + end + 2
			if lineStart {
				p.eatLine()
			}

		case c == '#' && strings.HasPrefix(p.s[p.i:], "#[["):
			end := strings.Index(p.s[p.i+3:], "]]#")
			if end < 0 {
				return nil, "", p.errf("text #[[ is not closed")
			}
			text.WriteString(p.s[p.i+3 : p.i+3+end])
			p.i += 3 + end + 3

		case c == '#':
			name, after, ok := directiveName(p.s, p.i)
			if !ok {
				text.WriteByte(c)
				p.i++
				continue
			}
			switch name {
			case "macro", "parse", "include", "define", "evaluate", "break", "stop":
				return nil, "", p.errf("#%s is not supported", name)
			case "else", "elseif", "end":
				if !nested {
					return nil, "", p.errf("#%s without #if or #foreach", name)
				}
				p.termLineStart = atLineStart()
				flush()
				p.i = after
				if name != "elseif" && p.termLineStart {
					p.eatLine()
				}
				return nodes, name, nil
			case "set", "if", "foreach":
				j := skipBlanks(p.s, after)
				if j >= len(p.s) || p.s[j] != '(' {
					text.WriteByte(c) // #if without a condition is text
					p.i++
					continue
				}
				lineStart := atLineStart()
				flush()
				p.i = j + 1
				n, err := p.directive(name, lineStart)
				if err != nil {
					return nil, "", err
				}
				nodes = append(nodes, n)
				if name == "set" && lineStart {
					p.eatLine()
				}
			default:
				text.WriteByte(c)
				p.i++
			}

		case c == '$':
			start := p.i
			if ref, ok, err := p.reference(); err != nil {
				return nil, "", err
			} else if ok {
				flush()
				nodes = append(nodes, ref)
				continue
			}
			p.i = start + 1
			text.WriteByte('$')

		default:
			text.WriteByte(c)
			p.i++
		}
	}
	flush()
	return nodes, "", nil
}

// eatLine skips the blanks and the line break after a directive that stood
// alone on its line.
func (p *vParser) eatLine() {
	j := p.i
	for j < len(p.s) && (p.s[j] == ' ' || p.s[j] == '\t') {
		j++
	}
	if j < len(p.s) && p.s[j] == '\r' {
		j++
	}
	if j == len(p.s) {
		p.i = j
	} else if p.s[j] == '\n' {
		p.i = j + 1
	}
}

func skipBlanks(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// directiveName reads #name or #{name} at s[i], and returns where it ends.
func directiveName(s string, i int) (name string, after int, ok bool) {
	j := i + 1
	braced := j < len(s) && s[j] == '{'
	if braced {
		j++
	}
	k := j
	for k < len(s) && (isVAlpha(s[k]) || k > j && s[k] >= '0' && s[k] <= '9') {
		k++
	}
	if k == j {
		return "", 0, false
	}
	name = s[j:k]
	if braced {
		if k >= len(s) || s[k] != '}' {
			return "", 0, false
		}
		k++
	} else if k < len(s) && (isVIdent(s[k])) {
		return "", 0, false // #endif is not #end
	}
	switch name {
	case "set", "if", "elseif", "else", "end", "foreach", "macro", "parse", "include", "define", "evaluate", "break", "stop":
		return name, k, true
	}
	return "", 0, false
}

// directive parses what follows the "(" of #set, #if or #foreach, with the
// blocks they hold.
func (p *vParser) directive(name string, lineStart bool) (vNode, error) {
	if p.depth++; p.depth > maxVelocityDepth {
		return nil, p.errf("directives are nested deeper than %d", maxVelocityDepth)
	}
	defer func() { p.depth-- }()
	switch name {
	case "set":
		p.skip()
		if p.i >= len(p.s) || p.s[p.i] != '$' {
			return nil, p.errf("#set wants a reference ($name = expression)")
		}
		p.i++
		start := p.i
		for p.i < len(p.s) && isVIdent(p.s[p.i]) {
			p.i++
		}
		if p.i == start || !isVAlpha(p.s[start]) {
			return nil, p.errf("#set wants a name after $")
		}
		v := p.s[start:p.i]
		p.skip()
		if p.i >= len(p.s) || p.s[p.i] != '=' || strings.HasPrefix(p.s[p.i:], "==") {
			return nil, p.errf("#set($%s wants = and an expression; setting a property is not supported", v)
		}
		p.i++
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		if err := p.closeParen(); err != nil {
			return nil, err
		}
		return vSet{v, e}, nil

	case "foreach":
		p.skip()
		if p.i >= len(p.s) || p.s[p.i] != '$' {
			return nil, p.errf("#foreach wants ($name in expression)")
		}
		p.i++
		start := p.i
		for p.i < len(p.s) && isVIdent(p.s[p.i]) {
			p.i++
		}
		v := p.s[start:p.i]
		p.skip()
		if v == "" || !strings.HasPrefix(p.s[p.i:], "in") {
			return nil, p.errf("#foreach wants ($name in expression)")
		}
		p.i += 2
		over, err := p.expr()
		if err != nil {
			return nil, err
		}
		if err := p.closeParen(); err != nil {
			return nil, err
		}
		if lineStart {
			p.eatLine()
		}
		body, term, err := p.block(true)
		if err != nil {
			return nil, err
		}
		if term != "end" {
			return nil, p.errf("#foreach is not closed by #end")
		}
		return vForeach{v, over, body}, nil

	default: // if
		n := vIf{}
		for {
			c, err := p.expr()
			if err != nil {
				return nil, err
			}
			if err := p.closeParen(); err != nil {
				return nil, err
			}
			if lineStart {
				p.eatLine()
			}
			body, term, err := p.block(true)
			if err != nil {
				return nil, err
			}
			n.conds, n.blocks = append(n.conds, c), append(n.blocks, body)
			switch term {
			case "elseif":
				j := skipBlanks(p.s, p.i)
				if j >= len(p.s) || p.s[j] != '(' {
					return nil, p.errf("#elseif wants a condition in parentheses")
				}
				p.i = j + 1
				lineStart = p.termLineStart
				continue
			case "else":
				other, term, err := p.block(true)
				if err != nil {
					return nil, err
				}
				if term != "end" {
					return nil, p.errf("#else is not closed by #end")
				}
				n.other = other
				return n, nil
			case "end":
				return n, nil
			}
			return nil, p.errf("#if is not closed by #end")
		}
	}
}

func (p *vParser) skip() { p.i = skipBlanks(p.s, p.i) }

func (p *vParser) closeParen() error {
	p.skip()
	if p.i >= len(p.s) || p.s[p.i] != ')' {
		return p.errf("%q: missing )", clip(p.s[min(p.i, len(p.s)):]))
	}
	p.i++
	return nil
}

func clip(s string) string {
	if len(s) > 20 {
		return s[:20] + "..."
	}
	return s
}

// reference parses a reference at p.i (a $); ok is false when it is plain text.
func (p *vParser) reference() (r vRef, ok bool, err error) {
	start := p.i
	i := p.i + 1
	if i < len(p.s) && p.s[i] == '!' {
		r.quiet = true
		i++
	}
	braced := i < len(p.s) && p.s[i] == '{'
	if braced {
		i++
	}
	if i >= len(p.s) || !isVAlpha(p.s[i]) {
		return r, false, nil
	}
	p.i = i
	path, err := p.path()
	if err != nil {
		return r, false, err
	}
	if braced {
		if p.i >= len(p.s) || p.s[p.i] != '}' {
			p.i = start
			return r, false, nil
		}
		p.i++
	}
	r.target, r.src = path, p.s[start:p.i]
	return r, true, nil
}

// path parses name.property[index].method(arguments) at p.i.
func (p *vParser) path() (*vPath, error) {
	start := p.i
	for p.i < len(p.s) && isVIdent(p.s[p.i]) {
		p.i++
	}
	// A trailing hyphen is not part of the name: "$a-$b" and "$total-".
	for p.i > start+1 && p.s[p.i-1] == '-' {
		p.i--
	}
	path := &vPath{name: p.s[start:p.i]}
	for p.i < len(p.s) {
		switch {
		case p.s[p.i] == '.' && p.i+1 < len(p.s) && isVAlpha(p.s[p.i+1]):
			j := p.i + 1
			for j < len(p.s) && isVIdent(p.s[j]) {
				j++
			}
			for j > p.i+2 && p.s[j-1] == '-' {
				j--
			}
			name := p.s[p.i+1 : j]
			p.i = j
			if p.i < len(p.s) && p.s[p.i] == '(' {
				p.i++
				if _, ok := velocityMethods[name]; !ok {
					return nil, p.errf("method %s() is not supported", name)
				}
				args, err := p.args()
				if err != nil {
					return nil, err
				}
				path.steps = append(path.steps, vStep{prop: name, call: true, args: args})
			} else {
				path.steps = append(path.steps, vStep{prop: name})
			}
		case p.s[p.i] == '[':
			p.i++
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			p.skip()
			if p.i >= len(p.s) || p.s[p.i] != ']' {
				return nil, p.errf("missing ] in %s", clip(p.s[start:]))
			}
			p.i++
			path.steps = append(path.steps, vStep{index: e})
		default:
			return path, nil
		}
	}
	return path, nil
}

func (p *vParser) args() ([]vExpr, error) {
	var args []vExpr
	p.skip()
	if p.i < len(p.s) && p.s[p.i] == ')' {
		p.i++
		return nil, nil
	}
	for {
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		args = append(args, e)
		p.skip()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == ')' {
			p.i++
			return args, nil
		}
		return nil, p.errf("missing ) after the arguments")
	}
}

// ---- expressions

type vExpr interface {
	eval(c *vctx) any
}

type (
	vLit   struct{ v any }
	vStr   struct{ parts []vNode } // "text with $references"
	vList  struct{ items []vExpr }
	vRange struct{ from, to vExpr }
	vUnary struct {
		op byte
		x  vExpr
	}
	vBinary struct {
		op   string
		x, y vExpr
	}
)

func (p *vParser) expr() (vExpr, error) { return p.or() }

func (p *vParser) op(ops ...string) (string, bool) {
	p.skip()
	for _, o := range ops {
		if strings.HasPrefix(p.s[p.i:], o) {
			// "<" must not take the first half of "<=", nor "=" of "==".
			rest := p.s[p.i+len(o):]
			if (o == "<" || o == ">" || o == "!") && strings.HasPrefix(rest, "=") {
				continue
			}
			p.i += len(o)
			return o, true
		}
	}
	return "", false
}

func (p *vParser) binary(next func() (vExpr, error), ops ...string) (vExpr, error) {
	x, err := next()
	if err != nil {
		return nil, err
	}
	for {
		o, ok := p.op(ops...)
		if !ok {
			return x, nil
		}
		y, err := next()
		if err != nil {
			return nil, err
		}
		x = vBinary{o, x, y}
	}
}

func (p *vParser) or() (vExpr, error)  { return p.binary(p.and, "||") }
func (p *vParser) and() (vExpr, error) { return p.binary(p.eq, "&&") }
func (p *vParser) eq() (vExpr, error)  { return p.binary(p.rel, "==", "!=") }
func (p *vParser) rel() (vExpr, error) { return p.binary(p.add, "<=", ">=", "<", ">") }
func (p *vParser) add() (vExpr, error) { return p.binary(p.mul, "+", "-") }
func (p *vParser) mul() (vExpr, error) { return p.binary(p.unary, "*", "/", "%") }

func (p *vParser) unary() (vExpr, error) {
	p.skip()
	if p.i < len(p.s) && (p.s[p.i] == '!' || p.s[p.i] == '-') && !strings.HasPrefix(p.s[p.i:], "!=") {
		op := p.s[p.i]
		if op == '-' && p.i+1 < len(p.s) && p.s[p.i+1] >= '0' && p.s[p.i+1] <= '9' {
			return p.primary() // a negative number
		}
		p.i++
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return vUnary{op, x}, nil
	}
	return p.primary()
}

func (p *vParser) primary() (vExpr, error) {
	p.skip()
	if p.i >= len(p.s) {
		return nil, p.errf("an expression is missing")
	}
	c := p.s[p.i]
	switch {
	case c == '(':
		p.i++
		e, err := p.expr()
		if err != nil {
			return nil, err
		}
		return e, p.closeParen()
	case c == '$':
		r, ok, err := p.reference()
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, p.errf("%q is not a reference", clip(p.s[p.i:]))
		}
		return vRefExpr{r}, nil
	case c == '"' || c == '\'':
		end := strings.IndexByte(p.s[p.i+1:], c)
		if end < 0 {
			return nil, p.errf("string is not closed")
		}
		raw := p.s[p.i+1 : p.i+1+end]
		p.i += end + 2
		if c == '\'' || !strings.ContainsAny(raw, "$\\") {
			return vLit{raw}, nil
		}
		sub := &vParser{s: raw, depth: p.depth}
		var parts []vNode
		var text strings.Builder
		for sub.i < len(raw) {
			if raw[sub.i] == '\\' && sub.i+1 < len(raw) && raw[sub.i+1] == '$' {
				text.WriteByte('$')
				sub.i += 2
				continue
			}
			if raw[sub.i] == '$' {
				start := sub.i
				if r, ok, err := sub.reference(); err != nil {
					return nil, err
				} else if ok {
					if text.Len() > 0 {
						parts = append(parts, vText(text.String()))
						text.Reset()
					}
					parts = append(parts, r)
					continue
				}
				sub.i = start
			}
			text.WriteByte(raw[sub.i])
			sub.i++
		}
		if text.Len() > 0 {
			parts = append(parts, vText(text.String()))
		}
		return vStr{parts}, nil
	case c == '[':
		p.i++
		p.skip()
		if p.i < len(p.s) && p.s[p.i] == ']' {
			p.i++
			return vList{}, nil
		}
		first, err := p.expr()
		if err != nil {
			return nil, err
		}
		p.skip()
		if strings.HasPrefix(p.s[p.i:], "..") {
			p.i += 2
			last, err := p.expr()
			if err != nil {
				return nil, err
			}
			p.skip()
			if p.i >= len(p.s) || p.s[p.i] != ']' {
				return nil, p.errf("missing ] after the range")
			}
			p.i++
			return vRange{first, last}, nil
		}
		items := []vExpr{first}
		for p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			e, err := p.expr()
			if err != nil {
				return nil, err
			}
			items = append(items, e)
			p.skip()
		}
		if p.i >= len(p.s) || p.s[p.i] != ']' {
			return nil, p.errf("missing ] after the list")
		}
		p.i++
		return vList{items}, nil
	case c >= '0' && c <= '9' || c == '-':
		start := p.i
		p.i++
		for p.i < len(p.s) && (p.s[p.i] >= '0' && p.s[p.i] <= '9' || p.s[p.i] == '.' && p.i+1 < len(p.s) && p.s[p.i+1] >= '0' && p.s[p.i+1] <= '9') {
			p.i++
		}
		lit := p.s[start:p.i]
		if n, err := strconv.ParseInt(lit, 10, 64); err == nil {
			return vLit{n}, nil
		}
		f, err := strconv.ParseFloat(lit, 64)
		if err != nil {
			return nil, p.errf("%q is not a number", lit)
		}
		return vLit{f}, nil
	case isVAlpha(c):
		start := p.i
		for p.i < len(p.s) && isVIdent(p.s[p.i]) {
			p.i++
		}
		switch w := p.s[start:p.i]; w {
		case "true":
			return vLit{true}, nil
		case "false":
			return vLit{false}, nil
		default:
			return nil, p.errf("%q is not an expression; a word is a reference only with a $", w)
		}
	}
	return nil, p.errf("%q is not an expression", clip(p.s[p.i:]))
}

// vRefExpr is a reference used in an expression.
type vRefExpr struct{ r vRef }

// ---- evaluation

type vctx struct {
	vars map[string]any
	out  strings.Builder
	err  error
}

func (c *vctx) write(s string) {
	if c.out.Len()+len(s) > maxVelocityOutput {
		if c.err == nil {
			c.err = fmt.Errorf("velocity: the result is more than %d MB", maxVelocityOutput>>20)
		}
		return
	}
	c.out.WriteString(s)
}

// run renders the template with the variables vars (which it changes).
func (t *velocityTemplate) run(vars map[string]any) (string, error) {
	c := &vctx{vars: vars}
	c.render(t.nodes)
	return c.out.String(), c.err
}

func (c *vctx) render(nodes []vNode) {
	for _, n := range nodes {
		if c.err != nil {
			return
		}
		switch n := n.(type) {
		case vText:
			c.write(string(n))
		case vRef:
			v, ok := n.target.value(c)
			switch {
			case ok && v != nil:
				c.write(vString(v))
			case !n.quiet:
				c.write(n.src)
			}
		case vSet:
			if v := n.expr.eval(c); v != nil {
				c.vars[n.name] = v
			}
		case vIf:
			done := false
			for i, cond := range n.conds {
				if vTruth(cond.eval(c)) {
					c.render(n.blocks[i])
					done = true
					break
				}
			}
			if !done {
				c.render(n.other)
			}
		case vForeach:
			c.foreach(n)
		}
	}
}

func (c *vctx) foreach(n vForeach) {
	var items []any
	switch x := n.over.eval(c).(type) {
	case []any:
		items = x
	case map[string]any, vHeaders:
		m, _ := unmap(x)
		for _, k := range sortedKeys(m) {
			items = append(items, m[k])
		}
	default:
		return
	}
	names := []string{n.name, "foreach", "velocityCount"}
	saved := make([]any, len(names))
	had := make([]bool, len(names))
	for i, name := range names {
		saved[i], had[i] = c.vars[name]
	}
	defer func() {
		for i, name := range names {
			if had[i] {
				c.vars[name] = saved[i]
			} else {
				delete(c.vars, name)
			}
		}
	}()
	for i, it := range items {
		c.vars[n.name] = it
		c.vars["foreach"] = map[string]any{
			"index": int64(i), "count": int64(i + 1), "hasNext": i < len(items)-1,
			"first": i == 0, "last": i == len(items)-1,
		}
		c.vars["velocityCount"] = int64(i + 1)
		c.render(n.body)
		if c.err != nil {
			return
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// value follows the path from its variable; ok is false if the variable is not set.
func (p *vPath) value(c *vctx) (any, bool) {
	v, ok := c.vars[p.name]
	if !ok {
		return nil, false
	}
	for _, s := range p.steps {
		switch {
		case s.index != nil:
			v = vIndex(v, s.index.eval(c))
		case s.call:
			args := make([]any, len(s.args))
			for i, a := range s.args {
				args[i] = a.eval(c)
			}
			v = velocityMethods[s.prop](v, args)
		default:
			v = vGet(v, s.prop)
		}
		if v == nil {
			return nil, true
		}
	}
	return v, true
}

func vIndex(v, idx any) any {
	switch x := v.(type) {
	case []any:
		if i, ok := idx.(int64); ok && i >= 0 && int(i) < len(x) {
			return x[i]
		}
	case map[string]any, vHeaders:
		return vGet(x, vString(idx))
	}
	return nil
}

func (e vRefExpr) eval(c *vctx) any {
	v, _ := e.r.target.value(c)
	return v
}

func (e vLit) eval(*vctx) any { return e.v }

func (e vStr) eval(c *vctx) any {
	var b strings.Builder
	for _, n := range e.parts {
		switch n := n.(type) {
		case vText:
			b.WriteString(string(n))
		case vRef:
			if v, ok := n.target.value(c); ok && v != nil {
				b.WriteString(vString(v))
			} else if !n.quiet {
				b.WriteString(n.src)
			}
		}
	}
	return b.String()
}

func (e vList) eval(c *vctx) any {
	items := make([]any, len(e.items))
	for i, it := range e.items {
		items[i] = it.eval(c)
	}
	return items
}

func (e vRange) eval(c *vctx) any {
	from, ok1 := e.from.eval(c).(int64)
	to, ok2 := e.to.eval(c).(int64)
	if !ok1 || !ok2 || int64(math.Abs(float64(to-from))) > maxVelocityRange {
		return nil
	}
	var items []any
	if from <= to {
		for i := from; i <= to; i++ {
			items = append(items, i)
		}
	} else {
		for i := from; i >= to; i-- {
			items = append(items, i)
		}
	}
	return items
}

func (e vUnary) eval(c *vctx) any {
	v := e.x.eval(c)
	if e.op == '!' {
		return !vTruth(v)
	}
	switch n := v.(type) {
	case int64:
		return -n
	case float64:
		return -n
	}
	return nil
}

func (e vBinary) eval(c *vctx) any {
	switch e.op {
	case "&&":
		return vTruth(e.x.eval(c)) && vTruth(e.y.eval(c))
	case "||":
		return vTruth(e.x.eval(c)) || vTruth(e.y.eval(c))
	}
	x, y := e.x.eval(c), e.y.eval(c)
	switch e.op {
	case "==":
		return vEqual(x, y)
	case "!=":
		return !vEqual(x, y)
	case "<", "<=", ">", ">=":
		d, ok := vCompare(x, y)
		if !ok {
			return false
		}
		switch e.op {
		case "<":
			return d < 0
		case "<=":
			return d <= 0
		case ">":
			return d > 0
		}
		return d >= 0
	}
	return vArith(e.op, x, y)
}

// vTruth: a value is true unless it is null or false.
func vTruth(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return v != nil
}

func vNumber(v any) (float64, bool, bool) { // value, ok, isInt
	switch n := v.(type) {
	case int64:
		return float64(n), true, true
	case float64:
		return n, true, false
	}
	return 0, false, false
}

// vEqual is Velocity's ==: numbers by value, anything else by its text, and
// null only to null.
func vEqual(x, y any) bool {
	if x == nil || y == nil {
		return x == nil && y == nil
	}
	if a, ok, _ := vNumber(x); ok {
		if b, ok, _ := vNumber(y); ok {
			return a == b
		}
	}
	return vString(x) == vString(y)
}

func vCompare(x, y any) (int, bool) {
	if a, ok, _ := vNumber(x); ok {
		if b, ok, _ := vNumber(y); ok {
			switch {
			case a < b:
				return -1, true
			case a > b:
				return 1, true
			}
			return 0, true
		}
	}
	s, ok1 := x.(string)
	t, ok2 := y.(string)
	if ok1 && ok2 {
		return strings.Compare(s, t), true
	}
	return 0, false
}

func vArith(op string, x, y any) any {
	if op == "+" {
		_, xs := x.(string)
		_, ys := y.(string)
		if xs || ys {
			if x == nil || y == nil {
				return nil
			}
			return vString(x) + vString(y)
		}
	}
	a, ok1, int1 := vNumber(x)
	b, ok2, int2 := vNumber(y)
	if !ok1 || !ok2 {
		return nil
	}
	if int1 && int2 {
		i, j := int64(a), int64(b)
		switch op {
		case "+":
			return i + j
		case "-":
			return i - j
		case "*":
			return i * j
		case "/":
			if j != 0 {
				return i / j
			}
		case "%":
			if j != 0 {
				return i % j
			}
		}
		return nil
	}
	switch op {
	case "+":
		return a + b
	case "-":
		return a - b
	case "*":
		return a * b
	case "/":
		if b != 0 {
			return a / b
		}
	case "%":
		if b != 0 {
			return math.Mod(a, b)
		}
	}
	return nil
}

// vString writes a value as Java does.
func vString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case bool:
		return strconv.FormatBool(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatFloat(x, 'f', 1, 64)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []any:
		parts := make([]string, len(x))
		for i, it := range x {
			parts[i] = vString(it)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any, vHeaders:
		m, _ := unmap(x)
		parts := make([]string, 0, len(m))
		for _, k := range sortedKeys(m) {
			parts = append(parts, k+"="+vString(m[k]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return fmt.Sprint(v)
}

// velocityMethods are the methods a template may call, on a string, a list or
// a map (a null receiver, or arguments of the wrong kind, give null).
var velocityMethods = map[string]func(recv any, args []any) any{
	"toString": func(r any, _ []any) any { return vString(r) },
	"length": func(r any, _ []any) any {
		if s, ok := r.(string); ok {
			return int64(len([]rune(s)))
		}
		return nil
	},
	"size": func(r any, _ []any) any {
		if x, ok := r.([]any); ok {
			return int64(len(x))
		}
		if m, ok := unmap(r); ok {
			return int64(len(m))
		}
		return nil
	},
	"isEmpty": func(r any, _ []any) any {
		switch x := r.(type) {
		case string:
			return x == ""
		case []any:
			return len(x) == 0
		}
		if m, ok := unmap(r); ok {
			return len(m) == 0
		}
		return nil
	},
	"trim":        vStringMethod(func(s string, _ []any) any { return strings.TrimSpace(s) }),
	"toUpperCase": vStringMethod(func(s string, _ []any) any { return strings.ToUpper(s) }),
	"toLowerCase": vStringMethod(func(s string, _ []any) any { return strings.ToLower(s) }),
	"equals": func(r any, a []any) any {
		if len(a) == 1 && r != nil {
			return vString(r) == vString(a[0]) && a[0] != nil
		}
		return nil
	},
	"equalsIgnoreCase": vStringMethod(func(s string, a []any) any {
		return len(a) == 1 && a[0] != nil && strings.EqualFold(s, vString(a[0]))
	}),
	"startsWith": vStringMethod(func(s string, a []any) any {
		return len(a) == 1 && a[0] != nil && strings.HasPrefix(s, vString(a[0]))
	}),
	"endsWith": vStringMethod(func(s string, a []any) any {
		return len(a) == 1 && a[0] != nil && strings.HasSuffix(s, vString(a[0]))
	}),
	"indexOf": vStringMethod(func(s string, a []any) any {
		if len(a) != 1 || a[0] == nil {
			return nil
		}
		return int64(strings.Index(s, vString(a[0])))
	}),
	"replace": vStringMethod(func(s string, a []any) any {
		if len(a) != 2 || a[0] == nil || a[1] == nil {
			return nil
		}
		return strings.ReplaceAll(s, vString(a[0]), vString(a[1]))
	}),
	"substring": vStringMethod(func(s string, a []any) any {
		r := []rune(s)
		from, to := int64(0), int64(len(r))
		if len(a) < 1 || len(a) > 2 {
			return nil
		}
		var ok bool
		if from, ok = a[0].(int64); !ok {
			return nil
		}
		if len(a) == 2 {
			if to, ok = a[1].(int64); !ok {
				return nil
			}
		}
		if from < 0 || to > int64(len(r)) || from > to {
			return nil
		}
		return string(r[from:to])
	}),
	"contains": func(r any, a []any) any {
		if len(a) != 1 {
			return nil
		}
		switch x := r.(type) {
		case string:
			return a[0] != nil && strings.Contains(x, vString(a[0]))
		case []any:
			for _, it := range x {
				if vEqual(it, a[0]) {
					return true
				}
			}
			return false
		}
		return nil
	},
	"get": func(r any, a []any) any {
		if len(a) != 1 {
			return nil
		}
		return vIndex(r, a[0])
	},
	"containsKey": func(r any, a []any) any {
		if m, ok := unmap(r); ok && len(a) == 1 {
			if _, has := m[vString(a[0])]; has {
				return true
			}
			if _, caseless := r.(vHeaders); caseless {
				for k := range m {
					if strings.EqualFold(k, vString(a[0])) {
						return true
					}
				}
			}
			return false
		}
		return nil
	},
	"keySet": func(r any, _ []any) any {
		if m, ok := unmap(r); ok {
			keys := make([]any, 0, len(m))
			for _, k := range sortedKeys(m) {
				keys = append(keys, k)
			}
			return keys
		}
		return nil
	},
	"values": func(r any, _ []any) any {
		if m, ok := unmap(r); ok {
			vals := make([]any, 0, len(m))
			for _, k := range sortedKeys(m) {
				vals = append(vals, m[k])
			}
			return vals
		}
		return nil
	},
}

func vStringMethod(f func(s string, args []any) any) func(any, []any) any {
	return func(r any, a []any) any {
		if s, ok := r.(string); ok {
			return f(s, a)
		}
		return nil
	}
}
