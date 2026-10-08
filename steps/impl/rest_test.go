package impl

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestRestSourceAndAction(t *testing.T) {
	addr, c := freeAddr(t), trustingClient(t)
	seen := make(chan message.Message, 10)
	echo := func(m message.Message, reply func(message.Message, error)) error {
		seen <- m
		out := message.New("got " + text(m[message.Body]))
		reply(out, nil)
		return nil
	}
	serve(t, "rest", map[string]any{"address": addr, "method": "post", "path": "/echo/json", "produces": "application/json"}, echo)
	waitServing(t, c, "https://"+addr+"/echo/json")

	// Another method is refused before the flow sees it.
	if status, _, h := call(t, c, http.MethodGet, "https://"+addr+"/echo/json", ""); status != http.StatusMethodNotAllowed || h.Get("Allow") != "POST" {
		t.Errorf("GET: status %d, Allow %q; want 405 and POST", status, h.Get("Allow"))
	}

	a := mustProcessor(t, stepdef.Action, "rest", map[string]any{
		"host": "https://" + addr + "/", "path": "echo/json", "produces": "text/x-in", "consumes": "application/json",
		"trustStoreFile": testTrustStore, "trustStorePassword": testPassword,
	}).(stepdef.ActionProcessor)
	out, err := a.Process(context.Background(), message.New("hi"))
	if err != nil {
		t.Fatal(err)
	}
	if out[message.Body] != "got hi" || out[message.ContentType] != "application/json" || out["http.status"] != 200 {
		t.Errorf("reply = %v, want got hi as application/json (the source's produces)", out)
	}
	in := <-seen
	if in["Content-Type"] != "text/x-in" || in["Accept"] != "application/json" {
		t.Errorf("request headers = %v, want Content-Type from produces and Accept from consumes", in)
	}

	// An error status fails the message by default.
	a = mustProcessor(t, stepdef.Action, "rest", map[string]any{
		"method": "get", "host": "https://" + addr, "path": "echo/json", "trustStoreFile": testTrustStore, "trustStorePassword": testPassword,
	}).(stepdef.ActionProcessor)
	if _, err := a.Process(context.Background(), message.New("")); err == nil || !strings.Contains(err.Error(), "405") {
		t.Errorf("err = %v, want the 405", err)
	}
}

func TestRestInvalid(t *testing.T) {
	ok := map[string]any{"serverIdentityFile": testIdentity, "serverIdentityPassword": testPassword}
	wantInvalid(t, stepdef.Source, "rest", ok, "missing required option path")
	ok["path"] = "/"
	wantInvalid(t, stepdef.Source, "rest", ok, "option path: empty path")
	ok["path"], ok["method"] = "x", "fetch"
	wantInvalid(t, stepdef.Source, "rest", ok, "option method")

	wantInvalid(t, stepdef.Action, "rest", map[string]any{"path": "x", "host": "ftp://h"}, `option host: want http(s)://host[:port], got "ftp://h"`)
	wantInvalid(t, stepdef.Action, "rest", map[string]any{"path": "x", "host": "localhost:9002"}, "option host")

	if restMethod("option") != "OPTIONS" || restMethod("patch") != "PATCH" {
		t.Error("restMethod")
	}
}
