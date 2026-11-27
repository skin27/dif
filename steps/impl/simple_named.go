package impl

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	flowdef "dif/flows/definition"
	"dif/internal/mapping"
	"dif/message"
)

type simpleRuntime struct {
	ctx     context.Context
	session *mapping.Session
}

func expressionEnv(m message.Message, contexts []context.Context) *env {
	ctx := context.Background()
	if len(contexts) > 0 && contexts[0] != nil {
		ctx = contexts[0]
	}
	return &env{m: m, runtime: &simpleRuntime{ctx: ctx}}
}
func namedFunction(c *compiler, b *block, raw string) (evalFn, error) {
	args := splitArgs(raw, true, true)
	if len(args) == 0 {
		return nil, fmt.Errorf("function requires a literal name")
	}
	name := strings.TrimSpace(args[0])
	if len(name) < 2 || !(name[0] == '\'' && name[len(name)-1] == '\'' || name[0] == '"' && name[len(name)-1] == '"') || strings.ContainsRune(name, phOpen) {
		return nil, fmt.Errorf("function requires a quoted literal name")
	}
	name = name[1 : len(name)-1]
	var scope *mapping.Functions
	if c.flow != nil {
		scope = c.flow.functions
	}
	f, err := scope.Resolve(name)
	if err != nil {
		return nil, err
	}
	if err = f.CheckArity(len(args) - 1); err != nil {
		return nil, err
	}
	values := make([]evalFn, 0, len(args)-1)
	for index, s := range args[1:] {
		if v, ok := functionLiteral(s); ok {
			if err := f.CheckArgument(index, v, scope.Limits()); err != nil {
				return nil, err
			}
		}
		if n, ok := exactLiteral(s); ok {
			values = append(values, func(*env) (any, error) { return n, nil })
			continue
		}
		v, err := c.value(b, strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("function argument: invalid expression")
		}
		values = append(values, v)
	}
	return func(e *env) (any, error) {
		if e.runtime == nil {
			e.runtime = &simpleRuntime{ctx: context.Background()}
		}
		if e.runtime.session == nil {
			e.runtime.session = scope.Session(e.runtime.ctx)
		}
		args := make([]any, 0, len(values))
		for _, fn := range values {
			v, err := fn(e)
			if err != nil {
				return nil, err
			}
			switch x := v.(type) {
			case jlist:
				v = []any(x)
			case jmap:
				v = map[string]any(x)
			case nullLiteral:
				v = nil
			}
			args = append(args, v)
		}
		return e.runtime.session.Invoke(f, args)
	}, nil
}

// exactCompare is used only when the new function bridge supplies json.Number.
// Legacy Simple numeric coercion remains unchanged for legacy values.
func exactCompare(a, b any) (int, bool) {
	_, an := a.(json.Number)
	_, bn := b.(json.Number)
	if !an && !bn {
		return 0, false
	}
	parse := func(v any) (*big.Rat, bool) {
		switch v.(type) {
		case json.Number, string, int, int64, float64, float32:
		default:
			return nil, false
		}
		s := render(v)
		if len(s) > 8192 || !json.Valid([]byte(s)) {
			return nil, false
		}
		if at := strings.IndexAny(s, "eE"); at >= 0 {
			exp, e := strconv.Atoi(s[at+1:])
			if e != nil || exp > 4096 || exp < -4096 {
				return nil, false
			}
		}
		return new(big.Rat).SetString(s)
	}
	x, ok := parse(a)
	y, ok2 := parse(b)
	if !ok || !ok2 {
		return 0, false
	}
	return x.Cmp(y), true
}

// ValidateFunctionExpressions checks configured named calls without constructing
// processors or exposing expression contents. Dynamic message templates compile later.
func ValidateFunctionExpressions(n *flowdef.Node) error {
	scope, _ := n.Bindings[mapping.FunctionsBindingKey].(*mapping.Functions)
	flow := &flowProperties{functions: scope}
	var check func(any) error
	check = func(v any) error {
		switch x := v.(type) {
		case string:
			if json.Valid([]byte(x)) {
				var decoded any
				if json.Unmarshal([]byte(x), &decoded) == nil {
					return check(decoded)
				}
			}
			if !strings.Contains(x, "function(") {
				return nil
			}
			if _, err := (&compiler{flow: flow}).compile(x); err != nil {
				return fmt.Errorf("step %q: invalid function expression: %s", n.ID, redactedFunctionError(err))
			}
		case map[string]any:
			for key, v := range x {
				if lang, _ := x["language"].(string); lang == "constant" && (key == "expression" || key == "value") {
					continue
				}
				if e := check(v); e != nil {
					return e
				}
			}
		case []any:
			for _, v := range x {
				if e := check(v); e != nil {
					return e
				}
			}
		}
		return nil
	}
	options := n.Options
	if n.URI == "setbody" && options["language"] == nil {
		options = map[string]any{}
		for k, v := range n.Options {
			options[k] = v
		}
		options["language"] = "constant"
	}
	if err := check(options); err != nil {
		return err
	}
	for _, l := range n.Links {
		if l.Language == "constant" {
			continue
		}
		if e := check(l.Expression); e != nil {
			return e
		}
	}
	return nil
}
func redactedFunctionError(err error) string {
	// Named-call failures contain declaration names and contracts only. Other Simple
	// parser diagnostics may contain the source; do not expose those during preparation.
	s := err.Error()
	if strings.HasPrefix(s, "function ") && !strings.Contains(s, "${") && !strings.Contains(s, "$simple{") {
		return s
	}
	return "invalid call syntax, arguments, or unresolved function"
}

func exactLiteral(s string) (json.Number, bool) {
	s = strings.TrimSpace(s)
	if len(s) == 0 || !(s[0] == '-' || s[0] >= '0' && s[0] <= '9') || !json.Valid([]byte(s)) {
		return "", false
	}
	return json.Number(s), true
}

func (c *compiler) validateFunctionSource(source string) error {
	if c.flow == nil || c.flow.functions == nil || !strings.Contains(source, "function(") {
		return nil
	}
	limits := c.flow.functions.Limits()
	if len(source) > limits.SourceBytes {
		return fmt.Errorf("function expression source limit exceeded")
	}
	depth := 0
	for i := 0; i < len(source); i++ {
		if n := blockStart(source, i); n > 0 {
			depth++
			if depth > limits.Depth {
				return fmt.Errorf("function expression nesting limit exceeded")
			}
			i += n - 1
		} else if source[i] == '\\' && i+1 < len(source) && source[i+1] == '}' {
			i++
		} else if source[i] == '}' && depth > 0 {
			depth--
		}
	}
	return nil
}

func functionLiteral(s string) (any, bool) {
	s = strings.TrimSpace(s)
	if strings.ContainsRune(s, phOpen) {
		return nil, false
	}
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1], true
	}
	if n, ok := exactLiteral(s); ok {
		return n, true
	}
	switch s {
	case "true":
		return true, true
	case "false":
		return false, true
	case "null":
		return nil, true
	}
	return nil, false
}
