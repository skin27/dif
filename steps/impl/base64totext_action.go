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
	decoded, err := decodeBase64Body(m)
	if err != nil {
		return nil, err
	}
	m[message.Body] = string(decoded)
	return m, nil
}

// base64ToBinaryAction decodes a base64 body like base64totext, but the body
// becomes the bytes, not text, so that a binary file such as a PDF stays exact
// for the steps that send it on.
type base64ToBinaryAction struct{}

func newBase64ToBinaryAction(string, stepdef.Params) (stepdef.Processor, error) {
	return base64ToBinaryAction{}, nil
}

func (base64ToBinaryAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	decoded, err := decodeBase64Body(m)
	if err != nil {
		return nil, err
	}
	m[message.Body] = decoded
	return m, nil
}

// decodeBase64Body returns the bytes the base64 body of m stands for.
func decodeBase64Body(m message.Message) ([]byte, error) {
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
	return decoded, nil
}
