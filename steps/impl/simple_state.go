package impl

import (
	"fmt"
	"sort"
	"strings"

	"dif/message"
)

// What the simple language reads that is not in the body or a header: the
// properties of the flow, the error a message carries to the error route,
// and variables.

// registerStateRefs adds ${flowId} and the other references of this file.
func registerStateRefs(refs map[string]refDef) {
	flowProp := func(get func(*flowProperties) string) refDef {
		return func(c *compiler, _ *block) (evalFn, error) {
			f := c.flow
			return func(*env) (any, error) {
				if f == nil {
					return nil, nil
				}
				return get(f), nil
			}, nil
		}
	}
	refs["flowId"] = flowProp(func(f *flowProperties) string { return f.id })
	refs["flowName"] = flowProp(func(f *flowProperties) string { return f.name })
	refs["flowVersion"] = flowProp(func(f *flowProperties) string { return f.version })
	refs["tenant"] = flowProp(func(f *flowProperties) string { return f.tenant })
	refs["environment"] = flowProp(func(f *flowProperties) string { return f.environment })
}

// metaData compiles ${variable:group:<id>:MetaData.<name>}, how the platform
// writes the properties of a flow.
func (c *compiler) metaData(b *block, name string) (evalFn, error) {
	key, ok := strings.CutPrefix(name, "MetaData.")
	if !ok {
		return nil, unsupported(b, "")
	}
	f := c.flow
	return func(*env) (any, error) {
		if f == nil {
			return nil, nil
		}
		switch key {
		case "FlowID":
			return f.id, nil
		case "FlowName":
			return f.name, nil
		case "FlowVersion":
			return f.version, nil
		case "TenantName":
			return f.tenant, nil
		case "EnvironmentName":
			return f.environment, nil
		}
		return nil, nil // the platform has more (Frontend); DIF does not
	}, nil
}

// exception compiles ${exception}, ${exception.message}, ${exception.class} and
// ${exception.stacktrace}. The engine puts the error on the message that goes
// along the error route as the headers error.message, error.class and
// error.stacktrace; on any other message there is no exception.
func (c *compiler) exception(b *block, rest string) (evalFn, error) {
	get := func(e *env, key string) any {
		if v, ok := e.m[key]; ok {
			return v
		}
		return nil
	}
	switch rest {
	case "":
		return func(e *env) (any, error) {
			msg := get(e, message.ErrorMessage)
			if msg == nil {
				return nil, nil
			}
			if class := get(e, message.ErrorClass); class != nil {
				return render(class) + ": " + render(msg), nil
			}
			return msg, nil
		}, nil
	case ".message":
		return func(e *env) (any, error) { return get(e, message.ErrorMessage), nil }, nil
	case ".class":
		return func(e *env) (any, error) { return get(e, message.ErrorClass), nil }, nil
	case ".stacktrace":
		return func(e *env) (any, error) { return get(e, message.ErrorStackTrace), nil }, nil
	}
	return nil, unsupported(b, "use ${exception}, ${exception.message}, ${exception.class} or ${exception.stacktrace}")
}

// Variables live on the message, as metadata, so that a later step reads what
// an earlier one set.
const variablePrefix = message.MetadataPrefix + "variable."

func variableValue(m message.Message, name string) any { return m[variablePrefix+name] }

// variablesOf returns all variables of m by name.
func variablesOf(m message.Message) jmap {
	vars := jmap{}
	for k, v := range m {
		if name, ok := strings.CutPrefix(k, variablePrefix); ok {
			vars[name] = v
		}
	}
	return vars
}

// storable makes a value fit for the message: lists and maps of a function are
// plain ones.
func storable(v any) any {
	switch x := v.(type) {
	case jlist:
		return []any(x)
	case jmap:
		return map[string]any(x)
	}
	return v
}

const (
	initStart = "$init{"
	initEnd   = "}init$"
)

// assignment is $name := value in an init block.
type assignment struct {
	name string
	fn   evalFn
}

// compileInit compiles an expression that starts with an init block,
// $init{ $name := value; ... }init$, which sets variables, followed by the
// expression itself, in which $name is the variable ${variable.name}.
func (c *compiler) compileInit(expr string) (expression, error) {
	end := strings.Index(expr, initEnd)
	if end < 0 {
		return expression{}, fmt.Errorf("init block: no %s after %s", initEnd, initStart)
	}
	body := expr[len(initStart):end]
	rest := expr[end+len(initEnd):]
	rest = strings.TrimPrefix(rest, "\n")

	stmts, err := splitInit(body)
	if err != nil {
		return expression{}, err
	}
	var assigns []assignment
	for _, st := range stmts {
		name, value, ok := strings.Cut(st, ":=")
		name = strings.TrimSpace(name)
		if !ok || !validVariable(name) {
			return expression{}, fmt.Errorf("init block: want $name := value, not %q", st)
		}
		pb, err := pseudoBlock(strings.TrimSpace(value))
		if err != nil {
			return expression{}, err
		}
		fn, err := c.operand(pb, pb.body)
		if err != nil {
			return expression{}, fmt.Errorf("init block %s: %w", name, err)
		}
		assigns = append(assigns, assignment{name[1:], fn})
	}

	// $name in the expression is ${variable.name}, the longest name first.
	names := make([]string, len(assigns))
	for i, a := range assigns {
		names[i] = a.name
	}
	sort.Slice(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
	for _, n := range names {
		rest = strings.ReplaceAll(rest, "$"+n, "${variable."+n+"}")
	}
	tail, err := c.compile(rest)
	if err != nil {
		return expression{}, err
	}
	return expression{fn: func(e *env) (any, error) {
		for _, a := range assigns {
			v, err := a.fn(e)
			if err != nil {
				return nil, fmt.Errorf("init block $%s: %w", a.name, err)
			}
			e.m[variablePrefix+a.name] = storable(v)
		}
		return tail.run(e)
	}}, nil
}

func validVariable(name string) bool {
	if len(name) < 2 || name[0] != '$' {
		return false
	}
	for _, c := range name[1:] {
		if !(c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

// splitInit cuts the body of an init block into statements at the semicolons
// that are outside quotes and ${...}, and drops the // comments.
func splitInit(body string) ([]string, error) {
	var stmts []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(body); {
		c := body[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
			i++
		case c == '\'' || c == '"':
			quote = c
			cur.WriteByte(c)
			i++
		case blockStart(body, i) > 0:
			end, err := scanBlock(body, i)
			if err != nil {
				return nil, err
			}
			cur.WriteString(body[i:end])
			i = end
		case strings.HasPrefix(body[i:], "//"):
			for i < len(body) && body[i] != '\n' {
				i++
			}
		case c == ';':
			if t := strings.TrimSpace(cur.String()); t != "" {
				stmts = append(stmts, t)
			}
			cur.Reset()
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		stmts = append(stmts, t)
	}
	return stmts, nil
}
