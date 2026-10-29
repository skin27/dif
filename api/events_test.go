package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

// startIn loads the flow at path into rt and starts it; its results arrive on
// the returned channel.
func startIn(t *testing.T, rt *Runtime, path string) (*Flow, chan outcome) {
	t.Helper()
	results := make(chan outcome, 100)
	f, err := rt.Load(path, func(res *Result, err error) { results <- outcome{res, err} })
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f.State() != Stopped {
			f.Stop()
		}
	})
	return f, results
}

// TestEventSource: a flow with an event source reacts to the failure and the
// stop of another flow, with the identity of the message that failed.
func TestEventSource(t *testing.T) {
	rt, err := NewRuntime(ChannelConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	alerts := dil(t, "alerts",
		step{"on-failure", "source", "event:exchange.failed", map[string]any{"flowId": "orders"}},
		step{"alert", "sink", "setbody", map[string]any{"expression": "alert"}},
	)
	stops := dil(t, "stops",
		step{"on-stop", "source", "event:flow.stopped", nil},
		step{"note", "sink", "setbody", map[string]any{"expression": "stopped"}},
	)
	orders := dil(t, "orders",
		step{"in", "source", "message:in", nil},
		step{"check", "action", "simplevalidator", map[string]any{"expression": "${body} == 'ok'"}},
		step{"done", "sink", "setbody", map[string]any{"expression": "done"}},
	)
	_, alertResults := startIn(t, rt, alerts)
	_, stopResults := startIn(t, rt, stops)
	flow, _ := startIn(t, rt, orders)

	good := flow.NewMessage()
	good[Body] = "ok"
	if _, err := flow.Request(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	bad := flow.NewMessage()
	bad[Body] = "bad"
	bad["Correlation-Id"] = "order-17"
	if _, err := flow.Request(context.Background(), bad); err == nil {
		t.Fatal("the bad message did not fail")
	}

	res := await(t, alertResults)
	m := res.Message
	if m["event.type"] != "exchange.failed" || m["event.flow.id"] != "orders" || m["event.step"] != "check" ||
		m["Correlation-Id"] != "order-17" || m["Causation-Id"] != bad["Message-Id"] ||
		!strings.Contains(text(m["error.message"]), "validation failed") || !strings.Contains(text(m["metadata.originalbody"]), `"type":"exchange.failed"`) {
		t.Errorf("alert message = %v", m)
	}
	select {
	case o := <-alertResults:
		t.Errorf("a second alert %+v; only the failed message gives one", o)
	case <-time.After(50 * time.Millisecond):
	}

	if err := flow.Stop(); err != nil {
		t.Fatal(err)
	}
	stop := await(t, stopResults).Message
	if stop["event.type"] != "flow.stopped" || stop["event.flow.id"] != "orders" {
		t.Errorf("stop message = %v", stop)
	}
	if n := rt.DroppedEvents(); n != 0 {
		t.Errorf("%d events dropped", n)
	}
}

// Events of a flow nobody listens to are not made, and a flow without
// subscribers is not slowed down by them.
func TestEventsWithoutSubscribers(t *testing.T) {
	rt, err := NewRuntime(ChannelConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	flow, results := startIn(t, rt, dil(t, "quiet",
		step{"in", "source", "message:in", nil},
		step{"done", "sink", "setbody", map[string]any{"expression": "done"}},
	))
	if _, err := flow.Request(context.Background(), flow.NewMessage()); err != nil {
		t.Fatal(err)
	}
	await(t, results)
	if err := flow.Stop(); err != nil || rt.DroppedEvents() != 0 {
		t.Errorf("stop: %v, %d events dropped", err, rt.DroppedEvents())
	}
}

func TestEventSourceNames(t *testing.T) {
	rt, err := NewRuntime(ChannelConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	path := dil(t, "bad", step{"on", "source", "event:flow.exploded", nil}, step{"x", "sink", "setbody", nil})
	if _, err := rt.Load(path, nil); err == nil || !strings.Contains(err.Error(), `event "flow.exploded" is not one of`) {
		t.Errorf("err = %v, want the unknown event type", err)
	}
}
