// Package channels owns local channel admission, storage and delivery. It has
// no dependency on the engine, processor contracts or DIL.
package channels

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"dif/message"
)

const DefaultCapacity = 10000

type Config struct {
	Requests        RequestConfig                `json:"requests,omitempty"`
	Directory       string                       `json:"directory,omitempty"`
	MaxDiskBytes    int64                        `json:"maxDiskBytes,omitempty"`
	MaxMessageBytes int                          `json:"maxMessageBytes,omitempty"`
	Queues          map[string]QueueConfig       `json:"queues,omitempty"`
	Topics          map[string]TopicConfig       `json:"topics,omitempty"`
	Idempotency     map[string]IdempotencyConfig `json:"idempotency,omitempty"`
}

type QueueConfig struct {
	Capacity      int    `json:"capacity,omitempty"`
	Durable       bool   `json:"durable,omitempty"`
	MaxDeliveries int    `json:"maxDeliveries,omitempty"`
	RetryDelayMS  int    `json:"retryDelay,omitempty"`
	DeadLetter    string `json:"deadLetter,omitempty"`
}

type TopicConfig struct {
	Capacity int `json:"capacity,omitempty"`
}
type IdempotencyConfig struct {
	Durable     bool  `json:"durable,omitempty"`
	RetentionMS int64 `json:"retention,omitempty"`
	MaxKeys     int   `json:"maxKeys,omitempty"`
}

type Policy struct {
	Overflow string
	Timeout  time.Duration
}

func (p Policy) Validate() error {
	if p.Overflow != "" && p.Overflow != "fail" && p.Overflow != "block" {
		return fmt.Errorf("overflow must be fail or block")
	}
	if p.Overflow == "block" && p.Timeout <= 0 {
		return fmt.Errorf("overflow block requires a positive enqueueTimeout")
	}
	return nil
}

// Entry contains process-local reply/accounting callbacks. They are never stored.
type Entry struct {
	Message message.Message
	Reply   func(message.Message, error)
	Wait    context.Context
	Release func()
	ID      uint64
}

type stored struct {
	Entry
	data             []byte
	attempts         int
	available        int64
	inFlight, parked bool
}

type Queue struct {
	r            *Runtime
	name         string
	config       QueueConfig
	items        []*stored
	blocked      int
	redeliveries uint64
	track        func() func()
}

type Runtime struct {
	requests        map[string]*pendingRequest
	consumers       map[string]*consumerState
	mu              sync.Mutex
	config          Config
	queues          map[string]*Queue
	topics          map[string]*Topic
	keys            map[string]map[string]*keyState
	changed         chan struct{}
	closed          bool
	failure         error
	failed          chan struct{}
	file, lock      *os.File
	journalBytes    int64
	nextID          uint64
	storageFailures uint64
}

// Open validates all declarations before opening storage. Configuration belongs
// to the runtime, so different steps cannot silently redefine the same channel.
func Open(c Config) (*Runtime, error) { return configure(c, true) }

// Validate checks declarations without creating directories or opening files.
func Validate(c Config) error { _, err := configure(c, false); return err }

func configure(c Config, openStorage bool) (*Runtime, error) {
	if c.MaxDiskBytes == 0 {
		c.MaxDiskBytes = 256 << 20
	}
	if c.MaxMessageBytes == 0 {
		c.MaxMessageBytes = 4 << 20
	}
	if c.MaxMessageBytes < 1 || c.MaxMessageBytes > 64<<20 || c.MaxDiskBytes < int64(c.MaxMessageBytes)*4 {
		return nil, fmt.Errorf("invalid storage limits: disk must hold at least four maximum messages")
	}
	r := &Runtime{config: c, queues: map[string]*Queue{}, topics: map[string]*Topic{}, keys: map[string]map[string]*keyState{}, changed: make(chan struct{}), failed: make(chan struct{})}
	if err := r.configureRequests(); err != nil {
		return nil, err
	}
	needsDisk := false
	for name, cfg := range c.Queues {
		if name == "" || cfg.Capacity < 0 || cfg.MaxDeliveries < 0 || cfg.RetryDelayMS < 0 || int64(cfg.RetryDelayMS) > int64((1<<63-1)/time.Millisecond) {
			return nil, fmt.Errorf("invalid queue configuration %q", name)
		}
		if cfg.Capacity == 0 {
			cfg.Capacity = DefaultCapacity
		}
		if cfg.MaxDeliveries == 0 {
			cfg.MaxDeliveries = 5
		}
		if cfg.RetryDelayMS == 0 {
			cfg.RetryDelayMS = 1000
		}
		if cfg.Durable && cfg.DeadLetter == "" {
			cfg.DeadLetter = name + ".DLQ"
		}
		if !cfg.Durable && cfg.DeadLetter != "" {
			return nil, fmt.Errorf("queue %q: deadLetter requires durable storage", name)
		}
		r.queues[name] = &Queue{r: r, name: name, config: cfg}
		needsDisk = needsDisk || cfg.Durable
	}
	for name, q := range r.queues {
		if !q.config.Durable {
			continue
		}
		dlq := q.config.DeadLetter
		if target := r.queues[dlq]; target != nil {
			if !target.config.Durable {
				return nil, fmt.Errorf("queue %q requires durable dead-letter queue %q", name, dlq)
			}
		} else {
			r.queues[dlq] = &Queue{r: r, name: dlq, config: QueueConfig{Capacity: DefaultCapacity, Durable: true, MaxDeliveries: 5, RetryDelayMS: 1000, DeadLetter: dlq}}
		}
	}
	for name, cfg := range c.Topics {
		if name == "" || cfg.Capacity < 0 {
			return nil, fmt.Errorf("invalid topic configuration %q", name)
		}
		if cfg.Capacity == 0 {
			cfg.Capacity = DefaultCapacity
		}
		r.topics[name] = &Topic{r: r, name: name, capacity: cfg.Capacity}
	}
	// Copy the namespace map: callers may reuse or modify their Config after Open.
	r.config.Idempotency = map[string]IdempotencyConfig{}
	for name, cfg := range c.Idempotency {
		if name == "" || cfg.RetentionMS < 0 || cfg.RetentionMS > int64((1<<63-1)/time.Millisecond) || cfg.MaxKeys < 0 {
			return nil, fmt.Errorf("invalid idempotency configuration %q", name)
		}
		if cfg.RetentionMS == 0 {
			cfg.RetentionMS = int64((24 * time.Hour) / time.Millisecond)
		}
		if cfg.MaxKeys == 0 {
			cfg.MaxKeys = 100000
		}
		r.config.Idempotency[name] = cfg
		r.keys[name] = map[string]*keyState{}
		needsDisk = needsDisk || cfg.Durable
	}
	if needsDisk || c.Directory != "" {
		if c.Directory == "" {
			return nil, fmt.Errorf("durable channels require a directory")
		}
		if !openStorage {
			return r, nil
		}
		if err := r.openJournal(); err != nil {
			if r.file != nil {
				r.file.Close()
			}
			if r.lock != nil {
				r.lock.Close()
			}
			return nil, err
		}
	}
	return r, nil
}

func (r *Runtime) notify() { close(r.changed); r.changed = make(chan struct{}) }
func (r *Runtime) status() error {
	if r.failure != nil {
		return r.failure
	}
	if r.closed {
		return errors.New("channel runtime closed")
	}
	return nil
}
func (r *Runtime) fail(err error) error {
	if r.failure == nil {
		r.failure = fmt.Errorf("channel storage: %w", err)
		r.storageFailures++
		close(r.failed)
		r.notify()
	}
	return r.failure
}
func (r *Runtime) Failed() <-chan struct{} { return r.failed }
func (r *Runtime) Err() error              { r.mu.Lock(); defer r.mu.Unlock(); return r.failure }
func (r *Runtime) Check() error            { r.mu.Lock(); defer r.mu.Unlock(); return r.status() }
func (r *Runtime) Queue(name string) *Queue {
	r.mu.Lock()
	defer r.mu.Unlock()
	q := r.queues[name]
	if q == nil {
		q = &Queue{r: r, name: name, config: QueueConfig{Capacity: DefaultCapacity}}
		r.queues[name] = q
	}
	return q
}
func (q *Queue) Durable() bool { return q.config.Durable }
func (q *Queue) Name() string  { return q.name }
func (q *Queue) Depth() int    { q.r.mu.Lock(); defer q.r.mu.Unlock(); return q.waiting() }
func (q *Queue) waiting() int {
	if !q.Durable() {
		return len(q.items)
	}
	n := 0
	for _, e := range q.items {
		if !e.inFlight {
			n++
		}
	}
	return n
}

// TrackRecovered registers backlog before a source reports ready. Existing
// accounting follows deliveries through reservation, retries and acknowledgements.
func (q *Queue) TrackRecovered(track func() func()) {
	q.r.mu.Lock()
	defer q.r.mu.Unlock()
	q.track = track
	for _, e := range q.items {
		if e.Release == nil && !e.parked {
			e.Release = track()
		}
	}
}

func (q *Queue) Enqueue(ctx context.Context, e Entry, p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Overflow == "block" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}
	var data []byte
	if q.Durable() {
		if e.Reply != nil || e.Wait != nil {
			return fmt.Errorf("durable queue %s supports only enqueue InOnly delivery", q.name)
		}
		var err error
		data, err = encodeMessage(e.Message)
		if err != nil {
			return err
		}
		if len(data) > q.r.config.MaxMessageBytes {
			return fmt.Errorf("message exceeds maxMessageBytes")
		}
	}
	r := q.r
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.status(); err != nil {
			return err
		}
		if q.waiting() < q.config.Capacity {
			break
		}
		if p.Overflow != "block" {
			return fmt.Errorf("queue %s is full (%d messages)", q.name, q.config.Capacity)
		}
		q.blocked++
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
		}
		r.mu.Lock()
		q.blocked--
	}
	r.nextID++
	e.ID = r.nextID
	s := &stored{Entry: e, data: data}
	if q.Durable() {
		if err := r.append(record{Op: "put", Queue: q.name, ID: e.ID, Data: data}); err != nil {
			return err
		}
	}
	if s.Release == nil && q.track != nil {
		s.Release = q.track()
	}
	// A durable backlog without a running consumer is already safe on disk.
	if q.Durable() && q.track == nil && s.Release != nil {
		s.Release()
		s.Release = nil
	}
	q.items = append(q.items, s)
	r.notify()
	return nil
}

func (q *Queue) Take(ctx context.Context) (Entry, error) {
	r := q.r
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return Entry{}, err
		}
		if err := r.status(); err != nil {
			return Entry{}, err
		}
		var wait time.Duration
		for i := 0; i < len(q.items); i++ {
			e := q.items[i]
			if e.inFlight || e.parked {
				continue
			}
			if e.Wait != nil && e.Wait.Err() != nil {
				q.remove(i)
				i--
				r.notify()
				continue
			}
			if d := time.Until(time.UnixMilli(e.available)); d > 0 {
				if wait == 0 || d < wait {
					wait = d
				}
				continue
			}
			out := e.Entry
			if q.Durable() {
				m, err := decodeMessage(e.data)
				if err != nil {
					return Entry{}, r.fail(err)
				}
				out.Message = m
				if err := r.append(record{Op: "attempt", Queue: q.name, ID: e.ID, Attempts: e.attempts + 1}); err != nil {
					return Entry{}, err
				}
				e.attempts++
				if e.attempts > 1 {
					q.redeliveries++
				}
				e.inFlight = true
			} else {
				q.detach(i)
			}
			r.notify()
			return out, nil
		}
		changed := r.changed
		r.mu.Unlock()
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-changed:
			case <-ctx.Done():
			case <-timer.C:
			}
			timer.Stop()
		} else {
			select {
			case <-changed:
			case <-ctx.Done():
			}
		}
		r.mu.Lock()
	}
}

// Return undoes admission to a stopping source; it does not count as an attempt.
func (q *Queue) Return(e Entry) error {
	r := q.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.status(); err != nil {
		return err
	}
	if q.Durable() {
		for _, s := range q.items {
			if s.ID == e.ID {
				if err := r.append(record{Op: "attempt", Queue: q.name, ID: s.ID, Attempts: s.attempts - 1}); err != nil {
					return err
				}
				s.attempts--
				s.inFlight = false
				r.notify()
				return nil
			}
		}
		return fmt.Errorf("unknown delivery %d", e.ID)
	}
	q.items = append([]*stored{{Entry: e}}, q.items...)
	r.notify()
	return nil
}

func (q *Queue) detach(i int) *stored {
	e := q.items[i]
	if i == 0 {
		q.items[0] = nil
		q.items = q.items[1:]
	} else {
		copy(q.items[i:], q.items[i+1:])
		q.items[len(q.items)-1] = nil
		q.items = q.items[:len(q.items)-1]
	}
	return e
}

func (q *Queue) remove(i int) {
	e := q.detach(i)
	if e.Release != nil {
		e.Release()
	}
}

// Complete acknowledges terminal success (including a handled error route), or
// schedules a retry. A DLQ move is a single journal record, not two writes.
func (q *Queue) Complete(id uint64, processingErr error) error {
	if !q.Durable() {
		return nil
	}
	r := q.r
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.status(); err != nil {
		return err
	}
	for i, e := range q.items {
		if e.ID != id {
			continue
		}
		if !e.inFlight {
			return fmt.Errorf("delivery %d is not reserved", id)
		}
		if processingErr == nil {
			if err := r.append(record{Op: "ack", Queue: q.name, ID: id}); err != nil {
				return err
			}
			q.remove(i)
		} else if e.attempts >= q.config.MaxDeliveries {
			dst := r.queues[q.config.DeadLetter]
			if dst == q {
				if err := r.append(record{Op: "park", Queue: q.name, ID: id}); err != nil {
					return err
				}
				e.parked = true
				e.inFlight = false
				if e.Release != nil {
					e.Release()
					e.Release = nil
				}
			} else {
				if dst.waiting() >= dst.config.Capacity {
					return fmt.Errorf("dead-letter queue %s is full; original delivery retained", dst.name)
				}
				m, err := decodeMessage(e.data)
				if err != nil {
					return r.fail(err)
				}
				m["error.message"] = processingErr.Error()
				m["error.queue"] = q.name
				data, err := encodeMessage(m)
				if err != nil {
					return err
				}
				if len(data) > r.config.MaxMessageBytes {
					return fmt.Errorf("dead letter exceeds maxMessageBytes; original retained")
				}
				if err := r.append(record{Op: "move", Queue: q.name, Target: dst.name, ID: id, Data: data}); err != nil {
					return err
				}
				// Transfer accounting without a zero-work gap.
				dst.items = append(dst.items, &stored{Entry: Entry{ID: id, Release: e.Release}, data: data})
				if dst.track == nil && e.Release != nil {
					e.Release()
					dst.items[len(dst.items)-1].Release = nil
				}
				e.Release = nil
				q.remove(i)
			}
		} else {
			at := time.Now().Add(time.Duration(q.config.RetryDelayMS) * time.Millisecond).UnixMilli()
			if err := r.append(record{Op: "retry", Queue: q.name, ID: id, Available: at}); err != nil {
				return err
			}
			e.inFlight = false
			e.available = at
		}
		r.notify()
		return nil
	}
	return fmt.Errorf("unknown delivery %d", id)
}

type QueueStatus struct {
	Name         string `json:"name"`
	Durable      bool   `json:"durable"`
	Waiting      int    `json:"waiting"`
	InFlight     int    `json:"inFlight"`
	Parked       int    `json:"parked"`
	Blocked      int    `json:"blocked"`
	Redeliveries uint64 `json:"redeliveries"`
}
type Status struct {
	Queues          []QueueStatus `json:"queues"`
	Topics          []TopicStatus `json:"topics"`
	JournalBytes    int64         `json:"journalBytes"`
	StorageFailures uint64        `json:"storageFailures"`
	Failed          bool          `json:"failed"`
}

type TopicStatus struct {
	Name          string `json:"name"`
	Subscriptions int    `json:"subscriptions"`
	Waiting       int    `json:"waiting"`
	Blocked       int    `json:"blocked"`
}

func (r *Runtime) Snapshot() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Status{JournalBytes: r.journalBytes, StorageFailures: r.storageFailures, Failed: r.failure != nil}
	for name, q := range r.queues {
		item := QueueStatus{Name: name, Durable: q.Durable(), Blocked: q.blocked, Redeliveries: q.redeliveries}
		for _, e := range q.items {
			if e.inFlight {
				item.InFlight++
			} else if e.parked {
				item.Parked++
			} else {
				item.Waiting++
			}
		}
		s.Queues = append(s.Queues, item)
	}
	sort.Slice(s.Queues, func(i, j int) bool { return s.Queues[i].Name < s.Queues[j].Name })
	for name, topic := range r.topics {
		item := TopicStatus{Name: name, Blocked: topic.blocked}
		for _, sub := range topic.subs {
			if sub.ctx.Err() == nil {
				item.Subscriptions++
				item.Waiting += sub.Queue.waiting()
			}
		}
		s.Topics = append(s.Topics, item)
	}
	sort.Slice(s.Topics, func(i, j int) bool { return s.Topics[i].Name < s.Topics[j].Name })
	return s
}

// Close follows source/runner shutdown. It wakes admission waiters and leaves
// unacknowledged disk records intact for the next runtime.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.failure
	}
	r.closed = true
	r.notify()
	for _, p := range r.requests {
		if p.release != nil {
			p.release()
			p.release = nil
		}
	}
	r.requests = nil
	var errs []error
	for _, q := range r.queues {
		for _, e := range q.items {
			if e.Release != nil {
				e.Release()
				e.Release = nil
			}
		}
	}
	if r.file != nil {
		errs = append(errs, r.file.Sync(), r.file.Close())
	}
	if r.lock != nil {
		errs = append(errs, r.lock.Close())
	}
	return errors.Join(append(errs, r.failure)...)
}
