package definition

import (
	"context"

	"dif/message"
)

type returnKey struct{}

// ReturnFunc is how the engine takes the reply a return step gives. It takes
// ownership of m and reports whether the reply was taken.
type ReturnFunc func(m message.Message, err error, detach bool) bool

// WithReturn returns ctx carrying fn, for the return step to reply with. The
// engine attaches it only to an exchange whose sender waits for a reply.
func WithReturn(ctx context.Context, fn ReturnFunc) context.Context {
	return context.WithValue(ctx, returnKey{}, fn)
}

// Return gives the sender of the exchange m (or err, when it is not nil) as its
// reply. With detach the sender gets it now and the flow goes on without it;
// otherwise the reply is fixed now and the sender gets it when the flow has
// processed the message, unless that fails. The first reply wins. Return takes
// ownership of m, and reports false when nobody waits for a reply or the
// sender already has, or is going to have, one.
func Return(ctx context.Context, m message.Message, err error, detach bool) bool {
	fn, ok := ctx.Value(returnKey{}).(ReturnFunc)
	return ok && fn(m, err, detach)
}
