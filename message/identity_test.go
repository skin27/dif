package message

import "testing"

func TestIdentityLineage(t *testing.T) {
	root := New("root")
	if root[MessageID] == root[TraceID] || root[CorrelationID] != root[MessageID] || root[CausationID] != nil {
		t.Fatalf("unexpected root identity: %v", root)
	}
	root[CorrelationID] = "order-123"
	child, sibling := root.Child("part"), root.Child("other")
	grandchild := child.Child("nested")
	seen := map[any]bool{}
	for _, m := range []Message{root, child, sibling, grandchild} {
		if seen[m[MessageID]] || m[MessageID] == nil {
			t.Fatalf("missing or duplicate message ID: %v", m)
		}
		seen[m[MessageID]] = true
		if m[TraceID] != root[TraceID] || m[CorrelationID] != "order-123" {
			t.Fatalf("lost conversation: %v", m)
		}
		copy := m.Copy()
		copy.EnsureIdentity()
		for _, key := range []string{MessageID, CorrelationID, CausationID, TraceID} {
			if copy[key] != m[key] {
				t.Errorf("copy changed %s", key)
			}
		}
	}
	if child[CausationID] != root[MessageID] || grandchild[CausationID] != child[MessageID] || root[Body] != "root" {
		t.Fatal("incorrect lineage or changed parent")
	}
}

func TestEnsurePartialIdentity(t *testing.T) {
	m := Message{MessageID: "external-id", TraceID: "external-trace"}
	m.EnsureIdentity()
	if m[MessageID] != "external-id" || m[CorrelationID] != "external-id" || m[TraceID] != "external-trace" {
		t.Fatalf("unexpected identity: %v", m)
	}
	for _, value := range []any{nil, "", 42} {
		m := Message{MessageID: value, CorrelationID: value, TraceID: value}
		m.EnsureIdentity()
		for _, key := range []string{MessageID, CorrelationID, TraceID} {
			if id, _ := m[key].(string); id == "" {
				t.Errorf("%s not initialized for %v", key, value)
			}
		}
	}
}
