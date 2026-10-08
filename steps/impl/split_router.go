package impl

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"dif/message"
	stepdef "dif/steps/definition"
)

// Headers the split router sets on every part.
const (
	SplitIndex    = "split.index"    // 0, 1, 2, ...
	SplitSize     = "split.size"     // number of parts
	SplitComplete = "split.complete" // true for the last part
)

// splitRouter splits the body into parts by an expression (see splitParts)
// and sends each part, as a copy of the message with the part as body, along
// the link with rule "split". The message itself then continues along the
// other link, if there is one, and that is the outcome; else the outcome is
// the last part's. A split with one link (in DIL, an action) needs no rule.
type splitRouter struct {
	split, main int // main is -1 if there is no other link
	parts       func(m message.Message) ([]string, error)
}

func newSplitRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	r, err := newSplitter(flowOf(p), p[stepdef.Links].([]stepdef.Link), p["language"].(string), p["expression"].(string), nil)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// newSplitWithNamespaceRouter is the split router whose xpath may use the prefix
// nsprefix, which stands for the namespace.
func newSplitWithNamespaceRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	ns, err := splitNamespace(p)
	if err != nil {
		return nil, err
	}
	r, err := newSplitter(flowOf(p), p[stepdef.Links].([]stepdef.Link), p["language"].(string), p["expression"].(string), ns)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// splitNamespace returns the namespace binding of the options nsprefix and
// namespace, nil if they give none.
func splitNamespace(p stepdef.Params) (map[string]string, error) {
	prefix, _ := p["nsprefix"].(string)
	uri, _ := p["namespace"].(string)
	switch {
	case prefix == "" && uri == "":
		return nil, nil
	case prefix == "" || uri == "":
		return nil, fmt.Errorf("options nsprefix and namespace go together")
	case strings.Contains(prefix, ":") || strings.ContainsAny(prefix, " \t\r\n"):
		return nil, fmt.Errorf("option nsprefix: %q is not an XML prefix", prefix)
	}
	return map[string]string{prefix: uri}, nil
}

// newSplitter returns a splitRouter for links that splits by expression in
// language (xpath, jsonpath, tokenize, xtokenize or simple). The prefixes of an
// xpath or xtokenize expression stand for the namespaces in ns.
func newSplitter(flow *flowProperties, links []stepdef.Link, language, expression string, ns map[string]string) (splitRouter, error) {
	r := splitRouter{split: -1, main: -1}
	for i, l := range links {
		target := &r.main
		if l.Rule == "split" || len(links) == 1 {
			target = &r.split
		}
		if *target >= 0 {
			return r, fmt.Errorf("needs one outbound link with rule split and at most one other")
		}
		*target = i
	}
	if r.split < 0 {
		return r, fmt.Errorf("needs an outbound link with rule split")
	}

	var err error
	if r.parts, err = splitParts(flow, language, expression, ns); err != nil {
		return r, fmt.Errorf("option expression: %w", err)
	}
	return r, nil
}

// splitParts returns the function that cuts the body of a message into parts,
// as text, by expression in language:
//
//   - xpath: the nodes the XPath 2.0 expression selects; XML elements as XML
//   - jsonpath: the values the path selects, as JSON (strings as they are); one
//     array it selects is split into its elements
//   - tokenize: what lies between the occurrences of the text expression
//   - xtokenize: the elements the path (or, without a /, the name) selects
//   - simple: the elements of the list the simple expression gives, or the
//     parts of a text between commas; nothing, if it gives nothing
//
// An expression with ${...} in it, such as ${header.expression}, is a simple
// template evaluated for every message; its result is the xpath or jsonpath.
func splitParts(flow *flowProperties, language, expression string, ns map[string]string) (func(message.Message) ([]string, error), error) {
	switch language {
	case "xpath", "xtokenize":
		pick := func(m message.Message, q xpath) ([]string, error) {
			nodes, err := q.selectXML(bytesOf(m[message.Body]))
			if err != nil {
				return nil, err
			}
			parts := make([]string, len(nodes))
			for i, n := range nodes {
				parts[i] = n.raw
			}
			return parts, nil
		}
		compile := func(s string) (xpath, error) {
			if language == "xtokenize" && !strings.Contains(s, "/") {
				s = "//*:" + s // a name stands for the elements with that name
			}
			return compileXPathNS(s, ns)
		}
		return dynamicParts(flow, expression, compile, pick)
	case "jsonpath":
		return dynamicParts(flow, expression, compileJSONPath, func(m message.Message, p jsonPath) ([]string, error) {
			v, err := decodeJSON(m[message.Body])
			if err != nil {
				return nil, err
			}
			values := p.eval(v)
			if len(values) == 1 {
				if a, ok := values[0].([]any); ok {
					values = a
				}
			}
			parts := make([]string, len(values))
			for i, x := range values {
				parts[i] = text(x)
			}
			return parts, nil
		})
	case "tokenize":
		if expression == "" {
			return nil, fmt.Errorf("tokenize needs the text to split at")
		}
		return func(m message.Message) ([]string, error) {
			var parts []string
			for _, t := range strings.Split(text(m[message.Body]), expression) {
				if t = strings.TrimSpace(t); t != "" {
					parts = append(parts, t)
				}
			}
			return parts, nil
		}, nil
	case "simple":
		x, err := compileExpressionIn(flow, "simple", expression)
		if err != nil {
			return nil, err
		}
		return func(m message.Message) ([]string, error) {
			v, err := x.value(m)
			if err != nil {
				return nil, err
			}
			var parts []string
			for _, e := range elements(v) {
				if t := render(e); t != "" {
					parts = append(parts, t)
				}
			}
			return parts, nil
		}, nil
	}
	return nil, fmt.Errorf("language %q is not supported for a split; use xpath, jsonpath, tokenize, xtokenize or simple", language)
}

// perMessage returns the function that gives the compiled expression for a
// message: the one compiled from expression; or, when expression holds ${...},
// such as ${header.expression}, a template evaluated for the message, the
// one compiled from the result (the results compiled are kept).
func perMessage[T any](flow *flowProperties, expression string, compile func(string) (T, error)) (func(message.Message) (T, error), error) {
	if !strings.Contains(expression, "${") {
		q, err := compile(expression)
		if err != nil {
			return nil, err
		}
		return func(message.Message) (T, error) { return q, nil }, nil
	}
	x, err := compileExpressionIn(flow, "simple", expression)
	if err != nil {
		return nil, err
	}
	var cache compiledCache[T]
	return func(m message.Message) (T, error) {
		s, err := x.eval(m)
		if err != nil {
			var zero T
			return zero, err
		}
		return cache.get(s, compile)
	}, nil
}

// dynamicParts is perMessage for the parts of a split.
func dynamicParts[T any](flow *flowProperties, expression string, compile func(string) (T, error), pick func(message.Message, T) ([]string, error)) (func(message.Message) ([]string, error), error) {
	get, err := perMessage(flow, expression, compile)
	if err != nil {
		return nil, err
	}
	return func(m message.Message) ([]string, error) {
		q, err := get(m)
		if err != nil {
			return nil, err
		}
		return pick(m, q)
	}, nil
}

// compiledCache keeps the expressions compiled from the texts a template gave.
type compiledCache[T any] struct {
	mu sync.Mutex
	m  map[string]T
}

func (c *compiledCache[T]) get(key string, compile func(string) (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.m[key]; ok {
		return v, nil
	}
	v, err := compile(key)
	if err != nil {
		return v, err
	}
	if c.m == nil || len(c.m) >= 64 {
		c.m = map[string]T{}
	}
	c.m[key] = v
	return v, nil
}

func (r splitRouter) Route(_ context.Context, m message.Message) ([]stepdef.Route, error) {
	routes, err := r.partRoutes(m)
	if err != nil {
		return nil, err
	}
	if r.main >= 0 {
		routes = append(routes, stepdef.Route{Next: r.main, Message: m})
	}
	return routes, nil
}

// partRoutes returns a route along the split link for every part of m's body.
func (r splitRouter) partRoutes(m message.Message) ([]stepdef.Route, error) {
	parts, err := r.parts(m)
	if err != nil {
		return nil, err
	}
	routes := make([]stepdef.Route, 0, len(parts)+1)
	for i, part := range parts {
		c := m.Child(part)
		c[SplitIndex], c[SplitSize], c[SplitComplete] = i, len(parts), i == len(parts)-1
		routes = append(routes, stepdef.Route{Next: r.split, Message: c})
	}
	return routes, nil
}
