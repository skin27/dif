package impl

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// connectionKindFields are the fields a typed connection of each kind may
// set (dil.core.connections, see flows/impl/connections.go), with a value of
// the right type.
var connectionKindFields = map[string]map[string]any{
	"sftp": {"host": "h", "port": "22", "socketTimeout": "1000", "authentication.username": "u", "authentication.password": "p",
		"authentication.privateKey": "k", "authentication.passphrase": "x", "hostKeys.knownHostsFile": "kh", "hostKeys.strict": true},
	"ftps": {"host": "h", "port": "21", "socketTimeout": "1000", "authentication.username": "u", "authentication.password": "p",
		"tls.trustStoreFile": "t", "tls.trustStorePassword": "tp", "tls.implicit": true},
	"smb": {"host": "h", "port": "445", "share": "s", "socketTimeout": "1000", "authentication.username": "u",
		"authentication.password": "p", "authentication.domain": "d"},
	"https": {"url": "https://h/v1", "connectTimeout": "1000", "socketTimeout": "1000", "authentication.username": "u",
		"authentication.password": "p", "tls.trustStoreFile": "t", "tls.trustStorePassword": "tp"},
	"smtp": {"host": "h", "port": "587", "socketTimeout": "1000", "authentication.username": "u", "authentication.password": "p",
		"authentication.token": "tk", "tls.trustStoreFile": "t", "tls.trustStorePassword": "tp"},
	"imaps": {"host": "h", "port": "993", "socketTimeout": "1000", "authentication.username": "u", "authentication.password": "p",
		"authentication.token": "tk", "tls.trustStoreFile": "t", "tls.trustStorePassword": "tp"},
	"as2": {"host": "h", "port": "443", "connectTimeout": "1000", "socketTimeout": "1000", "tls.enabled": true,
		"tls.trustStoreFile": "t", "tls.trustStorePassword": "tp"},
	"sql": {"dialect": "postgres", "host": "h", "port": "5432", "database": "d", "connectTimeout": "1000", "socketTimeout": "1000",
		"authentication.username": "u", "authentication.password": "p", "tls.enabled": true, "tls.version": "TLSv1.3",
		"tls.trustStoreFile": "t", "tls.trustStorePassword": "tp"},
}

// connected returns a node of the step with the connection bound.
func connected(kind, uri string, opts map[string]any, c *stepdef.Connection) *flowdef.Node {
	return &flowdef.Node{ID: "test-step", Kind: kind, URI: uri, Options: opts, Bindings: stepdef.Params{stepdef.ConnectionBinding: c}}
}

// Every step that takes a connection takes every field of its kind, and the
// options the fields set are options of the step, of the right type.
func TestConnectionMappingsMatchSchemas(t *testing.T) {
	// What a step cannot do with a field of its kind, on purpose.
	unsupported := map[string]string{
		"https/action": "authentication.token", // the https action has no bearer option
	}
	for key, m := range stepConnections {
		name, kind, _ := strings.Cut(key, "/")
		for _, k := range m.Kinds {
			fields := map[string]any{}
			for f, v := range connectionKindFields[k] {
				if f != unsupported[key] && !(name == "sql2" && sqlAddressField(f)) {
					fields[f] = v
				}
			}
			c := &stepdef.Connection{Name: "c", Kind: k, Fields: fields}
			opts := map[string]any{}
			switch name {
			case "sql", "sql2":
				opts["query"] = "select 1"
			case "as2":
				opts["requestUri"], opts["as2From"], opts["as2To"] = "/as2", "a", "b"
			case "smtp", "smtps":
				opts["to"] = "ann@example.com"
			}
			if err := steps.Validate(connected(kind, name+":", opts, c)); err != nil {
				t.Errorf("%s with a full %s connection: %v", key, k, err)
			}
			if field := unsupported[key]; field != "" {
				c.Fields[field] = "x"
				if err := steps.Validate(connected(kind, name+":", opts, c)); err == nil || !strings.Contains(err.Error(), "does not take "+field) {
					t.Errorf("%s with %s: err = %v", key, field, err)
				}
			}
		}
	}
}

// sqlAddressField reports whether the loader gives the field of a sql
// connection to sql2 in its option connection, not through the mapping.
func sqlAddressField(f string) bool {
	return f == "dialect" || f == "host" || f == "port" || f == "database" || strings.HasPrefix(f, "authentication.")
}

// hostPort splits an address of a test server.
func hostPort(t *testing.T, addr string) (string, string) {
	t.Helper()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func TestSFTPConnection(t *testing.T) {
	e := newSFTPEnv(t)
	host, port := hostPort(t, e.host)
	write(t, e.local("in/a.txt"), "A")
	c := &stepdef.Connection{Name: "partner", Kind: "sftp", Fields: map[string]any{
		"host": host, "port": port, "socketTimeout": "3000", "hostKeys.strict": false,
		"authentication.username": e.login["userName"], "authentication.password": e.login["password"],
	}}
	p, err := steps.Processor(connected(stepdef.Action, "sftpenrich:/in", nil, c))
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.(stepdef.ActionProcessor).Process(context.Background(), message.New("r"))
	if err != nil || out[message.Body] != "A" {
		t.Fatalf("body %v, err %v", out[message.Body], err)
	}
}

func TestFTPSConnection(t *testing.T) {
	f, e := ftpsEnv(t, false, nil)
	host, port := hostPort(t, e.host)
	write(t, e.local("in/a.txt"), "A")
	c := &stepdef.Connection{Name: "partner", Kind: "ftps", Fields: map[string]any{
		"host": host, "port": port,
		"authentication.username": e.login["userName"], "authentication.password": e.login["password"],
		"tls.trustStoreFile": testTrustStore, "tls.trustStorePassword": testPassword,
	}}
	p, err := steps.Processor(connected(stepdef.Action, "ftpsenrich:in", nil, c))
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.(stepdef.ActionProcessor).Process(context.Background(), message.New("r"))
	if err != nil || out[message.Body] != "A" {
		t.Fatalf("body %v, err %v", out[message.Body], err)
	}
	if got := f.commands("AUTH", "USER"); strings.Join(got, "|") != "AUTH TLS|USER dif" {
		t.Errorf("commands = %q", got)
	}
}

func TestSMBConnection(t *testing.T) {
	e := newSMBEnv(t)
	write(t, e.local("in/a.txt"), "A")
	c := &stepdef.Connection{Name: "share", Kind: "smb", Fields: map[string]any{
		"host": "127.0.0.1", "port": "445", "share": "testshare",
		"authentication.username": "u", "authentication.password": "p",
	}}
	p, err := steps.Processor(connected(stepdef.Action, "smbenrich:in", nil, c))
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.(stepdef.ActionProcessor).Process(context.Background(), message.New("r"))
	if err != nil || out[message.Body] != "A" {
		t.Fatalf("body %v, err %v", out[message.Body], err)
	}
}

func TestHTTPSConnection(t *testing.T) {
	got := make(chan *http.Request, 1)
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		got <- r
		w.Write([]byte("ok"))
	})
	c := &stepdef.Connection{Name: "api", Kind: "https", Fields: map[string]any{
		"url": srv.URL + "/v1", "authentication.username": "dif", "authentication.password": "s3cret",
		"tls.trustStoreFile": testTrustStore, "tls.trustStorePassword": testPassword,
	}}
	p, err := steps.Processor(connected(stepdef.Action, "https:/orders", nil, c))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.(stepdef.ActionProcessor).Process(context.Background(), message.New("")); err != nil {
		t.Fatal(err)
	}
	r := <-got
	if r.URL.Path != "/v1/orders" {
		t.Errorf("path = %s", r.URL.Path)
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("dif:s3cret")); r.Header.Get("Authorization") != want {
		t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
	}

	// The step's own authentication wins.
	p, err = steps.Processor(connected(stepdef.Action, "https:/orders", map[string]any{"authMethod": "None"}, c))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.(stepdef.ActionProcessor).Process(context.Background(), message.New("")); err != nil {
		t.Fatal(err)
	}
	if r := <-got; r.Header.Get("Authorization") != "" {
		t.Errorf("Authorization = %q, want none", r.Header.Get("Authorization"))
	}
}

func TestIMAPSConnection(t *testing.T) {
	s := newIMAPTestServer(t)
	s.add(t, "INBOX", "first", "bob@example.com", "one", false)
	host, port := hostPort(t, s.addr)
	c := &stepdef.Connection{Name: "mail", Kind: "imaps", Fields: map[string]any{
		"host": host, "port": port, "socketTimeout": "3000",
		"authentication.username": imapUser, "authentication.password": imapPass,
		"tls.trustStoreFile": testTrustStore, "tls.trustStorePassword": testPassword,
	}}
	p, err := steps.Processor(connected(stepdef.Source, "imaps:", map[string]any{"initialDelay": 0, "delay": 10}, c))
	if err != nil {
		t.Fatal(err)
	}
	r := startRemote(t, p.(stepdef.CompletionSourceProcessor), nil)
	if got := r.take(t, 1); got[0]["Subject"] != "first" {
		t.Errorf("email = %v", got[0])
	}
	r.stop(t)
}
