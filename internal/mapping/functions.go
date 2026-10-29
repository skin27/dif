package mapping

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

const FunctionsBindingKey = "dif.internal.functions"

type Resolver func(string) (*Function, error)
type Parameter struct {
	Name    string          `json:"name"`
	Type    string          `json:"type"`
	Default json.RawMessage `json:"default,omitempty"`
}

func (p *Parameter) UnmarshalJSON(data []byte) error {
	type plain Parameter
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode((*plain)(p)); err != nil {
		return fmt.Errorf("invalid parameter declaration")
	}
	return nil
}

type FunctionDefinition struct {
	Name       string      `json:"name"`
	Parameters []Parameter `json:"parameters"`
	Returns    string      `json:"returns"`
	Language   string      `json:"language"`
	Expression string      `json:"expression,omitempty"`
	SourceRef  string      `json:"sourceRef,omitempty"`
}
type FunctionInfo struct {
	Name       string          `json:"name"`
	Origin     string          `json:"origin,omitempty"`
	Returns    string          `json:"returns"`
	Parameters []ParameterInfo `json:"parameters"`
}
type ParameterInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Optional bool   `json:"optional"`
}
type Function struct {
	name, origin, returns string
	parameters            []Parameter
	defaults              []any
	code                  *expr
	height                int
}

func (f *Function) Info() FunctionInfo {
	i := FunctionInfo{Name: f.name, Origin: f.origin, Returns: f.returns, Parameters: []ParameterInfo{}}
	for _, p := range f.parameters {
		i.Parameters = append(i.Parameters, ParameterInfo{p.Name, p.Type, len(p.Default) > 0})
	}
	return i
}
func validType(t string) bool {
	if t == "any" {
		return true
	}
	t = strings.TrimSuffix(t, "?")
	switch t {
	case "string", "number", "boolean", "array", "object", "xml":
		return true
	}
	return false
}
func matches(t string, v any) bool {
	if isMissing(v) {
		return false
	}
	if v == nil {
		return t == "any" || strings.HasSuffix(t, "?")
	}
	if t == "any" {
		return true
	}
	t = strings.TrimSuffix(t, "?")
	switch v.(type) {
	case string:
		return t == "string"
	case bool:
		return t == "boolean"
	case *big.Rat:
		return t == "number"
	case []any:
		return t == "array"
	case object:
		return t == "object"
	case *xmlNode, xmlText:
		return t == "xml"
	}
	return false
}

// ValidateParameters also returns normalized immutable defaults.
func ValidateParameters(params []Parameter, l Limits) ([]any, error) {
	l, err := l.Normalize()
	if err != nil {
		return nil, err
	}
	if params == nil {
		return nil, fmt.Errorf("parameters are required")
	}
	defaults := make([]any, len(params))
	seen := map[string]bool{}
	optional := false
	b := &budget{ctx: context.Background(), limits: l}
	for i, p := range params {
		tokens, e := lex(p.Name, l)
		if e != nil || len(tokens) != 2 || tokens[0].kind != "id" || tokens[0].text != p.Name || p.Name == "payload" || p.Name == "headers" || seen[p.Name] {
			return nil, fmt.Errorf("invalid or duplicate parameter name")
		}
		seen[p.Name] = true
		if !validType(p.Type) {
			return nil, fmt.Errorf("parameter %q: invalid type", p.Name)
		}
		if len(p.Default) > 0 {
			optional = true
			v, e := readJSON(p.Default, b)
			if e != nil || !matches(p.Type, v) {
				return nil, fmt.Errorf("parameter %q: invalid default", p.Name)
			}
			defaults[i] = v
		} else if optional {
			return nil, fmt.Errorf("required parameters must precede defaulted parameters")
		}
	}
	return defaults, nil
}

// CheckNativeType validates a processor configuration argument without coercion.
func CheckNativeType(t string, v any, l Limits) error {
	l, e := l.Normalize()
	if e != nil {
		return e
	}
	n, e := normalize(v, &budget{ctx: context.Background(), limits: l}, 0)
	if e != nil {
		return e
	}
	if !validType(t) || !matches(t, n) {
		return fmt.Errorf("argument type mismatch")
	}
	return nil
}
func CompileFunction(ctx context.Context, d FunctionDefinition, origin string, l Limits, resolve Resolver) (*Function, error) {
	l, e := l.Normalize()
	if e != nil {
		return nil, e
	}
	if e = ctx.Err(); e != nil {
		return nil, e
	}
	if d.Name == "" || d.Language != "difmap/1" || !validType(d.Returns) {
		return nil, fmt.Errorf("invalid function name, language, or return type")
	}
	defaults, e := ValidateParameters(d.Parameters, l)
	if e != nil {
		return nil, e
	}
	scope := map[string]bool{}
	for _, p := range d.Parameters {
		scope[p.Name] = true
	}
	code, e := compileScoped(d.Expression, l, scope, resolve)
	if e != nil {
		return nil, e
	}
	if code.op == "literal" && !matches(d.Returns, code.value) {
		return nil, fmt.Errorf("function return type mismatch")
	}
	height := 1
	var visit func(*expr)
	visit = func(x *expr) {
		if x.function != nil {
			height = max(height, 1+x.function.height)
		}
		for _, a := range x.args {
			visit(a)
		}
	}
	visit(code)
	if height > l.CallDepth {
		return nil, fmt.Errorf("function call depth limit exceeded")
	}
	return &Function{name: d.Name, origin: origin, returns: d.Returns, parameters: append([]Parameter(nil), d.Parameters...), defaults: defaults, code: code, height: height}, nil
}
func (f *Function) CheckArity(n int) error {
	required := len(f.parameters)
	for required > 0 && len(f.parameters[required-1].Default) > 0 {
		required--
	}
	if n < required || n > len(f.parameters) {
		return fmt.Errorf("function %q: invalid arity", f.name)
	}
	return nil
}
func (f *Function) invoke(args []any, b *budget) (result any, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("function %q (%s): %w", f.name, f.origin, err)
		}
	}()
	if err = f.CheckArity(len(args)); err != nil {
		return nil, err
	}
	b.calls++
	defer func() { b.calls-- }()
	if b.calls > b.limits.CallDepth {
		return nil, fmt.Errorf("function call depth limit exceeded")
	}
	if err = b.node(0); err != nil {
		return nil, err
	}
	values := map[string]any{}
	for i, p := range f.parameters {
		v := f.defaults[i]
		if i < len(args) {
			v = args[i]
		}
		if !matches(p.Type, v) {
			return nil, fmt.Errorf("parameter %q requires %s", p.Name, p.Type)
		}
		values[p.Name] = v
	}
	result, err = f.code.eval(b, &environment{values: values})
	if err != nil {
		return nil, err
	}
	if !matches(f.returns, result) {
		return nil, fmt.Errorf("return requires %s", f.returns)
	}
	return result, nil
}

// Functions is immutable once bound into a prepared program.
type Functions struct {
	plans  map[string]*Function
	limits Limits
}

func NewFunctions(plans map[string]*Function, l Limits) *Functions {
	l, _ = l.Normalize()
	copy := map[string]*Function{}
	for k, v := range plans {
		copy[k] = v
	}
	return &Functions{copy, l}
}
func (s *Functions) Resolve(name string) (*Function, error) {
	if s != nil {
		if f := s.plans[name]; f != nil {
			return f, nil
		}
	}
	return nil, fmt.Errorf("function %q not found", name)
}
func (s *Functions) Session(ctx context.Context) *Session {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Session{b: budget{ctx: ctx, limits: s.limits}}
}

type Session struct {
	b           budget
	outputBytes int
}

func (s *Session) Invoke(f *Function, args []any) (any, error) {
	a := make([]any, len(args))
	for i, v := range args {
		n, e := normalize(v, &s.b, 0)
		if e != nil {
			return nil, e
		}
		a[i] = n
	}
	v, e := f.invoke(a, &s.b)
	if e != nil {
		return nil, e
	}
	// The JSON boundary rejects XML and preserves exact numbers in json.Number.
	w := output{b: &s.b}
	if e = w.json(v, 0); e != nil {
		return nil, e
	}
	if w.s.Len() > s.b.limits.Bytes-s.outputBytes {
		return nil, fmt.Errorf("function session output byte limit exceeded")
	}
	s.outputBytes += w.s.Len()
	if e = s.b.work(w.s.Len()); e != nil {
		return nil, e
	}
	d := json.NewDecoder(strings.NewReader(w.s.String()))
	d.UseNumber()
	var out any
	if e = d.Decode(&out); e != nil {
		return nil, e
	}
	return out, nil
}

func (s *Functions) Limits() Limits { return s.limits }

func (f *Function) CheckArgument(index int, v any, l Limits) error {
	if index < 0 || index >= len(f.parameters) {
		return fmt.Errorf("function argument index out of range")
	}
	if e := CheckNativeType(f.parameters[index].Type, v, l); e != nil {
		return fmt.Errorf("function %q parameter %q: %w", f.name, f.parameters[index].Name, e)
	}
	return nil
}
