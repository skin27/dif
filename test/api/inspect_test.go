package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dif/api"
	flowimpl "dif/flows/impl"
	stepdef "dif/steps/definition"
)

func TestInspectWithoutConstructingProcessor(t *testing.T) {
	called := false
	name := fmt.Sprintf("inspect-no-constructor-%d", time.Now().UnixNano())
	if err := api.RegisterStep(api.StepDefinition{Name: name, Kind: "source", Schema: []byte(`{"type":"object"}`), New: func(string, stepdef.Params) (stepdef.Processor, error) { called = true; return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	data, err := flowimpl.Template("hello", "offline")
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("message:hello"), []byte(name), 1)
	path := filepath.Join(t.TempDir(), "flow.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := api.Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("inspection constructed a processor")
	}
	if info.ID != "offline" || info.Source != "offline-source" || len(info.Steps) != 2 {
		t.Fatalf("unexpected description: %+v", info)
	}
}

func TestTemplatesLoadAndHelloRequest(t *testing.T) {
	for _, name := range []string{"hello", "timer", "file", "http"} {
		t.Run(name, func(t *testing.T) {
			data, err := flowimpl.Template(name, "starter")
			if err != nil {
				t.Fatal(err)
			}
			if name == "http" {
				data = bytes.Replace(data, []byte(`"method": "get"`), []byte(`"method": "get", "serverIdentityFile": "../../keystore/testdata/server-identity.p12", "serverIdentityPassword": "changeit"`), 1)
			}
			path := filepath.Join(t.TempDir(), "starter.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			flow, err := api.Load(path, nil)
			if err != nil {
				t.Fatalf("template does not load: %v", err)
			}
			if name == "hello" {
				flow.SetLogger(log.New(io.Discard, "", 0))
				if err := flow.Start(); err != nil {
					t.Fatal(err)
				}
				defer flow.Stop()
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				reply, err := flow.Request(ctx, flow.NewMessage())
				if err != nil {
					t.Fatal(err)
				}
				if reply[api.Body] != "Hello from DIF" {
					t.Fatalf("reply: %v", reply)
				}
			}
		})
	}
}

func TestInspectHTTPSWithoutCredentials(t *testing.T) {
	data, _ := flowimpl.Template("http", "endpoint")
	path := filepath.Join(t.TempDir(), "endpoint.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := api.Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range info.Configuration {
		if c.Environment == "DIF_SERVER_IDENTITY_PASSWORD" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing identity password configuration")
	}
	encoded, _ := json.Marshal(info)
	if strings.Contains(string(encoded), "127.0.0.1") {
		t.Fatal("description exposed URI or option values")
	}
}

func TestValidateInvalidStructureAndOptions(t *testing.T) {
	base, _ := flowimpl.Template("timer", "clock")
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"invalid json", []byte("{")},
		{"unknown step", bytes.Replace(base, []byte("timer:clock"), []byte("unknown:clock"), 1)},
		{"invalid period", bytes.Replace(base, []byte(`"period": 1000`), []byte(`"period": 0`), 1)},
		{"broken graph", bytes.Replace(base, []byte(`"bound": "in"`), []byte(`"bound": "out"`), 1)},
		{"duplicate step id", bytes.Replace(base, []byte(`"id": "clock-sink"`), []byte(`"id": "clock-source"`), 1)},
		{"missing flow id", bytes.Replace(base, []byte(`"id": "clock"`), []byte(`"id": ""`), 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "flow.json")
			if err := os.WriteFile(path, tc.data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := api.Validate(path); err == nil {
				t.Fatal("invalid flow accepted")
			}
		})
	}
}
