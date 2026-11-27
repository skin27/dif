// Package flowcheck checks a flow statically: it walks the flow's graph,
// infers what is known about the message on every link from what each step is
// known to do to it (its Effect), and reports what cannot work before anything
// runs.
//
// Typing is gradual and never wrong. A step without an effect makes the
// message unknown, and unknown produces no diagnostic. An Error means that
// every message reaching the step fails there; anything less certain is a
// Warning. The package works on the DSL-independent flow model; the caller
// supplies the effects.
package flowcheck

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	flowdef "dif/flows/definition"
	stepdef "dif/steps/definition"
)

// Effect is what a step does to a message; see stepdef.Effect.
type Effect = stepdef.Effect

// Severities.
const (
	Error   = "error"   // every message that reaches the step fails there
	Warning = "warning" // the flow probably does not do what it means to
	Info    = "info"    // worth knowing
)

// Diagnostic codes.
const (
	CodeBodyMismatch        = "body-mismatch"           // the step needs a body of another family
	CodeContentTypeMismatch = "content-type-mismatch"   // the step checks Content-Type, which is set to another media type
	CodeFormatMismatch      = "format-mismatch"         // the step expects another named format of the same family
	CodeHeaderNotSet        = "header-not-set"          // an expression reads a header that no step before it sets
	CodeNoOtherwise         = "no-otherwise"            // a content router whose links all have a condition
	CodeUnreachable         = "unreachable"             // steps after a step that stops every message
	CodeHandlerMatchesNone  = "handler-matches-nothing" // an error handler whose errorStep or errorUri matches no step
	CodeHandlerShadowed     = "handler-shadowed"        // an error handler with the same match options as one before it
	CodeReplyMismatch       = "reply-mismatch"          // the reply of a contracted flow breaks the contract's output format
)

// Diagnostic is a finding of the check of a flow.
type Diagnostic struct {
	Flow     string `json:"flow"`
	Step     string `json:"step,omitempty"` // the step or error handler it is about; "" for the flow
	Severity string `json:"severity"`       // Error, Warning or Info
	Code     string `json:"code"`           // a stable short id, such as body-mismatch
	Message  string `json:"message"`
}

func (d Diagnostic) String() string {
	s := d.Severity + " " + d.Code + ": flow " + d.Flow
	if d.Step != "" {
		s += ", step " + d.Step
	}
	return s + ": " + d.Message
}

// maxVisits bounds how often the type on the inbound link of a step may grow
// before it is taken as unknown, for a graph with a cycle.
const maxVisits = 8

type checker struct {
	flow     *flowdef.Flow
	effectOf func(*flowdef.Node) *Effect
	effects  map[*flowdef.Node]*Effect
	in       map[*flowdef.Node]Type // the type of the message a step gets
	visits   map[*flowdef.Node]int
	nodes    []*flowdef.Node // in the order they were first reached
	diags    []Diagnostic
}

// Check checks flow f, with effectOf telling what each step does (nil: it is
// unknown), and returns its diagnostics in a deterministic order: by the order
// of the steps in the flow, then by code and message.
func Check(f *flowdef.Flow, effectOf func(*flowdef.Node) *Effect) []Diagnostic {
	if f == nil || f.Source == nil {
		return nil
	}
	c := &checker{flow: f, effectOf: effectOf, effects: map[*flowdef.Node]*Effect{}, in: map[*flowdef.Node]Type{}, visits: map[*flowdef.Node]int{}}

	// A source makes the message: before it there is nothing.
	src := c.effect(f.Source)
	c.reach(f.Source, Type{Closed: true})
	out := apply(src, Type{Closed: true}, f.Source.ID)
	if src != nil {
		// A message sent to the flow by hand carries the headers of its configured message.
		for k := range f.Input {
			if k != "body" && !strings.HasPrefix(k, "metadata.") {
				if p, _ := out.presence(k); p == 0 {
					out.set(k, Maybe)
				}
			}
		}
	}
	c.follow(f.Source, src, out)
	for _, h := range f.ErrorHandlers() {
		if h.Route != nil {
			c.reach(h.Route, Type{}) // a failure can come from anywhere
		}
	}

	for _, n := range c.nodes {
		c.checkStep(n)
	}
	c.checkHandlers()
	c.checkReply()

	order := map[string]int{}
	for i, n := range c.nodes {
		if _, ok := order[n.ID]; !ok {
			order[n.ID] = i
		}
	}
	for i, h := range f.ErrorHandlers() {
		if _, ok := order[h.ID]; !ok {
			order[h.ID] = len(c.nodes) + i
		}
	}
	slices.SortStableFunc(c.diags, func(a, b Diagnostic) int {
		if a.Step != b.Step {
			return order[a.Step] - order[b.Step]
		}
		if a.Code != b.Code {
			return strings.Compare(a.Code, b.Code)
		}
		return strings.Compare(a.Message, b.Message)
	})
	return slices.CompactFunc(c.diags, func(a, b Diagnostic) bool { return a == b })
}

func (c *checker) effect(n *flowdef.Node) *Effect {
	e, ok := c.effects[n]
	if !ok {
		if c.effectOf != nil {
			e = c.effectOf(n)
		}
		c.effects[n] = e
	}
	return e
}

// reach records that a message of type t reaches step n, and follows it on.
func (c *checker) reach(n *flowdef.Node, t Type) {
	if n == nil {
		return
	}
	if old, seen := c.in[n]; seen {
		joined := join(old, t)
		if equal(joined, old) {
			return
		}
		c.visits[n]++
		if c.visits[n] > maxVisits {
			joined = Type{}
		}
		t = joined
	} else {
		c.nodes = append(c.nodes, n)
	}
	c.in[n] = t
	if n == c.flow.Source {
		return // the source's outbound links are followed by Check
	}
	e := c.effect(n)
	if e != nil && e.Stops {
		return
	}
	c.follow(n, e, apply(e, t, n.ID))
}

// follow sends the message that comes out of step n along its links. For a
// router with link effects, out is recomputed per link.
func (c *checker) follow(n *flowdef.Node, e *Effect, out Type) {
	for i, next := range n.Next {
		t := out
		if e != nil && i < len(e.Links) && e.Links[i] != nil {
			t = apply(e.Links[i], c.in[n], n.ID)
		}
		c.reach(next, t)
	}
}

func (c *checker) report(step, severity, code, format string, args ...any) {
	c.diags = append(c.diags, Diagnostic{Flow: c.flow.ID, Step: step, Severity: severity, Code: code, Message: fmt.Sprintf(format, args...)})
}

// stepName is the name of the step of n, as its URI starts with it.
func stepName(n *flowdef.Node) string {
	name, _, _ := strings.Cut(n.URI, ":")
	return name
}

func (c *checker) checkStep(n *flowdef.Node) {
	e := c.effect(n)
	if e == nil {
		return
	}
	t := c.in[n]
	if n != c.flow.Source {
		c.checkBody(n, e.Requires, e.Tolerant, t)
		c.checkReads(n, e.Reads, t)
	}
	if e.Stops && len(n.Next) > 0 {
		var ids []string
		for _, next := range n.Next {
			ids = append(ids, next.ID)
		}
		c.report(n.ID, Warning, CodeUnreachable, "%s stops every message, so the steps after it never run: %s", stepName(n), strings.Join(ids, ", "))
	}
	if e.Otherwise && len(n.Links) == len(n.Next) && len(n.Next) > 0 {
		otherwise := false
		for _, l := range n.Links {
			otherwise = otherwise || l.Expression == ""
		}
		if !otherwise {
			c.report(n.ID, Warning, CodeNoOtherwise, "every outbound link has a condition, so a message none of them holds for stops here; add a link without a condition")
		}
	}
}

// from says which step made something known, for a message.
func from(step string) string {
	if step == "" {
		return ""
	}
	return " (from step " + step + ")"
}

// checkBody reports a body that step n cannot take.
func (c *checker) checkBody(n *flowdef.Node, need stepdef.BodyType, tolerant bool, t Type) {
	what := stepName(n)
	if need.Media != "" && t.Body.Media != "" && normalMedia(need.Media) != t.Body.Media {
		c.report(n.ID, Error, CodeContentTypeMismatch, "%s needs Content-Type %s, but Content-Type is %s here%s",
			what, normalMedia(need.Media), t.Body.Media, from(t.mediaFrom))
		return
	}
	switch severity := conflict(t.Body.Family, need.Family); {
	case severity == "":
	case severity == Error && tolerant:
		c.report(n.ID, Warning, CodeBodyMismatch, "%s expects %s body, but the body is %s here%s, so the step cannot do anything useful with it",
			what, aLabel(need.Family), label(t.Body.Family), from(t.familyFrom))
		return
	default:
		c.report(n.ID, severity, CodeBodyMismatch, "%s needs %s body, but the body is %s here%s",
			what, aLabel(need.Family), label(t.Body.Family), from(t.familyFrom))
		return
	}
	if need.Format != "" && t.Body.Format != "" && need.Format != t.Body.Format {
		c.report(n.ID, Warning, CodeFormatMismatch, "%s expects format %s, but the body has format %s here%s; they may not be compatible",
			what, need.Format, t.Body.Format, from(t.familyFrom))
	}
}

// conflict returns the severity of a body of family have at a step that needs
// family need: Error when the step always fails on it (a JSON parser on XML,
// an XML parser on JSON, either on binary data), Warning when it may. Plain
// text may be anything, and anything is text, so text never conflicts.
func conflict(have, need string) string {
	switch {
	case have == "" || need == "" || have == need || need == stepdef.FamilyText || have == stepdef.FamilyText:
		return ""
	case (need == stepdef.FamilyJSON || need == stepdef.FamilyXML) &&
		(have == stepdef.FamilyJSON || have == stepdef.FamilyXML || have == stepdef.FamilyBinary):
		return Error
	}
	return Warning
}

// aLabel is label with its article: "a JSON", "an XML".
func aLabel(family string) string {
	if family == stepdef.FamilyXML {
		return "an " + label(family)
	}
	return "a " + label(family)
}

func label(family string) string {
	switch family {
	case stepdef.FamilyJSON, stepdef.FamilyXML, stepdef.FamilyCSV:
		return strings.ToUpper(family)
	case stepdef.FamilyBinary:
		return "binary"
	}
	return family
}

// provided reports whether the engine or the message itself always provides
// header name: the identity and request headers, metadata and the error headers.
func provided(name string) bool {
	switch name = strings.ToLower(name); name {
	case "message-id", "correlation-id", "causation-id", "reply-to", "request-id", "reply-deadline", "reply-status", "reply-reason", "body":
		return true
	}
	return strings.HasPrefix(name, "metadata.") || strings.HasPrefix(name, "error.")
}

// checkReads reports the headers step n reads that no step before it sets.
func (c *checker) checkReads(n *flowdef.Node, reads [][]string, t Type) {
	if !t.Closed {
		return
	}
	for _, names := range reads {
		if len(names) == 0 {
			continue
		}
		unset := true
		for _, name := range names {
			if p, _ := t.presence(name); p != 0 || provided(name) || name == "" {
				unset = false
			}
		}
		if !unset {
			continue
		}
		name := names[0]
		hint := ""
		if s := suggest(name, t.Headers); s != "" {
			hint = fmt.Sprintf("; did you mean %q?", s)
		}
		c.report(n.ID, Warning, CodeHeaderNotSet, "%s reads header %q, which no step before it sets%s", stepName(n), name, hint)
	}
}

// suggest returns the known header closest to name, if it is a few edits away.
func suggest(name string, headers map[string]header) string {
	lower := strings.ToLower(name)
	limit := 1 + len(name)/5
	best, bestDist := "", limit+1
	for k, h := range headers {
		if d := distance(lower, k); d < bestDist || d == bestDist && h.name < best {
			best, bestDist = h.name, d
		}
	}
	if bestDist > limit {
		return ""
	}
	return best
}

// distance is the Levenshtein edit distance between a and b, in bytes, as
// engine/suggest.go has it for flow ids.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// checkHandlers reports error handlers that can never be chosen.
func (c *checker) checkHandlers() {
	var ids, schemes []string
	for _, n := range c.nodes {
		ids = append(ids, n.ID)
		schemes = append(schemes, stepName(n))
	}
	matchesNone := func(pattern string, values []string) bool {
		re, err := glob(pattern)
		if err != nil {
			return false
		}
		return !slices.ContainsFunc(values, re.MatchString)
	}
	for i, h := range c.flow.Handlers {
		if len(h.Patterns) == 0 {
			continue
		}
		if p, ok := h.Patterns["errorStep"]; ok && matchesNone(p, ids) {
			c.report(h.ID, Warning, CodeHandlerMatchesNone, "its errorStep matches no step of the flow, so it never takes a failure")
		}
		if p, ok := h.Patterns["errorUri"]; ok && matchesNone(p, schemes) {
			c.report(h.ID, Warning, CodeHandlerMatchesNone, "its errorUri matches the URI scheme of no step of the flow, so it never takes a failure")
		}
		for _, before := range c.flow.Handlers[:i] {
			if equalPatterns(before.Patterns, h.Patterns) {
				c.report(h.ID, Warning, CodeHandlerShadowed, "it has the same match options as error handler %s, which is tried first, so it is never chosen", before.ID)
				break
			}
		}
	}
}

func equalPatterns(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// glob compiles a pattern of an error handler, in which * stands for any text
// and ? for one character, as flows/impl does.
func glob(s string) (*regexp.Regexp, error) {
	q := regexp.QuoteMeta(s)
	q = strings.ReplaceAll(q, `\*`, `(?s:.*)`)
	q = strings.ReplaceAll(q, `\?`, `(?s:.)`)
	return regexp.Compile("^" + q + "$")
}

// checkReply reports the reply of a flow with a service contract that cannot
// have the contract's output format, when the reply is certain: the flow is
// one chain of steps without a router and without a step that chooses the reply.
func (c *checker) checkReply() {
	src := c.effect(c.flow.Source)
	if src == nil || src.ReplyNeeds == (stepdef.BodyType{}) {
		return
	}
	n := c.flow.Source
	for len(n.Next) == 1 {
		n = n.Next[0]
		e := c.effect(n)
		if e == nil || e.Reply || e.Stops || len(e.Links) > 0 || n.Kind == flowdef.Router {
			return
		}
	}
	if len(n.Next) != 0 {
		return
	}
	end := apply(c.effect(n), c.in[n], n.ID) // the source's in is the empty message
	need := src.ReplyNeeds
	switch {
	case need.Media != "" && end.Body.Media != "" && normalMedia(need.Media) != end.Body.Media:
		c.report(n.ID, Error, CodeReplyMismatch, "the reply of the flow ends here with Content-Type %s%s, but its service contract needs %s",
			end.Body.Media, from(end.mediaFrom), normalMedia(need.Media))
	case conflict(end.Body.Family, need.Family) != "":
		c.report(n.ID, conflict(end.Body.Family, need.Family), CodeReplyMismatch, "the reply of the flow ends here with %s body%s, but its service contract needs %s",
			aLabel(end.Body.Family), from(end.familyFrom), label(need.Family))
	}
}
