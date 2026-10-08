package impl

import (
	"encoding/json"
	"fmt"
)

// Template produces a small DIL flow. HTTP uses DIF's HTTPS REST source and
// requires a server identity at runtime. File input is moved to .done.
func Template(name, id string) ([]byte, error) {
	if id == "" {
		return nil, fmt.Errorf("flow id must not be empty")
	}
	source := dilStep{ID: id + "-source", Type: "source"}
	sink := dilStep{ID: id + "-sink", Type: "sink", URI: "log", Options: map[string]any{"showBody": true}}
	core := map[string]any{}
	switch name {
	case "hello":
		source.URI = "message:hello"
		core["messages"] = map[string]any{"message": map[string]any{"name": "hello", "body": "Hello from DIF"}}
	case "timer":
		source.URI = "timer:" + id
		source.Options = map[string]any{"period": 1000}
	case "file":
		source.URI = "file:inbox"
	case "http":
		source.URI = "rest:hello"
		source.Options = map[string]any{"method": "get", "address": "127.0.0.1:9002", "produces": "text/plain"}
	default:
		return nil, fmt.Errorf("unknown template %q; use hello, timer, file or http", name)
	}
	source.Links.Link = []dilLink{{ID: id + "-link", Bound: "out"}}
	sink.Links.Link = []dilLink{{ID: id + "-link", Bound: "in"}}
	f := dilFlow{ID: id, Name: id}
	f.Steps.Step = []dilStep{source, sink}
	doc := map[string]any{"dil": map[string]any{"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": f}}}, "core": core}}
	data, err := json.MarshalIndent(doc, "", "  ")
	return append(data, '\n'), err
}
