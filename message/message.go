// Package message defines the Message that flows through every step.
package message

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Message is the unit of data passed between steps.
//
// All fields are exported and JSON-tagged so a Message can be serialized,
// which is the extension point for persisting state in a later iteration.
type Message struct {
	TraceID   string            `json:"traceid"`
	Timestamp time.Time         `json:"timestamp"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      any               `json:"body,omitempty"`
}

// New returns a Message with a fresh trace id, the current time and the given body.
func New(body any) *Message {
	return &Message{
		TraceID:   newTraceID(),
		Timestamp: time.Now(),
		Headers:   map[string]string{},
		Body:      body,
	}
}

func newTraceID() string {
	var b [16]byte
	rand.Read(b[:]) // never returns an error (Go 1.24+); a zero id is harmless anyway
	return hex.EncodeToString(b[:])
}
