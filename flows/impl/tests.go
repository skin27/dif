package impl

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"dif/message"
)

// dilTest is dil.core.tests.test: a declarative hermetic flow test.
// Production flow execution ignores dil.core.tests.
type dilTest struct {
	Name   string                 `json:"name"`
	Flow   string                 `json:"flow"`
	Input  *dilTestInput          `json:"input,omitempty"`
	Mocks  oneOrMany[dilTestMock] `json:"mocks,omitempty"`
	Expect *dilTestExpect         `json:"expect"`
}

type dilTestInput struct {
	Message string         `json:"message,omitempty"`
	Body    any            `json:"-"`
	HasBody bool           `json:"-"`
	Headers map[string]any `json:"headers,omitempty"`
}

type dilTestMock struct {
	Step    string         `json:"step"`
	Body    any            `json:"-"`
	HasBody bool           `json:"-"`
	Headers map[string]any `json:"headers,omitempty"`
	Error   string         `json:"error,omitempty"`
	Called  *int           `json:"called,omitempty"`
}

type dilTestExpect struct {
	Status        string            `json:"status,omitempty"`
	Body          any               `json:"-"`
	HasBody       bool              `json:"-"`
	BodyContains  string            `json:"bodyContains,omitempty"`
	Headers       map[string]any    `json:"headers,omitempty"`
	TrailContains oneOrMany[string] `json:"trailContains,omitempty"`
	ErrorContains string            `json:"errorContains,omitempty"`
}

func (t *dilTest) UnmarshalJSON(data []byte) error {
	type plain dilTest
	return strictDeclaration(data, (*plain)(t), "test")
}

func (in *dilTestInput) UnmarshalJSON(data []byte) error {
	type raw struct {
		Message string          `json:"message,omitempty"`
		Body    json.RawMessage `json:"body"`
		Headers map[string]any  `json:"headers,omitempty"`
	}
	var r raw
	if err := strictDeclaration(data, &r, "test input"); err != nil {
		return err
	}
	in.Message = r.Message
	in.Headers = r.Headers
	if len(r.Body) > 0 {
		var body any
		if err := json.Unmarshal(r.Body, &body); err != nil {
			return fmt.Errorf("invalid test input declaration (invalid body)")
		}
		in.Body = body
		in.HasBody = true
	}
	return nil
}

func (m *dilTestMock) UnmarshalJSON(data []byte) error {
	type raw struct {
		Step    string          `json:"step"`
		Body    json.RawMessage `json:"body"`
		Headers map[string]any  `json:"headers,omitempty"`
		Error   string          `json:"error,omitempty"`
		Called  *int            `json:"called,omitempty"`
	}
	var r raw
	if err := strictDeclaration(data, &r, "test mock"); err != nil {
		return err
	}
	m.Step = r.Step
	m.Headers = r.Headers
	m.Error = r.Error
	m.Called = r.Called
	if len(r.Body) > 0 {
		var body any
		if err := json.Unmarshal(r.Body, &body); err != nil {
			return fmt.Errorf("invalid test mock declaration (invalid body)")
		}
		m.Body = body
		m.HasBody = true
	}
	return nil
}

func (e *dilTestExpect) UnmarshalJSON(data []byte) error {
	type raw struct {
		Status        string            `json:"status,omitempty"`
		Body          json.RawMessage   `json:"body"`
		BodyContains  string            `json:"bodyContains,omitempty"`
		Headers       map[string]any    `json:"headers,omitempty"`
		TrailContains oneOrMany[string] `json:"trailContains,omitempty"`
		ErrorContains string            `json:"errorContains,omitempty"`
	}
	var r raw
	if err := strictDeclaration(data, &r, "test expect"); err != nil {
		return err
	}
	e.Status = r.Status
	e.BodyContains = r.BodyContains
	e.Headers = r.Headers
	e.TrailContains = r.TrailContains
	e.ErrorContains = r.ErrorContains
	if len(r.Body) > 0 {
		var body any
		if err := json.Unmarshal(r.Body, &body); err != nil {
			return fmt.Errorf("invalid test expect declaration (invalid body)")
		}
		e.Body = body
		e.HasBody = true
	}
	return nil
}

func (t *dilTest) validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("test requires a name")
	}
	if strings.TrimSpace(t.Flow) == "" {
		return fmt.Errorf("test %q: flow is required", t.Name)
	}
	if t.Expect == nil {
		return fmt.Errorf("test %q: expect is required", t.Name)
	}
	switch t.Expect.Status {
	case "", "ok", "handled", "error":
	default:
		return fmt.Errorf("test %q: expect.status must be ok, handled or error", t.Name)
	}
	seenMocks := map[string]bool{}
	for _, m := range t.Mocks {
		if strings.TrimSpace(m.Step) == "" {
			return fmt.Errorf("test %q: mock requires a step id", t.Name)
		}
		if seenMocks[m.Step] {
			return fmt.Errorf("test %q: duplicate mock for step %q", t.Name, m.Step)
		}
		seenMocks[m.Step] = true
		if m.Called != nil && *m.Called < 0 {
			return fmt.Errorf("test %q: mock %q: called must be 0 or more", t.Name, m.Step)
		}
	}
	return nil
}

// TestSpec is a validated, resolved declarative flow test.
type TestSpec struct {
	Name   string
	Flow   string
	Input  *TestInput
	Mocks  []TestMock
	Expect TestExpect
}

// TestInput specifies the input message for a declarative test.
type TestInput struct {
	MessageName string
	Base        message.Message
	HasBody     bool
	Body        any
	Headers     map[string]any
}

// TestMock substitutes a step processor during a hermetic test run.
type TestMock struct {
	Step    string
	HasBody bool
	Body    any
	Headers map[string]any
	Error   string
	Called  *int
}

// TestExpect defines assertions checked against a test flow run.
type TestExpect struct {
	Status        string // "ok", "handled", or "error"
	HasBody       bool
	Body          any
	BodyContains  string
	Headers       map[string]any
	TrailContains []string
	ErrorContains string
}

// Tests resolves the document's declarative tests against local and directly
// imported messages.
func (d *Document) Tests(imports map[string]*Document) ([]TestSpec, error) {
	if len(d.doc.DIL.Core.Tests.Test) == 0 {
		return nil, nil
	}
	messages := map[string]dilMessage{}
	for _, m := range d.doc.DIL.Core.Messages.Message {
		messages[m.Name] = m
	}
	for _, imp := range d.Imports() {
		other := imports[imp.Namespace]
		if other == nil {
			return nil, fmt.Errorf("missing import namespace %q", imp.Namespace)
		}
		for _, m := range other.doc.DIL.Core.Messages.Message {
			key := imp.Namespace + ":" + m.Name
			if _, exists := messages[key]; !exists {
				messages[key] = m
			}
		}
	}
	out := make([]TestSpec, 0, len(d.doc.DIL.Core.Tests.Test))
	for i := range d.doc.DIL.Core.Tests.Test {
		t := &d.doc.DIL.Core.Tests.Test[i]
		if err := t.validate(); err != nil {
			return nil, err
		}
		spec := TestSpec{
			Name: t.Name,
			Flow: t.Flow,
			Expect: TestExpect{
				Status:        t.Expect.Status,
				HasBody:       t.Expect.HasBody,
				Body:          cloneTestValue(t.Expect.Body),
				BodyContains:  t.Expect.BodyContains,
				Headers:       cloneTestMap(t.Expect.Headers),
				TrailContains: slices.Clone(t.Expect.TrailContains),
				ErrorContains: t.Expect.ErrorContains,
			},
		}
		if spec.Expect.Status == "" {
			spec.Expect.Status = "ok"
		}
		if t.Input != nil {
			in := &TestInput{
				MessageName: t.Input.Message,
				HasBody:     t.Input.HasBody,
				Body:        cloneTestValue(t.Input.Body),
				Headers:     cloneTestMap(t.Input.Headers),
			}
			if in.MessageName != "" {
				dm, ok := messages[in.MessageName]
				if !ok {
					return nil, fmt.Errorf("test %q: input.message %q not found", t.Name, in.MessageName)
				}
				base := message.Message{}
				for _, h := range dm.Headers.Header {
					base[h.Name] = h.Value
				}
				if dm.Body != nil {
					base[message.Body] = cloneTestValue(dm.Body)
				}
				in.Base = base
			}
			spec.Input = in
		}
		if len(t.Mocks) > 0 {
			spec.Mocks = make([]TestMock, len(t.Mocks))
			for j, m := range t.Mocks {
				var called *int
				if m.Called != nil {
					v := *m.Called
					called = &v
				}
				spec.Mocks[j] = TestMock{
					Step:    m.Step,
					HasBody: m.HasBody,
					Body:    cloneTestValue(m.Body),
					Headers: cloneTestMap(m.Headers),
					Error:   m.Error,
					Called:  called,
				}
			}
		}
		out = append(out, spec)
	}
	return out, nil
}

func cloneTestMap(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneTestValue(v)
	}
	return out
}

func cloneTestValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = cloneTestValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cloneTestValue(e)
		}
		return out
	default:
		return v
	}
}
