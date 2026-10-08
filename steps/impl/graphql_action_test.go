package impl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestGraphQL(t *testing.T) {
	type request struct {
		auth, contentType string
		payload           map[string]any
	}
	got := make(chan request, 1)
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		data, _ := io.ReadAll(r.Body)
		json.Unmarshal(data, &payload)
		got <- request{r.Header.Get("Authorization"), r.Header.Get("Content-Type"), payload}
		if payload["query"] == "fail" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":{"character":{"name":"Rick"}}}`)
	})
	opts := func(extra map[string]any) map[string]any {
		all := map[string]any{"url": srv.URL + "/graphql", "trustStoreFile": testTrustStore, "trustStorePassword": testPassword}
		for k, v := range extra {
			all[k] = v
		}
		return all
	}

	m := message.New("ignored")
	m["X-Secret"] = "not sent"
	out := process(t, "graphql", opts(map[string]any{"query": "query { character(id: 1) { name } }", "variables": `{"id": 1}`, "accessToken": "tok"}), m)
	r := <-got
	if r.auth != "Bearer tok" || r.contentType != "application/json" || r.payload["query"] != "query { character(id: 1) { name } }" || r.payload["variables"].(map[string]any)["id"] != 1.0 {
		t.Errorf("request = %+v", r)
	}
	if out[message.Body] != `{"data":{"character":{"name":"Rick"}}}` || out[message.ContentType] != "application/json" || out["http.status"] != 200 {
		t.Errorf("message = %v", out)
	}

	// Without the option, the body is the query; without variables, none are sent.
	process(t, "graphql", opts(nil), message.New("{ hello }"))
	if r := <-got; r.payload["query"] != "{ hello }" || r.auth != "" || r.payload["variables"] != nil {
		t.Errorf("request = %+v", r)
	}

	p := mustProcessor(t, stepdef.Action, "graphql", opts(map[string]any{"query": "fail"})).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("")); err == nil || !strings.Contains(err.Error(), "400 Bad Request") {
		t.Errorf("err = %v, want the 400", err)
	}
	<-got

	wantInvalid(t, stepdef.Action, "graphql", nil, `option url: want http(s)://host[:port]/path, got ""`)
	wantInvalid(t, stepdef.Action, "graphql", map[string]any{"url": "https://x.io/q", "variables": "[1]"}, "option variables: want a JSON object")
	if _, err := newProcessor(stepdef.Action, "graphql://x.io/q", nil); err == nil || !strings.Contains(err.Error(), `got "//x.io/q"`) {
		t.Errorf("URI without scheme: err = %v", err)
	}
	if _, err := newProcessor(stepdef.Action, "graphql:https://x.io/q", nil); err != nil {
		t.Errorf("URL from the URI: %v", err)
	}
}
