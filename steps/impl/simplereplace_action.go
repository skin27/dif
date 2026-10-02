package impl

import (
	"context"
	"fmt"

	"dif/message"
	stepdef "dif/steps/definition"
)

// simpleReplaceAction evaluates the body as a simple expression, so the
// ${body} and ${header.<name>} references in it are replaced by their values.
type simpleReplaceAction struct{}

func newSimpleReplaceAction(string, stepdef.Params) (stepdef.Processor, error) {
	return simpleReplaceAction{}, nil
}

func (simpleReplaceAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	expr, err := compileExpression("simple", text(m[message.Body]))
	if err != nil {
		return nil, fmt.Errorf("body: %w", err)
	}
	m[message.Body] = expr.eval(m)
	return m, nil
}
