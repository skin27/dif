package impl

import (
	"context"

	"dif/message"
	stepdef "dif/steps/definition"
)

// exchangePatternAction sets the exchange pattern of the message in its flow.
// setoneway makes the exchange one-way (InOnly): a sender that waits for a
// reply gets the message as it is now, and the flow goes on without it.
// setrequestreply sets the default, InOut: the sender gets the message the
// flow ends with. It cannot take back a reply setoneway already sent.
type exchangePatternAction struct{ pattern string }

func newSetOneWayAction(string, stepdef.Params) (stepdef.Processor, error) {
	return exchangePatternAction{message.InOnly}, nil
}

func newSetRequestReplyAction(string, stepdef.Params) (stepdef.Processor, error) {
	return exchangePatternAction{message.InOut}, nil
}

func (a exchangePatternAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	m[message.ExchangePattern] = a.pattern
	return m, nil
}
