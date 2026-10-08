package impl

import (
	"context"
	"dif/internal/channels"
	"dif/message"
	stepdef "dif/steps/definition"
	"fmt"
	"sync"
)

type memTopic struct {
	name string
	core *channels.Topic
	mu   sync.Mutex
	subs []*topicSubscription
}
type topicSubscription struct {
	q    *memQueue
	core *channels.Subscription
}

func topicNamed(name string) *memTopic { return defaultChannels.topic(name) }
func (r *Channels) topic(name string) *memTopic {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.topics[name]
	if t == nil {
		t = &memTopic{name: name, core: r.Runtime.Topic(name)}
		r.topics[name] = t
	}
	return t
}
func (t *memTopic) subscribe(ctx context.Context) *topicSubscription {
	s := t.core.Subscribe(ctx)
	sub := &topicSubscription{q: &memQueue{name: t.name, core: s.Queue}, core: s}
	t.mu.Lock()
	t.subs = append(t.subs, sub)
	t.mu.Unlock()
	return sub
}
func (t *memTopic) unsubscribe(s *topicSubscription) {
	t.core.Unsubscribe(s.core)
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, sub := range t.subs {
		if sub == s {
			t.subs = append(t.subs[:i], t.subs[i+1:]...)
			return
		}
	}
}
func (t *memTopic) publish(ctx context.Context, m message.Message) error {
	return t.core.Publish(ctx, m, channels.Policy{}, func() func() { return stepdef.TrackWork(ctx, true) })
}

type topicSource struct{ topic *memTopic }
type topicAction struct {
	topic  *memTopic
	policy channels.Policy
}

func topicOption(p stepdef.Params) (*memTopic, error) {
	name := p["path"].(string)
	if name == "" {
		return nil, fmt.Errorf("option path: empty topic name")
	}
	return channelRuntime(p).topic(name), nil
}

func newTopicSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	t, err := topicOption(p)
	return topicSource{t}, err
}

func newTopicAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	t, err := topicOption(p)
	if err != nil {
		return nil, err
	}
	policy, err := admissionPolicy(p)
	return topicAction{t, policy}, err
}

func (a topicAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	return m, a.topic.core.Publish(ctx, m, a.policy, func() func() { return stepdef.TrackWork(ctx, true) })
}

func (s topicSource) Run(ctx context.Context, emit stepdef.Emit) error {
	return s.RunReady(ctx, emit, func() {})
}

func (s topicSource) RunReady(ctx context.Context, emit stepdef.Emit, ready func()) error {
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
		err = emit(e.m, nil)
		if e.release != nil {
			e.release()
		}
		if err != nil {
			return nil // stopping discards this subscription's remaining messages
		}
	}
}

func (s topicSource) LocalChannel() string   { return "topic:" + s.topic.name }
func (a topicAction) LocalTargets() []string { return []string{"topic:" + a.topic.name} }
