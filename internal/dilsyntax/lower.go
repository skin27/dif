package dilsyntax

import (
	"strconv"
	"strings"
)

// Lower converts a parsed file to DIL JSON. name labels errors.
func Lower(name string, f *File) ([]byte, error) {
	var imports []any
	core := object{}
	var integrations []any
	implicit := -1 // index in integrations of the integration of the flows written outside one
	for _, item := range f.Items {
		switch x := item.(type) {
		case *Import:
			imports = append(imports, object{{key: "path", val: x.Path}, {key: "namespace", val: x.Namespace}})
		case *Declaration:
			d, err := lowerDeclaration(x)
			if err != nil {
				return nil, &Error{name, err.(*syntaxError).pos, err.(*syntaxError).msg}
			}
			plural := declarationKinds[x.Kind]
			wrapper := core.obj(plural)
			var list []any
			if v, ok := wrapper.get(x.Kind); ok {
				list = v.([]any)
			}
			list = append(list, d)
			if wrapper == nil {
				core = append(core, member{key: plural, val: object{{key: x.Kind, val: list}}})
			} else {
				wrapper[0].val = list
			}
		case *Integration:
			in, err := lowerIntegration(x)
			if err != nil {
				return nil, &Error{name, err.(*syntaxError).pos, err.(*syntaxError).msg}
			}
			integrations = append(integrations, in)
		case *Flow:
			fl, err := lowerFlow(x)
			if err != nil {
				return nil, &Error{name, err.(*syntaxError).pos, err.(*syntaxError).msg}
			}
			if implicit < 0 {
				implicit = len(integrations)
				integrations = append(integrations, object{{key: "flows", val: object{{key: "flow", val: []any{}}}}})
			}
			flows := integrations[implicit].(object)[0].val.(object)
			flows[0].val = append(flows[0].val.([]any), fl)
		}
	}
	dil := object{}
	if imports != nil {
		dil = append(dil, member{key: "imports", val: object{{key: "import", val: imports}}})
	}
	if len(core) > 0 {
		dil = append(dil, member{key: "core", val: core})
	}
	if integrations != nil {
		dil = append(dil, member{key: "integrations", val: object{{key: "integration", val: integrations}}})
	}
	return appendJSON(nil, object{{key: "dil", val: dil}}), nil
}

func errorAt(pos Pos, msg string) error { return &syntaxError{pos, msg} }

func lowerDeclaration(d *Declaration) (object, error) {
	key := "name"
	if d.Kind == "connection" {
		key = "id"
	}
	out := object{}
	if d.HasName {
		if _, dup := d.Fields.get(key); dup {
			return nil, errorAt(d.Pos, "the "+d.Kind+" has its "+key+" both after '"+d.Kind+"' and as a field")
		}
		out = append(out, member{key: key, val: d.Name})
	}
	out = append(out, d.Fields...)
	if d.Headers != nil {
		if _, dup := d.Fields.get("headers"); dup {
			return nil, errorAt(d.Headers[0].Pos, "the message has both header lines and a headers field")
		}
		list := []any{}
		for _, h := range d.Headers {
			o := object{{key: "name", val: h.Name}, {key: "value", val: h.Value}}
			if h.Language != "" {
				o = append(o, member{key: "language", val: h.Language})
			}
			list = append(list, o)
		}
		out = append(out, member{key: "headers", val: object{{key: "header", val: list}}})
	}
	return out, nil
}

func lowerIntegration(in *Integration) (object, error) {
	out := object{}
	if in.HasID {
		if _, dup := in.Attrs.get("id"); dup {
			return nil, errorAt(in.Pos, "the integration has its id both after 'integration' and as a field")
		}
		out = append(out, member{key: "id", val: in.ID})
	}
	for _, m := range in.Attrs {
		if m.key == "flows" {
			return nil, errorAt(m.pos, "an integration's flows are written inside its braces")
		}
		out = append(out, m)
	}
	flows := []any{}
	for _, f := range in.Flows {
		fl, err := lowerFlow(f)
		if err != nil {
			return nil, err
		}
		flows = append(flows, fl)
	}
	return append(out, member{key: "flows", val: object{{key: "flow", val: flows}}}), nil
}

// lowerFlow converts a flow. Its parameters are its name and its options.
// Steps are written in the order of the text: the chain from the source,
// each step before the routes that follow it, then each error handler before
// its route. Link ids are generated.
func lowerFlow(f *Flow) (object, error) {
	out := object{}
	if f.HasID {
		out = append(out, member{key: "id", val: f.ID})
	}
	var options object
	for _, m := range f.Params {
		switch m.key {
		case "id":
			if f.HasID {
				return nil, errorAt(m.pos, "the flow has its id both after 'flow' and as an option")
			}
			out = append(out, m)
		case "name":
			out = append(out, m)
		default:
			options = append(options, m)
		}
	}
	if options != nil {
		out = append(out, member{key: "options", val: options})
	}
	l := &flowLowering{taken: map[string]bool{}, counters: map[string]int{}, steps: []any{}}
	l.collect(f.Chain)
	for _, h := range f.Handlers {
		if h.HasID {
			l.taken[h.ID] = true
		}
		l.collect(h.Route)
	}
	l.chain(f.Chain, "", true)
	for _, h := range f.Handlers {
		id := h.ID
		if !h.HasID {
			id = l.generate("error")
		}
		s := object{{key: "id", val: id}, {key: "type", val: "error"}, {key: "uri", val: "failedexchange"}}
		if h.HasOptions {
			s = append(s, member{key: "options", val: h.Options})
		}
		var link string
		if h.Route != nil {
			link = l.link()
			s = append(s, member{key: "links", val: object{{key: "link", val: []any{object{{key: "id", val: link}, {key: "bound", val: "out"}}}}}})
		}
		l.steps = append(l.steps, s)
		l.chain(h.Route, link, false)
	}
	return append(out, member{key: "steps", val: object{{key: "step", val: l.steps}}}), nil
}

type flowLowering struct {
	taken    map[string]bool // the step ids of the flow
	counters map[string]int  // per base, the last number generated
	links    int
	steps    []any
}

// collect takes the ids written in the chain, so that generated ids differ.
func (l *flowLowering) collect(chain []*Step) {
	for _, s := range chain {
		if s.HasID {
			l.taken[s.ID] = true
		}
		for _, b := range s.Branches {
			l.collect(b.Chain)
		}
	}
}

// generate returns a new step id: base-1, base-2 and so on, skipping ids
// that are taken.
func (l *flowLowering) generate(base string) string {
	for {
		l.counters[base]++
		id := base + "-" + strconv.Itoa(l.counters[base])
		if !l.taken[id] {
			l.taken[id] = true
			return id
		}
	}
}

func (l *flowLowering) link() string {
	l.links++
	return "l" + strconv.Itoa(l.links)
}

// idBase is the base of a generated step id: the scheme of its URI, such as
// setbody for setbody or service for service:orders:PlaceOrder.
func idBase(uri string) string {
	scheme, _, _ := strings.Cut(uri, ":")
	base := strings.Map(func(r rune) rune {
		if 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return -1
	}, scheme)
	if base == "" {
		return "step"
	}
	return base
}

// inferType is the type of a step that has no explicit kind: the first step
// of a flow is its source, a step with routes a router, the last step of a
// chain a sink, and any other step an action.
func inferType(source, block, last bool) string {
	switch {
	case source:
		return "source"
	case block:
		return "router"
	case last:
		return "sink"
	}
	return "action"
}

// chain appends the steps of a chain. in is the id of the link into its
// first step; main is true for the chain that starts at the flow's source.
func (l *flowLowering) chain(chain []*Step, in string, main bool) {
	for i, s := range chain {
		typ := s.Kind
		if typ == "" {
			typ = inferType(main && i == 0, s.Block, i == len(chain)-1)
		}
		id := s.ID
		if !s.HasID {
			id = l.generate(idBase(s.URI))
		}
		var links []any
		if in != "" {
			links = append(links, object{{key: "id", val: in}, {key: "bound", val: "in"}})
		}
		var routes []string
		in = ""
		if s.Block {
			for _, b := range s.Branches {
				link := l.link()
				routes = append(routes, link)
				o := object{{key: "id", val: link}, {key: "bound", val: "out"}}
				for _, f := range []member{{key: "rule", val: b.Rule}, {key: "language", val: b.Language}, {key: "expression", val: b.Expression}} {
					if f.val != "" {
						o = append(o, f)
					}
				}
				links = append(links, o)
			}
		} else if i < len(chain)-1 {
			in = l.link()
			links = append(links, object{{key: "id", val: in}, {key: "bound", val: "out"}})
		}
		o := object{{key: "id", val: id}, {key: "type", val: typ}, {key: "uri", val: s.URI}}
		if s.HasOptions {
			o = append(o, member{key: "options", val: s.Options})
		}
		if links != nil {
			o = append(o, member{key: "links", val: object{{key: "link", val: links}}})
		}
		l.steps = append(l.steps, o)
		for j, b := range s.Branches {
			l.chain(b.Chain, routes[j], false)
		}
	}
}
