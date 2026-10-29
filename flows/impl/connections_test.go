package impl

import (
	"encoding/json"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	stepdef "dif/steps/definition"
)

// connectionDoc is a flow whose action b has the URI and options, with the
// connections as dil.core.connections.
func connectionDoc(uri, options, connections string) string {
	steps := `{"id":"a","type":"source","links":{"link":{"id":"b","bound":"out"}}},` +
		`{"id":"b","type":"action","uri":"` + uri + `","options":` + options + `,"links":{"link":[{"id":"b","bound":"in"},{"id":"c","bound":"out"}]}},` +
		`{"id":"c","type":"sink","uri":"log","links":{"link":{"id":"c","bound":"in"}}}`
	return `{"dil":{"integrations":{"integration":{"flows":{"flow":{"id":"f","steps":{"step":[` + steps + `]}}}}},"core":{"connections":` + connections + `}}}`
}

const partnerSftp = `{"connection":{"id":"partner","name":"Partner","type":"sftp","host":"sftp.example","port":22,
	"authentication":{"username":"dif","password":"ENC(AA|BB|CC)"},"hostKeys":{"strict":true},"socketTimeout":"5000"}}`

func parseNodes(t *testing.T, doc string) (map[string]*flowdef.Node, error) {
	t.Helper()
	nodes := map[string]*flowdef.Node{}
	_, err := Parse([]byte(doc), func(n *flowdef.Node) (stepdef.Processor, error) {
		nodes[n.ID] = n
		return noop{}, nil
	})
	return nodes, err
}

func TestTypedConnectionIsBound(t *testing.T) {
	nodes, err := parseNodes(t, connectionDoc("sftp:/in", `{"connection":"partner","fileName":"a.txt"}`, partnerSftp))
	if err != nil {
		t.Fatal(err)
	}
	b := nodes["b"]
	if _, has := b.Options["connection"]; has || b.Options["fileName"] != "a.txt" {
		t.Errorf("options = %v, want connection removed and the others kept", b.Options)
	}
	c, _ := b.Bindings[stepdef.ConnectionBinding].(*stepdef.Connection)
	if c == nil {
		t.Fatalf("bindings = %v, want the connection", b.Bindings)
	}
	want := map[string]any{
		"host": "sftp.example", "port": "22", "socketTimeout": "5000",
		"authentication.username": "dif", "authentication.password": "ENC(AA|BB|CC)", "hostKeys.strict": true,
	}
	if c.Name != "partner" || c.Kind != "sftp" || len(c.Fields) != len(want) {
		t.Errorf("connection = %+v", c)
	}
	for k, v := range want {
		if c.Fields[k] != v {
			t.Errorf("%s = %#v, want %#v", k, c.Fields[k], v)
		}
	}
	if _, has := nodes["c"].Bindings[stepdef.ConnectionBinding]; has {
		t.Error("a step without the option connection got one")
	}
}

func TestTypedConnectionIsChecked(t *testing.T) {
	for connection, want := range map[string]string{
		`{"id":"x","type":"ftp","host":"h"}`:                           `connection "x": type must be one of https, sftp`,
		`{"id":"x","type":"sftp","url":"https://h"}`:                   `connection "x": a sftp connection has no field url`,
		`{"id":"x","type":"sftp","tls":{"trustStoreFile":"t.p12"}}`:    `connection "x": a sftp connection has no field tls.trustStoreFile`,
		`{"id":"x","type":"sftp","authentication":{"user":"u"}}`:       `connection "x": a sftp connection has no field authentication.user`,
		`{"id":"x","type":"sftp","authentication":"u"}`:                `connection "x": authentication must be an object`,
		`{"id":"x","type":"sftp","port":"twenty-two"}`:                 `connection "x": field port must be a number`,
		`{"id":"x","type":"sftp","hostKeys":{"strict":"yes"}}`:         `connection "x": field hostKeys.strict must be a boolean`,
		`{"id":"x","type":"sftp","authentication":{"password":12345}}`: `connection "x": field authentication.password must be a string`,
		`{"id":"x","type":"sql","tls":{"implicit":true}}`:              `connection "x": a sql connection has no field tls.implicit`,
	} {
		doc := connectionDoc("log", `{}`, `{"connection":`+connection+`}`)
		_, err := parseNodes(t, doc)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %s", connection, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "12345") {
			t.Errorf("the error shows a value: %v", err)
		}
	}
}

func TestConnectionReferenceErrors(t *testing.T) {
	legacy := `{"connection":{"id":"old","type":"imaps","keys":{"host":"h"}}}`
	for _, tc := range []struct{ uri, options, connections, want string }{
		{"sftp:/in", `{"connection":"missing"}`, partnerSftp, `step b: connection "missing" not found in dil.core.connections`},
		{"imaps:", `{"connection":"old"}`, legacy, `step b: connection "old" has keys, the legacy form`},
		{"sftp:/in", `{"connection":7}`, partnerSftp, `step b: option connection must be the id of a connection`},
		{"sql2", `{"dataSource":"partner"}`, partnerSftp, `step b: connection "partner" is sftp; the step takes sql`},
	} {
		_, err := parseNodes(t, connectionDoc(tc.uri, tc.options, tc.connections))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s %s: err = %v, want %s", tc.uri, tc.options, err, tc.want)
		}
	}
}

// A sql2 step gets the address and authentication of a typed sql connection
// as its option connection, as for a legacy one, and the rest as a binding.
func TestTypedSQLConnection(t *testing.T) {
	db := `{"connection":{"id":"db","type":"sql","dialect":"postgres","host":"h","port":5432,"database":"orders",
		"authentication":{"username":"u","password":"p"},"tls":{"enabled":true,"version":"TLSv1.3"},"connectTimeout":1000}}`
	for _, options := range []string{`{"dataSource":"db","query":"select 1"}`, `{"connection":"db","query":"select 1"}`} {
		nodes, err := parseNodes(t, connectionDoc("sql2", options, db))
		if err != nil {
			t.Fatal(err)
		}
		b := nodes["b"]
		var keys map[string]any
		if err := json.Unmarshal([]byte(b.Options["connection"].(string)), &keys); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"dbtype": "postgres", "dbname": "orders", "host": "h", "port": "5432", "username": "u", "password": "p"}
		if len(keys) != len(want) {
			t.Errorf("%s: keys = %v", options, keys)
		}
		for k, v := range want {
			if keys[k] != v {
				t.Errorf("%s: key %s = %v, want %v", options, k, keys[k], v)
			}
		}
		c, _ := b.Bindings[stepdef.ConnectionBinding].(*stepdef.Connection)
		if c == nil || len(c.Fields) != 3 || c.Fields["tls.enabled"] != true || c.Fields["tls.version"] != "TLSv1.3" || c.Fields["connectTimeout"] != "1000" {
			t.Errorf("%s: binding = %+v, want tls and timeouts only", options, c)
		}
	}
}

// Documents are written out again when imports are merged; both forms survive.
func TestConnectionRoundTrip(t *testing.T) {
	for _, in := range []string{
		`{"id":"a","name":"","type":"jdbc","keys":{}}`,
		`{"id":"b","name":"B","type":"sftp","host":"h","authentication":{"username":"u"}}`,
	} {
		var c dilConnection
		if err := json.Unmarshal([]byte(in), &c); err != nil {
			t.Fatal(err)
		}
		out, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		var again dilConnection
		if err := json.Unmarshal(out, &again); err != nil {
			t.Fatal(err)
		}
		if again.typed() != c.typed() || again.ID != c.ID || again.Type != c.Type || string(mustJSON(t, again.Fields)) != string(mustJSON(t, c.Fields)) {
			t.Errorf("%s became %s", in, out)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A reusable processor's action resolves its connection where it is declared.
func TestProcessorConnection(t *testing.T) {
	doc := `{"dil":{"integrations":{"integration":{"flows":{"flow":{"id":"f","steps":{"step":[` +
		`{"id":"a","type":"source","links":{"link":{"id":"b","bound":"out"}}},` +
		`{"id":"b","type":"action","uri":"processor:upload","links":{"link":[{"id":"b","bound":"in"},{"id":"c","bound":"out"}]}},` +
		`{"id":"c","type":"sink","uri":"log","links":{"link":{"id":"c","bound":"in"}}}]}}}}},` +
		`"core":{"connections":` + partnerSftp + `,` +
		`"processors":{"processor":{"name":"upload","parameters":[],"action":{"uri":"sftp:/out","options":{"connection":"partner"}}}}}}}`
	nodes, err := parseNodes(t, doc)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := nodes["b"].Bindings[stepdef.ConnectionBinding].(*stepdef.Connection)
	if c == nil || c.Name != "partner" || c.Fields["host"] != "sftp.example" {
		t.Errorf("binding = %+v", c)
	}
	if _, has := nodes["b"].Options["connection"]; has {
		t.Errorf("options = %v", nodes["b"].Options)
	}

	param := strings.Replace(doc, `"name":"upload","parameters":[],"action":{"uri":"sftp:/out","options":{"connection":"partner"}}`,
		`"name":"upload","parameters":[{"name":"c","type":"string"}],"action":{"uri":"sftp:/out","options":{"connection":{"$param":"c"}}}`, 1)
	d, err := DecodeDocument([]byte(param))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.ProcessorChecks(); err == nil || !strings.Contains(err.Error(), `processor "upload": option connection cannot be a parameter`) {
		t.Errorf("err = %v", err)
	}
}
