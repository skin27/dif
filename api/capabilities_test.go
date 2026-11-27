package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dif/keystore"
	stepdef "dif/steps/definition"
)

const (
	capIdentity   = "../keystore/testdata/server-identity.p12"
	capTrustStore = "../keystore/testdata/truststore.p12"
	capPassword   = "changeit"
)

// capServer is a TLS server with the test identity, which the test trust store trusts.
func capServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	cert, err := keystore.LoadIdentity(capIdentity, capPassword)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// TestManifest: the steps' capabilities are in the program's description.
func TestManifest(t *testing.T) {
	path := dil(t, "orders",
		step{"in", "source", "file:inbox", nil},
		step{"call", "action", "https://api.example.com/orders/${header.id}", map[string]any{"trustStoreFile": "", "excludeHeaders": ".*"}},
		step{"lookup", "action", "https:${header.url}", map[string]any{"trustStoreFile": ""}},
		step{"name", "action", "setheader", map[string]any{"name": "a", "value": "b"}},
		step{"mail", "sink", "smtp:mail.example.com:587", map[string]any{"to": "a@example.com", "from": "b@example.com"}},
	)
	info, err := InspectProgram(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range info.Flows[0].Flow.Steps {
		got = append(got, fmt.Sprintf("%s %v %v", s.ID, s.Capabilities, s.CapabilitiesUndeclared))
	}
	want := []string{
		"in [fs.read inbox fs.write inbox] false",
		"call [net.connect api.example.com:443] false",
		"lookup [net.connect * (dynamic)] false",
		"name [] false",
		"mail [net.connect mail.example.com:587 env DIF_SMTP_PASSWORD] false",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("manifest:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(info.Diagnostics) != 0 {
		t.Errorf("no policy, yet diagnostics: %v", info.Diagnostics)
	}
}

// TestPolicyRefusesLoad: a program whose manifest the policy does not grant
// is refused, with every violation named; a granted one loads.
func TestPolicyRefusesLoad(t *testing.T) {
	custom := runtimeStep(t, func(_ context.Context, m Message) (Message, error) { return m, nil })
	path := dil(t, "orders",
		step{"in", "source", "message:in", nil},
		step{"call", "action", "https://api.example.com/orders", map[string]any{"trustStoreFile": ""}},
		step{"lookup", "action", "https:${header.url}", map[string]any{"trustStoreFile": ""}},
		step{"mine", "action", custom, nil},
		step{"out", "sink", "file:outbox", nil},
	)
	p := prepare(t, path)
	r := runtimeNew(t, ChannelConfig{})

	sandbox, _ := NewPolicy(nil)
	r.SetPolicy(sandbox)
	_, err := p.Load(context.Background(), r, nil, nil)
	want := `the policy does not permit what the program can do:
  error not-permitted: flow orders, step call: net.connect api.example.com:443 is not granted
  error not-permitted: flow orders, step lookup: net.connect * is known only per message, and nothing of net.connect is granted
  error not-permitted: flow orders, step mine: the step declares no capabilities, so the policy cannot tell what it touches
  error not-permitted: flow orders, step out: fs.write outbox is not granted`
	if err == nil || err.Error() != want {
		t.Fatalf("sandbox: %v\nwant:\n%s", err, want)
	}

	partial, _ := NewPolicy(map[string][]string{"net.connect": {"api.example.com:443"}, "fs.write": {"outbox"}})
	r.SetPolicy(partial)
	if _, err := p.Load(context.Background(), r, nil, nil); err == nil || strings.Count(err.Error(), "not-permitted") != 1 || !strings.Contains(err.Error(), "step mine") {
		t.Fatalf("granted all but the custom step: %v", err)
	}

	info := p.Info()
	ds := info.CheckPolicy(partial)
	if len(ds) != 1 || ds[0].Step != "mine" || len(info.Flows[0].Flow.Diagnostics) != 1 || len(info.Diagnostics) != 1 {
		t.Errorf("CheckPolicy: %v; flow %v; program %v", ds, info.Flows[0].Flow.Diagnostics, info.Diagnostics)
	}

	r.SetPolicy(nil) // no policy: as before
	if _, err := p.Load(context.Background(), r, nil, nil); err != nil {
		t.Fatalf("without a policy: %v", err)
	}
}

// TestPolicyRefusesDynamicTargetAtRunTime: a target known only per message
// passes the load check when its kind has a grant, and the step is checked
// when it runs: a granted server answers, another is refused with a
// *CapabilityError that an error handler takes by its class.
func TestPolicyRefusesDynamicTargetAtRunTime(t *testing.T) {
	granted := capServer(t, "granted")
	other := capServer(t, "other")
	link := func(id, bound string) map[string]any { return map[string]any{"id": id, "bound": bound} }
	links := func(l ...map[string]any) map[string]any { return map[string]any{"link": l} }
	path := writeDIL(t, "lookup", []map[string]any{
		{"id": "in", "type": "source", "uri": "message:in", "links": links(link("call", "out"))},
		{"id": "call", "type": "action", "uri": "https:${header.url}", "options": map[string]any{"trustStoreFile": capTrustStore, "trustStorePassword": capPassword},
			"links": links(link("call", "in"), link("done", "out"))},
		{"id": "done", "type": "sink", "uri": "passthrough", "links": links(link("done", "in"))},
		{"id": "refused", "type": "error", "uri": "failedexchange", "options": map[string]any{"errorClass": "*definition.CapabilityError"},
			"links": links(link("note", "out"))},
		{"id": "note", "type": "sink", "uri": "setbody", "options": map[string]any{"expression": "refused"}, "links": links(link("note", "in"))},
	})
	p := prepare(t, path)
	r := runtimeNew(t, ChannelConfig{})
	pol, err := NewPolicy(map[string][]string{
		"net.connect": {granted.Listener.Addr().String()},
		"fs.read":     {capTrustStore},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.SetPolicy(pol)
	prog, err := p.Load(context.Background(), r, nil, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	f := prog.Flows[0]
	if err := f.StartContext(WithPolicy(context.Background(), pol)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Stop() })

	request := func(url string) (string, error) {
		m := f.NewMessage()
		m["url"] = url
		reply, err := f.Request(context.Background(), m)
		return text(reply[Body]), err
	}
	if body, err := request(granted.URL); err != nil || body != "granted" {
		t.Errorf("granted server: %q, %v", body, err)
	}
	if body, err := request(other.URL); err != nil || body != "refused" {
		t.Errorf("other server: %q, %v; want the error route's outcome", body, err)
	}

	// Started without the guard, the flow is not checked when it runs.
	f.Stop()
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	if body, err := request(other.URL); err != nil || body != "other" {
		t.Errorf("without the guard: %q, %v", body, err)
	}
}

// TestCapabilityErrorClass: the error of a refusal is a *CapabilityError at
// the bottom of the chain, so errorClass names it.
func TestCapabilityErrorClass(t *testing.T) {
	pol, _ := NewPolicy(nil)
	err := fmt.Errorf("step call: %w", pol.Permit(stepdef.Capability{Kind: "net.connect", Target: "evil.example:443"}))
	var ce *CapabilityError
	if !errors.As(err, &ce) || errors.Unwrap(ce) != nil || fmt.Sprintf("%T", ce) != "*definition.CapabilityError" {
		t.Errorf("%v (%T)", err, ce)
	}
	if ce.Error() != "net.connect evil.example:443 is not permitted by the policy" {
		t.Errorf("message: %s", ce)
	}
}
