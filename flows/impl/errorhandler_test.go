package impl

import (
	"strings"
	"testing"
	"time"

	flowdef "dif/flows/definition"
)

// errorSteps are an action x that fails over to the error steps given: each has
// its own one-step error route, a sink named after the error step.
func errorFlow(errorSteps ...string) string {
	steps := `{"id":"a","type":"source","links":{"link":{"id":"x","bound":"out"}}},` +
		`{"id":"x","type":"action","links":{"link":[{"id":"x","bound":"in"},{"id":"b","bound":"out"}]}},` + sink
	for _, e := range errorSteps {
		steps += "," + e
	}
	return flow(steps)
}

// errorStepWithRoute is an error step with the options (a JSON object) and an
// error route that is a sink called route-<id>.
func errorStepWithRoute(id, options string) string {
	step := `{"id":"` + id + `","type":"error","uri":"failedexchange","options":` + options +
		`,"links":{"link":{"id":"route-` + id + `","bound":"out"}}},`
	return step + `{"id":"route-` + id + `","type":"sink","links":{"link":{"id":"route-` + id + `","bound":"in"}}}`
}

func TestParseErrorHandlersByError(t *testing.T) {
	f, err := Parse([]byte(errorFlow(
		errorStepWithRoute("files", `{"errorClass":"*fs.*","errorStep":"x","maximumRedeliveries":3,"redeliveryDelay":100,"backoffMultiplier":"2","maximumRedeliveryDelay":250}`),
		errorStepWithRoute("web", `{"errorUri":"https","errorMessage":"status [45]\\d\\d","handled":false}`),
		errorStepWithRoute("rest", `{}`),
	)), newNoop)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Handlers) != 2 || f.Error == nil || f.Error.ID != "rest" || f.Error.Match != nil {
		t.Fatalf("handlers = %v, default = %+v; want two with a match and the option-less one as default", f.Handlers, f.Error)
	}
	var ids []string
	for _, h := range f.ErrorHandlers() {
		ids = append(ids, h.ID+"->"+h.Route.ID)
	}
	if got := strings.Join(ids, " "); got != "files->route-files web->route-web rest->route-rest" {
		t.Errorf("handlers in order = %q, want the matched ones first and each with its own route", got)
	}

	files, web := f.Handlers[0], f.Handlers[1]
	if files.Redeliveries != 3 || files.RedeliveryDelay != 100*time.Millisecond || files.Backoff != 2 || files.MaxDelay != 250*time.Millisecond || files.Unhandled {
		t.Errorf("files handler = %+v", files)
	}
	if strings.Join(files.Matches, ",") != "errorClass,errorStep" && strings.Join(files.Matches, ",") != "errorStep,errorClass" {
		t.Errorf("files handler matches on %v", files.Matches)
	}
	if !web.Unhandled || web.Redeliveries != 0 || web.RedeliveryDelay != time.Second {
		t.Errorf("web handler = %+v, want unhandled with the default redelivery", web)
	}

	tests := []struct {
		name    string
		h       *flowdef.ErrorHandler
		failure flowdef.Failure
		want    bool
	}{
		{"class and step", files, flowdef.Failure{Step: "x", Class: "*fs.PathError"}, true},
		{"class only", files, flowdef.Failure{Step: "y", Class: "*fs.PathError"}, false},
		{"step only", files, flowdef.Failure{Step: "x", Class: "*net.OpError"}, false},
		{"uri scheme and message", web, flowdef.Failure{URI: "https://host/path", Message: "status 503 Service Unavailable"}, true},
		{"uri with another scheme", web, flowdef.Failure{URI: "sftp:host/dir", Message: "status 503"}, false},
		{"message not matching", web, flowdef.Failure{URI: "https", Message: "status 200"}, false},
	}
	for _, tt := range tests {
		if got := tt.h.Match(tt.failure); got != tt.want {
			t.Errorf("%s: match = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// A flow may have handlers with a match only; an error none takes fails.
func TestParseErrorHandlersWithoutDefault(t *testing.T) {
	f, err := Parse([]byte(errorFlow(errorStepWithRoute("files", `{"errorClass":"*fs.*"}`))), newNoop)
	if err != nil || f.Error != nil || len(f.Handlers) != 1 {
		t.Errorf("handlers = %v, default = %v, err = %v; want one handler and no default", f.Handlers, f.Error, err)
	}
}

func TestParseErrorHandlersInvalid(t *testing.T) {
	const errStep = `{"id":"e","type":"error","uri":"failedexchange"`
	tests := []struct {
		name, json, want string
	}{
		{"two defaults", errorFlow(errStep+`}`, strings.Replace(errStep, `"e"`, `"f"`, 1)+`}`), "step f: flow has more than one error step that takes every error"},
		{"empty pattern", errorFlow(errStep + `,"options":{"errorClass":""}}`), "option errorClass: want a pattern"},
		{"pattern is no text", errorFlow(errStep + `,"options":{"errorStep":3}}`), "option errorStep: want a pattern"},
		{"bad regular expression", errorFlow(errStep + `,"options":{"errorMessage":"("}}`), "option errorMessage:"},
		{"bad handled", errorFlow(errStep + `,"options":{"handled":"maybe"}}`), `option handled: want true or false, got maybe`},
		{"backoff below 1", errorFlow(errStep + `,"options":{"backoffMultiplier":0.5}}`), "option backoffMultiplier: want a number of 1 or more"},
		{"backoff no number", errorFlow(errStep + `,"options":{"backoffMultiplier":"fast"}}`), "option backoffMultiplier: want a number"},
		{"negative maximum delay", errorFlow(errStep + `,"options":{"maximumRedeliveryDelay":-5}}`), "option maximumRedeliveryDelay: want an integer of 0 or more"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.json), newNoop)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

// handled may be written as text, as DIL converted from XML has it.
func TestParseErrorHandlerHandledAsText(t *testing.T) {
	f, err := Parse([]byte(errorFlow(`{"id":"e","type":"error","uri":"failedexchange","options":{"handled":"false"}}`)), newNoop)
	if err != nil || !f.Error.Unhandled {
		t.Errorf("handler = %+v, %v; want unhandled", f.Error, err)
	}
}
