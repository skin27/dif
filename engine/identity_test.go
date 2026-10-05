package engine

import (
	"context"
	"errors"
	"testing"

	"dif/message"
)

type identityRetry struct{ attempts []message.Message }

func (p *identityRetry) Process(_ context.Context, m message.Message) (message.Message, error) {
	p.attempts = append(p.attempts, m.Copy())
	if len(p.attempts) == 1 {
		return nil, errors.New("try again")
	}
	return m, nil
}

func TestIdentityAcrossRetriesAndFlows(t *testing.T) {
	p := &identityRetry{}
	var ran []string
	f := withError(&ran, p, 1)
	res, err := Run(context.Background(), f, message.Message{message.Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.attempts) != 2 {
		t.Fatalf("attempts = %d", len(p.attempts))
	}
	next, err := Run(context.Background(), f, res.Message.Copy())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{message.MessageID, message.CorrelationID, message.TraceID} {
		id, _ := p.attempts[0][key].(string)
		if id == "" || p.attempts[1][key] != id || next.Message[key] != id {
			t.Errorf("%s not initialized or preserved", key)
		}
	}
}

func TestConfiguredMessageIdentity(t *testing.T) {
	var ran []string
	f := linear(&ran, nil)
	f.Input = message.Message{message.MessageID: "configured"}
	r := NewRunner(f, nil)
	m := r.NewMessage()
	if m[message.MessageID] != "configured" || m[message.CorrelationID] != "configured" {
		t.Fatalf("configured identity: %v", m)
	}
	f.Input = message.Message{message.CorrelationID: "order"}
	a, b := r.NewMessage(), r.NewMessage()
	if a[message.MessageID] == b[message.MessageID] || a[message.TraceID] == b[message.TraceID] || a[message.CorrelationID] != "order" || b[message.CorrelationID] != "order" {
		t.Fatal("new messages did not retain correlation with fresh identities")
	}
}
