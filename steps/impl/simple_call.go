package impl

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"dif/message"
)

// unsupported is the error for a reference the engine does not know.
func unsupported(b *block, hint string) error {
	if hint != "" {
		return fmt.Errorf("unsupported simple expression %s; %s", b.src, hint)
	}
	return fmt.Errorf("unsupported simple expression %s", b.src)
}

// block compiles one ${...}.
func (c *compiler) block(b *block) (evalFn, error) {
	if fn, ok, err := c.ternary(b); ok || err != nil {
		return fn, err
	}
	return c.reference(b, b.body)
}

// reference compiles the body of a block: a function, header, body, ....
func (c *compiler) reference(b *block, ref string) (evalFn, error) {
	// A result type in front: ${int:header.n}, ${string:jq(...)}.
	for _, conv := range []struct {
		prefix string
		to     func(any) any
	}{
		{"int:", toIntValue}, {"integer:", toIntValue}, {"long:", toIntValue},
		{"boolean:", toBoolValue}, {"string:", toStringValue},
	} {
		if rest, ok := strings.CutPrefix(ref, conv.prefix); ok {
			fn, err := c.reference(b, rest)
			if err != nil {
				return nil, err
			}
			return func(e *env) (any, error) {
				v, err := fn(e)
				return conv.to(v), err
			}, nil
		}
	}

	if what, ok := strings.CutPrefix(ref, "file:"); ok {
		return c.fileRef(b, what)
	}

	// ${variable:group:<id>:MetaData.FlowVersion}: what the platform knows of the flow.
	if rest, ok := strings.CutPrefix(ref, "variable:group:"); ok {
		_, name, _ := strings.Cut(rest, ":")
		return c.metaData(b, name)
	}

	// Functions of the platform that are written with a colon.
	if fn, ok, err := simpleFunction(ref); ok || err != nil {
		if err != nil {
			return nil, err
		}
		return func(*env) (any, error) { return fn(), nil }, nil
	}

	head, rest := splitHead(ref)
	switch head {
	case "body", "in.body":
		return c.ognl(b, bodyOf, rest)
	case "bodyAs", "mandatoryBodyAs":
		return c.bodyAs(b, head, rest)
	case "header", "headers", "in.header", "in.headers":
		return c.named(b, head, rest, headerValue, func(m message.Message) any { return headersOf(m) })
	case "variable", "variables":
		return c.named(b, head, rest, variableValue, func(m message.Message) any { return variablesOf(m) })
	case "exception":
		return c.exception(b, rest)
	}

	if strings.HasPrefix(rest, "(") {
		args, after, ok := matchParen(rest)
		if !ok {
			// A quote that is never closed: Camel takes everything up to the last parenthesis.
			if i := strings.LastIndexByte(rest, ')'); i > 0 {
				args, after, ok = rest[1:i], rest[i+1:], true
			}
		}
		if !ok {
			return nil, unsupported(b, "")
		}
		if f, found := simpleFuncs[head]; found {
			fn, err := f(c, b, args)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", b.src, err)
			}
			return c.ognl(b, fn, after)
		}
	} else if f, found := simpleRefs[head]; found {
		fn, err := f(c, b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", b.src, err)
		}
		return c.ognl(b, fn, rest)
	}
	return nil, unsupported(b, "")
}

func bodyOf(e *env) (any, error) { return e.bodyValue(), nil }

// splitHead cuts the name a reference starts with from its rest: the letters,
// digits and dashes, and the dots between names such as in.header.
func splitHead(ref string) (head, rest string) {
	i := 0
	for i < len(ref) {
		c := ref[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			i++
			continue
		}
		break
	}
	head, rest = ref[:i], ref[i:]
	// in.body and in.header(s) are the same as body and header(s).
	if head == "in" && strings.HasPrefix(rest, ".") {
		h2, r2 := splitHead(rest[1:])
		if h2 == "body" || h2 == "header" || h2 == "headers" {
			return "in." + h2, r2
		}
	}
	return head, rest
}

// matchParen returns the text in the parentheses that rest starts with, and
// what follows the closing one. Quotes and blocks do not count.
func matchParen(rest string) (inside, after string, ok bool) {
	depth := 0
	var quote byte
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return rest[1:i], rest[i+1:], true
			}
		}
	}
	return "", "", false
}

// bodyAs is ${bodyAs(String)}, the only type a body can be read as; with any
// other type it compiles but fails when run, as converting the body does in Camel.
func (c *compiler) bodyAs(b *block, name, rest string) (evalFn, error) {
	args, after, ok := matchParen(rest)
	typ := strings.TrimSpace(strings.Trim(args, `'"`))
	if !ok || !isTypeName(typ) {
		return nil, unsupported(b, "")
	}
	var fn evalFn = bodyOf
	if typ != "String" && typ != "java.lang.String" {
		err := fmt.Errorf("${%s(%s)}: the body cannot be converted to %s; only String is supported", name, typ, typ)
		fn = func(*env) (any, error) { return nil, err }
	} else {
		fn = func(e *env) (any, error) {
			v := e.bodyValue()
			if v == nil {
				return nil, nil
			}
			return render(v), nil
		}
	}
	return c.ognl(b, fn, after)
}

// named compiles ${header.name} and the other ways to write it: header:name,
// header[name], headers.name; the same for variables. A method or property may
// follow the name, as in ${header.name.trim()}. A message key that holds dots,
// such as file.name, is a header name as a whole. get finds the value of a
// name, all is the value of the plural (${headers}).
func (c *compiler) named(b *block, head, rest string, get func(message.Message, string) any, all func(message.Message) any) (evalFn, error) {
	plural := strings.HasSuffix(head, "s")
	lookup := func(e *env, key evalFn) (any, error) {
		k, err := key(e)
		if err != nil {
			return nil, err
		}
		return get(e.m, render(k)), nil
	}
	if rest == "" {
		if plural {
			return func(e *env) (any, error) { return all(e.m), nil }, nil
		}
		return nil, unsupported(b, "")
	}
	switch rest[0] {
	case '.', ':', '?':
		rest = rest[1:]
		if plural && (rest == "size" || rest == "length" || rest == "size()" || rest == "length()") {
			return func(e *env) (any, error) { return int64(len(elementsOfMap(all(e.m)))), nil }, nil
		}
	case '[':
		inside, after, ok := matchBracket(rest)
		if !ok {
			return nil, unsupported(b, "")
		}
		key, err := c.keyName(b, inside)
		if err != nil {
			return nil, err
		}
		return c.ognl(b, func(e *env) (any, error) { return lookup(e, key) }, after)
	default:
		return nil, unsupported(b, "")
	}
	if rest == "" {
		return nil, unsupported(b, "")
	}

	whole, err := c.keyName(b, rest)
	if err != nil {
		return nil, err
	}
	// The name is up to the first dot or bracket that starts a method, property or
	// index, when nothing has the whole text for a name.
	name, suffix := splitHeaderName(rest)
	if suffix == "" || strings.ContainsAny(name, " \t") { // ${header.Amount * 0.9} names a header
		return func(e *env) (any, error) { return lookup(e, whole) }, nil
	}
	nameKey, err := c.keyName(b, name)
	if err != nil {
		return nil, err
	}
	steps, err := c.ognl(b, func(e *env) (any, error) { return lookup(e, nameKey) }, suffix)
	if err != nil {
		return func(e *env) (any, error) { return lookup(e, whole) }, nil
	}
	return func(e *env) (any, error) {
		if k, _ := whole(e); k != nil {
			if v := get(e.m, render(k)); v != nil {
				return v, nil
			}
		}
		return steps(e)
	}, nil
}

// elementsOfMap is the map a plural reference gives.
func elementsOfMap(v any) map[string]any {
	switch x := v.(type) {
	case jmap:
		return x
	case map[string]any:
		return x
	}
	return nil
}

// keyName compiles a header name, which may hold blocks: ${header.${header.which}}.
func (c *compiler) keyName(b *block, s string) (evalFn, error) {
	// A header name never holds a quote: ${header['x']} and a stray ' as in
	// ${header.x'} name the header x.
	s = strings.NewReplacer("'", "", `"`, "").Replace(strings.TrimSpace(s))
	return c.template(b, s)
}

// headerValue returns the header of m with the given name. Names are not told
// apart by case, as in Camel (the HTTP source writes a request header condition
// as Condition): the name as it is wins, else the smallest name that matches.
func headerValue(m message.Message, name string) any {
	if v, ok := m[name]; ok {
		return v
	}
	best, found := "", false
	for k := range m {
		if k != message.Body && !message.IsMetadata(k) && strings.EqualFold(k, name) && (!found || k < best) {
			best, found = k, true
		}
	}
	return m[best]
}

// splitHeaderName cuts "name.method()" or "name[0]" into the name and ".method()" or "[0]".
func splitHeaderName(s string) (name, suffix string) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.', '[':
			return s[:i], s[i:]
		case '?':
			if i+1 < len(s) && s[i+1] == '.' {
				return s[:i], s[i:]
			}
		}
	}
	return s, ""
}

// headersOf is the headers of a message: its keys without the body and metadata.
func headersOf(m message.Message) jmap {
	h := make(jmap, len(m))
	for k, v := range m {
		if k != message.Body && !message.IsMetadata(k) {
			h[k] = v
		}
	}
	return h
}

// matchBracket returns the text in the brackets that rest starts with, and what follows.
func matchBracket(rest string) (inside, after string, ok bool) {
	depth := 0
	var quote byte
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '[':
			depth++
		case c == ']':
			depth--
			if depth == 0 {
				return rest[1:i], rest[i+1:], true
			}
		}
	}
	return "", "", false
}

// ternary compiles ${condition ? a : b}, with the ? and the : between spaces.
func (c *compiler) ternary(b *block) (evalFn, bool, error) {
	body := b.body
	q := findOutsideQuotes(body, " ? ")
	if q < 0 {
		return nil, false, nil
	}
	colon := findOutsideQuotes(body[q+3:], " : ")
	if colon < 0 {
		return nil, true, fmt.Errorf("%s: the ? needs a : after it", b.src)
	}
	colon += q + 3
	condText := strings.TrimSpace(body[:q])
	trueText := strings.TrimSpace(body[q+3 : colon])
	falseText := strings.TrimSpace(body[colon+3:])

	cond, err := c.condition(b, wrapCondition(b, condText))
	if err != nil {
		return nil, true, err
	}
	yes, err := c.value(b, trueText)
	if err != nil {
		return nil, true, err
	}
	no, err := c.value(b, falseText)
	if err != nil {
		return nil, true, err
	}
	return func(e *env) (any, error) {
		ok, err := cond(e)
		if err != nil {
			return nil, err
		}
		if ok {
			return yes(e)
		}
		return no(e)
	}, true, nil
}

// wrapCondition writes the left side of a condition that names a function as a
// block: header.count > 10 is ${header.count} > 10. The block is added to the
// kids of b.
func wrapCondition(b *block, cond string) string {
	if strings.ContainsRune(cond, phOpen) {
		return cond
	}
	_, i := findOperator(cond)
	if i <= 0 {
		return cond
	}
	left := strings.TrimSpace(cond[:i])
	if left == "" || left[0] == '\'' || left[0] == '"' || !isText(literalValue(left)) {
		return cond
	}
	b.kids = append(b.kids, &block{src: "${" + left + "}", body: left})
	return placeholder(len(b.kids)-1) + " " + cond[i:]
}

// value compiles the value of one side of a ternary: a quoted text, null, a
// number, another ternary, or a function written without ${}.
func (c *compiler) value(b *block, s string) (evalFn, error) {
	if s == "null" || s == "${null}" {
		return func(*env) (any, error) { return nil, nil }, nil
	}
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return c.template(b, s[1:len(s)-1])
	}
	if strings.ContainsRune(s, phOpen) {
		return c.template(b, s)
	}
	if lit := literalValue(s); !isText(lit) {
		return func(*env) (any, error) { return lit, nil }, nil
	}
	inner := &block{src: b.src, body: s, kids: b.kids}
	return c.block(inner)
}

// findOutsideQuotes returns the index of the first sep in s that is not in quotes, or -1.
func findOutsideQuotes(s, sep string) int {
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case strings.HasPrefix(s[i:], sep):
			return i
		}
	}
	return -1
}

// toIntValue converts to the type ${int:...} and ${long:...} give: a whole number, or nothing.
func toIntValue(v any) any {
	if n, ok := asInt(v); ok {
		return n
	}
	return nil
}

func toBoolValue(v any) any {
	if b, ok := asBool(v); ok {
		return b
	}
	return nil
}

func toStringValue(v any) any {
	if v == nil {
		return nil
	}
	return render(v)
}

// runeLen is the length of s in characters, as Java counts the length of a string.
func runeLen(s string) int { return utf8.RuneCountInString(s) }
