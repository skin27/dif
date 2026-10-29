package api

import (
	"context"
	"strings"
	"testing"
)

// TestErrorHandlersByStep loads a flow whose two validations are taken by
// different error steps: a failure of the first still fails the message after
// its error route (handled false), a failure of the second is handled.
func TestErrorHandlersByStep(t *testing.T) {
	link := func(id, bound string) map[string]any { return map[string]any{"id": id, "bound": bound} }
	links := func(l ...map[string]any) map[string]any { return map[string]any{"link": l} }
	path := writeDIL(t, "handlers", []map[string]any{
		{"id": "in", "type": "source", "uri": "message:in", "links": links(link("check-id", "out"))},
		{"id": "check-id", "type": "action", "uri": "simplevalidator", "options": map[string]any{"expression": "${header.orderId} regex '[0-9]+'"},
			"links": links(link("check-id", "in"), link("check-payload", "out"))},
		{"id": "check-payload", "type": "action", "uri": "simplevalidator", "options": map[string]any{"expression": "${body} !contains 'poison'"},
			"links": links(link("check-payload", "in"), link("done", "out"))},
		{"id": "done", "type": "sink", "uri": "setbody", "options": map[string]any{"expression": "fine"}, "links": links(link("done", "in"))},

		{"id": "refuse-id", "type": "error", "uri": "failedexchange", "options": map[string]any{"errorStep": "check-id", "handled": false},
			"links": links(link("noted", "out"))},
		{"id": "noted", "type": "sink", "uri": "setbody", "options": map[string]any{"expression": "noted"}, "links": links(link("noted", "in"))},
		{"id": "refuse-payload", "type": "error", "uri": "failedexchange", "options": map[string]any{"errorStep": "check-payload"},
			"links": links(link("refused", "out"))},
		{"id": "refused", "type": "sink", "uri": "setbody", "options": map[string]any{"expression": "payload refused"}, "links": links(link("refused", "in"))},
	})
	f, results := start(t, path, nil)

	send := func(orderID, body string) (string, error) {
		m := f.NewMessage()
		m[Body] = body
		if orderID != "" {
			m["orderId"] = orderID
		}
		reply, err := f.Request(context.Background(), m)
		return text(reply[Body]), err
	}

	if body, err := send("42", "hello"); err != nil || body != "fine" {
		t.Errorf("good message: %q, %v; want fine", body, err)
	}
	if res := await(t, results); res.Err != nil {
		t.Errorf("good message: handled failure %v", res.Err)
	}

	if body, err := send("42", "poison"); err != nil || body != "payload refused" {
		t.Errorf("poison: %q, %v; want the error route's outcome", body, err)
	}
	if res := await(t, results); res.Err == nil || res.Message[Body] != "payload refused" {
		t.Errorf("poison: result %v; want the handled failure", res)
	}

	// The first step's handler is unhandled: the route runs, the sender still gets the failure.
	if _, err := send("", "hello"); err == nil || !strings.Contains(err.Error(), "step check-id") {
		t.Errorf("no order id: err = %v; want the failure of check-id", err)
	}
	if o := <-results; o.err == nil || o.res == nil || o.res.Message[Body] != "noted" {
		t.Errorf("no order id: result %+v; want the failure and the route's outcome", o)
	}
}

func text(v any) string {
	s, _ := v.(string)
	return s
}
