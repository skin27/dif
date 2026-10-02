package impl

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode"

	"dif/message"
	stepdef "dif/steps/definition"
)

// base64ToTextAction decodes a base64 body to text. Whitespace, such as the
// line breaks of MIME-style base64, is ignored; padding is optional.
type base64ToTextAction struct{}

func newBase64ToTextAction(string, stepdef.Params) (stepdef.Processor, error) {
	return base64ToTextAction{}, nil
}

func (base64ToTextAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	encoded := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, string(bytesOf(m[message.Body])))

	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		if decoded, err = base64.RawStdEncoding.DecodeString(encoded); err != nil {
			return nil, fmt.Errorf("body is not valid base64: %w", err)
		}
	}
	m[message.Body] = string(decoded)
	return m, nil
}
