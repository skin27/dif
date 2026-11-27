package engine

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// Event is something that happened to a flow or to one of its messages.
type Event struct {
	Type     stepdef.EventType
	Time     time.Time
	FlowID   string
	FlowName string

	// For exchange and step events: how long it took and the identity of the
	// exchange. Step events and failed or handled exchanges also name the step.
	Duration      time.Duration
	MessageID     string
	CorrelationID string
	TraceID       string
	Step          string
	URI           string

	// Err is the failure of an exchange, a step, or a source; nil for the other events.
	Err error
}

// Observer receives the events of the flows it is set on. Wants is asked
// before an event is made, so an observer that nobody listens to costs a call.
// Observe is called on the goroutines of the flow and must not block.
type Observer interface {
	Wants(stepdef.EventType) bool
	Observe(Event)
}

// observerRef lets a Runner hold an Observer in an atomic pointer.
type observerRef struct{ Observer }

// SetObserver makes o receive the events of the flow; nil stops it. It may be
// called at any time, and a flow without an observer reports nothing.
func (r *Runner) SetObserver(o Observer) {
	if o == nil {
		r.observer.Store(nil)
		return
	}
	r.observer.Store(&observerRef{o})
}

// notify reports an event of the flow to its observer, if it wants it.
func (r *Runner) notify(e Event) {
	o := r.observer.Load()
	if o == nil || !o.Wants(e.Type) {
		return
	}
	e.Time = time.Now()
	e.FlowID, e.FlowName = r.flow.ID, r.flow.Name
	o.Observe(e)
}

// exchangeEvent reports how a message ended: failed, handled by an error route
// or completed. m is the message as it entered the flow, id its identity taken
// before it ran.
func (r *Runner) exchangeEvent(id exchangeIdentity, duration time.Duration, res *Result, err error) {
	e := Event{Type: stepdef.EventExchangeCompleted, Duration: duration,
		MessageID: id.message, CorrelationID: id.correlation, TraceID: id.trace}
	switch {
	case err != nil:
		e.Type, e.Err = stepdef.EventExchangeFailed, err
	case res != nil && res.Err != nil:
		e.Type, e.Err = stepdef.EventExchangeHandled, res.Err
	}
	var se *StepError
	if errors.As(e.Err, &se) {
		e.Step = se.Step
	}
	r.notify(e)
}

// exchangeIdentity is the identity of a message, as the event of its exchange
// carries it.
type exchangeIdentity struct{ message, correlation, trace string }

// identityOf returns the identity of m, and false for a message that is part
// of handling an event.
func identityOf(m message.Message) (exchangeIdentity, bool) {
	if _, handlingEvent := m[stepdef.EventHeader]; handlingEvent {
		return exchangeIdentity{}, false
	}
	text := func(key string) string { s, _ := m[key].(string); return s }
	return exchangeIdentity{text(message.MessageID), text(message.CorrelationID), text(message.TraceID)}, true
}

// stepObserver reports step.completed and step.failed events for one message
// when the runner's observer wants at least one of them.
type stepObserver struct {
	runner   *Runner
	identity exchangeIdentity
	wantDone bool
	wantFail bool
}

func stepScheme(uri string) string {
	scheme, _, _ := strings.Cut(uri, ":")
	return scheme
}

func (s *stepObserver) completed(step, uri string, duration time.Duration) {
	if !s.wantDone {
		return
	}
	s.runner.notify(Event{
		Type:          stepdef.EventStepCompleted,
		Duration:      duration,
		MessageID:     s.identity.message,
		CorrelationID: s.identity.correlation,
		TraceID:       s.identity.trace,
		Step:          step,
		URI:           stepScheme(uri),
	})
}

func (s *stepObserver) failed(step, uri string, duration time.Duration, err error) {
	if !s.wantFail {
		return
	}
	s.runner.notify(Event{
		Type:          stepdef.EventStepFailed,
		Duration:      duration,
		MessageID:     s.identity.message,
		CorrelationID: s.identity.correlation,
		TraceID:       s.identity.trace,
		Step:          step,
		URI:           stepScheme(uri),
		Err:           err,
	})
}

// Message makes the message of the event, for a flow to process. Its headers
// are event.type, event.time, event.flow.id, event.flow.name and, as they
// apply, event.step, event.uri, event.duration (milliseconds) and the error.*
// headers of an error route. An exchange or step event takes the correlation
// and trace of its exchange and names the exchange's message as its cause.
// The body is a JSON summary; it never holds the payload of the exchange.
func (e Event) Message() message.Message {
	summary := struct {
		Type       stepdef.EventType `json:"type"`
		Time       string            `json:"time"`
		FlowID     string            `json:"flowId,omitempty"`
		FlowName   string            `json:"flowName,omitempty"`
		Step       string            `json:"step,omitempty"`
		URI        string            `json:"uri,omitempty"`
		DurationMs *int64            `json:"durationMs,omitempty"`
		Error      string            `json:"error,omitempty"`
		ErrorClass string            `json:"errorClass,omitempty"`
	}{Type: e.Type, Time: e.Time.Format(time.RFC3339Nano), FlowID: e.FlowID, FlowName: e.FlowName, Step: e.Step, URI: e.URI}

	m := message.New(nil)
	m[stepdef.EventHeader] = string(e.Type)
	m["event.time"] = summary.Time
	m["event.flow.id"], m["event.flow.name"] = e.FlowID, e.FlowName
	if e.Step != "" {
		m["event.step"] = e.Step
	}
	if e.URI != "" {
		m["event.uri"] = e.URI
	}
	if e.MessageID != "" {
		m[message.CausationID] = e.MessageID
	}
	if e.CorrelationID != "" {
		m[message.CorrelationID] = e.CorrelationID
	}
	if e.TraceID != "" {
		m[message.TraceID] = e.TraceID
	}
	switch e.Type {
	case stepdef.EventExchangeCompleted, stepdef.EventExchangeHandled, stepdef.EventExchangeFailed,
		stepdef.EventStepCompleted, stepdef.EventStepFailed:
		ms := e.Duration.Milliseconds()
		summary.DurationMs = &ms
		m["event.duration"] = int(ms)
	}
	if e.Err != nil {
		var se *StepError
		cause := e.Err
		if errors.As(e.Err, &se) {
			cause = se.Err
		}
		m[ErrorMessage], m[ErrorClass], m[ErrorStackTrace] = cause.Error(), errorClass(cause), e.Err.Error()
		if e.Step != "" {
			m[ErrorStep] = e.Step
		}
		summary.Error, summary.ErrorClass = cause.Error(), errorClass(cause)
	}
	if body, err := json.Marshal(summary); err == nil {
		m[message.Body] = string(body)
		m[message.ContentType] = "application/json"
	}
	return m
}
