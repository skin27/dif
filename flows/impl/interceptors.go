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

// dilInterceptor is dil.core.interceptors.interceptor: cross-cutting advice
// woven around the steps of the flows its pointcut selects.
//
//   - at: "before" (default), "entry" (first step after the source), "after"
//     (after an action or sink succeeds, with metadata.step.duration and
//     metadata.step.target), or "onError" (when a matched step fails, before
//     flow-level error handling).
//   - mode: "observe" (default, detached copy whose failure is logged and never
//     mutates or fails the message), "transform" (inline mutation), or "guard"
//     (inline gate before/entry with condition and/or action).
//   - scope: "document" (default, flows of this document only) or "program"
//     (flows across all documents of a resolved program).
type dilInterceptor struct {
	Name     string `json:"name"`
	At       string `json:"at,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Pointcut struct {
		Flows oneOrMany[string] `json:"flows,omitempty"` // patterns on the flow's id or name
		Steps oneOrMany[string] `json:"steps,omitempty"` // patterns on the step's id
		URIs  oneOrMany[string] `json:"uris,omitempty"`  // patterns on the scheme of the step's URI
		Kinds oneOrMany[string] `json:"kinds,omitempty"` // action, router or sink
	} `json:"pointcut"`
	Condition *dilInterceptorCondition `json:"condition,omitempty"`
	OnDeny    *dilInterceptorDeny      `json:"onDeny,omitempty"`
	Action    *processorAction         `json:"action,omitempty"`
}

type dilInterceptorCondition struct {
	Language   string `json:"language,omitempty"` // simple (default), xpath or jsonpath
	Expression string `json:"expression"`
}

type dilInterceptorDeny struct {
	Message string `json:"message,omitempty"`
}

func (i *dilInterceptor) UnmarshalJSON(data []byte) error {
	type plain dilInterceptor
	return strictDeclaration(data, (*plain)(i), "interceptor")
}

func (c *dilInterceptorCondition) UnmarshalJSON(data []byte) error {
	type plain dilInterceptorCondition
	return strictDeclaration(data, (*plain)(c), "interceptor condition")
}

func (d *dilInterceptorDeny) UnmarshalJSON(data []byte) error {
	type plain dilInterceptorDeny
	return strictDeclaration(data, (*plain)(d), "interceptor onDeny")
}

// ProgramInterceptor is a validated scope:"program" interceptor bound to its
// defining document, passed to other documents in the same program without
// mutating them.
type ProgramInterceptor struct {
	plan interceptorPlan
}

// Name returns the interceptor's name.
func (p ProgramInterceptor) Name() string { return p.plan.decl.Name }

// interceptorPlan is an interceptor with its pointcut compiled.
type interceptorPlan struct {
	owner              *Document
	decl               *dilInterceptor
	flows, steps, uris []*regexp.Regexp
	kinds              map[string]bool
	at                 string // "before", "entry", "after", "onError"
	mode               string // "observe", "transform", "guard"
	scope              string // "document", "program"
	condLang           string // normalized language when decl.Condition != nil
}

// ProgramInterceptors returns the document's validated scope:"program" interceptors.
func (d *Document) ProgramInterceptors() ([]ProgramInterceptor, error) {
	plans, err := d.interceptorPlans()
	if err != nil {
		return nil, err
	}
	var out []ProgramInterceptor
	for _, p := range plans {
		if p.scope == "program" {
			out = append(out, ProgramInterceptor{plan: p})
		}
	}
	return out, nil
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

		p := interceptorPlan{owner: d, decl: decl, kinds: map[string]bool{}}
		switch decl.At {
		case "", "before":
			p.at = "before"
		case "entry":
			p.at = "entry"
			if len(decl.Pointcut.Steps)+len(decl.Pointcut.URIs)+len(decl.Pointcut.Kinds) > 0 {
				return fail("at entry selects no steps, only flows")
			}
		case "after":
			p.at = "after"
		case "onError":
			p.at = "onError"
		default:
			return fail("at must be before, entry, after or onError")
		}

		switch decl.Mode {
		case "", "observe":
			p.mode = "observe"
		case "transform":
			p.mode = "transform"
		case "guard":
			p.mode = "guard"
		default:
			return fail("mode must be observe, transform or guard")
		}

		if p.at == "onError" && p.mode != "observe" {
			return fail("at onError only supports observe mode")
		}
		if p.at == "after" && p.mode == "guard" {
			return fail("guard mode only supports at before or entry")
		}

		switch decl.Scope {
		case "", "document":
			p.scope = "document"
		case "program":
			p.scope = "program"
		default:
			return fail("scope must be document or program")
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
			if p.at == "after" && kind == flowdef.Router {
				return fail("at after cannot select router steps")
			}
			p.kinds[kind] = true
		}
		if p.at == "after" && len(p.kinds) == 0 {
			p.kinds[flowdef.Action] = true
			p.kinds[flowdef.Sink] = true
		}

		if p.mode != "guard" {
			if decl.Condition != nil {
				return fail("condition is only supported in guard mode")
			}
			if decl.OnDeny != nil {
				return fail("onDeny is only supported in guard mode")
			}
			if decl.Action == nil || decl.Action.URI == "" {
				return fail("requires an action with a URI")
			}
		} else {
			if decl.Condition == nil && decl.Action == nil {
				return fail("guard mode requires a condition or an action")
			}
			if decl.Action != nil && decl.Action.URI == "" {
				return fail("requires an action with a URI")
			}
			if decl.Condition != nil {
				lang := decl.Condition.Language
				if lang == "" {
					lang = "simple"
				}
				if lang != "simple" && lang != "xpath" && lang != "jsonpath" {
					return fail("condition language must be simple, xpath or jsonpath")
				}
				if strings.TrimSpace(decl.Condition.Expression) == "" {
					return fail("condition requires an expression")
				}
				p.condLang = lang
			}
		}

		if decl.Action != nil {
			scheme, _, _ := strings.Cut(decl.Action.URI, ":")
			if scheme == "interceptor" {
				return fail("the action cannot be an interceptor")
			}
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

// weaveInterceptors weaves applicable interceptors (program-scoped interceptors
// from other documents in extra, plus this document's own interceptors) into df.
// Only steps reachable from the source are selected, never error-route steps
// and never steps added by another interceptor. Returns the woven flow and a map
// from woven step ID to the defining Document of the interceptor that added it.
func (d *Document) weaveInterceptors(df dilFlow, extra []ProgramInterceptor) (dilFlow, map[string]*Document, error) {
	local, err := d.interceptorPlans()
	if err != nil {
		return df, nil, err
	}
	var plans []interceptorPlan
	for _, ep := range extra {
		if ep.plan.owner == d {
			continue // already in local in declaration order; never double-weave
		}
		plans = append(plans, ep.plan)
	}
	plans = append(plans, local...)
	if len(plans) == 0 {
		return df, nil, nil
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
		for _, id := range s.AfterLinks {
			used[id] = true
		}
		for _, id := range s.ErrorLinks {
			used[id] = true
		}
	}
	if source < 0 {
		return df, nil, nil // the builder reports a flow without a source
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

	// afterTail tracks the tail of the after-interceptor chain for each original
	// step so multiple after interceptors (observe and transform) run in order.
	afterTail := make([]int, original)
	for i := range afterTail {
		afterTail[i] = i
	}
	owners := map[string]*Document{}

	reserve := func(name string, ids ...string) error {
		for _, id := range ids {
			if used[id] {
				return fmt.Errorf("interceptor %q: id %q is already used", name, id)
			}
			used[id] = true
		}
		return nil
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
			if p.at == "entry" {
				if i != first {
					continue
				}
			} else {
				scheme, _, _ := strings.Cut(knownURI(s), ":")
				if (len(p.kinds) > 0 && !p.kinds[s.Type]) || !matchesAny(p.steps, s.ID) || !matchesAny(p.uris, scheme) {
					continue
				}
			}

			base := InterceptorPrefix + p.decl.Name + ":" + s.ID

			switch p.at {
			case "before", "entry":
				inbound := findSingleLink(steps[i].Links.Link, "in")
				if inbound < 0 {
					continue
				}
				switch p.mode {
				case "observe":
					ids := []string{base, base + ":action", base + ":main", base + ":tap"}
					if err := reserve(p.decl.Name, ids...); err != nil {
						return df, nil, err
					}
					links := append(oneOrMany[dilLink](nil), steps[i].Links.Link...)
					entering := links[inbound]
					links[inbound].ID = ids[2]
					steps[i].Links.Link = links

					router := dilStep{ID: base, Type: flowdef.Router, URI: "wiretap"}
					router.Links.Link = oneOrMany[dilLink]{
						{ID: entering.ID, Bound: "in"},
						{ID: ids[2], Bound: "out"},
						{ID: ids[3], Bound: "out", Rule: "wiretap"},
					}
					action := dilStep{ID: ids[1], Type: flowdef.Sink, URI: p.decl.Action.URI, Options: maps.Clone(p.decl.Action.Options)}
					action.Links.Link = oneOrMany[dilLink]{{ID: ids[3], Bound: "in"}}
					steps = append(steps, router, action)
					owners[ids[1]] = p.owner

				case "transform":
					nextLink := base + ":next"
					if err := reserve(p.decl.Name, base, nextLink); err != nil {
						return df, nil, err
					}
					links := append(oneOrMany[dilLink](nil), steps[i].Links.Link...)
					entering := links[inbound]
					links[inbound].ID = nextLink
					steps[i].Links.Link = links

					action := dilStep{ID: base, Type: flowdef.Action, URI: p.decl.Action.URI, Options: maps.Clone(p.decl.Action.Options)}
					action.Links.Link = oneOrMany[dilLink]{
						{ID: entering.ID, Bound: "in"},
						{ID: nextLink, Bound: "out"},
					}
					steps = append(steps, action)
					owners[base] = p.owner

				case "guard":
					switch {
					case p.decl.Condition != nil && p.decl.Action != nil:
						actionID, checkLink, nextLink := base+":action", base+":check", base+":next"
						if err := reserve(p.decl.Name, base, actionID, checkLink, nextLink); err != nil {
							return df, nil, err
						}
						links := append(oneOrMany[dilLink](nil), steps[i].Links.Link...)
						entering := links[inbound]
						links[inbound].ID = nextLink
						steps[i].Links.Link = links

						guard := guardDilStep(base, p, s.ID, entering.ID, checkLink)
						action := dilStep{ID: actionID, Type: flowdef.Action, URI: p.decl.Action.URI, Options: maps.Clone(p.decl.Action.Options)}
						action.Links.Link = oneOrMany[dilLink]{
							{ID: checkLink, Bound: "in"},
							{ID: nextLink, Bound: "out"},
						}
						steps = append(steps, guard, action)
						owners[base] = p.owner
						owners[actionID] = p.owner

					case p.decl.Condition != nil:
						nextLink := base + ":next"
						if err := reserve(p.decl.Name, base, nextLink); err != nil {
							return df, nil, err
						}
						links := append(oneOrMany[dilLink](nil), steps[i].Links.Link...)
						entering := links[inbound]
						links[inbound].ID = nextLink
						steps[i].Links.Link = links

						guard := guardDilStep(base, p, s.ID, entering.ID, nextLink)
						steps = append(steps, guard)
						owners[base] = p.owner

					default: // action only
						nextLink := base + ":next"
						if err := reserve(p.decl.Name, base, nextLink); err != nil {
							return df, nil, err
						}
						links := append(oneOrMany[dilLink](nil), steps[i].Links.Link...)
						entering := links[inbound]
						links[inbound].ID = nextLink
						steps[i].Links.Link = links

						action := dilStep{ID: base, Type: flowdef.Action, URI: p.decl.Action.URI, Options: maps.Clone(p.decl.Action.Options)}
						action.Links.Link = oneOrMany[dilLink]{
							{ID: entering.ID, Bound: "in"},
							{ID: nextLink, Bound: "out"},
						}
						steps = append(steps, action)
						owners[base] = p.owner
					}
				}

			case "after":
				steps[i].Measure = true
				tailIdx := afterTail[i]

				switch p.mode {
				case "observe":
					linkID := base + ":after"
					if err := reserve(p.decl.Name, base, linkID); err != nil {
						return df, nil, err
					}
					obs := dilStep{ID: base, Type: flowdef.Sink, URI: p.decl.Action.URI, Options: maps.Clone(p.decl.Action.Options)}
					obs.Links.Link = oneOrMany[dilLink]{{ID: linkID, Bound: "in"}}
					steps[tailIdx].AfterLinks = append(append([]string(nil), steps[tailIdx].AfterLinks...), linkID)
					steps = append(steps, obs)
					owners[base] = p.owner

				case "transform":
					outbound := findSingleLink(steps[tailIdx].Links.Link, "out")
					if outbound == -2 {
						continue // multiple outbound links: the builder reports it
					}
					inLink := base + ":in"
					if err := reserve(p.decl.Name, base, inLink); err != nil {
						return df, nil, err
					}
					if outbound >= 0 {
						links := append(oneOrMany[dilLink](nil), steps[tailIdx].Links.Link...)
						exiting := links[outbound]
						links[outbound].ID = inLink
						steps[tailIdx].Links.Link = links

						tr := dilStep{ID: base, Type: flowdef.Action, URI: p.decl.Action.URI, Options: maps.Clone(p.decl.Action.Options)}
						tr.Links.Link = oneOrMany[dilLink]{
							{ID: inLink, Bound: "in"},
							{ID: exiting.ID, Bound: "out"},
						}
						steps = append(steps, tr)
					} else {
						// Tail is currently a terminal sink (either the matched sink or a prior woven after-transform sink).
						links := append(oneOrMany[dilLink](nil), steps[tailIdx].Links.Link...)
						links = append(links, dilLink{ID: inLink, Bound: "out"})
						steps[tailIdx].Links.Link = links
						if tailIdx == i {
							steps[tailIdx].WovenOut = true
						} else {
							steps[tailIdx].Type = flowdef.Action
						}

						tr := dilStep{ID: base, Type: flowdef.Sink, URI: p.decl.Action.URI, Options: maps.Clone(p.decl.Action.Options)}
						tr.Links.Link = oneOrMany[dilLink]{{ID: inLink, Bound: "in"}}
						steps = append(steps, tr)
					}
					afterTail[i] = len(steps) - 1
					owners[base] = p.owner
				}

			case "onError":
				linkID := base + ":error"
				if err := reserve(p.decl.Name, base, linkID); err != nil {
					return df, nil, err
				}
				obs := dilStep{ID: base, Type: flowdef.Sink, URI: p.decl.Action.URI, Options: maps.Clone(p.decl.Action.Options)}
				obs.Links.Link = oneOrMany[dilLink]{{ID: linkID, Bound: "in"}}
				steps[i].ErrorLinks = append(append([]string(nil), steps[i].ErrorLinks...), linkID)
				steps = append(steps, obs)
				owners[base] = p.owner
			}
		}
	}
	df.Steps.Step = steps
	return df, owners, nil
}

func guardDilStep(id string, p interceptorPlan, targetStep, inLink, outLink string) dilStep {
	opts := map[string]any{
		"language":    p.condLang,
		"expression":  p.decl.Condition.Expression,
		"interceptor": p.decl.Name,
		"targetStep":  targetStep,
	}
	if p.decl.OnDeny != nil && p.decl.OnDeny.Message != "" {
		opts["message"] = p.decl.OnDeny.Message
	}
	step := dilStep{ID: id, Type: flowdef.Action, URI: "guard", Options: opts}
	step.Links.Link = oneOrMany[dilLink]{
		{ID: inLink, Bound: "in"},
		{ID: outLink, Bound: "out"},
	}
	return step
}

// findSingleLink returns the index of the sole link with the given bound,
// -1 if there is none, or -2 if there are more than one.
func findSingleLink(links []dilLink, bound string) int {
	found := -1
	for k, l := range links {
		if l.Bound == bound {
			if found >= 0 {
				return -2
			}
			found = k
		}
	}
	return found
}
