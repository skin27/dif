package impl

import (
	"context"
	"fmt"

	"dif/message"
	stepdef "dif/steps/definition"
)

// filterRouter passes the message on when its condition holds and stops it
// otherwise. In DIL it is an action: a router with one outbound link.
type filterRouter struct {
	cond predicate
}

func newFilterRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	if n := len(p[stepdef.Links].([]stepdef.Link)); n != 1 {
		return nil, fmt.Errorf("needs one outbound link, has %d", n)
	}
	cond, err := compilePredicateIn(flowOf(p), p["language"].(string), p["expression"].(string))
	if err != nil {
		return nil, fmt.Errorf("option expression: %w", err)
	}
	return filterRouter{cond}, nil
}

func (r filterRouter) Route(_ context.Context, m message.Message) ([]stepdef.Route, error) {
	ok, err := r.cond(m)
	if err != nil {
		return nil, err
	}
	if ok {
		return []stepdef.Route{{Next: 0, Message: m}}, nil
	}
	return nil, nil
}
