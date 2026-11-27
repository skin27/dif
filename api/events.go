package api

import (
	"context"
	"log"
	"sync"
	"sync/atomic"

	"dif/engine"
	flowdef "dif/flows/definition"
	"dif/internal/channels"
	stepdef "dif/steps/definition"
	stepimpl "dif/steps/impl"
)

// eventObserver publishes the events of the flows of one set of channels to
// its topics, which flows with an event source subscribe to. Nobody
// subscribed, nothing built: Wants tells the engine to skip the event.
//
// Publication never makes a flow wait. When a subscriber's buffer is full the
// event is dropped for all subscribers of that topic (a topic admits a message
// to every subscription or to none) and counted.
type eventObserver struct {
	channels *stepimpl.Channels
	dropped  atomic.Int64
	logged   sync.Once
}

func newEventObserver(c *stepimpl.Channels) *eventObserver { return &eventObserver{channels: c} }

// defaultEvents serves the flows loaded without a Runtime, which share the
// default channels.
var defaultEvents = newEventObserver(stepimpl.DefaultChannels())

func (o *eventObserver) topic(name string) *channels.Topic {
	return o.channels.Runtime.Topic(stepdef.EventTopic(name))
}

func (o *eventObserver) Wants(t stepdef.EventType) bool {
	return o.topic(string(t)).Subscribed() || o.topic(stepdef.AllEvents).Subscribed()
}

func (o *eventObserver) Observe(e engine.Event) {
	m := e.Message()
	for _, name := range []string{string(e.Type), stepdef.AllEvents} {
		topic := o.topic(name)
		if !topic.Subscribed() {
			continue
		}
		if err := topic.Publish(context.Background(), m, channels.Policy{}, nil); err != nil {
			o.dropped.Add(1)
			o.logged.Do(func() { log.Printf("events: dropped an event (%v); the count is Runtime.DroppedEvents", err) })
		}
	}
}

// DroppedEvents returns how many events were not delivered because a
// subscriber's buffer was full or the channels had failed.
func (r *Runtime) DroppedEvents() int64 { return r.events.dropped.Load() }

// newFlow wraps the runner of f, which reports its events to o.
func newFlow(f *flowdef.Flow, onResult func(*Result, error), o *eventObserver) *Flow {
	runner := engine.NewRunner(f, onResult)
	runner.SetObserver(o)
	return &Flow{Runner: runner}
}
