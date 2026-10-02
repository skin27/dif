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
