package impl

import (
	"strings"
	"testing"

	flowdef "dif/flows/definition"
)

// interceptedFlow is a flow named orders: source a, action x (setbody), sink b
// (log) and an error step e whose route is the sink h, in a document whose
// core is given.
func interceptedFlow(core string) string {
	return `{"dil":{"core":` + core + `,"integrations":{"integration":{"flows":{"flow":{"id":"f","name":"orders","steps":{"step":[` +
		`{"id":"a","type":"source","links":{"link":{"id":"x","bound":"out"}}},` +
		`{"id":"x","type":"action","uri":"setbody","links":{"link":[{"id":"x","bound":"in"},{"id":"b","bound":"out"}]}},` +
		`{"id":"b","type":"sink","uri":"log","links":{"link":{"id":"b","bound":"in"}}},` +
		`{"id":"e","type":"error","uri":"failedexchange","links":{"link":{"id":"h","bound":"out"}}},` +
		`{"id":"h","type":"sink","uri":"log","links":{"link":{"id":"h","bound":"in"}}}` +
		`]}}}}}}}`
}

// interceptors wraps interceptor declarations in a core.
func interceptors(declarations ...string) string {
	return `{"interceptors":{"interceptor":[` + strings.Join(declarations, ",") + `]}}`
}

const audit = `{"name":"audit","action":{"uri":"log","options":{"showBody":true}}`

// route lists the main path of the flow from the source, as kind:id.
func route(t *testing.T, core string) (string, *flowdef.Flow) {
	t.Helper()
	f, err := Parse([]byte(interceptedFlow(core)), newNoop)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for n := f.Source; n != nil; {
		ids = append(ids, n.Kind+":"+n.ID)
		if len(n.Next) == 0 {
			break
		}
		n = n.Next[0]
	}
	return strings.Join(ids, " "), f
}

func TestWeaveBeforeEveryStep(t *testing.T) {
	got, f := route(t, interceptors(audit+`}`))
	want := "source:a router:intercept:audit:x action:x router:intercept:audit:b sink:b"
	if got != want {
		t.Errorf("path = %q\nwant   %q", got, want)
	}

	// The router passes the message on to the step and taps a copy to the action.
	r := f.Source.Next[0]
	if r.URI != "wiretap" || len(r.Next) != 2 || r.Next[0].ID != "x" || r.Next[1].ID != "intercept:audit:x:action" {
		t.Fatalf("router = %+v", r)
	}
	if r.Links[0].Rule != "" || r.Links[1].Rule != "wiretap" {
		t.Errorf("links = %+v, want the tap to have the rule wiretap", r.Links)
	}
	tap := r.Next[1]
	if tap.Kind != flowdef.Sink || tap.URI != "log" || tap.Options["showBody"] != true || len(tap.Next) != 0 {
		t.Errorf("tap = %+v, want the log sink with the options of the declaration", tap)
	}
	// The error route is not intercepted.
	if h := f.Error.Route; h.ID != "h" || h.Kind != flowdef.Sink {
		t.Errorf("error route = %+v, want the flow's own step", h)
	}
}

func TestWeavePointcuts(t *testing.T) {
	tests := []struct {
		name, declaration, want string
	}{
		{"steps by id", audit + `,"pointcut":{"steps":["x"]}}`, "source:a router:intercept:audit:x action:x sink:b"},
		{"steps by pattern", audit + `,"pointcut":{"steps":"?"}}`, "source:a router:intercept:audit:x action:x router:intercept:audit:b sink:b"},
		{"uris by scheme", audit + `,"pointcut":{"uris":["log"]}}`, "source:a action:x router:intercept:audit:b sink:b"},
		{"kinds", audit + `,"pointcut":{"kinds":["sink"]}}`, "source:a action:x router:intercept:audit:b sink:b"},
		{"kind and step", audit + `,"pointcut":{"kinds":["sink"],"steps":["x"]}}`, "source:a action:x sink:b"},
		{"flow by name", audit + `,"pointcut":{"flows":["ord*"],"steps":["x"]}}`, "source:a router:intercept:audit:x action:x sink:b"},
		{"flow by id", audit + `,"pointcut":{"flows":["f"],"steps":["x"]}}`, "source:a router:intercept:audit:x action:x sink:b"},
		{"another flow", audit + `,"pointcut":{"flows":["other*"]}}`, "source:a action:x sink:b"},
		{"entry", `{"name":"audit","at":"entry","action":{"uri":"log"}}`, "source:a router:intercept:audit:x action:x sink:b"},
		{"entry of another flow", `{"name":"audit","at":"entry","pointcut":{"flows":["other"]},"action":{"uri":"log"}}`, "source:a action:x sink:b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, _ := route(t, interceptors(tt.declaration)); got != tt.want {
				t.Errorf("path = %q\nwant   %q", got, tt.want)
			}
		})
	}
}

// Several interceptors on a step run in the order they are declared, and an
// interceptor does not intercept the steps another one added.
func TestWeaveSeveralInterceptors(t *testing.T) {
	got, _ := route(t, interceptors(
		`{"name":"first","action":{"uri":"log"},"pointcut":{"steps":["x"]}}`,
		`{"name":"second","action":{"uri":"log"}}`,
	))
	want := "source:a router:intercept:first:x router:intercept:second:x action:x router:intercept:second:b sink:b"
	if got != want {
		t.Errorf("path = %q\nwant   %q", got, want)
	}
}

func TestWeaveWithoutInterceptors(t *testing.T) {
	got, _ := route(t, `{}`)
	if got != "source:a action:x sink:b" {
		t.Errorf("path = %q, want the flow as it is", got)
	}
}

// The action of an interceptor may be a reusable processor.
func TestWeaveReusableProcessor(t *testing.T) {
	core := `{"processors":{"processor":{"name":"note","parameters":[{"name":"text","type":"string","default":"seen"}],` +
		`"action":{"uri":"setbody","options":{"language":"constant","expression":{"$param":"text"}}}}},` +
		`"interceptors":{"interceptor":{"name":"audit","pointcut":{"steps":["x"]},"action":{"uri":"processor:note","options":{"arguments":{"text":"tapped"}}}}}}`
	_, f := route(t, core)
	tap := f.Source.Next[0].Next[1]
	if tap.URI != "setbody" || tap.Options["expression"] != "tapped" || tap.Kind != flowdef.Sink {
		t.Errorf("tap = %+v, want the setbody processor with its argument", tap)
	}
}

func TestInterceptorsInvalid(t *testing.T) {
	tests := []struct {
		name, core, want string
	}{
		{"no name", interceptors(`{"action":{"uri":"log"}}`), "interceptor requires a name"},
		{"name with a colon", interceptors(`{"name":"a:b","action":{"uri":"log"}}`), "interceptor requires a name"},
		{"twice", interceptors(audit+`}`, audit+`}`), `interceptor "audit": declared twice`},
		{"unknown field", interceptors(audit + `,"when":"always"}`), "invalid interceptor declaration"},
		{"bad at", interceptors(audit + `,"at":"after"}`), `interceptor "audit": at must be before or entry`},
		{"entry with steps", interceptors(`{"name":"audit","at":"entry","pointcut":{"steps":["x"]},"action":{"uri":"log"}}`), "at entry selects no steps"},
		{"bad kind", interceptors(audit + `,"pointcut":{"kinds":["source"]}}`), "pointcut kinds are action, router or sink"},
		{"empty pattern", interceptors(audit + `,"pointcut":{"steps":[""]}}`), "pointcut steps:"},
		{"no action", interceptors(`{"name":"audit"}`), "requires an action with a URI"},
		{"unused but invalid", interceptors(audit + `,"at":"after","pointcut":{"flows":["other"]}}`), "at must be before or entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(interceptedFlow(tt.core)), newNoop)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

// An id of the flow that an interceptor would take is reported.
func TestWeaveIdCollision(t *testing.T) {
	data := strings.Replace(interceptedFlow(interceptors(audit+`}`)), `"id":"h","type":"sink"`, `"id":"intercept:audit:x","type":"sink"`, 1)
	data = strings.Replace(data, `"link":{"id":"h","bound":"out"}`, `"link":{"id":"intercept:audit:x","bound":"out"}`, 1)
	data = strings.Replace(data, `"link":{"id":"h","bound":"in"}`, `"link":{"id":"intercept:audit:x","bound":"in"}`, 1)
	if _, err := Parse([]byte(data), newNoop); err == nil || !strings.Contains(err.Error(), `interceptor "audit": id "intercept:audit:x" is already used`) {
		t.Errorf("err = %v, want the collision", err)
	}
}
