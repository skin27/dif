package impl

import (
	"context"
	"fmt"

	"dif/message"
	stepdef "dif/steps/definition"
)

// setHeaderAction sets one header to the value of an expression.
type setHeaderAction struct {
	name  string
	value expression
}

func newSetHeaderAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	name := p["name"].(string)
	if err := checkHeaderName(name); err != nil {
		return nil, err
	}
	value, err := compileExpression(p["language"].(string), p["value"].(string))
	if err != nil {
		return nil, err
	}
	return setHeaderAction{name, value}, nil
}

// checkHeaderName rejects names a header step may not set: the body and metadata.
func checkHeaderName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("header name is empty")
	case name == message.Body:
		return fmt.Errorf("header name %q is reserved for the body; use setbody", name)
	case message.IsMetadata(name):
		return fmt.Errorf("header name %q is reserved for metadata", name)
	}
	return nil
}

func (a setHeaderAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	m[a.name] = a.value.eval(m)
	return m, nil
}
