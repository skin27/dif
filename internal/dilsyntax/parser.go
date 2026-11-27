package dilsyntax

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
)

// File is a parsed DIL text: its imports, declarations, integrations and the
// flows written outside an integration, in the order they are written.
type File struct {
	Items []Item // *Import, *Declaration, *Integration or *Flow
}

// Item is a top-level element of a file.
type Item interface{ position() Pos }

// Import is: import "path" as namespace
type Import struct {
	Pos       Pos
	Path      string
	Namespace string
}

// Declaration is a dil.core declaration: kind [name] { field: value ... }.
type Declaration struct {
	Pos     Pos
	Kind    string // function, processor, interceptor, format, map, service, message, resource, connection, store or test
	Name    string // the name; for a connection, its id
	HasName bool
	Fields  object
	Headers []Header // a message's header lines
}

// Header is a message header line: header name: [language] "value".
type Header struct {
	Pos      Pos
	Name     string
	Language string
	Value    string
}

// Integration is: integration [id] [(field: value ...)] { flow ... }.
type Integration struct {
	Pos   Pos
	ID    string
	HasID bool
	Attrs object
	Flows []*Flow
}

// Flow is: flow [id] [(name: ..., option: ...)] { chain  on error ... }.
type Flow struct {
	Pos      Pos
	ID       string
	HasID    bool
	Params   object // name, and the flow's options
	Chain    []*Step
	Handlers []*Handler
}

// Step is: [kind] uri [(option: value ...)] [as id] [{ branch ... }]. Only the
// last step of a chain has a block.
type Step struct {
	Pos        Pos
	Kind       string // source, action, router or sink; empty when inferred
	URI        string
	Options    object
	HasOptions bool
	ID         string
	HasID      bool // false: the id is generated
	Block      bool
	Branches   []*Branch
}

// Branch is a route of a router: rule [language] ["expression"] -> chain,
// where the rule _ stands for none.
type Branch struct {
	Pos        Pos
	Rule       string
	Language   string
	Expression string
	Chain      []*Step
}

// Handler is an error handler: on error [(option: value ...)] [as id] [{ chain }].
type Handler struct {
	Pos        Pos
	ID         string
	HasID      bool // false: the id is generated
	Options    object
	HasOptions bool
	Route      []*Step
}

func (x *Import) position() Pos      { return x.Pos }
func (x *Declaration) position() Pos { return x.Pos }
func (x *Integration) position() Pos { return x.Pos }
func (x *Flow) position() Pos        { return x.Pos }

// Error is an error in DIL text, at a position. It never quotes the text.
type Error struct {
	Name string // the file name, if any
	Pos  Pos
	Msg  string
}

func (e *Error) Error() string {
	if e.Name == "" {
		return fmt.Sprintf("line %d, column %d: %s", e.Pos.Line, e.Pos.Col, e.Msg)
	}
	return fmt.Sprintf("%s:%d:%d: %s", e.Name, e.Pos.Line, e.Pos.Col, e.Msg)
}

// declarationKinds are the kinds of dil.core declarations, with the name of
// their collection in DIL JSON.
var declarationKinds = map[string]string{
	"function": "functions", "processor": "processors", "interceptor": "interceptors",
	"format": "formats", "map": "maps", "service": "services", "message": "messages",
	"resource": "resources", "connection": "connections", "store": "stores",
	"test": "tests",
}

var stepKinds = []string{"source", "action", "router", "sink"}

var numberPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// Parse parses DIL text. name labels errors; it may be empty.
func Parse(name string, src []byte) (*File, error) {
	lx := newLexer(src)
	p := &parser{}
	for {
		t, err := lx.next()
		if err != nil {
			se := err.(*syntaxError)
			return nil, &Error{name, se.pos, se.msg}
		}
		p.toks = append(p.toks, t)
		if t.kind == tokEOF {
			break
		}
	}
	f, err := p.file()
	if err != nil {
		se := err.(*syntaxError)
		return nil, &Error{name, se.pos, se.msg}
	}
	return f, nil
}

type parser struct {
	toks  []token
	i     int
	depth int
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) peek2() token {
	if p.i+1 < len(p.toks) {
		return p.toks[p.i+1]
	}
	return p.toks[len(p.toks)-1]
}
func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tokEOF {
		p.i++
	}
	return t
}

// isWord reports whether the next token is the bare word w.
func (p *parser) isWord(w string) bool { t := p.peek(); return t.kind == tokWord && t.text == w }

func (p *parser) errorf(pos Pos, format string, args ...any) error {
	return &syntaxError{pos, fmt.Sprintf(format, args...)}
}

// expected reports that the next token is not what is expected.
func (p *parser) expected(what string) error {
	t := p.peek()
	return p.errorf(t.pos, "expected %s, found %s", what, t.describe())
}

func (p *parser) expect(kind tokenKind, what string) (token, error) {
	if p.peek().kind != kind {
		return token{}, p.expected(what)
	}
	return p.next(), nil
}

// name reads a name: a word or a string.
func (p *parser) name(what string) (token, error) {
	if k := p.peek().kind; k != tokWord && k != tokString {
		return token{}, p.expected(what)
	}
	return p.next(), nil
}

func (p *parser) enter(pos Pos) error {
	if p.depth++; p.depth > maxDepth {
		return p.errorf(pos, "nested too deep")
	}
	return nil
}

func (p *parser) file() (*File, error) {
	f := &File{}
	for p.peek().kind != tokEOF {
		t := p.peek()
		var item Item
		var err error
		switch {
		case p.isWord("import"):
			item, err = p.importLine()
		case p.isWord("integration"):
			item, err = p.integration()
		case p.isWord("flow"):
			item, err = p.flow()
		case t.kind == tokWord && declarationKinds[t.text] != "":
			item, err = p.declaration()
		default:
			return nil, p.expected("import, integration, flow or a declaration (function, processor, interceptor, format, map, service, message, resource, connection, store or test)")
		}
		if err != nil {
			return nil, err
		}
		f.Items = append(f.Items, item)
	}
	return f, nil
}

func (p *parser) importLine() (*Import, error) {
	imp := &Import{Pos: p.next().pos}
	path, err := p.name("the path of the imported file")
	if err != nil {
		return nil, err
	}
	if !p.isWord("as") {
		return nil, p.expected("'as' and a namespace after the import path")
	}
	p.next()
	ns, err := p.name("a namespace after 'as'")
	if err != nil {
		return nil, err
	}
	imp.Path, imp.Namespace = path.text, ns.text
	return imp, nil
}

func (p *parser) integration() (*Integration, error) {
	in := &Integration{Pos: p.next().pos}
	if k := p.peek().kind; k == tokWord || k == tokString {
		in.ID, in.HasID = p.next().text, true
	}
	if p.peek().kind == tokLParen {
		var err error
		if in.Attrs, err = p.params("field"); err != nil {
			return nil, err
		}
	}
	if _, err := p.expect(tokLBrace, "'{' to start the integration's flows"); err != nil {
		return nil, err
	}
	for p.peek().kind != tokRBrace {
		if !p.isWord("flow") {
			return nil, p.expected("a flow or '}' to end the integration")
		}
		f, err := p.flow()
		if err != nil {
			return nil, err
		}
		in.Flows = append(in.Flows, f)
	}
	p.next()
	return in, nil
}

func (p *parser) flow() (*Flow, error) {
	f := &Flow{Pos: p.next().pos}
	if k := p.peek().kind; k == tokWord || k == tokString {
		f.ID, f.HasID = p.next().text, true
	}
	if p.peek().kind == tokLParen {
		var err error
		if f.Params, err = p.params("option"); err != nil {
			return nil, err
		}
	}
	if _, err := p.expect(tokLBrace, "'{' to start the flow"); err != nil {
		return nil, err
	}
	for p.peek().kind != tokRBrace {
		if p.isWord("on") && p.peek2().kind == tokWord && p.peek2().text == "error" {
			h, err := p.handler()
			if err != nil {
				return nil, err
			}
			f.Handlers = append(f.Handlers, h)
			continue
		}
		if f.Chain != nil {
			// A flow has one chain, from its source; other routes are branches.
			return nil, p.expected("'->' before the next step, 'on error' or '}' to end the flow")
		}
		chain, err := p.chain()
		if err != nil {
			return nil, err
		}
		f.Chain = chain
	}
	p.next()
	return f, nil
}

func (p *parser) handler() (*Handler, error) {
	h := &Handler{Pos: p.next().pos}
	p.next() // error
	if p.peek().kind == tokLParen {
		var err error
		if h.Options, err = p.params("option"); err != nil {
			return nil, err
		}
		h.HasOptions = true
	}
	if p.isWord("as") {
		p.next()
		id, err := p.name("an id after 'as'")
		if err != nil {
			return nil, err
		}
		h.ID, h.HasID = id.text, true
	}
	if p.peek().kind == tokLBrace {
		p.next()
		route, err := p.chain()
		if err != nil {
			return nil, err
		}
		h.Route = route
		if _, err := p.expect(tokRBrace, "'}' to end the error route"); err != nil {
			return nil, err
		}
	}
	return h, nil
}

// chain reads steps connected by ->. A step with a block ends the chain.
func (p *parser) chain() ([]*Step, error) {
	if err := p.enter(p.peek().pos); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	var steps []*Step
	for {
		s, err := p.step()
		if err != nil {
			return nil, err
		}
		steps = append(steps, s)
		if p.peek().kind != tokArrow {
			return steps, nil
		}
		if s.Block {
			return nil, p.errorf(p.peek().pos, "a router's routes are inside its braces: write the step after it as a branch, such as '_ -> step'")
		}
		p.next()
	}
}

func (p *parser) step() (*Step, error) {
	t := p.peek()
	s := &Step{Pos: t.pos}
	if t.kind == tokWord && slices.Contains(stepKinds, t.text) {
		if n := p.peek2(); n.kind == tokString || n.kind == tokWord && n.text != "as" {
			s.Kind = p.next().text
		}
	}
	uri, err := p.name("a step")
	if err != nil {
		return nil, err
	}
	s.URI = uri.text
	if p.peek().kind == tokLParen {
		if s.Options, err = p.params("option"); err != nil {
			return nil, err
		}
		s.HasOptions = true
	}
	if p.isWord("as") {
		p.next()
		id, err := p.name("an id after 'as'")
		if err != nil {
			return nil, err
		}
		s.ID, s.HasID = id.text, true
	}
	if p.peek().kind == tokLBrace {
		p.next()
		s.Block = true
		for p.peek().kind != tokRBrace {
			b, err := p.branch()
			if err != nil {
				return nil, err
			}
			s.Branches = append(s.Branches, b)
		}
		p.next()
	}
	return s, nil
}

func (p *parser) branch() (*Branch, error) {
	t := p.peek()
	b := &Branch{Pos: t.pos}
	if t.kind == tokEOF {
		return nil, p.expected("a branch or '}' to end the router's routes")
	}
	if !(t.kind == tokWord && t.text == "_") {
		rule, err := p.name("a branch: a rule (or _ for none), an optional condition, '->' and its steps")
		if err != nil {
			return nil, err
		}
		b.Rule = rule.text
	} else {
		p.next()
	}
	// A language is a word before the expression; "" is no expression.
	if p.peek().kind == tokWord && p.peek2().kind == tokString {
		b.Language = p.next().text
	}
	if p.peek().kind == tokString {
		b.Expression = p.next().text
	}
	if _, err := p.expect(tokArrow, "'->' after the branch's rule and condition"); err != nil {
		return nil, err
	}
	chain, err := p.chain()
	if err != nil {
		return nil, err
	}
	b.Chain = chain
	return b, nil
}

func (p *parser) declaration() (*Declaration, error) {
	t := p.next()
	d := &Declaration{Pos: t.pos, Kind: t.text}
	if k := p.peek().kind; k == tokWord || k == tokString {
		d.Name, d.HasName = p.next().text, true
	}
	if _, err := p.expect(tokLBrace, "'{' to start the "+d.Kind+"'s fields"); err != nil {
		return nil, err
	}
	for p.peek().kind != tokRBrace {
		if d.Kind == "message" && p.isWord("header") && p.peek2().kind != tokColon {
			h, err := p.header()
			if err != nil {
				return nil, err
			}
			d.Headers = append(d.Headers, h)
		} else {
			m, err := p.field("field")
			if err != nil {
				return nil, err
			}
			if _, dup := d.Fields.get(m.key); dup {
				return nil, p.errorf(m.pos, "duplicate field")
			}
			d.Fields = append(d.Fields, m)
		}
		if p.peek().kind == tokComma {
			p.next()
		}
	}
	p.next()
	return d, nil
}

func (p *parser) header() (Header, error) {
	h := Header{Pos: p.next().pos}
	name, err := p.name("the header's name")
	if err != nil {
		return h, err
	}
	if _, err := p.expect(tokColon, "':' after the header's name"); err != nil {
		return h, err
	}
	if p.peek().kind == tokWord {
		h.Language = p.next().text
	}
	value, err := p.expect(tokString, "the header's value, a string (with an optional language before it)")
	if err != nil {
		return h, err
	}
	h.Name, h.Value = name.text, value.text
	return h, nil
}

// params reads ( name: value, ... ). Commas are optional.
func (p *parser) params(what string) (object, error) {
	p.next()
	o := object{}
	for p.peek().kind != tokRParen {
		m, err := p.field(what)
		if err != nil {
			return nil, err
		}
		if _, dup := o.get(m.key); dup {
			return nil, p.errorf(m.pos, "duplicate %s", what)
		}
		o = append(o, m)
		if p.peek().kind == tokComma {
			p.next()
		}
	}
	p.next()
	return o, nil
}

// field reads name: value.
func (p *parser) field(what string) (member, error) {
	key, err := p.name("a " + what + " name")
	if err != nil {
		return member{}, err
	}
	if _, err := p.expect(tokColon, "':' after the "+what+" name"); err != nil {
		return member{}, err
	}
	v, err := p.value()
	if err != nil {
		return member{}, err
	}
	return member{key: key.text, val: v, pos: key.pos}, nil
}

// value reads a string, number, true, false, null, list or object.
func (p *parser) value() (any, error) {
	t := p.peek()
	switch t.kind {
	case tokString:
		p.next()
		return t.text, nil
	case tokWord:
		switch {
		case t.text == "true" || t.text == "false":
			p.next()
			return t.text == "true", nil
		case t.text == "null":
			p.next()
			return nil, nil
		case numberPattern.MatchString(t.text):
			p.next()
			return json.Number(t.text), nil
		}
		return nil, p.errorf(t.pos, "expected a value, found a word (write text in quotes)")
	case tokLBrack:
		if err := p.enter(t.pos); err != nil {
			return nil, err
		}
		defer func() { p.depth-- }()
		p.next()
		list := []any{}
		for p.peek().kind != tokRBrack {
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			list = append(list, v)
			if p.peek().kind == tokComma {
				p.next()
			}
		}
		p.next()
		return list, nil
	case tokLBrace:
		if err := p.enter(t.pos); err != nil {
			return nil, err
		}
		defer func() { p.depth-- }()
		p.next()
		o := object{}
		for p.peek().kind != tokRBrace {
			m, err := p.field("field")
			if err != nil {
				return nil, err
			}
			if _, dup := o.get(m.key); dup {
				return nil, p.errorf(m.pos, "duplicate field")
			}
			o = append(o, m)
			if p.peek().kind == tokComma {
				p.next()
			}
		}
		p.next()
		return o, nil
	}
	return nil, p.expected("a value")
}
