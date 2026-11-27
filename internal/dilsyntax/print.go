package dilsyntax

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Print returns DIL JSON as DIL text in its canonical form. It writes what
// DIF reads: imports, every dil.core declaration with all its fields, and
// the integrations with their flows. A flow must be a tree of steps, as DIF
// builds it: Print fails on a flow it cannot write as one.
//
// Lowering the printed text gives a document that DIF reads the same as data,
// with generated link ids and every list written as an array.
func Print(data []byte) ([]byte, error) {
	v, err := decodeJSON(data)
	if err != nil {
		return nil, err
	}
	root, _ := v.(object)
	dil := root.obj("dil")
	if dil == nil {
		return nil, errors.New("document requires a dil object")
	}
	p := &printer{}
	imports := dil.obj("imports")
	importList, _ := imports.get("import")
	for _, x := range many(importList) {
		imp, _ := x.(object)
		path, ok1 := imp.str("path")
		ns, ok2 := imp.str("namespace")
		if !ok1 || !ok2 {
			return nil, errors.New("an import needs a path and a namespace")
		}
		p.line(0, "import "+quote(path)+" as "+name(ns))
	}
	for _, m := range dil.obj("core") {
		kind := ""
		for k, plural := range declarationKinds {
			if plural == m.key {
				kind = k
			}
		}
		if kind == "" {
			continue // DIF reads no other collection
		}
		c, _ := m.val.(object)
		list, _ := c.get(kind)
		for i, x := range many(list) {
			d, ok := x.(object)
			if !ok {
				return nil, fmt.Errorf("core %s %d is not an object", kind, i+1)
			}
			p.blank()
			p.declaration(kind, d)
		}
	}
	integrations, _ := dil.obj("integrations").get("integration")
	implicit := false // printed the flows of an integration without fields outside one
	for i, x := range many(integrations) {
		in, ok := x.(object)
		if !ok {
			return nil, fmt.Errorf("integration %d is not an object", i+1)
		}
		flows, _ := in.obj("flows").get("flow")
		attrs := slices.DeleteFunc(slices.Clone(in), func(m member) bool { return m.key == "flows" })
		if len(attrs) == 0 && !implicit && len(many(flows)) > 0 {
			implicit = true
			for _, f := range many(flows) {
				p.blank()
				if err := p.flow(0, f); err != nil {
					return nil, err
				}
			}
			continue
		}
		p.blank()
		head := "integration"
		if id, ok := in.str("id"); ok {
			head += " " + name(id)
			attrs = slices.DeleteFunc(attrs, func(m member) bool { return m.key == "id" })
		}
		if len(attrs) > 0 {
			head += p.params(attrs, 0)
		}
		if len(many(flows)) == 0 {
			p.line(0, head+" {}")
			continue
		}
		p.line(0, head+" {")
		for j, f := range many(flows) {
			if j > 0 {
				p.blank()
			}
			if err := p.flow(2, f); err != nil {
				return nil, err
			}
		}
		p.line(0, "}")
	}
	return []byte(p.b.String()), nil
}

type printer struct {
	b strings.Builder
}

func (p *printer) line(indent int, s string) {
	p.b.WriteString(strings.Repeat(" ", indent))
	p.b.WriteString(s)
	p.b.WriteByte('\n')
}

// blank separates top-level items.
func (p *printer) blank() {
	if p.b.Len() > 0 && !strings.HasSuffix(p.b.String(), "\n\n") {
		p.b.WriteByte('\n')
	}
}

func (p *printer) declaration(kind string, d object) {
	key := "name"
	if kind == "connection" {
		key = "id"
	}
	head := kind
	fields := d
	if n, ok := d.str(key); ok {
		head += " " + name(n)
		fields = slices.DeleteFunc(slices.Clone(d), func(m member) bool { return m.key == key })
	}
	var headers []string
	if kind == "message" {
		if hs, ok := headerLines(d); ok {
			headers = hs
			fields = slices.DeleteFunc(fields, func(m member) bool { return m.key == "headers" })
		}
	}
	if len(fields) == 0 && len(headers) == 0 {
		p.line(0, head+" {}")
		return
	}
	p.line(0, head+" {")
	for _, m := range fields {
		p.line(2, fieldName(m.key)+": "+value(m.val, 2))
	}
	for _, h := range headers {
		p.line(2, h)
	}
	p.line(0, "}")
}

// headerLines returns the headers of a message as header lines, if they can
// be written so: each header a name, a value and a language that are text.
// Header lines keep what DIF reads of a header, nothing else.
func headerLines(d object) ([]string, bool) {
	v, ok := d.get("headers")
	if !ok {
		return nil, true
	}
	hs, _ := v.(object)
	if hs == nil {
		return nil, false
	}
	list, _ := hs.get("header")
	var lines []string
	for _, x := range many(list) {
		h, _ := x.(object)
		if h == nil {
			return nil, false
		}
		text := map[string]string{}
		for _, k := range []string{"name", "value", "language"} {
			v, present := h.get(k)
			s, isText := v.(string)
			if present && !isText {
				return nil, false
			}
			text[k] = s
		}
		line := "header " + name(text["name"]) + ": "
		if lang := text["language"]; lang != "" {
			if !isBare(lang) {
				return nil, false
			}
			line += lang + " "
		}
		lines = append(lines, line+quote(text["value"]))
	}
	return lines, true
}

// flow prints a flow. Its parameters are its name and options; DIF reads no
// other field of a flow.
func (p *printer) flow(indent int, x any) error {
	f, ok := x.(object)
	if !ok {
		return errors.New("a flow is not an object")
	}
	head := "flow"
	var params object
	id, hasID := f.str("id")
	if hasID {
		head += " " + name(id)
	} else if v, ok := f.get("id"); ok {
		params = append(params, member{key: "id", val: v})
	}
	if v, ok := f.get("name"); ok {
		params = append(params, member{key: "name", val: v})
	}
	if v, ok := f.get("options"); ok && v != nil {
		o, ok := v.(object)
		if !ok {
			return fmt.Errorf("flow %q: options is not an object", id)
		}
		for _, m := range o {
			if m.key == "id" || m.key == "name" {
				continue // DIF does not read these options; the text uses the names for the flow's own fields
			}
			params = append(params, m)
		}
	}
	if len(params) > 0 {
		head += p.params(params, indent)
	}
	g, err := newGraph(f)
	if err != nil {
		return fmt.Errorf("flow %q: %w", id, err)
	}
	if g.source == nil && len(g.handlers) == 0 {
		p.line(indent, head+" {}")
		return nil
	}
	p.line(indent, head+" {")
	if g.source != nil {
		if err := p.chain(g, g.source, indent+2, true, false); err != nil {
			return fmt.Errorf("flow %q: %w", id, err)
		}
	}
	for _, h := range g.handlers {
		if g.source != nil || h != g.handlers[0] {
			p.blank()
		}
		line := "on error"
		if h.options != nil {
			line += p.params(h.options, indent+2)
		}
		line += " as " + name(h.id)
		if len(h.outs) == 0 {
			p.line(indent+2, line)
			continue
		}
		p.line(indent+2, line+" {")
		if err := p.chain(g, h.outs[0].target, indent+4, false, false); err != nil {
			return fmt.Errorf("flow %q: %w", id, err)
		}
		p.line(indent+2, "}")
	}
	if len(g.seen) != len(g.steps) {
		for _, s := range g.steps {
			if !g.seen[s] {
				return fmt.Errorf("flow %q: step %q is not reachable from the source or an error step", id, s.id)
			}
		}
	}
	p.line(indent, "}")
	return nil
}

// chain prints the steps from s on. The first step of a branch is written
// after '->' as the others are; main is true for the chain from the source.
func (p *printer) chain(g *graph, s *gstep, indent int, main, branch bool) error {
	for i := 0; ; i++ {
		if g.seen[s] {
			return fmt.Errorf("step %q is reached twice: the flow is not a tree", s.id)
		}
		g.seen[s] = true
		block := s.typ == "router" || len(s.outs) > 1 || len(s.outs) == 1 && s.outs[0].hasCondition()
		head, at := "", indent
		if branch || i > 0 {
			head = "-> "
			if !branch {
				at = indent + 2
			}
		}
		if inferType(main && i == 0, block, !block && len(s.outs) == 0) != s.typ {
			head += s.typ + " "
		}
		head += stepName(s.uri)
		if s.options != nil {
			head += p.params(s.options, at)
		}
		head += " as " + name(s.id)
		if !block {
			p.line(at, head)
			if len(s.outs) == 0 {
				return nil
			}
			s = s.outs[0].target
			continue
		}
		p.line(at, head+" {")
		for _, l := range s.outs {
			rule := "_"
			if l.rule != "" {
				rule = name(l.rule)
				if l.rule == "_" || l.rule == "as" {
					rule = quote(l.rule)
				}
			}
			if l.language != "" {
				if !isBare(l.language) {
					return fmt.Errorf("step %q: the language of a route must be a word", s.id)
				}
				rule += " " + l.language
			}
			if l.expression != "" || l.language != "" {
				rule += " " + quote(l.expression)
			}
			p.line(at+2, rule)
			if err := p.chain(g, l.target, at+4, false, true); err != nil {
				return err
			}
		}
		p.line(at, "}")
		return nil
	}
}

// params prints a parameter list: on one line when it is short, otherwise one
// parameter a line.
func (p *printer) params(o object, indent int) string {
	parts := make([]string, len(o))
	short, size := true, 0
	for i, m := range o {
		s, ok := inline(m.val)
		short = short && ok
		parts[i] = fieldName(m.key) + ": " + s
		size += len(parts[i]) + 2
	}
	if short && size <= 80 {
		return "(" + strings.Join(parts, ", ") + ")"
	}
	var b strings.Builder
	b.WriteString("(\n")
	for _, m := range o {
		b.WriteString(strings.Repeat(" ", indent+2) + fieldName(m.key) + ": " + value(m.val, indent+2) + "\n")
	}
	b.WriteString(strings.Repeat(" ", indent) + ")")
	return b.String()
}

// value prints a value: on one line when it is short, otherwise over lines,
// one element a line.
func value(v any, indent int) string {
	if s, ok := inline(v); ok && len(s)+indent <= 100 {
		return s
	}
	var b strings.Builder
	switch x := v.(type) {
	case []any:
		b.WriteString("[\n")
		for _, e := range x {
			b.WriteString(strings.Repeat(" ", indent+2) + value(e, indent+2) + "\n")
		}
		b.WriteString(strings.Repeat(" ", indent) + "]")
	case object:
		b.WriteString("{\n")
		for _, m := range x {
			b.WriteString(strings.Repeat(" ", indent+2) + fieldName(m.key) + ": " + value(m.val, indent+2) + "\n")
		}
		b.WriteString(strings.Repeat(" ", indent) + "}")
	case string:
		return quote(x)
	}
	return b.String()
}

// inline prints a value on one line; false if it holds text of several lines.
func inline(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "null", true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	case json.Number:
		return string(x), true
	case string:
		q := quote(x)
		return q, !strings.HasPrefix(q, "`")
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			s, ok := inline(e)
			if !ok {
				return "", false
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	case object:
		parts := make([]string, len(x))
		for i, m := range x {
			s, ok := inline(m.val)
			if !ok {
				return "", false
			}
			parts[i] = fieldName(m.key) + ": " + s
		}
		return "{" + strings.Join(parts, ", ") + "}", true
	}
	return "", false
}

// isBare reports whether s can be written as a bare word.
func isBare(s string) bool {
	if s == "" || strings.HasPrefix(s, ":") || strings.HasSuffix(s, ":") {
		return false
	}
	for _, r := range s {
		if !isWordRune(r) {
			return false
		}
	}
	return true
}

// name prints a name: bare if it can be, otherwise quoted.
func name(s string) string {
	if isBare(s) {
		return s
	}
	return quote(s)
}

// fieldName prints the name of a field or an option.
func fieldName(s string) string { return name(s) }

// stepName prints the URI of a step. The words that start a step, an error
// handler or an id are quoted, so the URI is not taken for them.
func stepName(s string) string {
	if slices.Contains(stepKinds, s) || s == "as" || s == "on" || s == "_" {
		return quote(s)
	}
	return name(s)
}

// quote prints text as a string: raw in backquotes when it has several lines
// (and no backquote or other control characters), in single quotes when it
// has double quotes but no single quotes, otherwise in double quotes.
func quote(s string) string {
	if strings.Contains(s, "\n") && !strings.Contains(s, "`") && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return r < ' ' && r != '\n' && r != '\t' }) {
		return "`" + s + "`"
	}
	q := byte('"')
	if strings.Contains(s, `"`) && !strings.Contains(s, "'") {
		q = '\''
	}
	b := []byte{q}
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == q || c == '\\':
				b = append(b, '\\', c)
			case c == '\n':
				b = append(b, `\n`...)
			case c == '\r':
				b = append(b, `\r`...)
			case c == '\t':
				b = append(b, `\t`...)
			case c < ' ' || c == 0x7f:
				b = append(b, `\u00`...)
				b = append(b, "0123456789abcdef"[c>>4], "0123456789abcdef"[c&15])
			default:
				b = append(b, c)
			}
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			b = append(b, `�`...)
		} else {
			b = append(b, s[i:i+n]...)
		}
		i += n
	}
	return string(append(b, q))
}

// graph is the steps of a flow, connected by their links.
type graph struct {
	steps    []*gstep
	source   *gstep
	handlers []*gstep
	seen     map[*gstep]bool
}

type gstep struct {
	id, typ, uri string
	options      object // nil: none
	outs         []*glink
	hasIn        bool
}

type glink struct {
	rule, language, expression string
	target                     *gstep
}

func (l *glink) hasCondition() bool { return l.rule != "" || l.language != "" || l.expression != "" }

func newGraph(f object) (*graph, error) {
	g := &graph{seen: map[*gstep]bool{}}
	list, _ := f.obj("steps").get("step")
	byIn := map[string]*gstep{}
	type pending struct {
		from *gstep
		id   string
		link *glink
	}
	var outs []pending
	for i, x := range many(list) {
		o, ok := x.(object)
		if !ok {
			return nil, fmt.Errorf("step %d is not an object", i+1)
		}
		s := &gstep{}
		for _, field := range []struct {
			key string
			dst *string
		}{{"id", &s.id}, {"type", &s.typ}, {"uri", &s.uri}} {
			v, present := o.get(field.key)
			text, ok := v.(string)
			if present && v != nil && !ok {
				return nil, fmt.Errorf("step %d: %s is not text", i+1, field.key)
			}
			*field.dst = text
		}
		if v, ok := o.get("options"); ok && v != nil {
			if s.options, ok = v.(object); !ok {
				return nil, fmt.Errorf("step %q: options is not an object", s.id)
			}
			if len(s.options) == 0 {
				s.options = nil
			}
		}
		links, _ := o.obj("links").get("link")
		for _, y := range many(links) {
			l, _ := y.(object)
			id, _ := l.str("id")
			bound, _ := l.str("bound")
			switch bound {
			case "in":
				if s.hasIn {
					return nil, fmt.Errorf("step %q has more than one inbound link", s.id)
				}
				if byIn[id] != nil {
					return nil, fmt.Errorf("link %q goes into more than one step", id)
				}
				s.hasIn = true
				byIn[id] = s
			case "out":
				gl := &glink{}
				gl.rule, _ = l.str("rule")
				gl.language, _ = l.str("language")
				gl.expression, _ = l.str("expression")
				s.outs = append(s.outs, gl)
				outs = append(outs, pending{s, id, gl})
			default:
				return nil, fmt.Errorf("step %q: a link's bound is not in or out", s.id)
			}
		}
		switch s.typ {
		case "error":
			if s.uri != "failedexchange" {
				return nil, fmt.Errorf("error step %q must use failedexchange", s.id)
			}
			if s.hasIn || len(s.outs) > 1 {
				return nil, fmt.Errorf("error step %q needs 0 inbound links and at most 1 outbound link", s.id)
			}
			g.handlers = append(g.handlers, s)
		case "source":
			if g.source != nil {
				return nil, fmt.Errorf("step %q: flow has more than one source", s.id)
			}
			if s.hasIn {
				return nil, fmt.Errorf("source step %q has an inbound link", s.id)
			}
			g.source = s
			g.steps = append(g.steps, s)
		case "action", "router", "sink":
			g.steps = append(g.steps, s)
		default:
			return nil, fmt.Errorf("step %q has an unknown type", s.id)
		}
	}
	for _, o := range outs {
		o.link.target = byIn[o.id]
		if o.link.target == nil {
			return nil, fmt.Errorf("step %q: outbound link %q has no target", o.from.id, o.id)
		}
	}
	if g.source == nil && len(g.steps) > 0 {
		return nil, errors.New("flow has no source step")
	}
	return g, nil
}
