package message

import (
	"regexp"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	m := New("hello")

	if m[Body] != "hello" {
		t.Errorf("body = %v, want hello", m[Body])
	}
	if id, _ := m[TraceID].(string); !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		t.Errorf("traceid = %q, want 32 hex chars", id)
	}
	if ts, _ := m[Timestamp].(string); ts == "" {
		t.Error("timestamp is empty")
	} else if _, err := time.Parse(time.RFC3339Nano, ts); err != nil {
		t.Errorf("timestamp: %v", err)
	}
	if New(nil)[TraceID] == m[TraceID] {
		t.Error("two messages share a traceid")
	}
}

func TestIsMetadata(t *testing.T) {
	for key, want := range map[string]bool{TraceID: true, Timestamp: true, Body: false, "greeting": false, "Metadata.x": false} {
		if got := IsMetadata(key); got != want {
			t.Errorf("IsMetadata(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestCopy(t *testing.T) {
	m := New("x")
	m["h"] = "v"
	c := m.Copy()
	c[Body], c["h2"] = "y", "w"
	delete(c, "h")
	if m[Body] != "x" || m["h"] != "v" || m["h2"] != nil {
		t.Errorf("changing the copy changed the original: %v", m)
	}
	if c[TraceID] != m[TraceID] {
		t.Error("the copy has another trace id")
	}
}
