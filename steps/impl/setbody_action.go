package impl

import (
	"context"

	"dif/message"
	stepdef "dif/steps/definition"
)

// setBodyAction replaces the body with the value of an expression.
type setBodyAction struct {
	expr expression
}

func newSetBodyAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	expr, err := compileExpressionIn(flowOf(p), p["language"].(string), p["expression"].(string))
	if err != nil {
		return nil, err
	}
	return setBodyAction{expr}, nil
}

func (a setBodyAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	v, err := a.expr.eval(m)
	if err != nil {
		return nil, err
	}
	m[message.Body] = v
	return m, nil
}
