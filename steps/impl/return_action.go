package impl

import (
	"context"
	"errors"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// returnAction chooses the reply of the exchange it is in: the message as it is,
// or with the body an expression gives. The message itself passes on unchanged.
// With detach the sender gets the reply now and the flow goes on without it;
// otherwise the reply is fixed here and the sender gets it when the flow has
// processed the message (a failure after the step replaces it). With status
// error the reply is a failure, whose text is the reply's body. Nothing happens
// when nobody waits for a reply, or the sender already has one.
type returnAction struct {
	expr   *expression // nil: the message as it is
	failed bool
	detach bool
}

func newReturnAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := returnAction{failed: p["status"] == "error", detach: p["detach"].(bool)}
	if src := p["expression"].(string); strings.TrimSpace(src) != "" {
		e, err := compileExpressionIn(flowOf(p), p["language"].(string), src)
		if err != nil {
			return nil, err
		}
		a.expr = &e
	}
	return a, nil
}

func (a returnAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	reply := m.Copy()
	if a.expr != nil {
		v, err := a.expr.eval(m, ctx)
		if err != nil {
			return nil, err
		}
		reply[message.Body] = v
	}
	var failure error
	if a.failed {
		reason := text(reply[message.Body])
		if reason == "" {
			reason = "returned an error"
		}
		failure = errors.New(reason)
	}
	stepdef.Return(ctx, reply, failure, a.detach)
	return m, nil
}
