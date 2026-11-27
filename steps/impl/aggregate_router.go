package impl

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"dif/internal/channels"
	"dif/message"
	stepdef "dif/steps/definition"
)

// aggregateRouter collects messages and, when a group is complete, releases
// one message with the aggregate of the group's bodies as body: a message of its
// own, made from the group's last, without the split headers, that goes on
// along the link with its result ignored. The message that came in carries on
// as it came, so a sender that waits for a reply gets it back at once, and a
// split's parts, wherever they end, come out as they went in for the split to
// aggregate. In DIL it is an action: a router with one outbound link.
//
// A group is complete with a message whose split.complete header is true
// (the last part of a split), when it holds completionSize messages, when
// completionTimeout has passed since its last message came, and every
// completionInterval (all groups). Without correlationKey there is one group
// at a time, as the Kamelet correlates all messages; with it, the messages
// whose key has the same value make a group, and each group completes on its
// own. Without a timer, the first part of a split (split.index 0) starts its
// group anew, dropping what is left of an earlier split that failed. A message
// that cannot be aggregated (not XML or JSON) fails when it comes in.
//
// A group completed by a timer is released by the engine as a message of its
// own (see stepdef.Releaser); one completed by a message goes on at once.
//
// With the option store, the groups are also kept in that store, under
// <flow>/<step>/<key>, and the router takes them back when it is made: with a
// durable store, a group that was not complete when dif stopped completes
// after a restart. Its timeout runs from its last message, by the clock, so a
// group whose timeout passed while dif was stopped completes when the flow
// starts. A group leaves the store when it completes, before the aggregate
// goes on.
type aggregateRouter struct {
	typ               string // aggregateType as the flow wrote it
	xml               bool   // aggregate as XML, else as JSON
	size              int    // completionSize; 0: complete only on split.complete
	timeout, interval time.Duration
	key               *expression     // correlationKey; nil: one group
	store             *channels.Store // keeps the groups; nil: in memory only
	prefix            string          // of the groups' keys in store

	mu     sync.Mutex
	groups map[string]*aggregateGroup // by the value of the key
	wake   chan struct{}              // tells Release the groups changed
}

// aggregateGroup is a group of messages that is not complete yet.
type aggregateGroup struct {
	parts []string        // the bodies, each ready to be joined
	last  message.Message // a copy of the last message; kept for a timer to use
	seen  time.Time       // when it came
}

func newAggregateRouter(stepID string, p stepdef.Params) (stepdef.Processor, error) {
	if n := len(p[stepdef.Links].([]stepdef.Link)); n != 1 {
		return nil, fmt.Errorf("needs one outbound link, has %d", n)
	}
	typ := p["aggregateType"].(string)
	a := &aggregateRouter{
		typ: typ, xml: isXMLType(typ), size: p["completionSize"].(int),
		timeout:  time.Duration(p["completionTimeout"].(int)) * time.Millisecond,
		interval: time.Duration(p["completionInterval"].(int)) * time.Millisecond,
		groups:   map[string]*aggregateGroup{},
		wake:     make(chan struct{}, 1),
	}
	if expr := optString(p, "correlationKey"); expr != "" {
		key, err := compileExpressionIn(flowOf(p), "simple", expr)
		if err != nil {
			return nil, fmt.Errorf("option correlationKey: %w", err)
		}
		a.key = &key
	}
	if name := optString(p, "store"); name != "" {
		s, err := openStore(p, name)
		if err != nil {
			return nil, fmt.Errorf("option store: %w", err)
		}
		flowID := ""
		if f := flowOf(p); f != nil {
			flowID = f.id
		}
		a.store, a.prefix = s, flowID+"/"+stepID+"/"
		if err := a.load(); err != nil {
			return nil, fmt.Errorf("option store: %w", err)
		}
	}
	return a, nil
}

// load takes back the groups the store keeps.
func (a *aggregateRouter) load() error {
	for _, k := range a.store.Keys() {
		key, ok := strings.CutPrefix(k, a.prefix)
		if !ok {
			continue
		}
		v, found, err := a.store.Get(k)
		if err != nil {
			return err
		}
		g, ok := v.(map[string]any)
		if !found || !ok {
			continue
		}
		parts, _ := g["parts"].([]string)
		last, _ := g["last"].(message.Message)
		if last == nil { // kept without a timer; a timer makes the aggregate from it
			last = message.Message{}
		}
		seen, _ := g["seen"].(int64)
		if len(parts) > 0 {
			a.groups[key] = &aggregateGroup{parts: parts, last: last, seen: time.UnixMilli(seen)}
		}
	}
	return nil
}

// save keeps the group in the store, if there is one.
func (a *aggregateRouter) save(key string, g *aggregateGroup) error {
	if a.store == nil {
		return nil
	}
	var last any
	if g.last != nil {
		last = g.last
	}
	return a.store.Put(a.prefix+key, map[string]any{"parts": g.parts, "last": last, "seen": g.seen.UnixMilli()})
}

// forget removes the group from the store, if there is one.
func (a *aggregateRouter) forget(key string) error {
	if a.store == nil {
		return nil
	}
	return a.store.Remove(a.prefix + key)
}

// aggregateTypeKey is where the router leaves the type it aggregates as on the
// messages that come in, for a splitandaggregate around it to aggregate the
// parts as the same.
const aggregateTypeKey = message.MetadataPrefix + "aggregatetype"

func (a *aggregateRouter) timed() bool { return a.timeout > 0 || a.interval > 0 }

func (a *aggregateRouter) Route(ctx context.Context, m message.Message) ([]stepdef.Route, error) {
	part, err := aggregatePart(a.xml, m[message.Body])
	if err != nil {
		return nil, fmt.Errorf("aggregate: %w", err)
	}
	key := ""
	if a.key != nil {
		if key, err = a.key.eval(m, ctx); err != nil {
			return nil, fmt.Errorf("aggregate: correlationKey: %w", err)
		}
	}
	m[aggregateTypeKey] = a.typ

	a.mu.Lock()
	defer a.mu.Unlock()
	g := a.groups[key]
	if g == nil {
		g = &aggregateGroup{}
	}
	old := *g // what the group was, should the store fail
	if m[SplitIndex] == 0 && !a.timed() {
		g.parts = nil
	}
	g.parts = append(g.parts, part)
	if m[SplitComplete] != true && (a.size == 0 || len(g.parts) < a.size) {
		if a.timed() {
			g.last, g.seen = m.Copy(), time.Now()
		}
		if err := a.save(key, g); err != nil {
			*g = old
			return nil, fmt.Errorf("aggregate: %w", err)
		}
		a.groups[key] = g
		if a.timed() {
			a.poke()
		}
		return nil, nil
	}
	if err := a.forget(key); err != nil {
		*g = old
		return nil, fmt.Errorf("aggregate: %w", err)
	}
	delete(a.groups, key)
	if a.timed() {
		a.poke()
	}
	return []stepdef.Route{{Next: 0, Message: a.aggregate(m, g.parts), Detached: true}}, nil
}

// aggregate returns the message that holds parts, made from last.
func (a *aggregateRouter) aggregate(last message.Message, parts []string) message.Message {
	agg := last.Child(joinParts(a.xml, parts))
	delete(agg, SplitIndex)
	delete(agg, SplitSize)
	delete(agg, SplitComplete)
	delete(agg, aggregateTypeKey)
	return agg
}

// poke tells Release that the group changed, without waiting.
func (a *aggregateRouter) poke() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

// Release completes the groups by their timers, until ctx is done.
func (a *aggregateRouter) Release(ctx context.Context, send func(message.Message) error) error {
	if !a.timed() {
		return nil
	}
	next := time.Now().Add(a.interval) // the end of the interval
	for {
		a.mu.Lock()
		var wait time.Duration
		timing := false
		if a.interval > 0 {
			wait, timing = time.Until(next), true
		}
		for _, g := range a.groups {
			if d := time.Until(g.seen.Add(a.timeout)); a.timeout > 0 && (!timing || d < wait) {
				wait, timing = d, true
			}
		}
		a.mu.Unlock()

		var due <-chan time.Time // stays nil, and so waits for a change, with no timer to wait for
		var timer *time.Timer
		if timing {
			timer = time.NewTimer(max(wait, 0))
			due = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return nil
		case <-a.wake:
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-due:
		}

		now := time.Now()
		a.mu.Lock()
		ended := a.interval > 0 && !now.Before(next)
		for ended && !now.Before(next) {
			next = next.Add(a.interval)
		}
		var keys []string // of the groups that are complete
		for key, g := range a.groups {
			if ended || a.timeout > 0 && !now.Before(g.seen.Add(a.timeout)) {
				keys = append(keys, key)
			}
		}
		a.mu.Unlock()
		slices.Sort(keys)
		for _, key := range keys {
			a.mu.Lock()
			g := a.groups[key]
			if g != nil {
				delete(a.groups, key)
				// A store that fails keeps the group, which then completes
				// again after a restart; trying again now would never end.
				_ = a.forget(key)
			}
			a.mu.Unlock()
			if g == nil {
				continue
			}
			if err := send(a.aggregate(g.last, g.parts)); err != nil {
				a.restore(key, g)
				return nil // the flow is stopping
			}
		}
	}
}

// restore puts back a group that could not go on, before what has come for
// its key since.
func (a *aggregateRouter) restore(key string, g *aggregateGroup) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cur := a.groups[key]; cur != nil {
		cur.parts = append(slices.Clone(g.parts), cur.parts...)
		g = cur
	}
	a.groups[key] = g
	_ = a.save(key, g) // the flow is stopping; a store that fails now loses the group
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
	return newSplitAndAggregate(p, nil)
}

// newSplitAndAggregateWithNamespaceRouter is splitandaggregate whose xpath may
// use the prefix nsprefix, which stands for the namespace.
func newSplitAndAggregateWithNamespaceRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	ns, err := splitNamespace(p)
	if err != nil {
		return nil, err
	}
	return newSplitAndAggregate(p, ns)
}

func newSplitAndAggregate(p stepdef.Params, ns map[string]string) (stepdef.Processor, error) {
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
	s, err := newSplitter(flowOf(p), links, p["language"].(string), expr, ns)
	if err != nil {
		return nil, err
	}
	if s.main < 0 {
		return nil, fmt.Errorf("needs an outbound link without a rule for the aggregate")
	}
	return splitAndAggregateRouter{s, isXMLType(p["aggregateType"].(string))}, nil
}

func (r splitAndAggregateRouter) Route(ctx context.Context, m message.Message) ([]stepdef.Route, error) {
	return r.partRoutes(m, ctx)
}

func (r splitAndAggregateRouter) Gather(_ context.Context, m message.Message, outcomes []stepdef.Outcome) ([]stepdef.Route, error) {
	if len(outcomes) == 0 { // nothing was split: the message goes on as it is
		return []stepdef.Route{{Next: r.main, Message: m}}, nil
	}
	bodies := make([]any, len(outcomes))
	asXML := r.xml
	for i, o := range outcomes {
		if o.Err != nil {
			return nil, o.Err
		}
		bodies[i] = o.Message[message.Body]
		if t, ok := o.Message[aggregateTypeKey].(string); ok { // an aggregate in the split route decides
			asXML = isXMLType(t)
		}
	}
	body, err := aggregateBodies(asXML, bodies)
	if err != nil {
		return nil, err
	}
	m[message.Body] = body
	return []stepdef.Route{{Next: r.main, Message: m}}, nil
}

func isXMLType(t string) bool { return strings.Contains(t, "xml") }

// aggregateStart opens an XML aggregate: the declaration with no line break
// after it, as the platform writes it (its flows compare the text exactly).
const aggregateStart = `<?xml version="1.0" encoding="UTF-8"?><Aggregated>`

// aggregateBodies returns the aggregate of bodies: as XML, their root
// elements in an Aggregated element; as JSON, an array of their values.
func aggregateBodies(asXML bool, bodies []any) (string, error) {
	parts := make([]string, len(bodies))
	for i, body := range bodies {
		part, err := aggregatePart(asXML, body)
		if err != nil {
			return "", fmt.Errorf("aggregate part %d: %w", i+1, err)
		}
		parts[i] = part
	}
	return joinParts(asXML, parts), nil
}

// aggregatePart checks that body is XML (or JSON) and returns the root element
// (or its value in JSON, compact) that joinParts puts in the aggregate.
func aggregatePart(asXML bool, body any) (string, error) {
	if asXML {
		data := bytesOf(body)
		if _, err := parseXMLTree(data); err != nil {
			return "", err
		}
		start, end, _, _ := rootSpan(data)
		return string(data[start:end]), nil
	}
	v, err := readJSON(body)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	writeJSON(&b, v)
	return b.String(), nil
}

// joinParts returns the aggregate of parts.
func joinParts(asXML bool, parts []string) string {
	if asXML {
		return aggregateStart + strings.Join(parts, "") + "</Aggregated>"
	}
	return "[" + strings.Join(parts, ",") + "]"
}
