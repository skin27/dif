package channels

import (
	"context"
	"fmt"

	"dif/message"
)

type Topic struct {
	r        *Runtime
	name     string
	capacity int
	subs     []*Subscription
	blocked  int
}
type Subscription struct {
	Queue *Queue
	ctx   context.Context
	stop  func() bool
}

func (r *Runtime) Topic(name string) *Topic {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.topics[name]
	if t == nil {
		t = &Topic{r: r, name: name, capacity: DefaultCapacity}
		r.topics[name] = t
	}
	return t
}
func (t *Topic) Subscribe(ctx context.Context) *Subscription {
	r := t.r
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &Subscription{Queue: &Queue{r: r, name: t.name, config: QueueConfig{Capacity: t.capacity}}, ctx: ctx}
	// Cancellation changes membership even before the source unwinds.
	s.stop = context.AfterFunc(ctx, func() { r.mu.Lock(); defer r.mu.Unlock(); r.notify() })
	t.subs = append(t.subs, s)
	r.notify()
	return s
}
func (t *Topic) Unsubscribe(s *Subscription) {
	r := t.r
	r.mu.Lock()
	defer r.mu.Unlock()
	s.stop()
	for i, sub := range t.subs {
		if sub == s {
			for len(s.Queue.items) > 0 {
				s.Queue.remove(0)
			}
			t.subs = append(t.subs[:i], t.subs[i+1:]...)
			r.notify()
			return
		}
	}
}
func (t *Topic) Publish(ctx context.Context, m message.Message, p Policy, track func() func()) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Overflow == "block" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}
	r := t.r
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.status(); err != nil {
			return err
		}
		full := false
		for _, s := range t.subs {
			if s.ctx.Err() == nil && s.Queue.waiting() >= t.capacity {
				full = true
				break
			}
		}
		if !full {
			for _, s := range t.subs {
				if s.ctx.Err() != nil {
					continue
				}
				e := &stored{Entry: Entry{Message: m.Copy()}}
				if track != nil {
					e.Release = track()
				}
				s.Queue.items = append(s.Queue.items, e)
			}
			r.notify()
			return nil
		}
		if p.Overflow != "block" {
			return fmt.Errorf("topic %s subscriber buffer is full (%d messages)", t.name, t.capacity)
		}
		t.blocked++
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
		}
		r.mu.Lock()
		t.blocked--
	}
}
