package impl

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"dif/message"
)

// This file compiles the simple language of Camel (camel-core-languages, the
// package org.apache.camel.language.simple) into closures. A template is
// literal text with ${...} blocks in it; a block is a function or reference
// and may hold other blocks, as in ${uppercase('Hello ${body}')}.

// env is what a compiled template reads when it runs.
type env struct {
	m       message.Message
	body    any  // the body a chain (~>) hands to the next function
	hasBody bool // body replaces the message's body
}

// bodyValue is the body: that of a chain, else the message's.
func (e *env) bodyValue() any {
	if e.hasBody {
		return e.body
	}
	return e.m[message.Body]
}

// evalFn computes a value (see simple_values.go).
type evalFn func(e *env) (any, error)

// block is one ${...} in a template.
type block struct {
	src  string   // as written, for messages
	body string   // the text in it; every block in it is replaced by a placeholder
	kids []*block // the blocks in the body, by placeholder number
}

// A placeholder stands for a block inside the body of another one.
const (
	phOpen  = '\x00'
	phClose = '\x01'
)

func placeholder(i int) string { return string(phOpen) + strconv.Itoa(i) + string(phClose) }

var errUnclosed = errors.New("unclosed ${ in expression")

// blockStart returns the length of the text that opens a block at s[i:], 0 if none.
func blockStart(s string, i int) int {
	switch {
	case strings.HasPrefix(s[i:], "${"):
		return 2
	case strings.HasPrefix(s[i:], "$simple{"):
		return 8
	}
	return 0
}

// scanBlock returns the index just after the } that closes the block at s[i:].
// A \} does not close it, and ${ inside it opens another block.
func scanBlock(s string, i int) (int, error) {
	depth := 0
	for j := i; j < len(s); {
		if n := blockStart(s, j); n > 0 {
			depth++
			j += n
			continue
		}
		switch s[j] {
		case '\\':
			if j+1 < len(s) && s[j+1] == '}' {
				j += 2
				continue
			}
		case '}':
			depth--
			j++
			if depth == 0 {
				return j, nil
			}
			continue
		}
		j++
	}
	return 0, errUnclosed
}

// newBlock reads the block src, which starts with ${ and ends with }.
func newBlock(src string) (*block, error) {
	inner := src[blockStart(src, 0) : len(src)-1]
	blk := &block{src: src}
	var b strings.Builder
	for i := 0; i < len(inner); {
		if blockStart(inner, i) > 0 {
			end, err := scanBlock(inner, i)
			if err != nil {
				return nil, err
			}
			kid, err := newBlock(inner[i:end])
			if err != nil {
				return nil, err
			}
			blk.kids = append(blk.kids, kid)
			b.WriteString(placeholder(len(blk.kids) - 1))
			i = end
			continue
		}
		if c := inner[i]; c == '\\' && i+1 < len(inner) {
			// \n, \t, \r and \} are the characters themselves, as in Camel.
			if r, ok := map[byte]string{'n': "\n", 't': "\t", 'r': "\r", '}': "}"}[inner[i+1]]; ok {
				b.WriteString(r)
				i += 2
				continue
			}
		}
		b.WriteByte(inner[i])
		i++
	}
	blk.body = b.String()
	return blk, nil
}

// item is a piece of a template: literal text, or a block.
type item struct {
	text string
	blk  *block
}

// parseTemplate splits s into literal text and blocks.
func parseTemplate(s string) ([]item, error) {
	if strings.ContainsAny(s, string([]rune{phOpen, phClose})) {
		return nil, fmt.Errorf("expression holds a control character")
	}
	var items []item
	start := 0
	for i := 0; i < len(s); {
		if blockStart(s, i) == 0 {
			i++
			continue
		}
		end, err := scanBlock(s, i)
		if err != nil {
			return nil, err
		}
		blk, err := newBlock(s[i:end])
		if err != nil {
			return nil, err
		}
		if i > start {
			items = append(items, item{text: s[start:i]})
		}
		items = append(items, item{blk: blk})
		i, start = end, end
	}
	if start < len(s) {
		items = append(items, item{text: s[start:]})
	}
	return items, nil
}

// argItems splits s, a piece of a block's body, into literal text and the
// blocks of kids its placeholders stand for.
func argItems(s string, kids []*block) []item {
	var items []item
	for len(s) > 0 {
		i := strings.IndexByte(s, phOpen)
		if i < 0 {
			return append(items, item{text: s})
		}
		j := strings.IndexByte(s[i:], phClose)
		n, err := strconv.Atoi(s[i+1 : i+j])
		if j < 0 || err != nil || n >= len(kids) {
			return append(items, item{text: s}) // cannot happen: only parseBlock makes placeholders
		}
		if i > 0 {
			items = append(items, item{text: s[:i]})
		}
		items = append(items, item{blk: kids[n]})
		s = s[i+j+1:]
	}
	return items
}

// compiler compiles the templates of one expression.
type compiler struct {
	flow *flowProperties // what the flow knows about itself; nil when compiled without a flow
}

// expression is a compiled template.
type expression struct {
	fn     evalFn
	text   string // the template, when it is plain text
	isText bool
}

// compileExpression compiles expr in language "constant" (literal text) or
// "simple", the language of Camel (see simple_funcs.go for the functions).
func compileExpression(language, expr string) (expression, error) {
	return compileExpressionIn(nil, language, expr)
}

// compileExpressionIn compiles the expression of a flow, which may refer to the
// properties of the flow. It is trimmed first, as Camel's DSL does by default:
// its leading and trailing spaces and line breaks are no part of it.
func compileExpressionIn(flow *flowProperties, language, expr string) (expression, error) {
	expr = strings.TrimSpace(expr)
	if language == "constant" {
		return expression{text: expr, isText: true}, nil
	}
	return (&compiler{flow: flow}).compile(expr)
}

// compileTemplate compiles text that is to be used as it is, such as the body of
// a message: a simple template, not trimmed.
func compileTemplate(text string) (expression, error) {
	return (&compiler{}).compile(text)
}

func (c *compiler) compile(expr string) (expression, error) {
	if strings.HasPrefix(expr, initStart) {
		return c.compileInit(expr)
	}
	items, err := parseTemplate(expr)
	if err != nil {
		return expression{}, err
	}
	if len(items) == 0 {
		return expression{isText: true}, nil
	}
	if len(items) == 1 && items[0].blk == nil {
		return expression{text: items[0].text, isText: true}, nil
	}
	fn, err := c.items(items)
	return expression{fn: fn}, err
}

// eval returns the text of the expression for m. A missing key gives "".
func (x expression) eval(m message.Message) (string, error) {
	if x.isText {
		return x.text, nil
	}
	v, err := x.fn(&env{m: m})
	return render(v), err
}

// value returns the value of the expression for m.
func (x expression) value(m message.Message) (any, error) {
	if x.isText {
		return x.text, nil
	}
	return x.fn(&env{m: m})
}

// run evaluates the expression in e.
func (x expression) run(e *env) (any, error) {
	if x.isText {
		return x.text, nil
	}
	return x.fn(e)
}

// template compiles a piece of a block's body as a template of its own.
func (c *compiler) template(b *block, s string) (evalFn, error) {
	if !strings.ContainsRune(s, phOpen) && !strings.Contains(s, " ?: ") && !strings.Contains(s, " ~> ") && !strings.Contains(s, " ?~> ") {
		return func(*env) (any, error) { return s, nil }, nil
	}
	return c.items(argItems(s, b.kids))
}

// A token of a template once its operators are found.
type token struct {
	val evalFn // a block, or literal text
	op  string // ?:, ~> or ?~> between two values
}

var templateOperators = []string{" ?~> ", " ?: ", " ~> "}

// items compiles a template: the blocks, the operators ?: ~> ?~> ++ -- between
// them, and the text.
func (c *compiler) items(its []item) (evalFn, error) {
	var toks []token
	for i, it := range its {
		if it.blk != nil {
			fn, err := c.block(it.blk)
			if err != nil {
				return nil, err
			}
			toks = append(toks, token{val: fn})
			continue
		}
		text := it.text
		// ++ and -- follow a block directly and end the word.
		if i > 0 && its[i-1].blk != nil && len(toks) > 0 && toks[len(toks)-1].op == "" && (strings.HasPrefix(text, "++") || strings.HasPrefix(text, "--")) &&
			(len(text) == 2 || text[2] == ' ') {
			prev := toks[len(toks)-1].val
			toks[len(toks)-1].val = step(prev, text[:2] == "++")
			text = text[2:]
		}
		for text != "" {
			at, op := -1, ""
			for _, o := range templateOperators {
				if j := strings.Index(text, o); j >= 0 && (at < 0 || j < at) {
					at, op = j, o
				}
			}
			if at < 0 {
				toks = append(toks, literal(text))
				break
			}
			if at > 0 {
				toks = append(toks, literal(text[:at]))
			}
			toks = append(toks, token{op: strings.TrimSpace(op)})
			text = text[at+len(op):]
			if text != "" && toks[len(toks)-1].op != "" {
				// The right side of an operator is one word or one quoted text.
				word, rest := operand(text)
				toks = append(toks, token{val: wordValue(word)})
				text = rest
			}
		}
	}

	// Apply the operators, from left to right.
	var parts []evalFn
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.op == "" {
			parts = append(parts, t.val)
			continue
		}
		if len(parts) == 0 || i+1 >= len(toks) || toks[i+1].op != "" {
			return nil, fmt.Errorf("operator %s needs a value on both sides", t.op)
		}
		left, right := parts[len(parts)-1], toks[i+1].val
		i++
		switch t.op {
		case "?:":
			parts[len(parts)-1] = elvis(left, right)
		default:
			// A chain goes on with the next ~> and ?~> directly after.
			rights := []evalFn{right}
			for i+2 < len(toks) && (toks[i+1].op == "~>" || toks[i+1].op == "?~>") && toks[i+2].op == "" && toks[i+1].op == t.op {
				rights = append(rights, toks[i+2].val)
				i += 2
			}
			parts[len(parts)-1] = chain(left, rights, t.op == "?~>")
		}
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return func(e *env) (any, error) {
		var b strings.Builder
		for _, p := range parts {
			v, err := p(e)
			if err != nil {
				return nil, err
			}
			b.WriteString(render(v))
		}
		return b.String(), nil
	}, nil
}

func literal(text string) token {
	return token{val: func(*env) (any, error) { return text, nil }}
}

// operand cuts the first word or quoted text off s, the right side of an operator.
func operand(s string) (word, rest string) {
	if s[0] == '\'' || s[0] == '"' {
		if j := strings.IndexByte(s[1:], s[0]); j >= 0 {
			return s[:j+2], s[j+2:]
		}
	}
	if j := strings.IndexByte(s, ' '); j >= 0 {
		return s[:j], s[j:]
	}
	return s, ""
}

// wordValue is the value of a literal on the right side of an operator: quotes
// are taken off, null, true, false and numbers are what they say.
func wordValue(w string) evalFn {
	v := literalValue(w)
	return func(*env) (any, error) { return v, nil }
}

// literalValue reads a literal of an expression.
func literalValue(w string) any {
	if n := len(w); n >= 2 && (w[0] == '\'' || w[0] == '"') && w[n-1] == w[0] {
		return w[1 : n-1]
	}
	switch w {
	case "null":
		return nil
	case "true":
		return true
	case "false":
		return false
	}
	if n, isInt, ok := parseNumber(w); ok {
		return number(n, isInt)
	}
	return w
}

// elvis is left, unless that is nothing, false, empty or 0: then it is right.
func elvis(left, right evalFn) evalFn {
	return func(e *env) (any, error) {
		v, err := left(e)
		if err != nil {
			return nil, err
		}
		if empty(v) || v == false {
			return right(e)
		}
		if n, _, ok := asNumber(v); ok && n == 0 && !isText(v) {
			return right(e)
		}
		return v, nil
	}
}

func isText(v any) bool { _, ok := v.(string); return ok }

// empty reports whether v is nothing, an empty text or an empty collection.
func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []byte:
		return len(x) == 0
	case jlist:
		return len(x) == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	case jmap:
		return len(x) == 0
	}
	return false
}

// chain evaluates left, makes that the body for the right sides, one after the
// other, and is the value of the last. With nullSafe it stops at a nil.
func chain(left evalFn, rights []evalFn, nullSafe bool) evalFn {
	return func(e *env) (any, error) {
		v, err := left(e)
		if err != nil {
			return nil, err
		}
		for _, r := range rights {
			if v == nil && nullSafe {
				return nil, nil
			}
			next := &env{m: e.m, body: v, hasBody: true}
			if v, err = r(next); err != nil {
				return nil, err
			}
		}
		return v, nil
	}
}

// step adds or subtracts one from a number and keeps its kind: ${header.n}++.
func step(f evalFn, up bool) evalFn {
	return func(e *env) (any, error) {
		v, err := f(e)
		if err != nil {
			return nil, err
		}
		n, isInt, ok := asNumber(v)
		if !ok {
			return nil, fmt.Errorf("cannot use %q as a number", render(v))
		}
		if up {
			n++
		} else {
			n--
		}
		if _, wasText := v.(string); wasText {
			return render(number(n, isInt)), nil
		}
		return number(n, isInt), nil
	}
}
