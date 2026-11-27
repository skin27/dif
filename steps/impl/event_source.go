package impl

import (
	"context"
	"fmt"
	"strings"

	stepdef "dif/steps/definition"
)

// eventSource receives the events of the flows, which the API publishes to
// topics named after the event type (see stepdef.EventTopic). It subscribes as
// a topic source does: while the flow runs, with a buffer of its own, and no
// replay of what happened before.
type eventSource struct {
	topic  *memTopic
	name   string // the event type, or stepdef.AllEvents
	flowID string // only events of this flow; "": of every flow
}

func newEventSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	name, _ := p["path"].(string)
	if !stepdef.ValidEventType(name) {
		types := []string{stepdef.AllEvents}
		for _, t := range stepdef.EventTypes() {
			types = append(types, string(t))
		}
		return nil, fmt.Errorf("event %q is not one of %s", name, strings.Join(types, ", "))
	}
	flowID, _ := p["flowId"].(string)
	return eventSource{channelRuntime(p).topic(stepdef.EventTopic(name)), name, flowID}, nil
}

func (s eventSource) Run(ctx context.Context, emit stepdef.Emit) error {
	return s.RunReady(ctx, emit, func() {})
}

func (s eventSource) RunReady(ctx context.Context, emit stepdef.Emit, ready func()) error {
	sub := s.topic.subscribe(ctx)
	defer s.topic.unsubscribe(sub)
	ready()
	for {
		e, err := sub.q.take(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if s.flowID != "" && e.m["event.flow.id"] != s.flowID {
			if e.release != nil {
				e.release()
			}
			continue
		}
		err = emit(e.m, nil)
		if e.release != nil {
			e.release()
		}
		if err != nil {
			return nil // stopping discards this subscription's remaining messages
		}
	}
}

// LocalChannel makes a service stop the event source after the flows it
// listens to, so that it still receives their last events.
func (s eventSource) LocalChannel() string { return "topic:" + stepdef.EventTopic(s.name) }
