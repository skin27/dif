package impl

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/itchyny/gojq"
)

// jq: ${jq(expression)} runs the jq program on the body (JSON) with
// github.com/itchyny/gojq, as Camel's jq language does. The program has
// $headers (the headers of the message) and $body, and the functions
// header("name") and body, as in Camel.

const jqPrelude = `def header($n): $headers[$n]; def body: $body; `

// jqTimeout bounds a program, such as one that loops forever.
var jqTimeout = 10 * time.Second

// jqFunc is ${jq(expression)} and ${jq(expression,type)}: the result of the
// program on the body; nothing is nothing, one value is that value (a text as
// it is, an object or array as JSON), and several are a list.
func jqFunc(c *compiler, b *block, args string) (evalFn, error) {
	toks := splitArgs(args, false, true)
	if strings.TrimSpace(args) == "" || len(toks) > 2 {
		return nil, fmt.Errorf("valid syntax: ${jq(expression)} or ${jq(expression,type)}")
	}
	prog, err := c.template(b, toks[0])
	if err != nil {
		return nil, err
	}
	typ := ""
	if len(toks) == 2 {
		typ = strings.TrimPrefix(toks[1], "java.lang.")
	}
	var static *gojq.Code
	if !strings.ContainsRune(toks[0], phOpen) {
		if static, err = compileJQ(toks[0]); err != nil {
			return nil, err
		}
	}
	return func(e *env) (any, error) {
		code := static
		if code == nil {
			s, _, err := str(e, prog)
			if err != nil {
				return nil, err
			}
			if code, err = compileJQ(s); err != nil {
				return nil, err
			}
		}
		out, err := runJQ(code, e)
		if err != nil {
			return nil, err
		}
		switch typ {
		case "":
			return out, nil
		case "String":
			return toStringValue(out), nil
		case "Integer", "Long", "int", "long":
			return toIntValue(out), nil
		case "Boolean", "boolean":
			return toBoolValue(out), nil
		}
		return nil, fmt.Errorf("jq result type %q is not supported", typ)
	}, nil
}

func compileJQ(program string) (*gojq.Code, error) {
	q, err := gojq.Parse(jqPrelude + strings.TrimSpace(program))
	if err != nil {
		return nil, fmt.Errorf("jq program %q: %w", program, err)
	}
	code, err := gojq.Compile(q, gojq.WithVariables([]string{"$headers", "$body"}))
	if err != nil {
		return nil, fmt.Errorf("jq program %q: %w", program, err)
	}
	return code, nil
}

// runJQ runs a program on the body of the message in e.
func runJQ(code *gojq.Code, e *env) (any, error) {
	doc, err := decodeJSON(e.bodyValue())
	if err != nil {
		return nil, err
	}
	headers := map[string]any{}
	for k, v := range headersOf(e.m) {
		headers[k] = jqInput(v)
	}
	ctx, cancel := context.WithTimeout(context.Background(), jqTimeout)
	defer cancel()
	it := code.RunWithContext(ctx, doc, headers, doc)
	var results []any
	for {
		v, ok := it.Next()
		if !ok {
			break
		}
		if err, isErr := v.(error); isErr {
			return nil, fmt.Errorf("jq: %w", err)
		}
		results = append(results, jqOutput(v))
	}
	switch len(results) {
	case 0:
		return nil, nil
	case 1:
		return results[0], nil
	}
	return jlist(results), nil
}

// jqInput makes a header a value jq can read.
func jqInput(v any) any {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64, string, bool, nil, map[string]any, []any:
		return x
	case []byte:
		return string(x)
	}
	return fmt.Sprint(v)
}

// jqOutput makes a result of jq a value of a message: whole numbers are int64.
func jqOutput(v any) any {
	switch x := v.(type) {
	case int:
		return int64(x)
	case *big.Int:
		if x.IsInt64() {
			return x.Int64()
		}
		return x.String()
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return int64(x)
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = jqOutput(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = jqOutput(e)
		}
		return out
	}
	return v
}
