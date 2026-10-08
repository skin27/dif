package impl

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestSetUUID(t *testing.T) {
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	m := process(t, "setuuid", nil, message.New("b"))
	first, _ := m["UUID"].(string)
	if !uuid.MatchString(first) {
		t.Errorf("UUID = %q, want a version 4 UUID", first)
	}
	m = process(t, "setuuid", map[string]any{"headerName": "id", "generator": "classic"}, m)
	if second, _ := m["id"].(string); !uuid.MatchString(second) || second == first || m["UUID"] != first {
		t.Errorf("headers = %v, want a new UUID in id", m)
	}
	wantInvalid(t, stepdef.Action, "setuuid", map[string]any{"headerName": "body"}, "reserved for the body")
}

func TestSetBodyByHeaderAndBack(t *testing.T) {
	m := message.New("old")
	m["foo"] = map[string]any{"a": 1.0}
	m = process(t, "setbodybyheader", map[string]any{"headerName": "foo"}, m)
	if b, ok := m[message.Body].(map[string]any); !ok || b["a"] != 1.0 {
		t.Errorf("body = %#v, want the header's JSON as is", m[message.Body])
	}
	if m = process(t, "setbodybyheader", map[string]any{"headerName": "none"}, m); text(m[message.Body]) != "" {
		t.Errorf("body = %#v, want empty for a missing header", m[message.Body])
	}

	m = process(t, "setheaderbybody", map[string]any{"headerName": "copy"}, message.New([]byte("raw")))
	if b, ok := m["copy"].([]byte); !ok || string(b) != "raw" {
		t.Errorf("header = %#v, want the body as is", m["copy"])
	}

	wantInvalid(t, stepdef.Action, "setbodybyheader", nil, "missing required option headerName")
	wantInvalid(t, stepdef.Action, "setheaderbybody", map[string]any{"headerName": "body"}, "reserved for the body")
	wantInvalid(t, stepdef.Action, "setheaderbybody", map[string]any{"headerName": "metadata.step"}, "reserved for metadata")
}

func TestWastebin(t *testing.T) {
	routes := route(t, "wastebin", nil, []stepdef.Link{{}}, message.New("x"))
	if len(routes) != 0 {
		t.Errorf("routes = %v, want none", routes)
	}
	sink := mustProcessor(t, stepdef.Sink, "wastebin", nil).(stepdef.SinkProcessor)
	if err := sink.Consume(context.Background(), message.New("x")); err != nil {
		t.Error(err)
	}
}

func TestDelay(t *testing.T) {
	p := mustProcessor(t, stepdef.Sink, "delay", map[string]any{"milliseconds": "30"}).(stepdef.ActionProcessor)
	start := time.Now()
	if out, err := p.Process(context.Background(), message.New("x")); err != nil || out[message.Body] != "x" {
		t.Fatalf("out = %v, %v", out, err)
	}
	if d := time.Since(start); d < 30*time.Millisecond {
		t.Errorf("passed on after %v, want 30ms", d)
	}

	p = mustProcessor(t, stepdef.Action, "delay", nil).(stepdef.ActionProcessor) // 5 s
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Process(ctx, message.New("x")); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's error", err)
	}
	wantInvalid(t, stepdef.Action, "delay", map[string]any{"milliseconds": -1.0}, "-1 is less than 0")
}

func TestLogger(t *testing.T) {
	buf := captureLog(t)
	m := message.New("hi")
	m["foo"] = "bar"
	process(t, "logger", map[string]any{"loggingLevel": "WARN", "language": "simple", "expression": "body ${body}, foo ${header.foo}"}, m)
	process(t, "logger", nil, m)
	process(t, "logger", map[string]any{"language": "constant", "expression": "${body}"}, m)
	process(t, "logger", map[string]any{"loggingLevel": "OFF"}, m)
	want := "step test-step: WARN body hi, foo bar\nstep test-step: INFO hi\nstep test-step: INFO ${body}\n"
	if buf.String() != want {
		t.Errorf("log = %q, want %q", buf.String(), want)
	}
	wantInvalid(t, stepdef.Action, "logger", map[string]any{"loggingLevel": "LOUD"}, "loggingLevel")
	wantInvalid(t, stepdef.Action, "logger", map[string]any{"expression": "${exchangeId}"}, "unsupported simple expression")
}

func TestSimpleValidator(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "simplevalidator", map[string]any{"expression": "${body} contains 'hello'"}).(stepdef.ActionProcessor)
	if out, err := p.Process(context.Background(), message.New("hello world")); err != nil || out[message.Body] != "hello world" {
		t.Errorf("valid: %v, %v", out, err)
	}
	if _, err := p.Process(context.Background(), message.New("bye")); err == nil || err.Error() != "validation failed: ${body} contains 'hello'" {
		t.Errorf("invalid: err = %v", err)
	}
	wantInvalid(t, stepdef.Action, "simplevalidator", nil, "missing required option expression")
	wantInvalid(t, stepdef.Action, "simplevalidator", map[string]any{"expression": " "}, "empty condition")
	wantInvalid(t, stepdef.Action, "simplevalidator", map[string]any{"expression": "${a} == 1 and ${b} == 2"}, "combine conditions with && and ||")
}

func TestCounter(t *testing.T) {
	src := mustProcessor(t, stepdef.Source, "counter", map[string]any{"start": "5", "numbers": "3", "period": "1"}).(stepdef.SourceProcessor)
	var got []string
	src.Run(context.Background(), func(m message.Message, _ func(message.Message, error)) error {
		got = append(got, text(m[message.Body])+" "+text(m[message.ContentType]))
		return nil
	})
	if s := strings.Join(got, ", "); s != "5 text/plain, 6 text/plain, 7 text/plain" {
		t.Errorf("emitted %s", s)
	}
	if got, want := mustProcessor(t, stepdef.Source, "counter", nil), (timerSource{period: 10 * time.Second, repeatCount: 1, contentType: "text/plain"}); got != want {
		t.Errorf("counter = %+v, want %+v", got, want)
	}
}
