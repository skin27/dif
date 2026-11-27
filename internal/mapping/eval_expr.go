package mapping

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// EvalExpression compiles and evaluates a standalone difmap/1 expression
// against body and headers without mutating them.
func EvalExpression(ctx context.Context, expr string, body any, contentType string, headers map[string]any, limits Limits) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l, err := limits.Normalize()
	if err != nil {
		return nil, err
	}
	src := strings.TrimSpace(expr)
	if src == "" {
		return nil, fmt.Errorf("mapping expression is required")
	}
	if len(src) > l.SourceBytes {
		return nil, fmt.Errorf("mapping source size limit exceeded")
	}
	code, err := compileScoped(src, l, map[string]bool{"payload": true, "headers": true}, nil)
	if err != nil {
		return nil, err
	}
	b := &budget{ctx: ctx, limits: l}
	input, err := decodeEvalInput(body, contentType, b)
	if err != nil {
		return nil, err
	}
	val, err := code.eval(b, &environment{values: map[string]any{"payload": input, "headers": headerValues(headers)}})
	if err != nil {
		return nil, err
	}
	return exportValue(val, b)
}

func decodeEvalInput(body any, contentType string, b *budget) (any, error) {
	if body == nil {
		return nil, nil
	}
	var raw []byte
	var isText bool
	switch x := body.(type) {
	case string:
		raw, isText = []byte(x), true
	case []byte:
		raw, isText = x, true
	}
	if isText {
		trimmed := bytes.TrimSpace(raw)
		ct := strings.ToLower(contentType)
		if strings.Contains(ct, "xml") {
			return readXML(raw, b)
		}
		if len(trimmed) > 0 {
			if v, err := readJSON(trimmed, b); err == nil {
				return v, nil
			}
			if trimmed[0] == '<' {
				if v, err := readXML(trimmed, b); err == nil {
					return v, nil
				}
			}
		}
		return normalize(string(raw), b, 0)
	}
	return normalize(body, b, 0)
}

func exportValue(v any, b *budget) (any, error) {
	switch x := v.(type) {
	case nil, bool, string:
		return x, nil
	case missing:
		return nil, nil
	case *big.Rat:
		if x.IsInt() && x.Num().IsInt64() {
			return x.Num().Int64(), nil
		}
		if s, err := decimal(x, b.limits); err == nil {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				return f, nil
			}
		}
		f, _ := x.Float64()
		return f, nil
	case object:
		out := make(map[string]any, len(x))
		for _, m := range x {
			ev, err := exportValue(m.value, b)
			if err != nil {
				return nil, err
			}
			out[m.key] = ev
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			ev, err := exportValue(e, b)
			if err != nil {
				return nil, err
			}
			out[i] = ev
		}
		return out, nil
	case *xmlNode:
		w := &output{b: b}
		if err := w.xml(x); err != nil {
			return nil, err
		}
		return w.s.String(), nil
	case xmlText:
		return string(x), nil
	default:
		return x, nil
	}
}
