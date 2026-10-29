package registry_test

import (
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/internal/secret"
	stepdef "dif/steps/definition"
	"dif/steps/registry"
)

const remoteSchema = `{"type": "object", "properties": {
	"path": {"type": "string"},
	"userName": {"type": "string"},
	"password": {"type": "string"},
	"authMethod": {"type": "string", "enum": ["None", "Basic"], "default": "None"},
	"socketTimeout": {"type": "integer", "default": 30000},
	"useTLS": {"type": "boolean", "default": false}
}, "additionalProperties": false}`

func connectionRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r := registry.New()
	mapping := &stepdef.ConnectionMapping{
		Kinds:   []string{"sftp"},
		Address: "path",
		Options: map[string]string{
			"authentication.username": "userName",
			"authentication.password": "password",
			"socketTimeout":           "socketTimeout",
			"tls.enabled":             "",
		},
		Implied: map[string]map[string]any{
			"authentication.username": {"authMethod": "Basic"},
			"tls.enabled":             {"useTLS": true},
		},
	}
	for _, d := range []stepdef.Definition{
		{Name: "remote", Kind: stepdef.Action, Schema: []byte(remoteSchema), Connection: mapping, New: newRecorder},
		{Name: "plain", Kind: stepdef.Action, Schema: []byte(remoteSchema), New: newRecorder},
	} {
		if err := r.Register(d); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func connectionNode(uri string, opts map[string]any, c *stepdef.Connection) *flowdef.Node {
	return &flowdef.Node{ID: "s1", Kind: flowdef.Action, URI: uri, Options: opts,
		Bindings: stepdef.Params{stepdef.ConnectionBinding: c}}
}

func TestConnectionSetsOptionsTheStepDoesNotSet(t *testing.T) {
	r := connectionRegistry(t)
	c := &stepdef.Connection{Name: "common:partner", Kind: "sftp", Fields: map[string]any{
		"host": "sftp.example", "port": "2222",
		"authentication.username": "dif", "authentication.password": "secret",
		"socketTimeout": "5000", "tls.enabled": true,
	}}
	p, err := r.Processor(connectionNode("remote:/in/orders", map[string]any{"password": "own"}, c))
	if err != nil {
		t.Fatal(err)
	}
	got := p.(recorder).params
	want := map[string]any{
		"path": "sftp.example:2222/in/orders", "userName": "dif", "password": "own",
		"authMethod": "Basic", "socketTimeout": 5000, "useTLS": true,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
	if _, leaked := got[stepdef.ConnectionBinding]; leaked {
		t.Error("the processor got the connection binding")
	}
}

func TestConnectionAddress(t *testing.T) {
	r := connectionRegistry(t)
	for _, tc := range []struct {
		fields map[string]any
		uri    string
		want   string
	}{
		{map[string]any{"host": "h"}, "remote", "h"},
		{map[string]any{"host": "h", "port": "22"}, "remote:dir", "h:22/dir"},
		{map[string]any{"host": "h", "share": "/docs/"}, "remote:/in", "h/docs/in"},
		{map[string]any{"url": "https://api.example/v1/"}, "remote:/orders", "https://api.example/v1/orders"},
		{map[string]any{}, "remote:h/dir", "h/dir"}, // no address: the step's own
	} {
		c := &stepdef.Connection{Name: "c", Kind: "sftp", Fields: tc.fields}
		p, err := r.Processor(connectionNode(tc.uri, nil, c))
		if err != nil {
			t.Fatal(err)
		}
		if got := p.(recorder).params["path"]; got != tc.want {
			t.Errorf("%v with %s: path = %v, want %s", tc.fields, tc.uri, got, tc.want)
		}
	}
}

func TestConnectionWithFalseImpliesNothing(t *testing.T) {
	r := connectionRegistry(t)
	c := &stepdef.Connection{Name: "c", Kind: "sftp", Fields: map[string]any{"tls.enabled": false}}
	p, err := r.Processor(connectionNode("remote:h", nil, c))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(recorder).params["useTLS"]; got != false {
		t.Errorf("useTLS = %v", got)
	}
}

func TestConnectionErrorsNameNoValues(t *testing.T) {
	r := connectionRegistry(t)
	for _, tc := range []struct {
		uri  string
		c    *stepdef.Connection
		want string
	}{
		{"plain:x", &stepdef.Connection{Name: "c", Kind: "sftp"}, `plain: connection "c": the step takes no connection`},
		{"remote:x", &stepdef.Connection{Name: "c", Kind: "smb"}, `remote: connection "c" is smb; the step takes sftp`},
		{"remote:x", &stepdef.Connection{Name: "c", Kind: "sftp", Fields: map[string]any{"authentication.token": "s3cr3t"}},
			`remote: connection "c": the step does not take authentication.token`},
	} {
		n := connectionNode(tc.uri, nil, tc.c)
		for name, err := range map[string]error{"Validate": r.Validate(n), "ValidateRedacted": r.ValidateRedacted(n)} {
			if err == nil || err.Error() != tc.want {
				t.Errorf("%s: %v, want %s", name, err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "s3cr3t") {
				t.Errorf("%s shows a value: %v", name, err)
			}
		}
	}
}

func TestConnectionPasswordIsDecrypted(t *testing.T) {
	r := connectionRegistry(t)
	t.Setenv(secret.PasswordEnv, "test-password")
	value := encrypted(t, "test-password", "plain")
	c := &stepdef.Connection{Name: "c", Kind: "sftp", Fields: map[string]any{"authentication.password": value}}
	n := connectionNode("remote:h", nil, c)
	if err := r.Validate(n); err != nil {
		t.Fatal(err)
	}
	p, err := r.Processor(n)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(recorder).params["password"]; got != "plain" {
		t.Errorf("password = %v", got)
	}
	if c.Fields["authentication.password"] != value {
		t.Error("the connection was changed")
	}
}

func TestStepWithoutConnectionIsUnchanged(t *testing.T) {
	r := connectionRegistry(t)
	p, err := r.Processor(&flowdef.Node{ID: "s1", Kind: flowdef.Action, URI: "remote:h/dir"})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(recorder).params; got["path"] != "h/dir" || got["authMethod"] != "None" {
		t.Errorf("params = %v", got)
	}
}
