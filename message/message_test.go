package message

import (
	"regexp"
	"testing"
)

func TestNew(t *testing.T) {
	m := New("hello")

	if m.Body != "hello" {
		t.Errorf("body = %v, want hello", m.Body)
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(m.TraceID) {
		t.Errorf("traceid = %q, want 32 hex chars", m.TraceID)
	}
	if m.Timestamp.IsZero() {
		t.Error("timestamp is zero")
	}
	if m.Headers == nil {
		t.Error("headers map is nil")
	}
	if New(nil).TraceID == m.TraceID {
		t.Error("two messages share a traceid")
	}
}
