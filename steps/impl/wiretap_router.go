package impl

import (
	"context"
	"fmt"

	"dif/message"
	stepdef "dif/steps/definition"
)

// wireTapRouter sends a copy of the message to the link with rule "wiretap"
// and the message itself on along the other link. The tap is detached: what
// happens on it never changes or fails the message.
type wireTapRouter struct {
	tap, main int
}

func newWireTapRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	r := wireTapRouter{-1, -1}
	for i, l := range p[stepdef.Links].([]stepdef.Link) {
		target := &r.main
		if l.Rule == "wiretap" {
			target = &r.tap
		}
		if *target >= 0 {
			return nil, fmt.Errorf("needs one outbound link with rule wiretap and one without, has more")
		}
		*target = i
	}
	if r.tap < 0 || r.main < 0 {
		return nil, fmt.Errorf("needs one outbound link with rule wiretap and one without")
	}
	return r, nil
}

func (r wireTapRouter) Route(_ context.Context, m message.Message) ([]stepdef.Route, error) {
	return []stepdef.Route{
		{Next: r.tap, Message: m.Copy(), Detached: true},
		{Next: r.main, Message: m},
	}, nil
}
