package impl

import (
	"context"

	"dif/message"
	stepdef "dif/steps/definition"
)

// setBodyAsStringAction makes the body a string, as Camel's convertBodyTo
// String does: bytes become their text. A body a step decoded (a JSON object or
// array) becomes its JSON, and a missing one the empty string.
type setBodyAsStringAction struct{}

func newSetBodyAsStringAction(string, stepdef.Params) (stepdef.Processor, error) {
	return setBodyAsStringAction{}, nil
}

func (setBodyAsStringAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	m[message.Body] = text(m[message.Body])
	return m, nil
}
