package impl

import (
	"context"
	"fmt"

	"dif/message"
	stepdef "dif/steps/definition"
)

// Headers the split router sets on every part.
const (
	SplitIndex    = "split.index"    // 0, 1, 2, ...
	SplitSize     = "split.size"     // number of parts
	SplitComplete = "split.complete" // true for the last part
)

// splitRouter splits the body into parts by an xpath or jsonpath expression
// and sends each part, as a copy of the message with the part as body, along
// the link with rule "split". The message itself then continues along the
// other link, if there is one, and that is the outcome; else the outcome is
// the last part's. A split with one link (in DIL, an action) needs no rule.
type splitRouter struct {
	split, main int // main is -1 if there is no other link
	xpath       xpath
	jsonPath    jsonPath // used when xpath is nil
}

func newSplitRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	r, err := newSplitter(p[stepdef.Links].([]stepdef.Link), p["language"].(string), p["expression"].(string))
	if err != nil {
		return nil, err
	}
	return r, nil
}

// newSplitter returns a splitRouter for links that splits by expression in
// language (xpath or jsonpath).
func newSplitter(links []stepdef.Link, language, expression string) (splitRouter, error) {
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
	if language == "xpath" {
		r.xpath, err = compileXPath(expression)
	} else {
		r.jsonPath, err = compileJSONPath(expression)
	}
	if err != nil {
		return r, fmt.Errorf("option expression: %w", err)
	}
	return r, nil
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
	parts, err := r.parts(m[message.Body])
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

// parts returns the parts of body as text: XML elements as they are in the
// document, JSON values as JSON (strings as is). A jsonpath that selects one
// array splits that array.
func (r splitRouter) parts(body any) ([]string, error) {
	var parts []string
	if r.xpath != nil {
		nodes, err := r.xpath.selectXML(bytesOf(body))
		if err != nil {
			return nil, err
		}
		for _, n := range nodes {
			parts = append(parts, n.raw)
		}
		return parts, nil
	}

	v, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}
	values := r.jsonPath.eval(v)
	if len(values) == 1 {
		if a, ok := values[0].([]any); ok {
			values = a
		}
	}
	for _, x := range values {
		parts = append(parts, text(x))
	}
	return parts, nil
}
