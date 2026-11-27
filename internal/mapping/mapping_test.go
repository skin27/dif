package mapping

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func jsonPlan(t *testing.T, src string, l Limits) *Plan {
	t.Helper()
	f := Format{Name: "json", MediaType: "application/json"}
	p, e := Compile(context.Background(), Definition{Name: "test", Input: "json", Output: "json", Language: "difmap/1", Expression: src}, f, f, "test.json", l)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestExpressions(t *testing.T) {
	cases := []struct{ src, input, want string }{
		{`{id: string(payload.id), name: trim(payload.name)}`, `{"id":9007199254740993,"name":"  Ada  "}`, `{"id":"9007199254740993","name":"Ada"}`},
		{`{a: payload.missing.x default "fallback", b: payload.nil default 4, c: payload.zero default 9, d: payload.flag default true, e: payload.empty default "x"}`, `{"nil":null,"zero":0,"flag":false,"empty":""}`, `{"a":"fallback","b":4,"c":0,"d":false,"e":""}`},
		{`let active = payload filter (x) -> x.keep == true; active map (x, i) -> {n: i + 1, v: number(x.qty) * 0.1}`, `[{"keep":true,"qty":"3"},{"keep":false},{"keep":true,"qty":"7"}]`, `[{"n":1,"v":0.3},{"n":2,"v":0.7}]`},
		{`if (payload == null) {x: 1} else {x: 2}`, `null`, `{"x":1}`},
		{`{a: exists(payload.a), b: exists(payload.b), c: isNull(payload.b)}`, `{"b":null}`, `{"a":false,"b":true,"c":true}`},
		{`[false and number("invalid"), true or number("invalid"), 4 default number("invalid")]`, `null`, `[false,true,4]`},
		{`/* comment */ {s: upper("élan") ++ lower("ABC"), size: size("é中"), b: boolean("false")}`, `null`, `{"s":"ÉLANabc","size":2,"b":false}`},
		{`[round(1 / 3, 3), round(-1.235, 2), sum([0.1, 0.2]), 10 / 4]`, `null`, `[0.333,-1.24,0.3,2.5]`},
		{`{x: payload[9] default 0, y: payload[0]["odd-key"]}`, `[{"odd-key":2}]`, `{"x":0,"y":2}`},
		{`[1 < 2, 2 >= 2, "a" < "b", {a:1,b:2} == {b:2,a:1}, not false, 1 != "1"]`, `null`, `[true,true,true,true,true,true]`},
		{`{v: headers["x-id"], hidden: headers["metadata.secret"] default null, body: headers.body default null}`, `null`, `{"v":"header","hidden":null,"body":null}`},
		{`{all: headers, count: size(headers), equal: headers == {"x-id": "header"}}`, `null`, `{"all":{"x-id":"header"},"count":1,"equal":true}`},
		{`let x = 2; {a: (let y = 3; x + y), b: x}`, `null`, `{"a":5,"b":2}`},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			p := jsonPlan(t, c.src, Limits{})
			out, e := p.Apply(context.Background(), c.input, "Application/JSON; charset=UTF-8", map[string]any{"x-id": "header", "metadata.secret": "PRIVATE", "body": "PRIVATE"})
			if e != nil || out != c.want {
				t.Fatalf("got %s (%v), want %s", out, e, c.want)
			}
		})
	}
}
func TestInvalidExpressionsAndValues(t *testing.T) {
	for _, src := range []string{`{x:1,x:2}`, `missingVariable`, `readFile("SECRET")`, `let x = x; x`, `let x=1; let x=2; x`, `[`, `if (true) 1`, `payload map (a,a) -> a`, `payload map (payload) -> payload`, `sum(1,2)`, `/* missing end`, `"unterminated`} {
		t.Run(src, func(t *testing.T) {
			l, _ := (Limits{}).Normalize()
			if _, e := compileExpression(src, l); e == nil {
				t.Fatal("accepted invalid source")
			}
		})
	}
	for _, src := range []string{`{v: payload.missing}`, `[payload.missing]`, `payload.null.x`, `"12" + 1`, `1 / 0`, `1 / 3`, `number("PRIVATE")`, `boolean("PRIVATE")`, `payload map (x) -> x`, `if (1) 2 else 3`, `1 and true`, `trim(2)`, `element("bad:name", {}, [])`, `{x:headers.bad}`} {
		p := jsonPlan(t, src, Limits{})
		_, e := p.Apply(context.Background(), `{"null":null}`, "", map[string]any{"bad": make(chan int)})
		if e == nil || strings.Contains(e.Error(), "PRIVATE") {
			t.Fatalf("unsafe or missing error for %s: %v", src, e)
		}
	}
	p := jsonPlan(t, `payload`, Limits{})
	for _, input := range []string{`{"a":1,"a":2}`, `{"nested":{"a":1,"a":2}}`, `1 2`, `NaN`, `1e9999999`} {
		if _, e := p.Apply(context.Background(), input, "", nil); e == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if _, e := p.Apply(context.Background(), `{}`, "text/csv", nil); e == nil {
		t.Fatal("accepted wrong media type")
	}
	if _, e := p.Apply(context.Background(), `{}`, "application/json; charset=iso-8859-1", nil); e == nil {
		t.Fatal("accepted wrong encoding")
	}
}
func TestContractsAndExactSchema(t *testing.T) {
	f := Format{Name: "json", MediaType: "application/json", Schema: `{"type":"object","properties":{"n":{"type":"integer","minimum":9007199254740993,"maximum":9007199254740993}},"required":["n"],"additionalProperties":false}`}
	p, e := Compile(context.Background(), Definition{Name: "identity", Language: "identity"}, f, f, "", Limits{})
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range []struct {
		body  string
		valid bool
	}{{`{"n":9007199254740993}`, true}, {`{"n":9007199254740992}`, false}, {`{"n":9007199254740994}`, false}, {`{"n":null}`, false}, {`{}`, false}, {`{"n":9007199254740993,"extra":1}`, false}} {
		_, e := p.Apply(context.Background(), c.body, "", nil)
		if (e == nil) != c.valid {
			t.Fatalf("schema %s: %v", c.body, e)
		}
	}
	for _, schema := range []string{`{"$ref":"SECRET"}`, `{"pattern":"["}`, `{"minItems":-1}`, `{"type":"SECRET"}`, `{"minimum":"SECRET"}`} {
		bad := f
		bad.Schema = schema
		if _, e := Compile(context.Background(), Definition{Language: "identity"}, bad, bad, "", Limits{}); e == nil || strings.Contains(e.Error(), "SECRET") {
			t.Fatalf("unsafe schema error %v", e)
		}
	}
	f.Schema = `{"properties":{"s":{"pattern":"^ok$","minLength":2,"maxLength":2},"a":{"type":"array","minItems":1,"maxItems":2,"items":{"enum":[1,2]}}},"required":["s","a"]}`
	p, e = Compile(context.Background(), Definition{Language: "identity"}, f, f, "", Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Apply(context.Background(), `{"s":"ok","a":[1,2]}`, "", nil); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Apply(context.Background(), `{"s":"PRIVATE","a":[3]}`, "", nil); e == nil || strings.Contains(e.Error(), "PRIVATE") {
		t.Fatalf("schema diagnostic: %v", e)
	}
}
func TestLimitsCancellationAndConcurrentPlans(t *testing.T) {
	for _, c := range []struct {
		src, input string
		l          Limits
	}{{`payload`, `[1,2,3]`, Limits{Nodes: 2}}, {`payload`, `[[[[1]]]]`, Limits{Depth: 3}}, {`"too long"`, `null`, Limits{Bytes: 5}}, {`payload map (x) -> x + 1`, `[1,2,3]`, Limits{Operations: 12}}, {`999 * 999`, `null`, Limits{Digits: 3}}} {
		p := jsonPlan(t, c.src, c.l)
		if _, e := p.Apply(context.Background(), c.input, "", nil); e == nil {
			t.Fatalf("limit ignored: %+v", c)
		}
	}
	p := jsonPlan(t, `{n:payload.n + 1}`, Limits{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := p.Apply(ctx, `{"n":1}`, "", nil); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancellation: %v", e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, e := p.Apply(context.Background(), `{"n":1}`, "", nil)
			if e != nil || out != `{"n":2}` {
				t.Errorf("concurrent evaluation: %s, %v", out, e)
			}
		}()
	}
	wg.Wait()
	body := map[string]any{"n": int64(9007199254740993)}
	out, e := p.Apply(context.Background(), body, "", nil)
	if e != nil || out != `{"n":9007199254740994}` || body["n"] != int64(9007199254740993) {
		t.Fatalf("native input changed: %s, %v", out, e)
	}
	cyclic := map[string]any{}
	cyclic["self"] = cyclic
	if _, e := p.Apply(context.Background(), cyclic, "", nil); e == nil {
		t.Fatal("cyclic native input accepted")
	}
}
func FuzzExpressionParser(f *testing.F) {
	for _, s := range []string{`payload`, `[`, `payload map (x) -> {a:x}`, `let a=1; a / 3`, `"\\\""`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		l, _ := (Limits{SourceBytes: 4096, Operations: 4096, Depth: 32}).Normalize()
		_, _ = compileExpression(s, l)
	})
}
func BenchmarkCompile(b *testing.B) {
	l, _ := (Limits{}).Normalize()
	for b.Loop() {
		if _, e := compileExpression(`payload map (x) -> {id:string(x.id), amount:round(number(x.qty)*0.15,2)}`, l); e != nil {
			b.Fatal(e)
		}
	}
}
func BenchmarkApply(b *testing.B) {
	f := Format{Name: "json", MediaType: "application/json"}
	p, e := Compile(context.Background(), Definition{Language: "difmap/1", Expression: `payload map (x) -> {id:string(x.id), amount:round(number(x.qty)*0.15,2)}`}, f, f, "", Limits{})
	if e != nil {
		b.Fatal(e)
	}
	for b.Loop() {
		if _, e := p.Apply(context.Background(), `[{"id":1,"qty":3},{"id":2,"qty":7}]`, "", nil); e != nil {
			b.Fatal(e)
		}
	}
}
