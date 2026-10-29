package impl

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	stepdef "dif/steps/definition"
)

// A connection in dil.core.connections has one of two forms:
//
//   - the legacy form has keys, a bag of values that only the option
//     dataSource of sql2 reads, as the Java platform exports it;
//   - the typed form has no keys: its type is one of connectionKinds and its
//     fields are those of connectionFields that the type allows. Steps refer
//     to it with the option connection, by its id or, through a direct
//     import, as <namespace>:<id>.

// connectionKinds are the types of a typed connection.
var connectionKinds = []string{"https", "sftp", "ftps", "smb", "smtp", "imaps", "sql", "as2"}

// connectionGroups are the fields that a typed connection holds as an object.
var connectionGroups = []string{"authentication", "tls", "hostKeys"}

// connectionFields are the fields of a typed connection, by dotted name: the
// types of connection that may set the field, and the JSON type of its value
// ("number" also takes the number as text, as DIL converted from XML has it).
var connectionFields = map[string]struct{ kinds, typ string }{
	"host":                      {"sftp ftps smb smtp imaps sql as2", "string"},
	"port":                      {"sftp ftps smb smtp imaps sql as2", "number"},
	"url":                       {"https", "string"},
	"share":                     {"smb", "string"},
	"dialect":                   {"sql", "string"},
	"database":                  {"sql", "string"},
	"connectTimeout":            {"https sql as2", "number"},
	"socketTimeout":             {"https sftp ftps smb smtp imaps sql as2", "number"},
	"authentication.username":   {"https sftp ftps smb smtp imaps sql", "string"},
	"authentication.password":   {"https sftp ftps smb smtp imaps sql", "string"},
	"authentication.token":      {"https smtp imaps", "string"},
	"authentication.privateKey": {"sftp", "string"},
	"authentication.passphrase": {"sftp", "string"},
	"authentication.domain":     {"smb", "string"},
	"tls.trustStoreFile":        {"https ftps smtp imaps sql as2", "string"},
	"tls.trustStorePassword":    {"https ftps smtp imaps sql as2", "string"},
	"tls.implicit":              {"ftps", "boolean"},
	"tls.enabled":               {"sql as2", "boolean"},
	"tls.version":               {"sql", "string"},
	"hostKeys.knownHostsFile":   {"sftp", "string"},
	"hostKeys.strict":           {"sftp", "boolean"},
}

// dilConnection is a connection to a system outside the flow, such as a
// database or an SFTP server.
type dilConnection struct {
	ID   string
	Name string
	Type string // such as jdbc for the legacy form, sftp for the typed form

	// Keys are the values of the legacy form (dbtype, dbname, host, port,
	// username, password); nil for the typed form.
	Keys map[string]any

	// Fields are the fields of the typed form as they are written, with the
	// groups of connectionGroups as objects; checked by check.
	Fields map[string]any
}

func (c *dilConnection) UnmarshalJSON(data []byte) error {
	var all map[string]any
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	var head struct {
		ID   string         `json:"id"`
		Name string         `json:"name"`
		Type string         `json:"type"`
		Keys map[string]any `json:"keys"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	*c = dilConnection{ID: head.ID, Name: head.Name, Type: head.Type}
	if _, legacy := all["keys"]; legacy {
		c.Keys = head.Keys
		if c.Keys == nil {
			c.Keys = map[string]any{}
		}
		return nil
	}
	delete(all, "id")
	delete(all, "name")
	delete(all, "type")
	c.Fields = all
	return nil
}

func (c dilConnection) MarshalJSON() ([]byte, error) {
	out := map[string]any{"id": c.ID, "name": c.Name, "type": c.Type}
	if c.Keys != nil {
		out["keys"] = c.Keys
	} else {
		maps.Copy(out, c.Fields)
	}
	return json.Marshal(out)
}

// typed reports whether c has the typed form.
func (c dilConnection) typed() bool { return c.Keys == nil }

// check checks a typed connection: its type, and the name and JSON type of
// each field. The errors name the connection and the field, never a value.
func (c dilConnection) check() error {
	if !c.typed() {
		return nil
	}
	if !slices.Contains(connectionKinds, c.Type) {
		return fmt.Errorf("connection %q: type must be one of %s (or give keys, the legacy form)", c.ID, strings.Join(connectionKinds, ", "))
	}
	for _, name := range slices.Sorted(maps.Keys(c.Fields)) {
		v := c.Fields[name]
		if !slices.Contains(connectionGroups, name) {
			if err := c.checkField(name, v); err != nil {
				return err
			}
			continue
		}
		group, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("connection %q: %s must be an object", c.ID, name)
		}
		for _, sub := range slices.Sorted(maps.Keys(group)) {
			if err := c.checkField(name+"."+sub, group[sub]); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c dilConnection) checkField(name string, v any) error {
	f, ok := connectionFields[name]
	if !ok || !slices.Contains(strings.Fields(f.kinds), c.Type) {
		return fmt.Errorf("connection %q: a %s connection has no field %s", c.ID, c.Type, name)
	}
	valid := false
	switch x := v.(type) {
	case string:
		_, err := strconv.ParseFloat(x, 64)
		valid = f.typ == "string" || f.typ == "number" && err == nil
	case float64:
		valid = f.typ == "number"
	case bool:
		valid = f.typ == "boolean"
	}
	if !valid {
		return fmt.Errorf("connection %q: field %s must be a %s", c.ID, name, f.typ)
	}
	return nil
}

// ConnectionInfo describes a connection in dil.core.connections without its values.
type ConnectionInfo struct {
	ID     string `json:"id"`
	Type   string `json:"type,omitempty"`
	Legacy bool   `json:"legacy,omitempty"` // has keys, which only the dataSource of sql2 reads
}

// ConnectionInfo describes the document's connections.
func (d *Document) ConnectionInfo() []ConnectionInfo {
	out := []ConnectionInfo{}
	for _, c := range d.doc.DIL.Core.Connections.Connection {
		out = append(out, ConnectionInfo{ID: c.ID, Type: c.Type, Legacy: !c.typed()})
	}
	return out
}

// resolved returns the typed connection as steps get it, named as the step
// referred to it.
func (c dilConnection) resolved(name string) *stepdef.Connection {
	fields := map[string]any{}
	for k, v := range c.Fields {
		if group, ok := v.(map[string]any); ok {
			for sub, x := range group {
				fields[k+"."+sub] = x
			}
		} else {
			fields[k] = v
		}
	}
	for k, v := range fields {
		if s, ok := v.(string); ok && connectionFields[k].typ == "number" {
			fields[k] = strings.TrimSpace(s)
		} else if f, ok := v.(float64); ok {
			fields[k] = strconv.FormatFloat(f, 'f', -1, 64)
		}
	}
	return &stepdef.Connection{Name: name, Kind: c.Type, Fields: fields}
}

// resolveConnection resolves the connection the step refers to, so the
// processor needs no knowledge of DIL. It returns the step's options without
// the option connection, and the connection, nil if the step refers to none.
//
// A sql2 step refers to a connection with dataSource (or connection): a
// legacy one becomes its option connection, the keys as a JSON object, as
// before; for a typed one, that JSON object is made from its address and
// authentication, and the rest of it (tls, timeouts) is returned.
func resolveConnection(uri string, opts map[string]any, core coreRefs) (map[string]any, *stepdef.Connection, error) {
	scheme, _, _ := strings.Cut(uri, ":")
	if scheme == "sql2" {
		return resolveDataSource(opts, core)
	}
	name, ok := opts["connection"].(string)
	if !ok {
		if _, set := opts["connection"]; set {
			return nil, nil, fmt.Errorf("option connection must be the id of a connection in dil.core.connections")
		}
		return opts, nil, nil
	}
	c, ok := core.connections[name]
	if !ok {
		return nil, nil, fmt.Errorf("connection %q not found in dil.core.connections", name)
	}
	if !c.typed() {
		return nil, nil, fmt.Errorf("connection %q has keys, the legacy form, which only the dataSource of sql2 reads; give it a type and fields", name)
	}
	out := maps.Clone(opts)
	delete(out, "connection")
	return out, c.resolved(name), nil
}

// sqlKeys are the fields of a typed sql connection that make the option
// connection of sql2, by the name of its key.
var sqlKeys = map[string]string{
	"dialect": "dbtype", "database": "dbname", "host": "host", "port": "port",
	"authentication.username": "username", "authentication.password": "password",
}

// resolveDataSource adds to the options of a sql2 step the option "connection":
// the keys of the connection in dil.core.connections that its option
// dataSource (or, for a typed connection, connection) names, as a JSON object.
func resolveDataSource(opts map[string]any, core coreRefs) (map[string]any, *stepdef.Connection, error) {
	id, _ := opts["dataSource"].(string)
	if id == "" {
		// connection is the id of a typed connection, unless it already is
		// the JSON object (in a reusable processor that was resolved).
		ref, _ := opts["connection"].(string)
		if ref == "" || strings.HasPrefix(strings.TrimSpace(ref), "{") {
			return opts, nil, nil
		}
		if c, ok := core.connections[ref]; !ok || !c.typed() {
			return resolveConnection("", opts, core) // the same errors as other steps
		}
		id = ref
	}
	c, ok := core.connections[id]
	if !ok {
		return nil, nil, fmt.Errorf("connection %q not found in dil.core.connections", id)
	}
	keys, rest := c.Keys, (*stepdef.Connection)(nil)
	if c.typed() {
		if c.Type != "sql" {
			return nil, nil, fmt.Errorf("connection %q is %s; the step takes sql", id, c.Type)
		}
		rest = c.resolved(id)
		keys = map[string]any{}
		for field, key := range sqlKeys {
			if v, ok := rest.Fields[field]; ok {
				keys[key] = v
				delete(rest.Fields, field)
			}
		}
	}
	data, err := json.Marshal(keys)
	if err != nil {
		return nil, nil, err
	}
	out := maps.Clone(opts)
	out["connection"] = string(data)
	return out, rest, nil
}
