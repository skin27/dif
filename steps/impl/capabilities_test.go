package impl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/internal/policy"
	"dif/message"
	stepdef "dif/steps/definition"
	"dif/steps/registry"
)

// TestEveryBuiltinStepDeclaresCapabilities: a built-in step that does not say
// what it touches outside the process cannot be registered, and every entry
// of the table is a registered step.
func TestEveryBuiltinStepDeclaresCapabilities(t *testing.T) {
	registered := map[string]bool{}
	for _, s := range steps.Steps() {
		registered[s.Name+"/"+s.Kind] = true
	}
	for key := range stepCapabilities {
		if !registered[key] {
			t.Errorf("stepCapabilities has %s, which is no registered step", key)
		}
	}
	for _, s := range steps.Steps() {
		n := &flowdef.Node{ID: "n", Kind: s.Kind, URI: s.Name}
		if _, declared := steps.Capabilities(n); !declared && len(requiredOptions(s)) == 0 {
			t.Errorf("%s (%s) declares no capabilities", s.Name, s.Kind)
		}
	}

	saved := stepCapabilities["log/action"]
	delete(stepCapabilities, "log/action")
	defer func() { stepCapabilities["log/action"] = saved }()
	err := Register(registry.New())
	if err == nil || !strings.Contains(err.Error(), "register step log (action): it declares no capabilities") {
		t.Errorf("Register without a declaration for log: %v", err)
	}
}

func requiredOptions(s registry.StepInfo) []string {
	var names []string
	for _, o := range s.Options {
		if o.Required {
			names = append(names, o.Name)
		}
	}
	return names
}

// TestCapabilities derives the capabilities of representative steps from
// their options, as the registry gives them for a node.
func TestCapabilities(t *testing.T) {
	home, _ := os.UserHomeDir()
	t.Setenv("DIF_OAUTH2_TOKEN_URL", "") // the manifest never reads the environment
	// The trust store has a default file, whose password comes from the environment.
	const defaultTrust = " fs.read security/outbound-truststore.p12 env DIF_TRUSTSTORE_PASSWORD]"
	tests := []struct {
		name, kind, uri string
		options         map[string]any
		connection      *stepdef.Connection
		want            string
	}{
		{"https action, fixed", stepdef.Action, "https://api.example.com/orders", nil, nil,
			"[net.connect api.example.com:443" + defaultTrust},
		{"https action, port and user", stepdef.Action, "https:https://user:secret@api.example.com:8443/orders?key=x",
			map[string]any{"trustStoreFile": "trust.p12"}, nil,
			"[net.connect api.example.com:8443 fs.read trust.p12 env DIF_TRUSTSTORE_PASSWORD]"},
		{"https action, expression in the path", stepdef.Action, "https://api.example.com/orders/${header.id}", nil, nil,
			"[net.connect api.example.com:443" + defaultTrust},
		{"https action, expression in the host", stepdef.Action, "https://${header.host}/orders", nil, nil,
			"[net.connect ${header.host}:443 (dynamic)" + defaultTrust},
		{"https action, all an expression", stepdef.Action, "https:${header.url}", nil, nil,
			"[net.connect * (dynamic)" + defaultTrust},
		{"https action, typed connection", stepdef.Action, "https:orders", nil,
			&stepdef.Connection{Name: "partner", Kind: "https", Fields: map[string]any{"url": "https://partner.example:9443", "tls.trustStoreFile": "t.p12", "tls.trustStorePassword": "x"}},
			"[net.connect partner.example:9443 fs.read t.p12]"},
		{"rest action, http", stepdef.Action, "rest", map[string]any{"host": "http://localhost", "path": "api"}, nil,
			"[net.connect localhost:80" + defaultTrust},
		{"file source", stepdef.Source, "file:inbox", nil, nil,
			"[fs.read inbox fs.write inbox]"},
		{"file sink", stepdef.Sink, "file:outbox", nil, nil,
			"[fs.write outbox]"},
		{"fileenrich, keeping the file", stepdef.Action, "fileenrich:in", nil, nil,
			"[fs.read in]"},
		{"ftp source", stepdef.Source, "ftp://files.example.com/in", map[string]any{"userName": "u", "password": "p"}, nil,
			"[net.connect files.example.com:21]"},
		{"ftps, implicit, password from the environment", stepdef.Sink, "ftps://files.example.com/out", map[string]any{"implicit": true}, nil,
			"[net.connect files.example.com:990 env DIF_FTPS_PASSWORD" + defaultTrust},
		{"sftp sink with a key", stepdef.Sink, "sftp://files.example.com:2222/out",
			map[string]any{"userName": "u", "privateKey": "id_ed25519", "strictHostKeyChecking": true}, nil,
			"[net.connect files.example.com:2222 env DIF_SFTP_PASSWORD fs.read id_ed25519 env DIF_SFTP_PRIVATE_KEY_PASSPHRASE fs.read " + filepath.Join(home, ".ssh", "known_hosts") + "]"},
		{"sftp, typed connection", stepdef.Source, "sftp:in", nil,
			&stepdef.Connection{Name: "partner", Kind: "sftp", Fields: map[string]any{"host": "sftp.example.com", "authentication.username": "u", "authentication.password": "p"}},
			"[net.connect sftp.example.com:22 fs.read " + filepath.Join(home, ".ssh", "known_hosts") + "]"},
		{"smb", stepdef.Sink, `smb:\\fs.example.com\share\out`, map[string]any{"userName": "u", "password": "p"}, nil,
			"[net.connect fs.example.com:445]"},
		{"smtp with its password variable", stepdef.Action, "smtp:mail.example.com:587", map[string]any{"to": "a@example.com", "from": "b@example.com"}, nil,
			"[net.connect mail.example.com:587 env DIF_SMTP_PASSWORD]"},
		{"smtps with a password", stepdef.Action, "smtps:mail.example.com:465", map[string]any{"to": "a@example.com", "from": "b@example.com", "password": "x"}, nil,
			"[net.connect mail.example.com:465]"},
		{"imaps", stepdef.Source, "imaps:imap.example.com", map[string]any{"username": "u"}, nil,
			"[net.connect imap.example.com:993 env DIF_IMAPS_PASSWORD]"},
		{"sql, default port", stepdef.Action, "sql", map[string]any{"connectionType": "postgres", "host": "db.example.com", "query": "select 1", "password": "x"}, nil,
			"[net.connect db.example.com:5432]"},
		{"sql, host from a header", stepdef.Action, "sql", map[string]any{"connectionType": "mysql", "host": "${header.db}", "query": "select 1"}, nil,
			"[net.connect ${header.db}:3306 (dynamic) env DIF_SQL_PASSWORD]"},
		{"sql2", stepdef.Action, "sql2", map[string]any{"query": "select 1", "connection": `{"dbtype":"sql_server","host":"mssql.example.com","port":14330,"dbname":"d"}`}, nil,
			"[net.connect mssql.example.com:14330]"},
		{"https source as a listener", stepdef.Source, "https://0.0.0.0:9443/orders",
			map[string]any{"serverIdentityFile": "server.p12"}, nil,
			"[net.listen 0.0.0.0:9443 fs.read server.p12 env DIF_SERVER_IDENTITY_PASSWORD]"},
		{"rest source", stepdef.Source, "rest:orders", map[string]any{"serverIdentityFile": "server.p12", "serverIdentityPassword": "x"}, nil,
			"[net.listen 0.0.0.0:9002 fs.read server.p12]"},
		{"as2 action", stepdef.Action, "as2", map[string]any{"as2From": "a", "as2To": "b", "hostName": "as2.example.com", "targetPortNumber": "8443", "certificateForEncrypt": "https://keys.example.com/partner.pem", "certificateForSigning": "own.p12"}, nil,
			"[net.connect as2.example.com:8443 fs.read own.p12 net.connect keys.example.com:443]"},
		{"oauth2token, endpoint from the environment", stepdef.Sink, "oauth2token", map[string]any{"tokenName": "partner", "clientId": "c"}, nil,
			"[net.connect * (dynamic) env DIF_OAUTH2_PARTNER_TOKEN_URL env DIF_OAUTH2_TOKEN_URL env DIF_OAUTH2_PARTNER_CLIENT_SECRET env DIF_OAUTH2_CLIENT_SECRET env DIF_OAUTH2_PARTNER_REFRESH_TOKEN env DIF_OAUTH2_REFRESH_TOKEN env DIF_OAUTH2_PARTNER_SCOPE env DIF_OAUTH2_SCOPE]"},
		{"googledrive", stepdef.Action, "googledrive:folder", map[string]any{"accessToken": "@{token}"}, nil,
			"[net.connect www.googleapis.com:443]"},
		{"xslt from a file", stepdef.Action, "xslt:transform.xsl", nil, nil,
			"[fs.read transform.xsl]"},
		{"validate with an inline schema", stepdef.Action, "validate", map[string]any{"schema": "{}"}, nil,
			"[]"},
		{"a pure step", stepdef.Action, "setheader", map[string]any{"name": "a", "value": "b"}, nil,
			"[]"},
		{"a router", stepdef.Router, "split", map[string]any{"expression": "${body}"}, nil,
			"[]"},
	}
	for _, tc := range tests {
		n := &flowdef.Node{ID: "n", Kind: tc.kind, URI: tc.uri, Options: tc.options}
		if tc.connection != nil {
			n.Bindings = map[string]any{stepdef.ConnectionBinding: tc.connection}
		}
		caps, declared := steps.Capabilities(n)
		if !declared {
			t.Errorf("%s: undeclared (options valid? %v)", tc.name, steps.Validate(n))
			continue
		}
		if got := fmt.Sprint(caps); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

func TestURLTarget(t *testing.T) {
	for raw, want := range map[string]string{
		"https://api.example.com":              "net.connect api.example.com:443",
		"http://api.example.com/x":             "net.connect api.example.com:80",
		"HTTPS://API.example.com:8443/x?y#z":   "net.connect API.example.com:8443",
		"//api.example.com/x":                  "net.connect api.example.com:443",
		"https://u:p@api.example.com/x":        "net.connect api.example.com:443",
		"https://[2001:db8::1]/x":              "net.connect [2001:db8::1]:443",
		"https://[2001:db8::1]:8443/x":         "net.connect [2001:db8::1]:8443",
		"https://${header.h}:${header.p}/x":    "net.connect ${header.h}:${header.p} (dynamic)",
		"https://${header.a/b}/x":              "net.connect ${header.a/b}:443 (dynamic)",
		"https://@{tenant.host}/x":             "net.connect @{tenant.host}:443 (dynamic)",
		"${header.url}":                        "net.connect * (dynamic)",
		"https://":                             "net.connect * (dynamic)",
		"https://ENC(abc|def|ghi)/x":           "net.connect ENC(abc|def|ghi):443 (dynamic)",
		"https://api.example.com/${header.id}": "net.connect api.example.com:443",
	} {
		if got := urlTarget(raw, "443", true).String(); got != want {
			t.Errorf("urlTarget(%q) = %s, want %s", raw, got, want)
		}
	}
	// An address the step takes as it is (no expression) is never dynamic, but
	// when it is encrypted.
	for raw, want := range map[string]string{
		"https://${header.h}/x":      "net.connect ${header.h}:443",
		"${header.url}":              "net.connect ${header.url}:443",
		"https://ENC(abc|def|ghi)/x": "net.connect ENC(abc|def|ghi):443 (dynamic)",
	} {
		if got := urlTarget(raw, "443", false).String(); got != want {
			t.Errorf("urlTarget(%q) without expressions = %s, want %s", raw, got, want)
		}
	}
}

// guarded returns a context whose guard is the policy of grants.
func guarded(t *testing.T, grants map[string][]string) context.Context {
	t.Helper()
	p, err := policy.New(grants)
	if err != nil {
		t.Fatal(err)
	}
	return stepdef.WithCapabilityGuard(context.Background(), p)
}

// TestGuardRefusesDynamicHTTPSTarget: an https action whose address comes from
// a header reaches a granted server and is refused another, with a
// *stepdef.CapabilityError; a redirect to a host that is not granted is
// refused too.
func TestGuardRefusesDynamicHTTPSTarget(t *testing.T) {
	var reached []string
	granted := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, "granted")
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
			return
		}
		w.Write([]byte("ok"))
	})
	other := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		reached = append(reached, "other")
		w.Write([]byte("other"))
	})
	ctx := guarded(t, map[string][]string{stepdef.NetConnect: {granted.Listener.Addr().String()}})
	a := newHTTPSActionT(t, "https:${header.url}", nil)

	m := message.New("")
	m["url"] = granted.URL + "/ok"
	if out, err := a.Process(ctx, m); err != nil || out[message.Body] != "ok" {
		t.Fatalf("granted: %v %v", out, err)
	}

	m = message.New("")
	m["url"] = other.URL + "/x"
	_, err := a.Process(ctx, m)
	var ce *stepdef.CapabilityError
	if !errors.As(err, &ce) || ce.Kind != stepdef.NetConnect || ce.Target != other.Listener.Addr().String() {
		t.Fatalf("other: %v, want a *stepdef.CapabilityError", err)
	}
	if !strings.Contains(err.Error(), "is not permitted by the policy") {
		t.Errorf("error: %v", err)
	}

	m = message.New("")
	m["url"] = granted.URL + "/redirect?to=" + other.URL + "/x"
	if _, err := a.Process(ctx, m); !errors.As(err, &ce) || ce.Target != other.Listener.Addr().String() {
		t.Fatalf("redirect: %v, want a *stepdef.CapabilityError", err)
	}
	if strings.Join(reached, " ") != "granted granted" {
		t.Errorf("servers reached: %v; the other server must never be", reached)
	}

	// Without a guard everything goes, as before.
	if _, err := a.Process(context.Background(), m); err != nil {
		t.Errorf("without a guard: %v", err)
	}
}

// TestGuardRefusesFileOutsideTheDirectory: the file sink writes under its
// directory a name from a header; one that leads out of it is refused.
func TestGuardRefusesFileOutsideTheDirectory(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	sink := mustProcessor(t, stepdef.Sink, "file:"+out, map[string]any{"autoCreate": true}).(stepdef.SinkProcessor)
	ctx := guarded(t, map[string][]string{stepdef.FSWrite: {out}})

	m := message.New("fine")
	m[FileName] = "a.txt"
	if err := sink.Consume(ctx, m); err != nil {
		t.Fatal(err)
	}
	m = message.New("escape")
	m[FileName] = "../escaped.txt"
	var ce *stepdef.CapabilityError
	if err := sink.Consume(ctx, m); !errors.As(err, &ce) || ce.Kind != stepdef.FSWrite {
		t.Fatalf("../escaped.txt: %v, want a *stepdef.CapabilityError", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); !os.IsNotExist(err) {
		t.Errorf("the file outside the directory was written: %v", err)
	}
}

// TestGuardRefusesDynamicSQLHost: a sql step whose host comes from a header
// asks the guard before it opens the database.
func TestGuardRefusesDynamicSQLHost(t *testing.T) {
	a := mustProcessor(t, stepdef.Action, "sql", map[string]any{"connectionType": "postgres", "host": "${header.db}", "query": "select 1", "password": "x"}).(stepdef.ActionProcessor)
	ctx := guarded(t, map[string][]string{stepdef.NetConnect: {"db.example.com:5432"}})
	m := message.New("")
	m["db"] = "evil.example"
	var ce *stepdef.CapabilityError
	if _, err := a.Process(ctx, m); !errors.As(err, &ce) || ce.Target != "evil.example:5432" {
		t.Fatalf("%v, want a *stepdef.CapabilityError for evil.example:5432", err)
	}
}

// TestGuardRefusesListener: a source asks the guard before it listens.
func TestGuardRefusesListener(t *testing.T) {
	addr := freeAddr(t)
	src := mustProcessor(t, stepdef.Source, "https://"+addr+"/x", map[string]any{"serverIdentityFile": testIdentity, "serverIdentityPassword": testPassword}).(stepdef.SourceProcessor)
	ctx := guarded(t, map[string][]string{stepdef.NetListen: {"127.0.0.1:1"}})
	var ce *stepdef.CapabilityError
	if err := src.Run(ctx, func(message.Message, func(message.Message, error)) error { return nil }); !errors.As(err, &ce) || ce.Target != addr {
		t.Fatalf("%v, want a *stepdef.CapabilityError for %s", err, addr)
	}
}
