package regression

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// flowSource is what the harness needs to know about the source of a flow.
type flowSource struct {
	FlowID string
	Scheme string // https, flowlink, queue, quartz, ...
	Path   string // https: the lower-case path, such as /regressiontests/xmltojson
}

// sourceOf reads the source step of a DIL document.
func sourceOf(data []byte) (flowSource, error) {
	var doc map[string]any
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &doc); err != nil {
		return flowSource{}, err
	}
	flow := first(dig(doc, "dil", "integrations", "integration", "flows", "flow"))
	f, _ := flow.(map[string]any)
	if f == nil {
		return flowSource{}, fmt.Errorf("no flow")
	}
	src := flowSource{}
	src.FlowID, _ = f["id"].(string)
	for _, s := range list(dig(f, "steps", "step")) {
		step, _ := s.(map[string]any)
		if step == nil || step["type"] != "source" {
			continue
		}
		uri, _ := step["uri"].(string)
		src.Scheme, _, _ = strings.Cut(uri, ":")
		if src.Scheme == "https" {
			if u, err := url.Parse(uri); err == nil {
				src.Path = strings.ToLower(strings.TrimRight(u.Path, "/"))
			}
		}
		return src, nil
	}
	return src, fmt.Errorf("no source step")
}

func dig(v any, keys ...string) any {
	for _, k := range keys {
		m, _ := v.(map[string]any)
		if m == nil {
			return nil
		}
		v = m[k]
	}
	return v
}

// list returns the elements of a JSON array, or the single value: DIL JSON is
// converted from XML, where one child element becomes an object.
func list(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case nil:
		return nil
	}
	return []any{v}
}

func first(v any) any {
	if l := list(v); len(l) > 0 {
		return l[0]
	}
	return nil
}

// The flows call each other over the HTTPS addresses of the Dovetail test
// servers, and listen on 0.0.0.0:9001. In the test they all use one local port.
var testServerRe = regexp.MustCompile(`https://[A-Za-z0-9-]+\.dovetail\.world/[A-Za-z]+/inbound_http/regressiontests/`)

// overlay points the addresses in a flow at the local test server.
func overlay(data []byte, port int) []byte {
	data = testServerRe.ReplaceAll(data, []byte(fmt.Sprintf("https://127.0.0.1:%d/regressiontests/", port)))
	return bytes.ReplaceAll(data, []byte("https://0.0.0.0:9001/"), []byte(fmt.Sprintf("https://127.0.0.1:%d/", port)))
}
