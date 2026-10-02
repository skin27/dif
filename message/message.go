// Package message defines the Message that flows through every step.
package message

import (
	"crypto/rand"
	"encoding/hex"
	"maps"
	"strings"
	"time"
)

// Message is the unit of data passed between steps: one mutable map holding
// the body (key Body), user-defined headers and metadata headers (keys
// prefixed with MetadataPrefix). Keys are case-sensitive.
//
// Header values are strings, booleans, ints, []byte, decoded JSON (maps and
// slices) or XML (as a string). Metadata is internal to DIF and is never sent
// outside a flow.
//
// A Message is a plain map so it can be serialized, which is the extension
// point for persisting state in a later iteration.
type Message map[string]any

// Fixed keys.
const (
	Body           = "body"
	MetadataPrefix = "metadata."
	TraceID        = MetadataPrefix + "traceid"
	Timestamp      = MetadataPrefix + "timestamp" // RFC 3339 string
)

// New returns a Message with a fresh trace id, the current time and the given body.
func New(body any) Message {
	return Message{
		TraceID:   newTraceID(),
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Body:      body,
	}
}

// Copy returns a copy of m to send down another path, such as a router's
// branch. Values are shared, which is safe as steps replace values rather than
// change them.
func (m Message) Copy() Message { return maps.Clone(m) }

// IsMetadata reports whether key is a metadata header.
func IsMetadata(key string) bool { return strings.HasPrefix(key, MetadataPrefix) }

func newTraceID() string {
	var b [16]byte
	rand.Read(b[:]) // never returns an error (Go 1.24+); a zero id is harmless anyway
	return hex.EncodeToString(b[:])
}
