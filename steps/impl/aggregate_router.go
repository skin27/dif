package impl

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"

	"dif/message"
	stepdef "dif/steps/definition"
)

// aggregateRouter collects messages and passes one on when the group is
// complete: the message that completed it, with the aggregate of the group's
// bodies as body and without the split headers. Other messages stop here. In
// DIL it is an action: a router with one outbound link.
//
// A group is complete with a message whose split.complete header is true
// (the last part of a split), or when it holds completionSize messages. There
// is one group at a time, as the Kamelet correlates all messages; the first
// part of a split (split.index 0) starts a new one, dropping what is left of
// an earlier split that failed.
type aggregateRouter struct {
	xml  bool // aggregate as XML, else as JSON
	size int  // completionSize; 0: complete only on split.complete

	mu     sync.Mutex
	bodies []any
}

func newAggregateRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	if n := len(p[stepdef.Links].([]stepdef.Link)); n != 1 {
		return nil, fmt.Errorf("needs one outbound link, has %d", n)
	}
	for _, opt := range []string{"completionTimeout", "completionInterval"} {
		if p[opt].(int) > 0 {
			return nil, fmt.Errorf("option %s: completing by time is not supported yet; use completionSize or a split", opt)
		}
	}
	return &aggregateRouter{xml: isXMLType(p["aggregateType"].(string)), size: p["completionSize"].(int)}, nil
}

func (a *aggregateRouter) Route(_ context.Context, m message.Message) ([]stepdef.Route, error) {
	a.mu.Lock()
	if m[SplitIndex] == 0 {
		a.bodies = a.bodies[:0]
	}
	a.bodies = append(a.bodies, m[message.Body])
	if m[SplitComplete] != true && (a.size == 0 || len(a.bodies) < a.size) {
		a.mu.Unlock()
		return nil, nil
	}
	bodies := a.bodies
	a.bodies = nil
	a.mu.Unlock()

	body, err := aggregateBodies(a.xml, bodies)
	if err != nil {
		return nil, err
	}
	m[message.Body] = body
	delete(m, SplitIndex)
	delete(m, SplitSize)
	delete(m, SplitComplete)
	return []stepdef.Route{{Next: 0, Message: m}}, nil
}

// splitAndAggregateRouter splits the body like the split router, sends every
// part along the link with rule "split", aggregates what comes out of them
// into the body and sends the message on along the other link. A part that
// fails fails the message.
type splitAndAggregateRouter struct {
	splitRouter
	xml bool
}

func newSplitAndAggregateRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	links := p[stepdef.Links].([]stepdef.Link)
	expr := p["expression"].(string)
	if expr == "" { // DIL may keep it on the split link only
		for _, l := range links {
			if l.Rule == "split" {
				expr = l.Expression
			}
		}
	}
	if expr == "" {
		return nil, fmt.Errorf("needs an expression: the option expression or the split link's")
	}
	s, err := newSplitter(flowOf(p), links, p["language"].(string), expr)
	if err != nil {
		return nil, err
	}
	if s.main < 0 {
		return nil, fmt.Errorf("needs an outbound link without a rule for the aggregate")
	}
	return splitAndAggregateRouter{s, isXMLType(p["aggregateType"].(string))}, nil
}

func (r splitAndAggregateRouter) Route(_ context.Context, m message.Message) ([]stepdef.Route, error) {
	return r.partRoutes(m)
}

func (r splitAndAggregateRouter) Gather(_ context.Context, m message.Message, outcomes []stepdef.Outcome) ([]stepdef.Route, error) {
	if len(outcomes) == 0 { // nothing was split: the message goes on as it is
		return []stepdef.Route{{Next: r.main, Message: m}}, nil
	}
	bodies := make([]any, len(outcomes))
	for i, o := range outcomes {
		if o.Err != nil {
			return nil, o.Err
		}
		bodies[i] = o.Message[message.Body]
	}
	body, err := aggregateBodies(r.xml, bodies)
	if err != nil {
		return nil, err
	}
	m[message.Body] = body
	return []stepdef.Route{{Next: r.main, Message: m}}, nil
}

func isXMLType(t string) bool { return strings.Contains(t, "xml") }

// aggregateBodies returns the aggregate of bodies: as XML, their root
// elements in an Aggregated element; as JSON, an array of their values.
func aggregateBodies(asXML bool, bodies []any) (string, error) {
	var b bytes.Buffer
	if asXML {
		b.WriteString("<Aggregated>")
		for i, body := range bodies {
			data := bytesOf(body)
			if _, err := parseXMLTree(data); err != nil {
				return "", fmt.Errorf("aggregate part %d: %w", i+1, err)
			}
			start, end, _, _ := rootSpan(data)
			b.Write(data[start:end])
		}
		b.WriteString("</Aggregated>")
		return b.String(), nil
	}
	b.WriteByte('[')
	for i, body := range bodies {
		v, err := readJSON(body)
		if err != nil {
			return "", fmt.Errorf("aggregate part %d: %w", i+1, err)
		}
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSON(&b, v)
	}
	b.WriteByte(']')
	return b.String(), nil
}
