package impl

import (
	"context"
	"sync"
	"time"

	"dif/internal/channels"
	"dif/message"
	stepdef "dif/steps/definition"
)

// ChannelRuntimeKey is injected after schema validation by the API loader.
const ChannelRuntimeKey = "dif.internal.channels"

type Channels struct {
	*channels.Runtime
	mu     sync.Mutex
	queues map[string]*memQueue
	topics map[string]*memTopic
}

func NewChannels(config channels.Config) (*Channels, error) {
	r, err := channels.Open(config)
	if err != nil {
		return nil, err
	}
	return &Channels{Runtime: r, queues: map[string]*memQueue{}, topics: map[string]*memTopic{}}, nil
}

var defaultChannels = func() *Channels {
	r, err := NewChannels(channels.Config{})
	if err != nil {
		panic(err)
	}
	return r
}()

func channelRuntime(p stepdef.Params) *Channels {
	if r, ok := p[ChannelRuntimeKey].(*Channels); ok {
		return r
	}
	return defaultChannels
}
func admissionPolicy(p stepdef.Params) (channels.Policy, error) {
	overflow, _ := p["overflow"].(string)
	timeout, _ := p["enqueueTimeout"].(int)
	policy := channels.Policy{Overflow: overflow, Timeout: time.Duration(timeout) * time.Millisecond}
	return policy, policy.Validate()
}

const queueCapacity = channels.DefaultCapacity

type memQueue struct {
	name string
	core *channels.Queue
}
type queued struct {
	m       message.Message
	reply   func(message.Message, error)
	wait    context.Context
	release func()
	id      uint64
}

func (e queued) entry() channels.Entry {
	return channels.Entry{Message: e.m, Reply: e.reply, Wait: e.wait, Release: e.release, ID: e.id}
}
func queueNamed(name string) *memQueue { return defaultChannels.queue(name) }
func (r *Channels) queue(name string) *memQueue {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.queues[name]
	if q == nil {
		q = &memQueue{name: name, core: r.Runtime.Queue(name)}
		r.queues[name] = q
	}
	return q
}
func (q *memQueue) put(m message.Message) error { return q.putQueued(queued{m: m}) }
func (q *memQueue) putQueued(e queued) error {
	return q.core.Enqueue(context.Background(), e.entry(), channels.Policy{})
}
func (q *memQueue) putContext(ctx context.Context, e queued) error {
	return q.putPolicy(ctx, e, channels.Policy{})
}
func (q *memQueue) putPolicy(ctx context.Context, e queued, p channels.Policy) error {
	e.release = stepdef.TrackWork(ctx, true)
	if err := q.core.Enqueue(ctx, e.entry(), p); err != nil {
		e.release()
		return err
	}
	return nil
}
func (q *memQueue) take(ctx context.Context) (queued, error) {
	e, err := q.core.Take(ctx)
	return queued{m: e.Message, reply: e.Reply, wait: e.Wait, release: e.Release, id: e.ID}, err
}
func (q *memQueue) putBack(e queued) { _ = q.core.Return(e.entry()) }
func (q *memQueue) len() int         { return q.core.Depth() }
