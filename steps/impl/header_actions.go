package impl

import (
	"context"
	"crypto/rand"
	"fmt"

	"dif/message"
	stepdef "dif/steps/definition"
)

// setUUIDAction sets a header to a new random UUID (version 4), such as
// 3f2b8c1e-9a4d-4e7f-b1c2-5d6e7f8a9b0c.
type setUUIDAction struct{ name string }

func newSetUUIDAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	name := p["headerName"].(string)
	if err := checkHeaderName(name); err != nil {
		return nil, err
	}
	return setUUIDAction{name}, nil
}

func (a setUUIDAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	m[a.name] = newUUID()
	return m, nil
}

// newUUID returns a random UUID (RFC 9562 version 4).
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])         // never returns an error (Go 1.24+)
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// setBodyByHeaderAction replaces the body with the value of a header (nothing
// if it is not set), as it is: JSON stays JSON.
type setBodyByHeaderAction struct{ name string }

func newSetBodyByHeaderAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	name := p["headerName"].(string)
	if name == "" {
		return nil, fmt.Errorf("header name is empty")
	}
	return setBodyByHeaderAction{name}, nil
}

func (a setBodyByHeaderAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	m[message.Body] = m[a.name]
	return m, nil
}

// setHeaderByBodyAction sets a header to the body, as it is.
type setHeaderByBodyAction struct{ name string }

func newSetHeaderByBodyAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	name := p["headerName"].(string)
	if err := checkHeaderName(name); err != nil {
		return nil, err
	}
	return setHeaderByBodyAction{name}, nil
}

func (a setHeaderByBodyAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	m[a.name] = m[message.Body]
	return m, nil
}
