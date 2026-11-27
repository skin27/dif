package engine

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"sync"

	"dif/message"
)

// HeaderDiff records the value of a user header before and after a step.
type HeaderDiff struct {
	Before any `json:"before"`
	After  any `json:"after"`
}

// StepTrace records what one step did to a message: its body before and after,
// user header additions/changes/removals (excluding metadata.*), duration, and
// any error.
type StepTrace struct {
	StepID         string                `json:"stepId"`
	Kind           string                `json:"kind"`
	URI            string                `json:"uri"`
	DurationMs     int64                 `json:"durationMs"`
	BodyBefore     any                   `json:"bodyBefore"`
	BodyAfter      any                   `json:"bodyAfter"`
	HeadersAdded   map[string]any        `json:"headersAdded,omitempty"`
	HeadersChanged map[string]HeaderDiff `json:"headersChanged,omitempty"`
	HeadersRemoved []string              `json:"headersRemoved,omitempty"`
	Error          string                `json:"error,omitempty"`
	Detached       bool                  `json:"detached,omitempty"`
}

// Trace collects StepTraces for one flow execution. It is safe for concurrent
// use across parallel router branches.
type Trace struct {
	mu    sync.Mutex
	Steps []StepTrace
}

// NewTrace returns an empty Trace ready to attach with WithTrace.
func NewTrace() *Trace {
	return &Trace{}
}

type traceKey struct{}

// WithTrace returns ctx carrying t so engine.Run records step-level diffs into
// t. A nil t leaves ctx unchanged.
func WithTrace(ctx context.Context, t *Trace) context.Context {
	if t == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceKey{}, t)
}

// TraceFrom returns the Trace attached to ctx, or nil if none is attached.
func TraceFrom(ctx context.Context) *Trace {
	if ctx == nil {
		return nil
	}
	t, _ := ctx.Value(traceKey{}).(*Trace)
	return t
}

func (t *Trace) record(s StepTrace) {
	t.mu.Lock()
	t.Steps = append(t.Steps, s)
	t.mu.Unlock()
}

// Snapshot returns an independent copy of the recorded step traces in arrival
// order.
func (t *Trace) Snapshot() []StepTrace {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.Steps) == 0 {
		return []StepTrace{}
	}
	out := make([]StepTrace, len(t.Steps))
	for i, s := range t.Steps {
		s.BodyBefore = snapshotValue(s.BodyBefore)
		s.BodyAfter = snapshotValue(s.BodyAfter)
		if len(s.HeadersAdded) > 0 {
			s.HeadersAdded = maps.Clone(s.HeadersAdded)
			for k, v := range s.HeadersAdded {
				s.HeadersAdded[k] = snapshotValue(v)
			}
		}
		if len(s.HeadersChanged) > 0 {
			changed := make(map[string]HeaderDiff, len(s.HeadersChanged))
			for k, d := range s.HeadersChanged {
				changed[k] = HeaderDiff{Before: snapshotValue(d.Before), After: snapshotValue(d.After)}
			}
			s.HeadersChanged = changed
		}
		if len(s.HeadersRemoved) > 0 {
			s.HeadersRemoved = slices.Clone(s.HeadersRemoved)
		}
		out[i] = s
	}
	return out
}

func snapshotUserHeaders(m message.Message) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k == message.Body || message.IsMetadata(k) {
			continue
		}
		out[k] = snapshotValue(v)
	}
	return out
}

func snapshotValue(v any) any {
	return snapshotValueDepth(v, 0)
}

func snapshotValueDepth(v any, depth int) any {
	if depth >= 64 || v == nil {
		return v
	}
	switch x := v.(type) {
	case []byte:
		return slices.Clone(x)
	case []any:
		c := make([]any, len(x))
		for i, e := range x {
			c[i] = snapshotValueDepth(e, depth+1)
		}
		return c
	case map[string]any:
		c := make(map[string]any, len(x))
		for k, e := range x {
			c[k] = snapshotValueDepth(e, depth+1)
		}
		return c
	case message.Message:
		c := make(message.Message, len(x))
		for k, e := range x {
			c[k] = snapshotValueDepth(e, depth+1)
		}
		return c
	default:
		return v
	}
}

func diffHeaders(before, after map[string]any) (added map[string]any, changed map[string]HeaderDiff, removed []string) {
	for k, vAfter := range after {
		vBefore, existed := before[k]
		if !existed {
			if added == nil {
				added = map[string]any{}
			}
			added[k] = vAfter
		} else if !reflect.DeepEqual(vBefore, vAfter) {
			if changed == nil {
				changed = map[string]HeaderDiff{}
			}
			changed[k] = HeaderDiff{Before: vBefore, After: vAfter}
		}
	}
	for k := range before {
		if _, exists := after[k]; !exists {
			removed = append(removed, k)
		}
	}
	if len(removed) > 1 {
		slices.Sort(removed)
	}
	return added, changed, removed
}
