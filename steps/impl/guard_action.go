package impl

import (
	"context"
	"fmt"
	"strings"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// guardAction evaluates a condition in simple, xpath or jsonpath. When it
// holds, the message passes on unchanged; otherwise it fails with *flowdef.GuardError.
type guardAction struct {
	cond        predicate
	interceptor string
	targetStep  string
	reason      string
}

func newGuardAction(id string, p stepdef.Params) (stepdef.Processor, error) {
	lang := p["language"].(string)
	expr := strings.TrimSpace(p["expression"].(string))
	if expr == "" {
		return nil, fmt.Errorf("option expression: empty condition")
	}
	cond, err := compilePredicateIn(flowOf(p), lang, expr)
	if err != nil {
		return nil, fmt.Errorf("option expression: %w", err)
	}
	target := optString(p, "targetStep")
	if target == "" {
		target = id
	}
	return guardAction{
		cond:        cond,
		interceptor: optString(p, "interceptor"),
		targetStep:  target,
		reason:      optString(p, "message"),
	}, nil
}

func (g guardAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	ok, err := g.cond(m, ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &flowdef.GuardError{
			Interceptor: g.interceptor,
			Step:        g.targetStep,
			Reason:      g.reason,
		}
	}
	return m, nil
}
