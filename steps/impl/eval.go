package impl

import (
	"context"
	"fmt"
	"strings"

	"dif/message"
)

// EvalExpression evaluates a simple, xpath, or jsonpath expression against a
// copy of m without mutating m.
func EvalExpression(ctx context.Context, language, expr string, m message.Message) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lang := strings.ToLower(strings.TrimSpace(language))
	if m == nil {
		m = message.Message{}
	} else {
		m = m.Copy()
	}
	switch lang {
	case "simple":
		compiled, err := compileExpression("simple", expr)
		if err != nil {
			return nil, err
		}
		v, err := compiled.value(m, ctx)
		if err != nil {
			return nil, err
		}
		return unwrapEvalValue(v), nil
	case "xpath":
		q, err := compileXPath(expr)
		if err != nil {
			return nil, err
		}
		return q.value(bytesOf(m[message.Body]))
	case "jsonpath":
		p, err := compileJSONPath(expr)
		if err != nil {
			return nil, err
		}
		doc, err := decodeJSON(m[message.Body])
		if err != nil {
			return nil, err
		}
		res := p.eval(doc)
		switch len(res) {
		case 0:
			return nil, nil
		case 1:
			return unwrapEvalValue(res[0]), nil
		default:
			return unwrapEvalValue(res), nil
		}
	default:
		return nil, fmt.Errorf("unsupported expression language %q", language)
	}
}

func unwrapEvalValue(v any) any {
	switch x := v.(type) {
	case jlist:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = unwrapEvalValue(e)
		}
		return out
	case jmap:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = unwrapEvalValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = unwrapEvalValue(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = unwrapEvalValue(e)
		}
		return out
	default:
		return v
	}
}
