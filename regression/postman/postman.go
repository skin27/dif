// Package postman reads the Postman collections stored as YAML (the layout
// Postman writes for its git integration: postman/collections/<collection>/
// <request>.request.yaml and postman/environments/*.environment.yaml) and
// converts them to the JSON that Newman runs.
package postman

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Header is a request header or query parameter.
type Header struct {
	Key, Value string
	Disabled   bool
}

// Request is one request of a collection with its test script.
type Request struct {
	ID         string // path of the request file below the directory given to Load
	Collection string
	Name       string
	Order      int
	Method     string
	URL        string // with the address of the server replaced by {{BASE}}
	Flow       string // the flow the URL calls: its last path segment, lower case
	Headers    []Header
	BodyMode   string // "", "raw" or "urlencoded"
	BodyLang   string // raw: xml, json, html or text
	Body       string
	Form       []Header // urlencoded
	Tests      string   // the afterResponse script
	Prerequest string   // the beforeRequest script
	Auth       map[string]any
	Problem    string // why the request cannot be run, or ""
}

// ProblemAttachment marks a request whose body is a file that the collection
// does not contain.
const ProblemAttachment = "the body is an attachment that is not in the repository"

type rawRequest struct {
	Name        string    `yaml:"name"`
	URL         string    `yaml:"url"`
	Method      string    `yaml:"method"`
	Headers     yaml.Node `yaml:"headers"`
	QueryParams yaml.Node `yaml:"queryParams"`
	Body        *struct {
		Type    string    `yaml:"type"`
		Content yaml.Node `yaml:"content"`
	} `yaml:"body"`
	Scripts []struct {
		Type string `yaml:"type"`
		Code string `yaml:"code"`
	} `yaml:"scripts"`
	Order int `yaml:"order"`
	Auth  *struct {
		Type        string    `yaml:"type"`
		Credentials yaml.Node `yaml:"credentials"`
	} `yaml:"auth"`
}

// The address of the server in a request URL: Postman variables, or the
// addresses of the Dovetail test servers. Both end in the path of the flows.
var serverRe = regexp.MustCompile(`^(?:\{\{HOST\}\}|https?)://(?:\{\{INSTANCE\}\}|[\w-]+)\.(?:\{\{BASE_URI\}\}|dovetail\.world)/[\w{}-]+/(?:\{\{PATH\}\}|inbound_http/regressiontests)/`)

// Load reads the requests of all collections below dir/collections, sorted by
// collection, order and name. Requests that cannot be read are returned with a
// Problem.
func Load(dir string) ([]Request, error) {
	files, err := filepath.Glob(filepath.Join(dir, "collections", "*", "*.request.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var reqs []Request
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f)
		req := Request{ID: filepath.ToSlash(rel), Collection: filepath.Base(filepath.Dir(f))}
		req.Name = strings.TrimSuffix(filepath.Base(f), ".request.yaml")
		if err := req.read(f); err != nil {
			req.Problem = err.Error()
		}
		reqs = append(reqs, req)
	}
	sort.SliceStable(reqs, func(i, j int) bool {
		a, b := reqs[i], reqs[j]
		if a.Collection != b.Collection {
			return a.Collection < b.Collection
		}
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.Name < b.Name
	})
	return reqs, nil
}

func (r *Request) read(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw rawRequest
	if err := yaml.Unmarshal(data, &raw); err != nil {
		// Some files hold control characters and invalid UTF-8 that YAML forbids,
		// inside double-quoted strings, where they can be written as escapes.
		var err2 error
		if raw, err2 = decodeLenient(data); err2 != nil {
			return fmt.Errorf("not valid YAML: %w", err)
		}
	}
	if raw.Name != "" {
		r.Name = raw.Name
	}
	r.Order = raw.Order
	r.Method = strings.ToUpper(raw.Method)
	if r.Method == "" {
		r.Method = "GET"
	}
	r.URL = serverRe.ReplaceAllString(raw.URL, "{{BASE}}/")
	r.Headers = pairs(&raw.Headers)

	// Postman repeats the query in queryParams; add what the URL lacks.
	have := map[string]bool{}
	if _, q, ok := strings.Cut(r.URL, "?"); ok {
		for _, kv := range strings.Split(q, "&") {
			k, _, _ := strings.Cut(kv, "=")
			have[k] = true
		}
	}
	for _, q := range pairs(&raw.QueryParams) {
		if q.Disabled || have[q.Key] {
			continue
		}
		sep := "?"
		if strings.Contains(r.URL, "?") {
			sep = "&"
		}
		r.URL += sep + q.Key + "=" + q.Value
	}
	r.Flow = flowOf(r.URL)

	if b := raw.Body; b != nil {
		switch b.Type {
		case "xml", "json", "html", "text":
			r.BodyMode, r.BodyLang, r.Body = "raw", b.Type, b.Content.Value
		case "urlencoded":
			r.BodyMode, r.Form = "urlencoded", pairs(&b.Content)
		case "file":
			r.Problem = ProblemAttachment
		case "":
		default:
			r.Problem = "unsupported body type " + b.Type
		}
	}
	for _, s := range raw.Scripts {
		switch s.Type {
		case "afterResponse":
			r.Tests = s.Code
		case "beforeRequest":
			r.Prerequest = s.Code
		}
	}
	if a := raw.Auth; a != nil && a.Type != "" && a.Type != "noauth" {
		r.Auth = auth(a.Type, &a.Credentials)
	}
	return nil
}

func flowOf(url string) string {
	path, _, _ := strings.Cut(url, "?")
	path = strings.TrimRight(path, "/")
	return strings.ToLower(path[strings.LastIndex(path, "/")+1:])
}

// pairs reads a mapping, or a list of {key, value, disabled}.
func pairs(n *yaml.Node) []Header {
	var out []Header
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			out = append(out, Header{Key: n.Content[i].Value, Value: n.Content[i+1].Value})
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			var h Header
			for i := 0; i+1 < len(item.Content); i += 2 {
				switch item.Content[i].Value {
				case "key":
					h.Key = item.Content[i+1].Value
				case "value":
					h.Value = item.Content[i+1].Value
				case "disabled":
					h.Disabled = item.Content[i+1].Value == "true"
				}
			}
			out = append(out, h)
		}
	}
	return out
}

// auth converts the credentials of a request to the auth object of a v2.1 collection.
func auth(kind string, n *yaml.Node) map[string]any {
	var list []map[string]string
	for _, p := range pairs(n) {
		list = append(list, map[string]string{"key": p.Key, "value": p.Value, "type": "string"})
	}
	return map[string]any{"type": kind, kind: list}
}

// decodeLenient parses data after rewriting the characters that YAML does not
// allow (and invalid UTF-8) as escape sequences, which double-quoted strings
// understand. It is only used when the file does not parse as it is.
func decodeLenient(data []byte) (rawRequest, error) {
	var out bytes.Buffer
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		switch {
		case r == utf8.RuneError && size <= 1:
			out.WriteString(`�`)
		case r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r < 0x7f) || r == 0x85 || (r >= 0xa0 && r <= 0xd7ff) || (r >= 0xe000 && r <= 0xfffd) || r >= 0x10000:
			out.Write(data[:size])
		case r < 0x100:
			fmt.Fprintf(&out, `\x%02x`, r)
		default:
			fmt.Fprintf(&out, `\u%04x`, r)
		}
		data = data[size:]
	}
	var raw rawRequest
	err := yaml.Unmarshal(out.Bytes(), &raw)
	return raw, err
}

// Item converts r to an item of a v2.1 collection. The ID of the item is the
// ID of the request, so that a Newman report can be matched to it.
func (r Request) Item() map[string]any {
	headers := []map[string]any{}
	hasType := false
	for _, h := range r.Headers {
		if strings.EqualFold(h.Key, "Content-Type") && !h.Disabled {
			hasType = true
		}
		headers = append(headers, map[string]any{"key": h.Key, "value": h.Value, "disabled": h.Disabled})
	}
	req := map[string]any{"method": r.Method, "url": r.URL}
	switch r.BodyMode {
	case "raw":
		req["body"] = map[string]any{"mode": "raw", "raw": r.Body, "options": map[string]any{"raw": map[string]string{"language": r.BodyLang}}}
		if !hasType {
			headers = append(headers, map[string]any{"key": "Content-Type", "value": map[string]string{"xml": "application/xml", "json": "application/json", "html": "text/html", "text": "text/plain"}[r.BodyLang]})
		}
	case "urlencoded":
		var form []map[string]any
		for _, f := range r.Form {
			form = append(form, map[string]any{"key": f.Key, "value": f.Value, "disabled": f.Disabled})
		}
		req["body"] = map[string]any{"mode": "urlencoded", "urlencoded": form}
	}
	req["header"] = headers
	if r.Auth != nil {
		req["auth"] = r.Auth
	}
	item := map[string]any{"id": r.ID, "name": r.Name, "request": req}
	var events []map[string]any
	for _, e := range []struct{ listen, code string }{{"prerequest", r.Prerequest}, {"test", r.Tests}} {
		if strings.TrimSpace(e.code) != "" {
			events = append(events, map[string]any{"listen": e.listen, "script": map[string]any{"type": "text/javascript", "exec": strings.Split(e.code, "\n")}})
		}
	}
	if events != nil {
		item["event"] = events
	}
	return item
}

// Collection returns the v2.1 collection of the requests, in the given order.
func Collection(name string, reqs []Request) ([]byte, error) {
	items := make([]map[string]any, 0, len(reqs))
	for _, r := range reqs {
		items = append(items, r.Item())
	}
	return json.Marshal(map[string]any{
		"info": map[string]any{"name": name, "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
		"item": items,
	})
}

type valuesFile struct {
	Name   string `yaml:"name"`
	Values []struct {
		Key     string `yaml:"key"`
		Value   any    `yaml:"value"`
		Enabled *bool  `yaml:"enabled"`
	} `yaml:"values"`
}

// Variables reads the values of an environment or globals file.
func Variables(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f valuesFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	vars := map[string]string{}
	for _, v := range f.Values {
		if v.Enabled != nil && !*v.Enabled {
			continue
		}
		vars[v.Key] = fmt.Sprint(v.Value)
	}
	return vars, nil
}

// Environment returns a Newman environment file with the variables, sorted by name.
func Environment(name string, vars map[string]string) ([]byte, error) {
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	values := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		values = append(values, map[string]any{"key": k, "value": vars[k], "enabled": true})
	}
	return json.Marshal(map[string]any{"name": name, "values": values})
}
