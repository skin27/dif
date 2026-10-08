package channels

import (
	"fmt"
	"time"

	"dif/message"
)

// RequestConfig bounds process-local conversations, including retained terminal
// records. Pending requests and outcomes are deliberately not journaled.
type RequestConfig struct {
	Capacity    int   `json:"capacity,omitempty"`
	RetentionMS int64 `json:"retention,omitempty"`
}

type pendingRequest struct {
	queue              string
	deadline           time.Time
	identity           message.Message
	outcome            message.Message
	finished           time.Time
	admitted, inFlight bool
	release            func()
}

type consumerState struct {
	ordinary int
	reply    bool
}

func (q *Queue) AcquireConsumer(reply bool) (func(), error) {
	return q.r.AcquireConsumer(q.name, reply)
}

func (r *Runtime) configureRequests() error {
	c := &r.config.Requests
	if c.Capacity < 0 || c.RetentionMS < 0 || c.RetentionMS > int64((1<<63-1)/time.Millisecond) {
		return fmt.Errorf("invalid requests configuration")
	}
	if c.Capacity == 0 {
		c.Capacity = DefaultCapacity
	}
	if c.RetentionMS == 0 {
		c.RetentionMS = int64(24 * time.Hour / time.Millisecond)
	}
	r.requests = map[string]*pendingRequest{}
	r.consumers = map[string]*consumerState{}
	return nil
}

// AcquireConsumer reserves a queue for either competing ordinary consumers or
// one reply consumer. The reservation lasts only for the source's running life.
func (r *Runtime) AcquireConsumer(queue string, reply bool) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.status(); err != nil {
		return nil, err
	}
	s := r.consumers[queue]
	if s == nil {
		s = &consumerState{}
		r.consumers[queue] = s
	}
	if s.reply || (reply && s.ordinary != 0) {
		return nil, fmt.Errorf("queue %q already has a conflicting consumer", queue)
	}
	if reply {
		s.reply = true
	} else {
		s.ordinary++
	}
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if reply {
			s.reply = false
		} else {
			s.ordinary--
		}
	}, nil
}

func (r *Runtime) pruneRequests(now time.Time) {
	for id, p := range r.requests {
		if !p.finished.IsZero() && !now.Before(p.finished.Add(time.Duration(r.config.Requests.RetentionMS)*time.Millisecond)) {
			delete(r.requests, id)
		}
	}
}

// RegisterRequest must precede publication so even an immediate reply can match.
// Only identity headers are retained, never the request body.
func (r *Runtime) RegisterRequest(m message.Message, queue string, deadline, now time.Time, release func()) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.status(); err != nil {
		return err
	}
	r.pruneRequests(now)
	id, _ := m[message.RequestID].(string)
	if id == "" || queue == "" || !deadline.After(now) {
		return fmt.Errorf("invalid asynchronous request")
	}
	if r.requests[id] != nil {
		return fmt.Errorf("request %q already registered", id)
	}
	if len(r.requests) >= r.config.Requests.Capacity {
		return fmt.Errorf("pending request capacity reached")
	}
	identity := message.Message{}
	for _, key := range []string{message.MessageID, message.RequestID, message.CorrelationID, message.TraceID, message.ReplyDeadline} {
		identity[key] = m[key]
	}
	r.requests[id] = &pendingRequest{queue: queue, deadline: deadline, identity: identity, release: release}
	return nil
}

func (r *Runtime) AdmitRequest(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.requests[id]; p != nil {
		p.admitted = true
	}
}

// AbortRequest rolls back a failed admission. Admission never exposes a failed
// enqueue to a consumer, so no outcome can have been selected for this request.
func (r *Runtime) AbortRequest(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p := r.requests[id]; p != nil && p.release != nil {
		p.release()
	}
	delete(r.requests, id)
}

func (p *pendingRequest) expire(now time.Time) {
	if p.outcome == nil && !now.Before(p.deadline) {
		p.outcome = p.identity.Child(nil)
		p.outcome[message.ReplyStatus] = "timeout"
	}
}

// MatchReply selects the first outcome under the same lock as expiration. A
// reply observed at or after the deadline is late, even before a timeout sweep.
// Nonempty reason means the caller must route the response as unmatched.
func (r *Runtime) MatchReply(queue string, m message.Message, now time.Time) (reason string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.status(); err != nil {
		return "", err
	}
	r.pruneRequests(now)
	id, _ := m[message.RequestID].(string)
	status, _ := m[message.ReplyStatus].(string)
	if id == "" || (status != "success" && status != "error") {
		return "malformed", nil
	}
	p := r.requests[id]
	if p == nil {
		return "unknown", nil
	}
	if p.queue != queue {
		return "wrong-destination", nil
	}
	p.expire(now)
	if p.outcome != nil {
		if p.outcome[message.ReplyStatus] == "timeout" {
			return "late", nil
		}
		return "duplicate", nil
	}
	p.outcome = m.Copy()
	return "", nil
}

// RequestOutcome returns one selected outcome for serial delivery by the owning
// reply source. It remains available until the result flow completes successfully.
func (r *Runtime) RequestOutcome(queue string, now time.Time) (message.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.status(); err != nil {
		return nil, err
	}
	r.pruneRequests(now)
	for _, p := range r.requests {
		if p.queue != queue || !p.finished.IsZero() || !p.admitted || p.inFlight {
			continue
		}
		p.expire(now)
		if p.outcome != nil {
			p.inFlight = true
			return p.outcome.Copy(), nil
		}
	}
	return nil, nil
}

func (r *Runtime) FinishRequest(id string, success bool, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.status(); err != nil {
		return err
	}
	p := r.requests[id]
	if p == nil || p.outcome == nil {
		return fmt.Errorf("request %q has no outcome", id)
	}
	p.inFlight = false
	if !success {
		return nil
	}
	// Retain only enough state to classify subsequent replies.
	p.outcome = message.Message{message.ReplyStatus: p.outcome[message.ReplyStatus]}
	p.finished = now
	if p.release != nil {
		p.release()
		p.release = nil
	}
	return nil
}
