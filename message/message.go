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
// slices) or XML (as a string). Metadata is internal to DIF; HTTP explicitly
// maps TraceID to DIF-Trace-Id without exposing other metadata.
//
// A Message is a plain map. Durable channels use a type-preserving encoding
// for supported values and reject unsupported values before admission.
type Message map[string]any

// Fixed keys.
const (
	Body = "body"

	// ContentType is the media type of the body, such as application/json.
	// It is a user header: steps that produce a known format set it, https
	// sends it outside and setheader can change it.
	ContentType = "Content-Type"

	// Identity headers travel with the logical message, including on retries.
	MessageID     = "Message-Id"
	CorrelationID = "Correlation-Id"
	CausationID   = "Causation-Id"

	// Request headers identify a single asynchronous exchange within a conversation.
	ReplyTo       = "Reply-To"
	RequestID     = "Request-Id"
	ReplyDeadline = "Reply-Deadline" // RFC 3339 nano, absolute response deadline
	ReplyStatus   = "Reply-Status"   // success, error, or timeout
	ReplyReason   = "Reply-Reason"   // classification of an unmatched response

	MetadataPrefix = "metadata."
	TraceID        = MetadataPrefix + "traceid"
	Timestamp      = MetadataPrefix + "timestamp" // RFC 3339 string

	// Trail lists the steps the message entered, as space-separated "kind:id"
	// entries, across flows: entering a flow adds "flow:id". It is a string, so
	// copies of a message never share a trail they append to.
	Trail = MetadataPrefix + "trail"
	Step  = MetadataPrefix + "step" // id of the step the message is in, or was last in

	// OriginalBody is the body as the message entered its current flow.
	OriginalBody = MetadataPrefix + "originalbody"

	// ExchangePattern is InOnly once the exchange of the message in its
	// current flow is one-way: its sender, if it waits for a reply, gets one
	// at once and the flow goes on without it. InOut, request-reply, is the
	// default.
	ExchangePattern = MetadataPrefix + "exchangepattern"
)

// Exchange patterns.
const (
	InOnly = "InOnly" // one-way, fire and forget
	InOut  = "InOut"  // request-reply
)

// New returns a Message with fresh message and trace IDs, the current time and body.
func New(body any) Message {
	m := Message{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Body:      body,
	}
	m.EnsureIdentity()
	return m
}

// EnsureIdentity initializes missing, empty or non-string message, correlation
// and trace IDs on a non-nil message. Supplied non-empty strings are preserved.
// A root's correlation ID defaults to its message ID; causation is optional.
func (m Message) EnsureIdentity() {
	for _, key := range []string{MessageID, TraceID} {
		if id, _ := m[key].(string); id == "" {
			m[key] = newTraceID()
		}
	}
	if id, _ := m[CorrelationID].(string); id == "" {
		m[CorrelationID] = m[MessageID]
	}
}

// Child copies m into a new logical message, retaining correlation and trace,
// and recording m as its cause. It initializes m's identity if needed. Like
// Copy, it shares payload values: processors must replace rather than mutate them.
func (m Message) Child(body any) Message {
	m.EnsureIdentity()
	c := m.Copy()
	c[MessageID] = newTraceID()
	c[CausationID] = m[MessageID]
	c[Timestamp] = time.Now().Format(time.RFC3339Nano)
	c[Body] = body
	return c
}

// Copy returns a copy of m to send down another path, such as a router's
// branch. Values are shared, which is safe as steps replace values rather than
// change them. All IDs are preserved; use Child to create a new logical message.
func (m Message) Copy() Message { return maps.Clone(m) }

// IsMetadata reports whether key is a metadata header.
func IsMetadata(key string) bool { return strings.HasPrefix(key, MetadataPrefix) }

func newTraceID() string {
	var b [16]byte
	rand.Read(b[:]) // never returns an error (Go 1.24+); a zero id is harmless anyway
	return hex.EncodeToString(b[:])
}
