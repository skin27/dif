package impl

import (
	"net/http"

	"dif/message"
)

// traceIDHeader is DIF's transport mapping, not a W3C traceparent header.
const traceIDHeader = "DIF-Trace-Id"

// writeIdentityHeaders uses the canonical message keys as the authority.
// It never exports other metadata or invalid HTTP header values.
func writeIdentityHeaders(h http.Header, m message.Message) {
	for _, key := range []string{message.MessageID, message.CorrelationID, message.CausationID, message.TraceID} {
		name := key
		if key == message.TraceID {
			name = traceIDHeader
		}
		h.Del(name)
		if id, _ := m[key].(string); id != "" && validHeader(name, id) {
			h.Set(name, id)
		}
	}
}
