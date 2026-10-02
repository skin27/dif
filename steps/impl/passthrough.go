package impl

import (
	"context"

	"dif/message"
	stepdef "dif/steps/definition"
)

// passthrough returns the message unchanged.
type passthrough struct{}

func newPassthrough(string, stepdef.Params) (stepdef.Processor, error) { return passthrough{}, nil }

func (passthrough) Process(_ context.Context, m message.Message) (message.Message, error) {
	return m, nil
}

// messageSource produces nothing by itself: its flow receives messages only
// when they are sent to it, such as the configured message by the CLI's send.
type messageSource struct{}

func newMessageSource(string, stepdef.Params) (stepdef.Processor, error) { return messageSource{}, nil }

func (messageSource) Run(context.Context, stepdef.Emit) error { return nil }
