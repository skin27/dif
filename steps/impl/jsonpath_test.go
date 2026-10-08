package impl

import (
	"encoding/json"
	"strings"
	"testing"
)

const store = `{"store": {"book": [
	{"author": "Nigel Rees", "title": "Sayings", "price": 8.95},
	{"author": "Evelyn Waugh", "title": "Sword", "price": 12.99, "isbn": null}
], "bicycle": {"color": "red", "sold": false}}}`

func TestJSONPath(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(store), &doc); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		expr, want string
	}{
		{"$.store.book[*].author", `["Nigel Rees","Evelyn Waugh"]`},
		{"$.store.book[0].title", `["Sayings"]`},
		{"$.store.book[-1].title", `["Sword"]`},
		{"$['store']['bicycle'].color", `["red"]`},
		{`$.store.bicycle["color"]`, `["red"]`},
		{"$.store.bicycle.*", `["red",false]`},
		{"$.store.book[5]", `null`},
		{"$.store.none.x", `null`},
		{"$.store.book.author", `null`},
		{"$", `[` + strings.Join(strings.Fields(compact(store)), " ") + `]`},
	}
	for _, tt := range tests {
		p, err := compileJSONPath(tt.expr)
		if err != nil {
			t.Fatalf("%s: %v", tt.expr, err)
		}
		got, _ := json.Marshal(p.eval(doc))
		if string(got) != tt.want {
			t.Errorf("%s = %s, want %s", tt.expr, got, tt.want)
		}
	}
}

func compact(s string) string {
	var v any
	json.Unmarshal([]byte(s), &v)
	b, _ := json.Marshal(v)
	return string(b)
}

func TestJSONPathUnsupported(t *testing.T) {
	for _, expr := range []string{"", "store.book", "$..author", "$.store.book[?(@.price < 10)]", "$.store.book[0:2]", "$.", "$[", "$.a[]"} {
		if _, err := compileJSONPath(expr); err == nil || !strings.Contains(err.Error(), "unsupported jsonpath") {
			t.Errorf("%q: err = %v, want unsupported", expr, err)
		}
	}
}

func TestJSONPathMatch(t *testing.T) {
	for expr, want := range map[string]bool{
		"$.store.book[*].author": true,
		"$.store.bicycle.color":  true,
		"$.store.bicycle.sold":   false,
		"$.store.book[1].isbn":   false,
		"$.store.none":           false,
	} {
		p, _ := compileJSONPath(expr)
		if got := p.matchJSON(store); got != want {
			t.Errorf("%s = %v, want %v", expr, got, want)
		}
	}
	p, _ := compileJSONPath("$.a")
	if !p.matchJSON(map[string]any{"a": 1.0}) || !p.matchJSON([]byte(`{"a": 1}`)) {
		t.Error("decoded JSON or bytes did not match")
	}
	if p.matchJSON("123") || p.matchJSON("not json") {
		t.Error("a body without $.a matched")
	}
}
