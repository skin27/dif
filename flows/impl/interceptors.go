package impl

import (
	"fmt"
	"maps"
	"regexp"
	"strings"

	flowdef "dif/flows/definition"
)

// InterceptorPrefix starts the id of every step an interceptor adds to a flow.
const InterceptorPrefix = "intercept:"

// dilInterceptor is dil.core.interceptors.interceptor: one action that runs
// beside the steps of the flows of its document that its pointcut selects, as a
// wire tap in front of each of them (Camel's intercept and interceptFrom). It
// only observes: it gets a copy of the message, and what happens to the copy
// never changes or fails the message.
type dilInterceptor struct {
	Name string `json:"name"`
	// At is "before" (the default): in front of every step the pointcut selects;
	// or "entry": in front of the first step after the source.
	At       string `json:"at,omitempty"`
	Pointcut struct {
		Flows oneOrMany[string] `json:"flows,omitempty"` // patterns on the flow's id or name
		Steps oneOrMany[string] `json:"steps,omitempty"` // patterns on the step's id
		URIs  oneOrMany[string] `json:"uris,omitempty"`  // patterns on the scheme of the step's URI
		Kinds oneOrMany[string] `json:"kinds,omitempty"` // action, router or sink
	} `json:"pointcut"`
	Action processorAction `json:"action"`
}

func (i *dilInterceptor) UnmarshalJSON(data []byte) error {
	type plain dilInterceptor
	return strictDeclaration(data, (*plain)(i), "interceptor")
}

// interceptorPlan is an interceptor with its pointcut compiled.
type interceptorPlan struct {
	decl               *dilInterceptor
	flows, steps, uris []*regexp.Regexp
	kinds              map[string]bool
	entry              bool
}

// interceptorPlans checks every interceptor of the document, used or not, and
// compiles its pointcut.
func (d *Document) interceptorPlans() ([]interceptorPlan, error) {
	var plans []interceptorPlan
	names := map[string]bool{}
	for k := range d.doc.DIL.Core.Interceptors.Interceptor {
		decl := &d.doc.DIL.Core.Interceptors.Interceptor[k]
		fail := func(format string, args ...any) ([]interceptorPlan, error) {
			return nil, fmt.Errorf("interceptor %q: %s", decl.Name, fmt.Sprintf(format, args...))
		}
		if decl.Name == "" || strings.Contains(decl.Name, ":") {
			return nil, fmt.Errorf("interceptor requires a name without a colon")
		}
		if names[decl.Name] {
			return fail("declared twice")
		}
		names[decl.Name] = true
		p := interceptorPlan{decl: decl, kinds: map[string]bool{}}
		switch decl.At {
		case "", "before":
		case "entry":
			p.entry = true
			if len(decl.Pointcut.Steps)+len(decl.Pointcut.URIs)+len(decl.Pointcut.Kinds) > 0 {
				return fail("at entry selects no steps, only flows")
			}
		default:
			return fail("at must be before or entry")
		}
		var err error
		if p.flows, err = compileGlobs(decl.Pointcut.Flows); err != nil {
			return fail("pointcut flows: %v", err)
		}
		if p.steps, err = compileGlobs(decl.Pointcut.Steps); err != nil {
			return fail("pointcut steps: %v", err)
		}
		if p.uris, err = compileGlobs(decl.Pointcut.URIs); err != nil {
			return fail("pointcut uris: %v", err)
		}
		for _, kind := range decl.Pointcut.Kinds {
			if kind != flowdef.Action && kind != flowdef.Router && kind != flowdef.Sink {
				return fail("pointcut kinds are action, router or sink")
			}
			p.kinds[kind] = true
		}
		if decl.Action.URI == "" {
			return fail("requires an action with a URI")
		}
		scheme, _, _ := strings.Cut(decl.Action.URI, ":")
		if scheme == "interceptor" {
			return fail("the action cannot be an interceptor")
		}
		plans = append(plans, p)
	}
	return plans, nil
}

// prepareInterceptors reports an invalid interceptor declaration.
func (d *Document) prepareInterceptors() error {
	_, err := d.interceptorPlans()
	return err
}

func compileGlobs(patterns []string) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, pattern := range patterns {
		re, err := compileGlob(pattern)
		if err != nil {
			return nil, err
		}
		out = append(out, re)
	}
	return out, nil
}

// matchesAny reports whether s matches one of the patterns; no pattern matches everything.
func matchesAny(patterns []*regexp.Regexp, s string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, re := range patterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// weaveInterceptors adds the steps of the document's interceptors to the flow:
// in front of each step an interceptor selects, a wiretap router takes over the
// step's inbound link, passes the message on to the step and sends a copy to the
// interceptor's action. Only the steps reachable from the source are selected,
// not those of the error routes and not those an interceptor added. Several
// interceptors on a step run in the order they are declared.
func (d *Document) weaveInterceptors(df dilFlow) (dilFlow, error) {
	plans, err := d.interceptorPlans()
	if err != nil || len(plans) == 0 {
		return df, err
	}
	steps := append(oneOrMany[dilStep](nil), df.Steps.Step...)
	original := len(steps)

	// The steps that can be reached from the source, and the one after it.
	byIn := map[string]int{}
	used := map[string]bool{}
	source := -1
	for i, s := range steps {
		used[s.ID] = true
		if s.Type == flowdef.Source && source < 0 {
			source = i
		}
		for _, l := range s.Links.Link {
			used[l.ID] = true
			if l.Bound == "in" {
				byIn[l.ID] = i
			}
		}
	}
	if source < 0 {
		return df, nil // the builder reports a flow without a source
	}
	reachable := map[int]bool{}
	first := -1
	for todo := []int{source}; len(todo) > 0; {
		i := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		for _, l := range steps[i].Links.Link {
			if next, ok := byIn[l.ID]; ok && l.Bound == "out" && !reachable[next] {
				reachable[next] = true
				if i == source {
					first = next
				}
				todo = append(todo, next)
			}
		}
	}

	for _, p := range plans {
		if !matchesAny(p.flows, df.ID) && !matchesAny(p.flows, df.Name) {
			continue
		}
		for i := 0; i < original; i++ {
			s := steps[i]
			if !reachable[i] || s.Type == flowdef.Source || s.Type == "error" {
				continue
			}
			if p.entry {
				if i != first {
					continue
				}
			} else {
				scheme, _, _ := strings.Cut(knownURI(s), ":")
				if (len(p.kinds) > 0 && !p.kinds[s.Type]) || !matchesAny(p.steps, s.ID) || !matchesAny(p.uris, scheme) {
					continue
				}
			}
			inbound := -1
			for k, l := range s.Links.Link {
				if l.Bound == "in" {
					if inbound >= 0 {
						inbound = -2 // more than one: the builder reports it
						break
					}
					inbound = k
				}
			}
			if inbound < 0 {
				continue
			}

			base := InterceptorPrefix + p.decl.Name + ":" + s.ID
			ids := []string{base, base + ":action", base + ":main", base + ":tap"}
			for _, id := range ids {
				if used[id] {
					return df, fmt.Errorf("interceptor %q: id %q is already used", p.decl.Name, id)
				}
				used[id] = true
			}
			links := append(oneOrMany[dilLink](nil), s.Links.Link...)
			entering := links[inbound]
			links[inbound].ID = ids[2]
			steps[i].Links.Link = links

			router := dilStep{ID: base, Type: flowdef.Router, URI: "wiretap"}
			router.Links.Link = oneOrMany[dilLink]{{ID: entering.ID, Bound: "in"}, {ID: ids[2], Bound: "out"}, {ID: ids[3], Bound: "out", Rule: "wiretap"}}
			action := dilStep{ID: ids[1], Type: flowdef.Sink, URI: p.decl.Action.URI, Options: maps.Clone(p.decl.Action.Options)}
			action.Links.Link = oneOrMany[dilLink]{{ID: ids[3], Bound: "in"}}
			steps = append(steps, router, action)
		}
	}
	df.Steps.Step = steps
	return df, nil
}
