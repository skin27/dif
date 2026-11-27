package impl

import (
	"context"
	"fmt"

	"dif/message"
	stepdef "dif/steps/definition"
)

// simpleReplaceAction evaluates the body as a simple expression, so the
// ${body} and ${header.<name>} references in it are replaced by their values.
type simpleReplaceAction struct{ flow *flowProperties }

func newSimpleReplaceAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return simpleReplaceAction{flowOf(p)}, nil
}

func (a simpleReplaceAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	expr, err := (&compiler{flow: a.flow}).compile(text(m[message.Body]))
	if err != nil {
		return nil, fmt.Errorf("body: %w", err)
	}
	v, err := expr.eval(m, ctx)
	if err != nil {
		return nil, fmt.Errorf("body: %w", err)
	}
	m[message.Body] = v
	return m, nil
}
