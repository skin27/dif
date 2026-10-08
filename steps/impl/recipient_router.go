package impl

import (
	"context"

	"dif/message"
	stepdef "dif/steps/definition"
)

// recipientRouter sends the message to every outbound link in order, each
// a copy (a static recipient list). The outcome is the last recipient's.
type recipientRouter struct {
	n int // number of recipients
}

func newRecipientRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return recipientRouter{len(p[stepdef.Links].([]stepdef.Link))}, nil
}

func (r recipientRouter) Route(_ context.Context, m message.Message) ([]stepdef.Route, error) {
	routes := make([]stepdef.Route, r.n)
	for i := range routes {
		routes[i] = stepdef.Route{Next: i, Message: m.Copy()}
	}
	return routes, nil
}
