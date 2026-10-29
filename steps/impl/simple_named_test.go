package impl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"dif/internal/mapping"
	"dif/message"
	stepdef "dif/steps/definition"
)

func namedScope(t *testing.T, l mapping.Limits) *mapping.Functions {
	t.Helper()
	plans := map[string]*mapping.Function{}
	for _, d := range []mapping.FunctionDefinition{
		{Name: "normal", Parameters: []mapping.Parameter{{Name: "x", Type: "string"}}, Returns: "string", Language: "difmap/1", Expression: `upper(trim(x))`},
		{Name: "exact", Parameters: []mapping.Parameter{{Name: "x", Type: "number"}}, Returns: "number", Language: "difmap/1", Expression: `x`},
		{Name: "object", Parameters: []mapping.Parameter{}, Returns: "object", Language: "difmap/1", Expression: `{n:9007199254740993,a:[true,null]}`},
	} {
		f, e := mapping.CompileFunction(context.Background(), d, "lib.json", l, nil)
		if e != nil {
			t.Fatal(e)
		}
		plans[d.Name] = f
	}
	return mapping.NewFunctions(plans, l)
}
func TestNamedSimpleFunctions(t *testing.T) {
	scope := namedScope(t, mapping.Limits{})
	flow := &flowProperties{functions: scope}
	for _, c := range []struct{ src, want string }{
		{`${function('normal', ${header.customer})}`, "ABC"},
		{`id=${function('normal', ${header.customer})}`, "id=ABC"},
		{`${function('exact', 9007199254740993)}`, "9007199254740993"},
		{`${function('exact', ${function('exact', ${header.n})})}`, "9007199254740993"},
		{`${function('object')}`, `{"a":[true,null],"n":9007199254740993}`},
	} {
		x, e := compileExpressionIn(flow, "simple", c.src)
		if e != nil {
			t.Fatal(c.src, e)
		}
		v, e := x.eval(message.Message{"customer": " abc ", "n": json.Number("9007199254740993")})
		if e != nil || v != c.want {
			t.Fatalf("%s: %s (%v)", c.src, v, e)
		}
	}
	for _, src := range []string{`${function('exact', ${header.n})} == 9007199254740993`, `9007199254740992 < ${function('exact', ${header.n})}`, `${function('exact', ${header.n})} != 9007199254740992`} {
		p, e := compilePredicateIn(flow, "simple", src)
		if e != nil {
			t.Fatal(e)
		}
		yes, e := p(message.Message{"n": json.Number("9007199254740993")})
		if e != nil || !yes {
			t.Fatalf("%s: %v %v", src, yes, e)
		}
	}
	for _, src := range []string{`${function('missing')}`, `${function(${header.name})}`, `${function('normal')}`, `${function('normal','a','b')}`} {
		if _, e := compileExpressionIn(flow, "simple", src); e == nil {
			t.Fatal("accepted", src)
		}
	}
}
func TestNamedSimpleCancellationBudgetAndDynamicScope(t *testing.T) {
	scope := namedScope(t, mapping.Limits{Operations: 25})
	flow := &flowProperties{functions: scope}
	x, e := compileExpressionIn(flow, "simple", `${function('normal', 'abc')}${function('normal', 'abc')}`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = x.eval(message.New(nil)); e == nil {
		t.Fatal("top-level calls did not share their budget")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = x.eval(message.New(nil), ctx); e == nil {
		t.Fatal("ignored cancellation")
	}
	p, e := newSimpleReplaceAction("", stepdef.Params{mapping.FunctionsBindingKey: namedScope(t, mapping.Limits{})})
	if e != nil {
		t.Fatal(e)
	}
	out, e := p.(stepdef.ActionProcessor).Process(context.Background(), message.Message{message.Body: `${function('normal', ${header.id})}`, "id": " a "})
	if e != nil || out[message.Body] != "A" {
		t.Fatalf("dynamic scope %v %v", out, e)
	}
	m := message.Message{message.Body: `${function('missing','PRIVATE')}`}
	_, e = p.(stepdef.ActionProcessor).Process(context.Background(), m)
	if e == nil || strings.Contains(e.Error(), "PRIVATE") {
		t.Fatal(e)
	}
}
