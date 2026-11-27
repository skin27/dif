package engine

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// recorder is an observer that keeps the events it wants, in order.
type recorder struct {
	mu     sync.Mutex
	events chan Event
	wanted map[stepdef.EventType]bool // nil: every type
	asked  int
}

func newRecorder(types ...stepdef.EventType) *recorder {
	r := &recorder{events: make(chan Event, 100)}
	if len(types) > 0 {
		r.wanted = map[stepdef.EventType]bool{}
		for _, t := range types {
			r.wanted[t] = true
		}
	}
	return r
}

func (r *recorder) Wants(t stepdef.EventType) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked++
	return r.wanted == nil || r.wanted[t]
}

func (r *recorder) Observe(e Event) { r.events <- e }

// next returns the next event, failing the test if none comes soon.
func (r *recorder) next(t *testing.T) Event {
	t.Helper()
	select {
	case e := <-r.events:
		return e
	case <-time.After(time.Second):
		t.Fatal("no event within 1s")
		return Event{}
	}
}

// none checks that no event arrives.
func (r *recorder) none(t *testing.T) {
	t.Helper()
	select {
	case e := <-r.events:
		t.Fatalf("unexpected event %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEventsOfTheLifecycle(t *testing.T) {
	r, _, _ := setup()
	r.flow.ID, r.flow.Name = "f1", "First"
	rec := newRecorder()
	r.SetObserver(rec)

	must(t, r.Start())
	must(t, r.Pause())
	must(t, r.Resume())
	must(t, r.Stop())

	for _, want := range []stepdef.EventType{stepdef.EventFlowStarted, stepdef.EventFlowPaused, stepdef.EventFlowResumed, stepdef.EventFlowStopped} {
		e := rec.next(t)
		if e.Type != want || e.FlowID != "f1" || e.FlowName != "First" || e.Time.IsZero() {
			t.Errorf("event = %+v, want %s of flow f1", e, want)
		}
	}
	rec.none(t)
}

func TestEventOfASourceFailure(t *testing.T) {
	want := errors.New("bind failed")
	r := NewRunner(flow(readyTestSource{entered: make(chan struct{}), err: want}), nil)
	rec := newRecorder(stepdef.EventSourceFailed)
	r.SetObserver(rec)
	r.Start()
	e := rec.next(t)
	if e.Type != stepdef.EventSourceFailed || !errors.Is(e.Err, want) {
		t.Errorf("event = %+v, want the source failure", e)
	}
	r.Stop()
}

// exchanges starts a flow a -> x -> sink, whose x fails the body "fail" and
// whose error route (if handled) takes the failure, and returns it with its observer.
func exchanges(t *testing.T, handledByRoute bool) (*Runner, *recorder) {
	t.Helper()
	var ran []string
	f := withError(&ran, failOn{}, 0)
	f.ID, f.Name = "f1", "First"
	if !handledByRoute {
		f.Error = nil
	}
	rec := newRecorder(stepdef.EventExchangeCompleted, stepdef.EventExchangeHandled, stepdef.EventExchangeFailed)
	r := started(t, f, nil)
	r.SetObserver(rec)
	return r, rec
}

func TestEventsOfExchanges(t *testing.T) {
	r, rec := exchanges(t, true)

	ok := message.New("ok")
	ok[message.CorrelationID] = "order-1"
	must(t, r.Send(ok))
	e := rec.next(t)
	if e.Type != stepdef.EventExchangeCompleted || e.Err != nil || e.Step != "" ||
		e.MessageID != ok[message.MessageID] || e.CorrelationID != "order-1" || e.TraceID != ok[message.TraceID] {
		t.Errorf("event = %+v, want a completed exchange with the identity of the message", e)
	}

	must(t, r.Send(message.New("fail")))
	e = rec.next(t)
	if e.Type != stepdef.EventExchangeHandled || e.Step != "x" || e.Err == nil || !strings.Contains(e.Err.Error(), "step x: ") {
		t.Errorf("event = %+v, want the failure of x handled by the error route", e)
	}
}

func TestEventOfAFailedExchange(t *testing.T) {
	r, rec := exchanges(t, false)
	must(t, r.Send(message.New("fail")))
	e := rec.next(t)
	if e.Type != stepdef.EventExchangeFailed || e.Step != "x" || e.Err == nil || e.Duration < 0 {
		t.Errorf("event = %+v, want a failed exchange at x", e)
	}
}

// A message that is part of handling an event makes no exchange event, so a
// flow that reacts to events cannot trigger itself.
func TestNoEventsForMessagesHandlingEvents(t *testing.T) {
	r, rec := exchanges(t, true)
	handling := message.New("ok")
	handling[stepdef.EventHeader] = string(stepdef.EventExchangeFailed)
	must(t, r.Send(handling))
	must(t, r.Send(message.New("ok")))

	e := rec.next(t)
	if e.MessageID == handling[message.MessageID] {
		t.Errorf("event = %+v, want the one for the plain message only", e)
	}
	rec.none(t)
}

func TestEventsOnlyWhenWanted(t *testing.T) {
	r, _ := exchanges(t, true)
	rec := newRecorder(stepdef.EventFlowStopped)
	r.SetObserver(rec)
	must(t, r.Send(message.New("ok")))
	must(t, r.Send(message.New("fail")))
	rec.none(t)

	must(t, r.Stop())
	if e := rec.next(t); e.Type != stepdef.EventFlowStopped {
		t.Errorf("event = %+v, want the stop", e)
	}
}

func TestEventsStopWithoutAnObserver(t *testing.T) {
	r, rec := exchanges(t, true)
	r.SetObserver(nil)
	must(t, r.Send(message.New("ok")))
	rec.none(t)
}

func TestEventMessage(t *testing.T) {
	boom := &StepError{Step: "x", Message: message.Message{}, Err: errors.New("boom"), URI: "test:x"}
	e := Event{Type: stepdef.EventExchangeFailed, Time: time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC), FlowID: "f1", FlowName: "First",
		Step: "x", Duration: 1500 * time.Millisecond, MessageID: "m1", CorrelationID: "c1", TraceID: "t1", Err: boom}
	m := e.Message()

	for key, want := range map[string]any{
		stepdef.EventHeader: "exchange.failed", "event.time": "2026-10-29T12:00:00Z", "event.flow.id": "f1", "event.flow.name": "First",
		"event.step": "x", "event.duration": 1500, message.CausationID: "m1", message.CorrelationID: "c1", message.TraceID: "t1",
		ErrorMessage: "boom", ErrorStep: "x", ErrorClass: "*errors.errorString", ErrorStackTrace: "step x: boom", message.ContentType: "application/json",
	} {
		if m[key] != want {
			t.Errorf("header %s = %v, want %v", key, m[key], want)
		}
	}
	if m[message.MessageID] == "m1" || m[message.MessageID] == "" {
		t.Errorf("Message-Id = %v, want a new one", m[message.MessageID])
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(m[message.Body].(string)), &body); err != nil {
		t.Fatal(err)
	}
	if body["type"] != "exchange.failed" || body["flowId"] != "f1" || body["step"] != "x" || body["durationMs"] != float64(1500) || body["error"] != "boom" {
		t.Errorf("body = %v", body)
	}

	// A lifecycle event has no exchange and no failure.
	m = Event{Type: stepdef.EventFlowStopped, Time: e.Time, FlowID: "f1"}.Message()
	for _, key := range []string{ErrorMessage, "event.step", "event.duration", message.CausationID} {
		if _, ok := m[key]; ok {
			t.Errorf("a stop event has the header %s", key)
		}
	}
}

// Events only matter to flows that can be seen from the outside: the observer
// is asked first, and nothing is built for an event it does not want.
func TestObserverIsAskedFirst(t *testing.T) {
	var ran []string
	f := withError(&ran, failOn{}, 0)
	rec := newRecorder(stepdef.EventFlowStopped)
	r := started(t, f, nil)
	r.SetObserver(rec)
	must(t, r.Send(message.New("ok")))
	eventually(t, func() bool { rec.mu.Lock(); defer rec.mu.Unlock(); return rec.asked > 0 })
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("condition not met within 1s")
}

func TestStepEvents(t *testing.T) {
	var ran []string
	f := withError(&ran, failOn{}, 0)
	f.ID, f.Name = "f1", "First"
	f.Source.Next[0].URI = "https://user:secret@example.com/api"
	f.Source.Next[0].Next[0].URI = "log"

	rec := newRecorder(stepdef.EventStepCompleted, stepdef.EventStepFailed)
	r := started(t, f, nil)
	r.SetObserver(rec)

	ok := message.New("ok")
	ok[message.CorrelationID] = "corr-1"
	must(t, r.Send(ok))

	e1 := rec.next(t)
	e2 := rec.next(t)
	if e1.Type != stepdef.EventStepCompleted || e1.Step != "x" || e1.URI != "https" || e1.CorrelationID != "corr-1" || e1.MessageID != ok[message.MessageID] {
		t.Errorf("e1 = %+v, want step.completed for x with scheme https", e1)
	}
	if e2.Type != stepdef.EventStepCompleted || e2.Step != "y" || e2.URI != "log" {
		t.Errorf("e2 = %+v, want step.completed for y with scheme log", e2)
	}

	// Verify event.uri never leaks credentials and event.duration is set.
	msg := e1.Message()
	if msg["event.step"] != "x" || msg["event.uri"] != "https" || msg["event.duration"] == nil {
		t.Errorf("step.completed message = %v", msg)
	}
	if strings.Contains(msg[message.Body].(string), "secret") {
		t.Errorf("step.completed body leaked URI credentials: %s", msg[message.Body])
	}

	// Failing step emits step.failed with error.* headers, then error route step emits step.completed.
	must(t, r.Send(message.New("fail")))
	ef := rec.next(t)
	if ef.Type != stepdef.EventStepFailed || ef.Step != "x" || ef.URI != "https" || ef.Err == nil {
		t.Errorf("ef = %+v, want step.failed for x", ef)
	}
	fmsg := ef.Message()
	if fmsg["event.step"] != "x" || fmsg["event.uri"] != "https" || fmsg[ErrorMessage] != "boom" || fmsg[ErrorStep] != "x" {
		t.Errorf("step.failed message = %v", fmsg)
	}
	// Error route sink runs after step.failed.
	er := rec.next(t)
	if er.Type != stepdef.EventStepCompleted || er.Step != "z" {
		t.Errorf("er = %+v, want step.completed for error route z", er)
	}
	rec.none(t)
}

func TestNoStepEventsForMessagesHandlingEvents(t *testing.T) {
	var ran []string
	f := withError(&ran, failOn{}, 0)
	f.ID = "f1"
	rec := newRecorder() // all event types, including step.* and exchange.*
	r := started(t, f, nil)
	r.SetObserver(rec)

	handling := message.New("ok")
	handling[stepdef.EventHeader] = string(stepdef.EventStepFailed)
	must(t, r.Send(handling))
	rec.none(t)
}
