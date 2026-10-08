package impl

import (
	"context"
	"dif/internal/channels"
	"dif/message"
	stepdef "dif/steps/definition"
	"fmt"
)

// deadLetterSink puts a copy of the message on a configured queue, the dead
// letter queue. It is meant for a flow's error route: the message keeps its
// headers, among them error.message and error.step.
type deadLetterSink struct {
	q      *memQueue
	policy channels.Policy
}

func newDeadLetterSink(_ string, p stepdef.Params) (stepdef.Processor, error) {
	name := p["deadLetterQueue"].(string)
	if name == "" {
		return nil, fmt.Errorf("option deadLetterQueue: empty queue name")
	}
	policy, err := admissionPolicy(p)
	return deadLetterSink{channelRuntime(p).queue(name), policy}, err
}

func (s deadLetterSink) Consume(ctx context.Context, m message.Message) error {
	return s.q.putPolicy(ctx, queued{m: m.Copy()}, s.policy)
}

func (s deadLetterSink) LocalTargets() []string { return []string{"queue:" + s.q.name} }

// newQueueAction addresses a logical queue by URI path or targetQueueId.
// Existing actions wait for processing; delivery=enqueue only waits for admission.
func newQueueAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	id, legacy := p["targetQueueId"].(string)
	path, named := p["path"].(string)
	if named {
		if legacy && id != path {
			return nil, fmt.Errorf("path and targetQueueId name different queues")
		}
		id = path
	}
	if !named && !legacy {
		return nil, fmt.Errorf("missing required option targetQueueId or queue URI path")
	}
	if id == "" {
		return nil, fmt.Errorf("option targetQueueId: empty queue id")
	}
	wait := p["delivery"] != "enqueue"
	if !wait && p["exchangePattern"] == message.InOut {
		return nil, fmt.Errorf("delivery enqueue does not support exchangePattern InOut")
	}
	q := channelRuntime(p).queue(id)
	if q.core.Durable() && wait {
		return nil, fmt.Errorf("durable queues require delivery enqueue and exchangePattern InOnly")
	}
	policy, err := admissionPolicy(p)
	if err != nil {
		return nil, err
	}
	return flowLinkAction{
		target:   q,
		policy:   policy,
		targetID: id,
		wait:     wait,
		inOut:    p["exchangePattern"] == message.InOut,
		timeout:  requestTimeout(p),
	}, nil
}

// queueSource emits the messages of a configured queue, such as a dead
// letter queue, as they arrive. Messages keep their headers and trace id; a
// sender that waits for the outcome gets it.
type queueSource struct {
	q *memQueue
}

func newQueueSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	if p["path"] == "" {
		return nil, fmt.Errorf("option path: empty queue name")
	}
	return queueSource{channelRuntime(p).queue(p["path"].(string))}, nil
}

func (s queueSource) Run(ctx context.Context, emit stepdef.Emit) error {
	release, err := s.q.core.AcquireConsumer(false)
	if err != nil {
		return err
	}
	defer release()
	return s.run(ctx, emit)
}

func (s queueSource) run(ctx context.Context, emit stepdef.Emit) error {
	if s.q.core.Durable() {
		return fmt.Errorf("durable source requires completion-aware runner")
	}
	for {
		e, err := s.q.take(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if emit(e.m, e.reply) != nil {
			s.q.putBack(e) // the flow is stopping and did not take it
			return nil
		}
		if e.release != nil {
			e.release()
		}
	}
}

func (s queueSource) LocalChannel() string { return "queue:" + s.q.name }

func (s queueSource) RunReady(ctx context.Context, emit stepdef.Emit, ready func()) error {
	release, err := s.q.core.AcquireConsumer(false)
	if err != nil {
		return err
	}
	defer release()
	ready()
	return s.run(ctx, emit)
}

// RunDelivery receives a completion callback independent of an early reply.
func (s queueSource) RunDelivery(ctx context.Context, emit stepdef.EmitDelivery, ready func()) error {
	if !s.q.core.Durable() {
		return s.RunReady(ctx, func(m message.Message, reply func(message.Message, error)) error { return emit(m, reply, nil) }, ready)
	}
	s.q.core.TrackRecovered(func() func() { return stepdef.TrackWork(ctx, true) })
	release, err := s.q.core.AcquireConsumer(false)
	if err != nil {
		return err
	}
	defer release()
	ready()
	for {
		e, err := s.q.take(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		completed := make(chan error, 1)
		err = emit(e.m, nil, func(processingErr error) error {
			err := s.q.core.Complete(e.id, processingErr)
			if err != nil {
				stepdef.ReportSourceFailure(ctx, err)
			}
			completed <- err
			return err
		})
		if err != nil {
			return s.q.core.Return(e.entry())
		}
		// A single source reserves at most one delivery. Other sources may compete.
		select {
		case err := <-completed:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}
