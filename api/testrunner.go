package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"dif/engine"
	flowdef "dif/flows/definition"
	flowimpl "dif/flows/impl"
	"dif/internal/channels"
	"dif/internal/policy"
	"dif/internal/program"
	"dif/message"
	stepdef "dif/steps/definition"
	stepimpl "dif/steps/impl"
)

// TestResult is the outcome of one declarative flow test.
type TestResult struct {
	Name       string             `json:"name"`
	Flow       string             `json:"flow"`
	Origin     string             `json:"origin,omitempty"`
	Passed     bool               `json:"passed"`
	DurationMs int64              `json:"durationMs"`
	Failures   []string           `json:"failures,omitempty"`
	Trace      []engine.StepTrace `json:"trace,omitempty"`
}

// TestReport summarizes a RunTests invocation.
type TestReport struct {
	Total      int          `json:"total"`
	Passed     int          `json:"passed"`
	Failed     int          `json:"failed"`
	DurationMs int64        `json:"durationMs"`
	Results    []TestResult `json:"results"`
}

// RunTests prepares sources and runs all declarative tests in the entrypoint
// documents hermetically under an empty capability sandbox.
func RunTests(ctx context.Context, sources []ProgramSource, limits ProgramLimits) (*TestReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	prepared, err := PrepareProgram(ctx, sources, limits)
	if err != nil {
		return nil, err
	}
	return prepared.RunTests(ctx)
}

// RunTests runs all declarative tests declared in the program's entrypoint
// documents hermetically: live sources are bypassed, mocked steps are
// substituted without constructing their real processors, unmocked steps with
// external capabilities are rejected before execution, and an empty capability
// sandbox guards runtime targets.
func (p *PreparedProgram) RunTests(ctx context.Context) (*TestReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report := &TestReport{Results: []TestResult{}}
	if p == nil || p.resolved == nil {
		return report, nil
	}
	reportStart := time.Now()
	plansByID := make(map[string]program.Entry, len(p.resolved.Flows))
	infosByID := make(map[string]*FlowInfo, len(p.resolved.Flows))
	for i, entry := range p.resolved.Flows {
		plansByID[entry.Plan.ID] = entry
		infosByID[entry.Plan.ID] = p.infos[i]
	}
	sandbox, err := policy.New(nil)
	if err != nil {
		return nil, err
	}
	for _, te := range p.resolved.Tests {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res := p.runOneTest(ctx, te, plansByID, infosByID, sandbox)
		report.Results = append(report.Results, res)
		report.Total++
		if res.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	report.DurationMs = time.Since(reportStart).Milliseconds()
	return report, nil
}

func (p *PreparedProgram) runOneTest(
	ctx context.Context,
	te program.TestEntry,
	plansByID map[string]program.Entry,
	infosByID map[string]*FlowInfo,
	sandbox *policy.Policy,
) TestResult {
	start := time.Now()
	res := TestResult{
		Name:   te.Test.Name,
		Flow:   te.Test.Flow,
		Origin: te.Origin,
	}
	fail := func(failures ...string) TestResult {
		res.Failures = append(res.Failures, failures...)
		res.Passed = false
		res.DurationMs = time.Since(start).Milliseconds()
		return res
	}

	entry, ok := plansByID[te.Test.Flow]
	if !ok {
		return fail(fmt.Sprintf("flow %q not found", te.Test.Flow))
	}
	info := infosByID[te.Test.Flow]

	stepExists := make(map[string]bool, len(info.Steps))
	for _, s := range info.Steps {
		stepExists[s.ID] = true
	}

	mocksByStep := make(map[string]*mockState, len(te.Test.Mocks))
	for _, m := range te.Test.Mocks {
		if !stepExists[m.Step] {
			return fail(fmt.Sprintf("mock references unknown step %q in flow %q", m.Step, te.Test.Flow))
		}
		mocksByStep[m.Step] = &mockState{spec: m}
	}

	// Capability-enforced hermeticity: every non-source, non-mocked step must
	// declare zero external capabilities.
	var capFailures []string
	for _, s := range info.Steps {
		if s.Kind == stepdef.Source || s.ID == info.Source {
			continue
		}
		if _, mocked := mocksByStep[s.ID]; mocked {
			continue
		}
		if s.CapabilitiesUndeclared {
			capFailures = append(capFailures, fmt.Sprintf("step %q declares no capabilities — add a mock in test %q", s.ID, te.Test.Name))
			continue
		}
		for _, c := range s.Capabilities {
			capFailures = append(capFailures, fmt.Sprintf("step %q requires %s — add a mock in test %q", s.ID, c.String(), te.Test.Name))
		}
	}
	if len(capFailures) > 0 {
		return fail(capFailures...)
	}

	rt, err := newHermeticRuntime(p)
	if err != nil {
		return fail(fmt.Sprintf("hermetic runtime initialization failed: %v", err))
	}
	defer rt.Close()

	flow, err := entry.Plan.Build(func(n *flowdef.Node) (stepdef.Processor, error) {
		if n.Kind == stepdef.Source {
			return noopSourceProcessor{}, nil
		}
		if ms, mocked := mocksByStep[n.ID]; mocked {
			if n.Kind == flowdef.Sink {
				return &mockSinkProcessor{state: ms}, nil
			}
			return &mockActionProcessor{state: ms}, nil
		}
		return steps.ProcessorWithParams(n, stepdef.Params{
			stepimpl.ChannelRuntimeKey: rt.channels,
			stepimpl.FlowRuntimeKey:    n.Flow,
		})
	})
	if err != nil {
		return fail(fmt.Sprintf("flow %q build failed: %v", te.Test.Flow, err))
	}

	inMsg := buildTestInput(flow.Input, te.Test.Input)
	tr := engine.NewTrace()
	sandboxCtx := engine.WithTrace(WithPolicy(ctx, sandbox), tr)
	runRes, runErr := engine.Run(sandboxCtx, flow, inMsg)
	res.Trace = tr.Snapshot()
	res.Failures = checkExpectations(te.Test, runRes, runErr, mocksByStep)
	res.Passed = len(res.Failures) == 0
	res.DurationMs = time.Since(start).Milliseconds()
	return res
}

// newHermeticRuntime creates an isolated in-memory Runtime for a single test
// or trace execution, declaring any program stores as non-durable so disk is
// never touched and state never leaks between runs.
func newHermeticRuntime(p *PreparedProgram) (*Runtime, error) {
	rt, err := NewRuntime(ChannelConfig{})
	if err != nil {
		return nil, err
	}
	for _, doc := range p.resolved.Documents {
		for _, s := range doc.Stores {
			if _, err := rt.channels.DeclareStore(s.Name, channels.StoreConfig{
				Durable: false,
				TTLMS:   s.TTL,
				MaxKeys: s.MaxKeys,
			}); err != nil {
				rt.Close()
				return nil, fmt.Errorf("%s: %w", doc.Name, err)
			}
		}
	}
	return rt, nil
}

func buildTestInput(flowInput message.Message, spec *flowimpl.TestInput) message.Message {
	m := message.New(nil)
	delete(m, message.CorrelationID)
	base := flowInput
	if spec != nil && spec.MessageName != "" {
		base = spec.Base
	}
	for k, v := range base {
		if !message.IsMetadata(k) {
			m[k] = cloneAny(v)
		}
	}
	if spec != nil {
		if spec.HasBody {
			m[message.Body] = cloneAny(spec.Body)
		}
		for k, v := range spec.Headers {
			m[k] = cloneAny(v)
		}
	}
	m.EnsureIdentity()
	return m
}

func checkExpectations(spec flowimpl.TestSpec, runRes *engine.Result, runErr error, mocks map[string]*mockState) []string {
	var failures []string

	actualStatus := "ok"
	var actualErr error
	if runErr != nil {
		actualStatus = "error"
		actualErr = runErr
	} else if runRes != nil && runRes.Err != nil {
		actualStatus = "handled"
		actualErr = runRes.Err
	}

	wantStatus := spec.Expect.Status
	if wantStatus == "" {
		wantStatus = "ok"
	}
	if actualStatus != wantStatus {
		if actualErr != nil {
			failures = append(failures, fmt.Sprintf("status: expected %q, got %q (%v)", wantStatus, actualStatus, actualErr))
		} else {
			failures = append(failures, fmt.Sprintf("status: expected %q, got %q", wantStatus, actualStatus))
		}
	}

	if spec.Expect.ErrorContains != "" {
		errText := ""
		if actualErr != nil {
			errText = actualErr.Error()
		}
		if !strings.Contains(errText, spec.Expect.ErrorContains) {
			failures = append(failures, fmt.Sprintf("error: expected substring %q, got %q", spec.Expect.ErrorContains, errText))
		}
	}

	var outMsg message.Message
	if runRes != nil {
		outMsg = runRes.Message
	}

	if spec.Expect.HasBody {
		var actualBody any
		if outMsg != nil {
			actualBody = outMsg[message.Body]
		}
		if !bodiesEqual(actualBody, spec.Expect.Body) {
			failures = append(failures, fmt.Sprintf("body: expected %s, got %s", formatTestValue(spec.Expect.Body), formatTestValue(actualBody)))
		}
	}

	if spec.Expect.BodyContains != "" {
		var actualBody any
		if outMsg != nil {
			actualBody = outMsg[message.Body]
		}
		bodyText := bodyAsText(actualBody)
		if !strings.Contains(bodyText, spec.Expect.BodyContains) {
			failures = append(failures, fmt.Sprintf("body: expected substring %q, got %q", spec.Expect.BodyContains, bodyText))
		}
	}

	if len(spec.Expect.Headers) > 0 {
		for _, k := range slices.Sorted(maps.Keys(spec.Expect.Headers)) {
			wantVal := spec.Expect.Headers[k]
			if outMsg == nil {
				failures = append(failures, fmt.Sprintf("header %q: expected %s, got no message", k, formatTestValue(wantVal)))
				continue
			}
			gotVal, exists := outMsg[k]
			if !exists {
				failures = append(failures, fmt.Sprintf("header %q: missing (expected %s)", k, formatTestValue(wantVal)))
				continue
			}
			if !valuesEqualJSON(gotVal, wantVal) {
				failures = append(failures, fmt.Sprintf("header %q: expected %s, got %s", k, formatTestValue(wantVal), formatTestValue(gotVal)))
			}
		}
	}

	if len(spec.Expect.TrailContains) > 0 {
		var trail []string
		if runRes != nil {
			trail = runRes.Trail
		}
		for _, want := range spec.Expect.TrailContains {
			if !trailContainsEntry(trail, want) {
				failures = append(failures, fmt.Sprintf("trail: expected %q in [%s]", want, strings.Join(trail, " -> ")))
			}
		}
	}

	for _, m := range spec.Mocks {
		if m.Called != nil {
			got := int(mocks[m.Step].calls.Load())
			if got != *m.Called {
				failures = append(failures, fmt.Sprintf("mock %q: expected %d call(s), got %d", m.Step, *m.Called, got))
			}
		}
	}

	return failures
}

func trailContainsEntry(trail []string, want string) bool {
	hasColon := strings.Contains(want, ":")
	for _, entry := range trail {
		if entry == want {
			return true
		}
		if !hasColon {
			if _, id, ok := strings.Cut(entry, ":"); ok && id == want {
				return true
			}
		}
	}
	return false
}

func bodiesEqual(actual, expected any) bool {
	if valuesEqualJSON(actual, expected) {
		return true
	}
	// Allow structured expected JSON (object/array) to match serialized JSON
	// string/bytes bodies produced by steps such as mapper or setbody, and vice
	// versa.
	if isStructured(expected) {
		if raw, ok := asBytes(actual); ok {
			var parsed any
			if json.Unmarshal(raw, &parsed) == nil {
				return valuesEqualJSON(parsed, expected)
			}
		}
	}
	if isStructured(actual) {
		if raw, ok := asBytes(expected); ok {
			var parsed any
			if json.Unmarshal(raw, &parsed) == nil {
				return valuesEqualJSON(actual, parsed)
			}
		}
	}
	return false
}

func valuesEqualJSON(a, b any) bool {
	if reflect.DeepEqual(a, b) {
		return true
	}
	if ab, ok := a.([]byte); ok {
		if bs, ok := b.(string); ok {
			return string(ab) == bs
		}
	}
	if as, ok := a.(string); ok {
		if bb, ok := b.([]byte); ok {
			return as == string(bb)
		}
	}
	na, okA := normalizeJSONValue(a)
	nb, okB := normalizeJSONValue(b)
	if okA && okB {
		return reflect.DeepEqual(na, nb)
	}
	return false
}

func normalizeJSONValue(v any) (any, bool) {
	if v == nil {
		return nil, true
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, false
	}
	return out, true
}

func isStructured(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

func asBytes(v any) ([]byte, bool) {
	switch x := v.(type) {
	case string:
		return []byte(x), true
	case []byte:
		return x, true
	default:
		return nil, false
	}
}

func bodyAsText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case map[string]any, []any:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
	}
	return fmt.Sprint(v)
}

func formatTestValue(v any) string {
	if v == nil {
		return "null"
	}
	if b, ok := v.([]byte); ok {
		return fmt.Sprintf("%q", string(b))
	}
	if data, err := json.Marshal(v); err == nil {
		return string(data)
	}
	return fmt.Sprint(v)
}

type noopSourceProcessor struct{}

func (noopSourceProcessor) Run(ctx context.Context, _ stepdef.Emit) error {
	<-ctx.Done()
	return nil
}

type mockState struct {
	spec  flowimpl.TestMock
	calls atomic.Int64
}

func (ms *mockState) apply(m message.Message) error {
	ms.calls.Add(1)
	if ms.spec.Error != "" {
		return errors.New(ms.spec.Error)
	}
	if ms.spec.HasBody {
		m[message.Body] = cloneAny(ms.spec.Body)
	}
	for k, v := range ms.spec.Headers {
		m[k] = cloneAny(v)
	}
	return nil
}

type mockActionProcessor struct {
	state *mockState
}

func (p *mockActionProcessor) Process(_ context.Context, m message.Message) (message.Message, error) {
	if err := p.state.apply(m); err != nil {
		return nil, err
	}
	return m, nil
}

type mockSinkProcessor struct {
	state *mockState
}

func (p *mockSinkProcessor) Consume(_ context.Context, m message.Message) error {
	return p.state.apply(m)
}
