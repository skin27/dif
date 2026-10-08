package message_test

import (
	"regexp"
	"testing"
	"time"

	"dif/message"
)

func TestNew(t *testing.T) {
	m := message.New("hello")

	if m[message.Body] != "hello" {
		t.Errorf("body = %v, want hello", m[message.Body])
	}
	if id, _ := m[message.TraceID].(string); !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		t.Errorf("traceid = %q, want 32 hex chars", id)
	}
	if ts, _ := m[message.Timestamp].(string); ts == "" {
		t.Error("timestamp is empty")
	} else if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Errorf("timestamp: %v", err)
	}
	if message.New(nil)[message.TraceID] == m[message.TraceID] {
		t.Error("two messages share a traceid")
	}
}

func TestIsMetadata(t *testing.T) {
	for key, want := range map[string]bool{message.TraceID: true, message.Timestamp: true, message.Trail: true, message.Step: true, message.OriginalBody: true, message.Body: false, message.ContentType: false, "greeting": false, "Metadata.x": false} {
		if got := message.IsMetadata(key); got != want {
			t.Errorf("IsMetadata(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestCopy(t *testing.T) {
	m := message.New("x")
	m["h"] = "v"
	c := m.Copy()
	c[message.Body], c["h2"] = "y", "w"
	delete(c, "h")
	if m[message.Body] != "x" || m["h"] != "v" || m["h2"] != nil {
		t.Errorf("changing the copy changed the original: %v", m)
	}
	if c[message.TraceID] != m[message.TraceID] {
		t.Error("the copy has another trace id")
	}
}
