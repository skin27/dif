// Package definition defines the contracts every step implements.
package definition

import (
	"context"

	"dif/message"
)

// Step receives a Message and returns a Message.
type Step interface {
	Execute(*message.Message) (*message.Message, error)
}

// Source produces messages until it is done or ctx is cancelled.
// emit runs one message through the flow; it returns an error only when the
// flow is stopping, never because a step failed.
type Source interface {
	Run(ctx context.Context, emit func(*message.Message) error) error
}
