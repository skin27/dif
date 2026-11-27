package mapping

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func testFunction(t testing.TB, name, src, returns string, params []Parameter, l Limits, resolve Resolver) *Function {
	t.Helper()
	f, e := CompileFunction(context.Background(), FunctionDefinition{Name: name, Parameters: params, Returns: returns, Language: "difmap/1", Expression: src}, "functions.json", l, resolve)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func TestPureLibrary(t *testing.T) {
	cases := []struct{ src, want string }{
		{`[isEmpty(payload.no),isEmpty(null),isEmpty(""),isEmpty([]),isEmpty({}),isEmpty(0),isEmpty(false),isEmpty(" ")]`, `[true,true,true,true,true,false,false,false]`},
		{`[coalesce(payload.no,null,0,number("bad")),coalesce(false,true),coalesce("","x"),coalesce(null,null)]`, `[0,false,"",null]`},
		{`[replace("a-a","a","é"),substring("é中🙂x",1,3),contains("abc",""),startsWith("abc","ab"),endsWith("abc","bc")]`, `["é-é","中🙂",true,true,true]`},
		{`[split("a,,b,",","),join(["a","b"],"/"),join([],",")]`, `[["a","","b",""],"a/b",""]`},
		{`[min([9007199254740993,9007199254740992]),max([0.1,0.2]),abs(-1.23)]`, `[9007199254740992,0.2,1.23]`},
		{`[distinct([2,1,2,1.0,{a:1,b:2},{b:2,a:1}]),keys({z:1,a:2}),values({z:1,a:2})]`, `[[2,1,{"a":1,"b":2}],["z","a"],[1,2]]`},
		{`formatDate("2026-07-10T10:20:30Z","2006-01-02T15:04:05Z07:00","2006-01-02 15:04 -0700","Europe/Amsterdam")`, `"2026-07-10 12:20 +0200"`},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			p := jsonPlan(t, c.src, Limits{})
			out, e := p.Apply(context.Background(), `{}`, "", nil)
			if e != nil || out != c.want {
				t.Fatalf("got %s (%v), want %s", out, e, c.want)
			}
		})
	}
	for _, src := range []string{`replace("x","","a")`, `substring("é",0,2)`, `substring("x",0.1,1)`, `split("x","")`, `join([1],",")`, `min([])`, `max(["1"])`, `abs(null)`, `keys([])`, `formatDate("2026-02-30Z","2006-01-02Z07","2006","UTC")`, `formatDate("2026-01-01","2006-01-02","2006","UTC")`, `formatDate("2026-01-01Z","2006-01-02Z07","2006","Local")`} {
		p := jsonPlan(t, src, Limits{})
		if _, e := p.Apply(context.Background(), `{}`, "", nil); e == nil {
			t.Fatalf("accepted %s", src)
		}
	}
}
func TestTypedFunctionsAndSharedBudgets(t *testing.T) {
	params := []Parameter{{Name: "x", Type: "number"}, {Name: "scale", Type: "number", Default: json.RawMessage(`2`)}}
	f := testFunction(t, "money", `round(x * 1.21,scale)`, "number", params, Limits{}, nil)
	scope := NewFunctions(map[string]*Function{"money": f}, Limits{})
	got, e := scope.Session(context.Background()).Invoke(f, []any{json.Number("9007199254740993")})
	if e != nil || got != json.Number("10898711098236601.53") {
		t.Fatalf("precision: %v %v", got, e)
	}
	for _, args := range [][]any{{"1"}, {nil}, {1, nil}, {}, {1, 2, 3}} {
		if _, e := scope.Session(context.Background()).Invoke(f, args); e == nil {
			t.Fatalf("accepted arguments %#v", args)
		}
	}
	outer := testFunction(t, "outer", `function("money",x)`, "number", []Parameter{{Name: "x", Type: "number"}}, Limits{}, scope.Resolve)
	limited := NewFunctions(map[string]*Function{"outer": outer}, Limits{Operations: 8})
	if _, e := limited.Session(context.Background()).Invoke(outer, []any{1}); e == nil {
		t.Fatal("nested call reset budget")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := scope.Session(ctx).Invoke(f, []any{1}); e == nil {
		t.Fatal("ignored cancellation")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := scope.Session(context.Background()).Invoke(f, []any{1})
			if e != nil || v != json.Number("1.21") {
				t.Errorf("concurrent result %v %v", v, e)
			}
		}()
	}
	wg.Wait()
}
func TestFunctionContractsAndBoundaries(t *testing.T) {
	for _, src := range []string{`payload`, `headers`, `random()`, `function(x,1)`, `function("missing",1)`} {
		_, e := CompileFunction(context.Background(), FunctionDefinition{Name: "f", Parameters: []Parameter{}, Returns: "any", Language: "difmap/1", Expression: src}, "", Limits{}, nil)
		if e == nil {
			t.Fatal("accepted", src)
		}
	}
	for _, params := range [][]Parameter{nil, {{Name: "payload", Type: "any"}}, {{Name: "x", Type: "any"}, {Name: "x", Type: "any"}}, {{Name: "x", Type: "integer"}}, {{Name: "x", Type: "number", Default: json.RawMessage(`null`)}}, {{Name: "x", Type: "number", Default: json.RawMessage(`1`)}, {Name: "y", Type: "number"}}} {
		if _, e := ValidateParameters(params, Limits{}); e == nil {
			t.Fatalf("accepted %#v", params)
		}
	}
	f := testFunction(t, "echo", `v`, "any", []Parameter{{Name: "v", Type: "any"}}, Limits{}, nil)
	scope := NewFunctions(nil, Limits{Depth: 8})
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	for _, v := range []any{cyclic, make(chan int)} {
		if _, e := scope.Session(context.Background()).Invoke(f, []any{v}); e == nil {
			t.Fatal("accepted unsupported/cyclic value")
		}
	}
	f = testFunction(t, "wrong", `if (v) 1 else "PRIVATE"`, "number", []Parameter{{Name: "v", Type: "boolean"}}, Limits{}, nil)
	if _, e := scope.Session(context.Background()).Invoke(f, []any{false}); e == nil || strings.Contains(e.Error(), "PRIVATE") {
		t.Fatal(e)
	}
}
func BenchmarkNamedFunction(b *testing.B) {
	f := testFunction(b, "normal", `upper(trim(x))`, "string", []Parameter{{Name: "x", Type: "string"}}, Limits{}, nil)
	s := NewFunctions(nil, Limits{})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, e := s.Session(context.Background()).Invoke(f, []any{"  customer-1  "}); e != nil {
			b.Fatal(e)
		}
	}
}
func FuzzNamedFunctionParser(f *testing.F) {
	f.Add(`function("f", "x")`)
	f.Add(`coalesce(null, "x")`)
	f.Fuzz(func(t *testing.T, src string) {
		l, _ := (Limits{SourceBytes: 4096, Depth: 32, Operations: 10000}).Normalize()
		_, _ = compileScoped(src, l, map[string]bool{"x": true}, func(string) (*Function, error) { return nil, context.Canceled })
	})
}
func TestFunctionSessionOutputLimit(t *testing.T) {
	f := testFunction(t, "constant", `"12345"`, "string", []Parameter{}, Limits{}, nil)
	s := NewFunctions(nil, Limits{Bytes: 10}).Session(context.Background())
	if _, e := s.Invoke(f, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Invoke(f, nil); e == nil {
		t.Fatal("repeated calls bypassed cumulative output bound")
	}
}
func BenchmarkFunctionDistinct(b *testing.B) {
	f := testFunction(b, "unique", `distinct(items)`, "array", []Parameter{{Name: "items", Type: "array"}}, Limits{}, nil)
	s := NewFunctions(nil, Limits{})
	input := make([]any, 100)
	for i := range input {
		input[i] = i % 10
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, e := s.Session(context.Background()).Invoke(f, []any{input}); e != nil {
			b.Fatal(e)
		}
	}
}
