package impl

import (
	"context"
	"encoding/base64"

	"dif/message"
	stepdef "dif/steps/definition"
)

// textToBase64Action encodes the body as standard base64, without line breaks.
type textToBase64Action struct{}

func newTextToBase64Action(string, stepdef.Params) (stepdef.Processor, error) {
	return textToBase64Action{}, nil
}

func (textToBase64Action) Process(_ context.Context, m message.Message) (message.Message, error) {
	m[message.Body] = base64.StdEncoding.EncodeToString(bytesOf(m[message.Body]))
	return m, nil
}
