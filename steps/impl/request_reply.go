package impl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"dif/internal/channels"
	"dif/message"
	stepdef "dif/steps/definition"
)

type requestAction struct {
	r       *Channels
	q       *memQueue
	replyTo string
	timeout time.Duration
	policy  channels.Policy
}

func newRequestAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	r := channelRuntime(p)
	name, replyTo := p["path"].(string), p["replyTo"].(string)
	if name == "" || replyTo == "" || name == replyTo {
		return nil, fmt.Errorf("request and reply queues must be nonempty and different")
	}
	q := r.queue(name)
	if q.core.Durable() || r.queue(replyTo).core.Durable() {
		return nil, fmt.Errorf("asynchronous requests currently require memory queues")
	}
	ms := p["requestTimeout"].(int)
	if ms < 1 || int64(ms) > int64((1<<63-1)/time.Millisecond) {
		return nil, fmt.Errorf("invalid requestTimeout")
	}
	policy, err := admissionPolicy(p)
	return requestAction{r, q, replyTo, time.Duration(ms) * time.Millisecond, policy}, err
}

func (a requestAction) LocalTargets() []string {
	return []string{"queue:" + a.q.name, "queue:" + a.replyTo}
}

func (a requestAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req := m.Child(m[message.Body])
	id := req[message.MessageID].(string)
	now := time.Now()
	deadline := now.Add(a.timeout)
	req[message.RequestID], req[message.ReplyTo] = id, a.replyTo
	req[message.ReplyDeadline] = deadline.Format(time.RFC3339Nano)
	delete(req, message.ReplyStatus)
	delete(req, message.ReplyReason)
	release := stepdef.TrackWork(ctx, true)
	if err := a.r.RegisterRequest(req, a.replyTo, deadline, now, release); err != nil {
		release()
		return nil, err
	}
	if err := a.q.putPolicy(ctx, queued{m: req}, a.policy); err != nil {
		a.r.AbortRequest(id)
		return nil, err
	}
	a.r.AdmitRequest(id)
	out := m.Copy()
	out[message.RequestID] = id
	return out, nil
}

type replySink struct {
	r      *Channels
	status string
	policy channels.Policy
}

func newReplySink(_ string, p stepdef.Params) (stepdef.Processor, error) {
	policy, err := admissionPolicy(p)
	return replySink{channelRuntime(p), p["status"].(string), policy}, err
}

func (s replySink) Consume(ctx context.Context, m message.Message) error {
	name, _ := m[message.ReplyTo].(string)
	id, _ := m[message.RequestID].(string)
	if name == "" || id == "" {
		return fmt.Errorf("reply requires Reply-To and Request-Id")
	}
	q := s.r.queue(name)
	if q.core.Durable() {
		return fmt.Errorf("asynchronous replies currently require memory queues")
	}
	out := m.Child(m[message.Body])
	out[message.ReplyStatus] = s.status
	delete(out, message.ReplyTo)
	delete(out, message.ReplyDeadline)
	delete(out, message.ReplyReason)
	return q.putPolicy(ctx, queued{m: out}, s.policy)
}

type replySource struct {
	r            *Channels
	q, unmatched *memQueue
}

func newReplySource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	r := channelRuntime(p)
	name := p["path"].(string)
	unmatched := p["unmatchedQueue"].(string)
	if unmatched == "" {
		unmatched = name + ".unmatched"
	}
	if name == "" || unmatched == name {
		return nil, fmt.Errorf("reply and unmatched queues must be nonempty and different")
	}
	q := r.queue(name)
	if q.core.Durable() {
		return nil, fmt.Errorf("asynchronous replies currently require memory queues")
	}
	return replySource{r, q, r.queue(unmatched)}, nil
}

func (s replySource) LocalChannel() string   { return "queue:" + s.q.name }
func (s replySource) LocalTargets() []string { return []string{"queue:" + s.unmatched.name} }

func (s replySource) Run(context.Context, stepdef.Emit) error {
	return fmt.Errorf("reply source requires completion-aware runner")
}

// Intake continues while the result flow runs, so slow result processing does
// not turn timely responses into timeouts. There is one fixed worker per source,
// never one goroutine per outstanding request.
func (s replySource) RunDelivery(ctx context.Context, emit stepdef.EmitDelivery, ready func()) error {
	release, err := s.q.core.AcquireConsumer(true)
	if err != nil {
		return err
	}
	defer release()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { err := s.deliver(runCtx, emit); done <- err; cancel() }()
	ready()
	err = s.receive(runCtx)
	cancel()
	return errors.Join(err, <-done)
}

func (s replySource) receive(ctx context.Context) error {
	for {
		e, err := s.q.take(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		reason, err := s.r.MatchReply(s.q.name, e.m, time.Now())
		if err == nil && reason != "" {
			out := e.m.Copy()
			out[message.ReplyReason] = reason
			err = s.unmatched.putPolicy(ctx, queued{m: out}, channels.Policy{})
		}
		if err != nil {
			return errors.Join(err, s.q.core.Return(e.entry()))
		}
		if e.reply != nil {
			e.reply(e.m.Copy(), nil)
		}
		if e.release != nil {
			e.release()
		}
	}
}

func (s replySource) deliver(ctx context.Context, emit stepdef.EmitDelivery) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	wait := false
	for {
		if wait {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
		if ctx.Err() != nil {
			return nil
		}
		m, err := s.r.RequestOutcome(s.q.name, time.Now())
		if err != nil {
			return err
		}
		if m == nil {
			wait = true
			continue
		}
		id := m[message.RequestID].(string)
		type completion struct {
			err   error
			retry bool
		}
		completed := make(chan completion, 1)
		err = emit(m, nil, func(processingErr error) error {
			err := s.r.FinishRequest(id, processingErr == nil, time.Now())
			completed <- completion{err, processingErr != nil}
			return err
		})
		if err != nil {
			finishErr := s.r.FinishRequest(id, false, time.Now())
			if ctx.Err() != nil {
				return finishErr
			}
			return errors.Join(err, finishErr)
		}
		select {
		case result := <-completed:
			if result.err != nil {
				return result.err
			}
			wait = result.retry
		case <-ctx.Done():
			return nil
		}
	}
}
