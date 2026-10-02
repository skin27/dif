package impl

import (
	"context"
	"fmt"

	"dif/message"
	stepdef "dif/steps/definition"
)

// contentRouter sends the message along the first outbound link, in link
// order, whose condition holds. The link without a condition is the
// otherwise route, taken when none holds; without one the message stops.
type contentRouter struct {
	when      []when
	otherwise int // -1 if there is none
}

type when struct {
	next int
	cond predicate
}

func newContentRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	r := contentRouter{otherwise: -1}
	for i, l := range p[stepdef.Links].([]stepdef.Link) {
		if l.Expression == "" {
			if r.otherwise >= 0 {
				return nil, fmt.Errorf("outbound links %d and %d both have no condition; only the otherwise link may lack one", r.otherwise, i)
			}
			r.otherwise = i
			continue
		}
		lang := l.Language
		if lang == "" {
			lang = "simple"
		}
		cond, err := compilePredicate(lang, l.Expression)
		if err != nil {
			return nil, fmt.Errorf("outbound link %d (rule %s): %w", i, l.Rule, err)
		}
		r.when = append(r.when, when{i, cond})
	}
	return r, nil
}

func (r contentRouter) Route(_ context.Context, m message.Message) ([]stepdef.Route, error) {
	for _, w := range r.when {
		if w.cond(m) {
			return []stepdef.Route{{Next: w.next, Message: m}}, nil
		}
	}
	if r.otherwise >= 0 {
		return []stepdef.Route{{Next: r.otherwise, Message: m}}, nil
	}
	return nil, nil
}
