package impl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestSplitIdentity(t *testing.T) {
	root := message.New(`[[1,2],[3,4]]`)
	opts := map[string]any{"language": "jsonpath", "expression": "$"}
	parts := route(t, "split", opts, []stepdef.Link{{}}, root)
	if len(parts) != 2 {
		t.Fatalf("parts = %d", len(parts))
	}
	seen := map[any]bool{root[message.MessageID]: true}
	for _, part := range parts {
		children := route(t, "split", opts, []stepdef.Link{{}}, part.Message)
		if len(children) != 2 {
			t.Fatalf("nested parts = %d", len(children))
		}
		for _, pair := range [][2]message.Message{{root, part.Message}, {part.Message, children[0].Message}, {part.Message, children[1].Message}} {
			parent, child := pair[0], pair[1]
			if seen[child[message.MessageID]] || child[message.CausationID] != parent[message.MessageID] || child[message.CorrelationID] != root[message.CorrelationID] || child[message.TraceID] != root[message.TraceID] {
				t.Fatalf("bad split lineage: %v", child)
			}
			seen[child[message.MessageID]] = true
		}
	}
}

func TestHTTPIdentityRoundTrip(t *testing.T) {
	m := message.New("payload").Child("child")
	m[message.Trail] = "private"
	m[traceIDHeader] = "stale"
	got := make(chan message.Message, 1)
	source := httpsSource{}
	server := httptest.NewTLSServer(source.handler(func(in message.Message, reply func(message.Message, error)) error {
		got <- in.Copy()
		if reply != nil {
			reply(in, nil)
		}
		return nil
	}))
	a := httpsAction{url: server.URL, method: http.MethodPost, client: server.Client()}
	out, err := a.Process(context.Background(), m)
	server.Close()
	if err != nil {
		t.Fatal(err)
	}
	in := <-got
	for _, key := range []string{message.MessageID, message.CorrelationID, message.CausationID, message.TraceID} {
		if in[key] != m[key] || out[key] != m[key] {
			t.Errorf("lost %s", key)
		}
	}
	if in[message.Trail] != nil || in[http.CanonicalHeaderKey(traceIDHeader)] != nil {
		t.Errorf("leaked metadata or retained transport alias: %v", in)
	}
}

func TestHTTPPartialIdentityAndReply(t *testing.T) {
	for _, supplied := range []bool{false, true} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("body"))
		if supplied {
			r.Header.Set("message-id", "external-message")
			r.Header.Set("dif-trace-id", "external-trace")
		}
		w := httptest.NewRecorder()
		var received message.Message
		httpsSource{}.handler(func(m message.Message, reply func(message.Message, error)) error {
			received = m.Copy()
			reply(m, nil)
			return nil
		})(w, r)
		if supplied && (received[message.MessageID] != "external-message" || received[message.TraceID] != "external-trace") {
			t.Fatalf("overwrote supplied identity: %v", received)
		}
		if received[message.CorrelationID] != received[message.MessageID] {
			t.Fatal("correlation did not default to received message ID")
		}
		for _, key := range []string{message.MessageID, message.CorrelationID, message.TraceID} {
			name := key
			if key == message.TraceID {
				name = traceIDHeader
			}
			if w.Header().Get(name) == "" || w.Header().Get(name) != received[key] {
				t.Errorf("reply lost %s", key)
			}
		}
	}
}

func TestHTTPIdentityRejectsInvalidValues(t *testing.T) {
	m := message.New("body")
	m[message.CausationID] = "bad\r\nInjected: value"
	m[message.TraceID] = "bad\ntrace"
	h := http.Header{}
	h.Set(traceIDHeader, "stale")
	writeIdentityHeaders(h, m)
	if h.Get(message.CausationID) != "" || h.Get(traceIDHeader) != "" || h.Get(message.MessageID) != m[message.MessageID] {
		t.Fatalf("unsafe identity export: %v", h)
	}
}
