package api

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"dif/engine"
	flowdef "dif/flows/definition"
	"dif/internal/dilsyntax"
	"dif/internal/mapping"
	"dif/message"
	stepdef "dif/steps/definition"
	stepimpl "dif/steps/impl"
)

type (
	Trace      = engine.Trace
	StepTrace  = engine.StepTrace
	HeaderDiff = engine.HeaderDiff
)

// Trace runs msg once through f with an opt-in step-diff Trace attached,
// bypassing f's live source processor.
func (f *Flow) Trace(ctx context.Context, msg Message) (*Result, *engine.Trace, error) {
	if f == nil || f.Runner == nil {
		return nil, nil, fmt.Errorf("flow is nil")
	}
	return f.Runner.Trace(ctx, msg)
}

// TraceFlow runs msg once through flow with an opt-in step-diff Trace attached,
// bypassing the live source processor.
func TraceFlow(ctx context.Context, flow *Flow, msg Message) (*Result, *engine.Trace, error) {
	if flow == nil || flow.Runner == nil {
		return nil, nil, fmt.Errorf("flow is nil")
	}
	return flow.Trace(ctx, msg)
}

// EvalExpression evaluates a simple, difmap/1 (or map), xpath, or jsonpath
// expression against msg without mutating msg, returning the computed value
// and its human-readable type name.
func EvalExpression(ctx context.Context, lang, expr string, msg Message) (any, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var m message.Message
	if msg != nil {
		m = cloneMessage(msg)
	} else {
		m = message.Message{}
	}
	normalized := strings.ToLower(strings.TrimSpace(lang))
	var (
		val any
		err error
	)
	switch normalized {
	case "simple", "xpath", "jsonpath":
		val, err = stepimpl.EvalExpression(ctx, normalized, expr, m)
	case "map", "difmap/1":
		ct, _ := m[message.ContentType].(string)
		val, err = mapping.EvalExpression(ctx, expr, m[message.Body], ct, m, mapping.Limits{})
	default:
		return nil, "", fmt.Errorf("unsupported language %q; use simple, map (difmap/1), xpath or jsonpath", lang)
	}
	if err != nil {
		return nil, "", err
	}
	return val, valueTypeName(val), nil
}

// ExecInline wraps a .dil step chain (e.g.
// `setbody(language: "simple", expression: "${uppercase(${body})}") -> passthrough`)
// into an ephemeral flow, compiles it via internal/dilsyntax, runs it once
// against a copy of msg with step tracing attached, and returns the outcome
// and trace.
func ExecInline(ctx context.Context, dilChain string, msg Message) (*Result, *engine.Trace, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	chain := strings.TrimSpace(dilChain)
	chain = strings.TrimSpace(strings.TrimPrefix(chain, "->"))
	if chain == "" {
		return nil, nil, fmt.Errorf("empty chain")
	}
	src := "flow _repl {\n  message:_repl\n  -> " + chain + "\n}\n"
	data, err := dilsyntax.Document("<repl>", "", []byte(src))
	if err != nil {
		return nil, nil, err
	}
	f, err := parseFlow(data, func(n *flowdef.Node) (stepdef.Processor, error) {
		if n.Kind == stepdef.Source {
			return noopSourceProcessor{}, nil
		}
		return steps.Processor(n)
	})
	if err != nil {
		return nil, nil, err
	}
	var in message.Message
	if msg != nil {
		in = cloneMessage(msg)
	} else {
		in = message.New(nil)
	}
	tr := engine.NewTrace()
	res, err := engine.Run(engine.WithTrace(ctx, tr), f, in)
	return res, tr, err
}

// TraceFlow builds a single flow from a prepared program, bypassing its live
// source processor, and runs one message through it with step tracing enabled.
// When flowID is empty, the program must contain exactly one flow.
func (p *PreparedProgram) TraceFlow(ctx context.Context, flowID string, body any, hasBody bool, headers map[string]any) (string, *Result, *engine.Trace, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if p == nil || p.resolved == nil || len(p.resolved.Flows) == 0 {
		return "", nil, nil, fmt.Errorf("program contains no flows")
	}
	var targetIdx = -1
	if flowID == "" {
		if len(p.resolved.Flows) > 1 {
			ids := make([]string, len(p.resolved.Flows))
			for i, e := range p.resolved.Flows {
				ids[i] = e.Plan.ID
			}
			slices.Sort(ids)
			return "", nil, nil, fmt.Errorf("multiple flows found (%s); specify one with --flow", strings.Join(ids, ", "))
		}
		targetIdx = 0
	} else {
		for i, e := range p.resolved.Flows {
			if e.Plan.ID == flowID {
				targetIdx = i
				break
			}
		}
		if targetIdx < 0 {
			return "", nil, nil, fmt.Errorf("flow %q not found", flowID)
		}
	}
	entry := p.resolved.Flows[targetIdx]
	rt, err := newHermeticRuntime(p)
	if err != nil {
		return entry.Plan.ID, nil, nil, err
	}
	defer rt.Close()
	f, err := entry.Plan.Build(func(n *flowdef.Node) (stepdef.Processor, error) {
		if n.Kind == stepdef.Source {
			return noopSourceProcessor{}, nil
		}
		return steps.ProcessorWithParams(n, stepdef.Params{
			stepimpl.ChannelRuntimeKey: rt.channels,
			stepimpl.FlowRuntimeKey:    n.Flow,
		})
	})
	if err != nil {
		return entry.Plan.ID, nil, nil, fmt.Errorf("flow %q initialization failed: %w", entry.Plan.ID, err)
	}
	in := message.New(nil)
	delete(in, message.CorrelationID)
	for k, v := range f.Input {
		if !message.IsMetadata(k) {
			in[k] = cloneAny(v)
		}
	}
	if hasBody {
		in[message.Body] = cloneAny(body)
	}
	for k, v := range headers {
		in[k] = cloneAny(v)
	}
	in.EnsureIdentity()
	tr := engine.NewTrace()
	res, err := engine.Run(engine.WithTrace(ctx, tr), f, in)
	return entry.Plan.ID, res, tr, err
}

func valueTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []byte:
		return "bytes"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func cloneMessage(m message.Message) message.Message {
	out := make(message.Message, len(m))
	for k, v := range m {
		out[k] = cloneAny(v)
	}
	return out
}

func cloneAny(v any) any {
	switch x := v.(type) {
	case []byte:
		return slices.Clone(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cloneAny(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = cloneAny(e)
		}
		return out
	default:
		return v
	}
}
