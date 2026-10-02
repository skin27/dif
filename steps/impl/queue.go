package impl

import (
	"context"
	"fmt"
	"sync"

	"dif/message"
	stepdef "dif/steps/definition"
)

// Queues are named in-memory FIFO queues of messages, shared by all flows in
// the process: the deadletter and flowlink steps put messages on one, the
// queue and flowlink sources take them off. They are not persisted; their
// messages are lost when dif exits.

// queueCapacity limits the messages a queue holds; putting more fails.
const queueCapacity = 10000

type memQueue struct {
	name  string
	mu    sync.Mutex
	msgs  []queued
	ready chan struct{} // holds a token while msgs may be non-empty
}

// queued is a message on a queue. A sender that waits for the outcome gives
// reply, which the consuming flow calls, and wait: once wait is done the
// sender no longer waits, and the message is dropped if it was not taken yet.
type queued struct {
	m     message.Message
	reply func(message.Message, error) // nil if no one waits
	wait  context.Context              // nil if no one waits
}

var queues = struct {
	sync.Mutex
	m map[string]*memQueue
}{m: map[string]*memQueue{}}

// queueNamed returns the queue name, creating it the first time.
func queueNamed(name string) *memQueue {
	queues.Lock()
	defer queues.Unlock()
	q, ok := queues.m[name]
	if !ok {
		q = &memQueue{name: name, ready: make(chan struct{}, 1)}
		queues.m[name] = q
	}
	return q
}

// put adds m to the end of the queue.
func (q *memQueue) put(m message.Message) error {
	return q.putQueued(queued{m: m})
}

func (q *memQueue) putQueued(e queued) error {
	q.mu.Lock()
	if len(q.msgs) >= queueCapacity {
		q.mu.Unlock()
		return fmt.Errorf("queue %s is full (%d messages)", q.name, queueCapacity)
	}
	q.msgs = append(q.msgs, e)
	q.mu.Unlock()
	q.signal()
	return nil
}

// putBack returns e, which take gave, to the front of the queue.
func (q *memQueue) putBack(e queued) {
	q.mu.Lock()
	q.msgs = append([]queued{e}, q.msgs...)
	q.mu.Unlock()
	q.signal()
}

// take removes the first message whose sender still waits (or never did),
// waiting for one until ctx is done.
func (q *memQueue) take(ctx context.Context) (queued, error) {
	for {
		q.mu.Lock()
		for len(q.msgs) > 0 {
			e := q.msgs[0]
			q.msgs[0] = queued{}
			q.msgs = q.msgs[1:]
			if e.wait != nil && e.wait.Err() != nil {
				continue // the sender gave up
			}
			more := len(q.msgs) > 0
			q.mu.Unlock()
			if more {
				q.signal() // let another taker in
			}
			return e, nil
		}
		q.mu.Unlock()
		select {
		case <-q.ready:
		case <-ctx.Done():
			return queued{}, ctx.Err()
		}
	}
}

func (q *memQueue) signal() {
	select {
	case q.ready <- struct{}{}:
	default: // a token is there already
	}
}

func (q *memQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.msgs)
}

// deadLetterSink puts a copy of the message on an in-memory queue, the dead
// letter queue. It is meant for a flow's error route: the message keeps its
// headers, among them error.message and error.step.
type deadLetterSink struct {
	q *memQueue
}

func newDeadLetterSink(_ string, p stepdef.Params) (stepdef.Processor, error) {
	name := p["deadLetterQueue"].(string)
	if name == "" {
		return nil, fmt.Errorf("option deadLetterQueue: empty queue name")
	}
	return deadLetterSink{queueNamed(name)}, nil
}

func (s deadLetterSink) Consume(_ context.Context, m message.Message) error {
	return s.q.put(m.Copy())
}

// queueSource emits the messages of an in-memory queue, such as a dead
// letter queue, as they arrive. Messages keep their headers and trace id; a
// sender that waits for the outcome gets it.
type queueSource struct {
	q *memQueue
}

func newQueueSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return queueSource{queueNamed(p["path"].(string))}, nil
}

func (s queueSource) Run(ctx context.Context, emit stepdef.Emit) error {
	for {
		e, err := s.q.take(ctx)
		if err != nil {
			return nil // the flow stopped
		}
		if emit(e.m, e.reply) != nil {
			s.q.putBack(e) // the flow is stopping and did not take it
			return nil
		}
	}
}
