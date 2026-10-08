package engine

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Engine is the registry of flows, by flow id. Every flow runs independently
// in its own Runner; the engine only finds them and lists them.
//
// mu guards the map only. It is never held while a Runner method runs, so a
// slow lifecycle call on one flow never blocks another.
type Engine struct {
	mu    sync.RWMutex
	flows map[string]*Runner
}

// FlowStatus is a flow id with its lifecycle state, the start of its current
// run and the number of messages it has processed.
type FlowStatus struct {
	ID    string
	State State
	Since time.Time // when the current run started; zero when stopped

	Completed     int64  // messages processed to the end, including those an error route handled
	Failed        int64  // messages that failed
	Source        string // starting, ready, running, completed, failed or cancelled
	InFlight      int64
	DurationNanos int64
}

// NotFoundError is returned for an unknown flow id, with the registered ids
// that look like it.
type NotFoundError struct {
	ID          string
	Suggestions []string
}

func (e *NotFoundError) Error() string {
	if len(e.Suggestions) > 0 {
		return fmt.Sprintf("flow %s not found. Do you mean: %s?", e.ID, strings.Join(e.Suggestions, ", "))
	}
	return fmt.Sprintf("flow %s not found", e.ID)
}

// New returns an empty engine.
func New() *Engine {
	return &Engine{flows: map[string]*Runner{}}
}

// Add registers r under its flow id, which must be set and unique.
func (e *Engine) Add(r *Runner) error {
	id := r.ID()
	if id == "" {
		return fmt.Errorf("flow has no id")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, dup := e.flows[id]; dup {
		return fmt.Errorf("flow %s is already registered", id)
	}
	e.flows[id] = r
	return nil
}

// GetFlow returns the flow with the given id. If there is none, the error
// suggests registered flows with a similar id: "flow time not found. Do you mean: timer?"
func (e *Engine) GetFlow(id string) (*Runner, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if r, ok := e.flows[id]; ok {
		return r, nil
	}

	ids := make([]string, 0, len(e.flows))
	for known := range e.flows {
		ids = append(ids, known)
	}
	return nil, &NotFoundError{ID: id, Suggestions: suggest(id, ids)}
}

func (e *Engine) StartFlow(id string) error  { return e.do(id, (*Runner).Start) }
func (e *Engine) PauseFlow(id string) error  { return e.do(id, (*Runner).Pause) }
func (e *Engine) ResumeFlow(id string) error { return e.do(id, (*Runner).Resume) }
func (e *Engine) StopFlow(id string) error   { return e.do(id, (*Runner).Stop) }

// ForceStopFlow stops the flow immediately; a message in progress may be lost.
func (e *Engine) ForceStopFlow(id string) error { return e.do(id, (*Runner).ForceStop) }

func (e *Engine) do(id string, op func(*Runner) error) error {
	r, err := e.GetFlow(id)
	if err != nil {
		return err
	}
	return op(r)
}

// ListFlows returns the flows in the given state, or all flows if state is
// empty, sorted by id.
func (e *Engine) ListFlows(state State) []FlowStatus {
	rs := e.runners()
	list := make([]FlowStatus, 0, len(rs))
	for _, r := range rs {
		if s := r.Status(); state == "" || s.State == state {
			list = append(list, s)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}

// Shutdown stops every running flow, waits until they have finished and
// returns the errors of their sources.
func (e *Engine) Shutdown() error {
	var errs []error
	for _, r := range e.runners() {
		if r.State() != Stopped {
			r.Stop() // an error only means it was stopped meanwhile
		}
		if err := r.Wait(); err != nil {
			errs = append(errs, fmt.Errorf("flow %s: %w", r.ID(), err))
		}
	}
	return errors.Join(errs...)
}

// runners returns a snapshot of the registered flows.
func (e *Engine) runners() []*Runner {
	e.mu.RLock()
	defer e.mu.RUnlock()
	rs := make([]*Runner, 0, len(e.flows))
	for _, r := range e.flows {
		rs = append(rs, r)
	}
	return rs
}
