package message_test

import (
	"testing"

	"dif/message"
)

func TestIdentityLineage(t *testing.T) {
	root := message.New("root")
	if root[message.MessageID] == root[message.TraceID] || root[message.CorrelationID] != root[message.MessageID] || root[message.CausationID] != nil {
		t.Fatalf("unexpected root identity: %v", root)
	}
	root[message.CorrelationID] = "order-123"
	child, sibling := root.Child("part"), root.Child("other")
	grandchild := child.Child("nested")
	seen := map[any]bool{}
	for _, m := range []message.Message{root, child, sibling, grandchild} {
		if seen[m[message.MessageID]] || m[message.MessageID] == nil {
			t.Fatalf("missing or duplicate message ID: %v", m)
		}
		seen[m[message.MessageID]] = true
		if m[message.TraceID] != root[message.TraceID] || m[message.CorrelationID] != "order-123" {
			t.Fatalf("lost conversation: %v", m)
		}
		copy := m.Copy()
		copy.EnsureIdentity()
		for _, key := range []string{message.MessageID, message.CorrelationID, message.CausationID, message.TraceID} {
			if copy[key] != m[key] {
				t.Errorf("copy changed %s", key)
			}
		}
	}
	if child[message.CausationID] != root[message.MessageID] || grandchild[message.CausationID] != child[message.MessageID] || root[message.Body] != "root" {
		t.Fatal("incorrect lineage or changed parent")
	}
}

func TestEnsurePartialIdentity(t *testing.T) {
	m := message.Message{message.MessageID: "external-id", message.TraceID: "external-trace"}
	m.EnsureIdentity()
	if m[message.MessageID] != "external-id" || m[message.CorrelationID] != "external-id" || m[message.TraceID] != "external-trace" {
		t.Fatalf("unexpected identity: %v", m)
	}
	for _, value := range []any{nil, "", 42} {
		m := message.Message{message.MessageID: value, message.CorrelationID: value, message.TraceID: value}
		m.EnsureIdentity()
		for _, key := range []string{message.MessageID, message.CorrelationID, message.TraceID} {
			if id, _ := m[key].(string); id == "" {
				t.Errorf("%s not initialized for %v", key, value)
			}
		}
	}
}
