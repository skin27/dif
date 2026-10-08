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

func TestJSONPathInvalid(t *testing.T) {
	for _, expr := range []string{"", "store.book", "$.", "$[", "$.a[]", "$..", "$.a[?(@.b ==)]", "$.a[?(@.b > )]", "$.a[?@.b]", "$.a[1:2:3:4]", "$.a.nosuch()", "$.a[?(@.b == 'x)]", "$.a b"} {
		if _, err := compileJSONPath(expr); err == nil || !strings.Contains(err.Error(), "jsonpath") {
			t.Errorf("%q: err = %v, want an error", expr, err)
		}
	}
}

const fullStore = `{"store": {
	"book": [
		{"category": "reference", "author": "Nigel Rees", "title": "Sayings", "price": 8.95},
		{"category": "fiction", "author": "Evelyn Waugh", "title": "Sword", "price": 12.99, "tags": ["a", "b"]},
		{"category": "fiction", "author": "Herman Melville", "title": "Moby Dick", "isbn": "0-553-21311-3", "price": 8.99, "tags": ["b"]},
		{"category": "fiction", "author": "J. R. R. Tolkien", "title": "Rings", "isbn": "0-395-19395-8", "price": 22.99}
	],
	"bicycle": {"color": "red", "price": 19.95}
}, "expensive": 10}`

// The expressions of Jayway's documentation and of the regression flows.
func TestJSONPathJayway(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(fullStore), &doc); err != nil {
		t.Fatal(err)
	}
	for expr, want := range map[string]string{
		"$.store.book[0].author":                  `["Nigel Rees"]`,
		"@.store.book[0].author":                  `["Nigel Rees"]`,
		"$.store.book[*].author":                  `["Nigel Rees","Evelyn Waugh","Herman Melville","J. R. R. Tolkien"]`,
		"$.store.book.[0].author":                 `["Nigel Rees"]`,
		"$..author":                               `["Nigel Rees","Evelyn Waugh","Herman Melville","J. R. R. Tolkien"]`,
		"$..price":                                `[19.95,8.95,12.99,8.99,22.99]`, // members in key order: bicycle before book
		"$..book[2]":                              `[` + compact(`{"category":"fiction","author":"Herman Melville","title":"Moby Dick","isbn":"0-553-21311-3","price":8.99,"tags":["b"]}`) + `]`,
		"$..book[-1:].title":                      `["Rings"]`,
		"$..book[0,1].title":                      `["Sayings","Sword"]`,
		"$..book[:2].title":                       `["Sayings","Sword"]`,
		"$..book[1:3].title":                      `["Sword","Moby Dick"]`,
		"$.store['bicycle','book'].length()":      `[2]`,
		"$..book[?(@.isbn)].title":                `["Moby Dick","Rings"]`,
		"$..book[?(!@.isbn)].title":               `["Sayings","Sword"]`,
		"$..book[?(@.price<8.99)].price":          `[8.95]`,
		"$.store.book[?(@.price < 10)].title":     `["Sayings","Moby Dick"]`,
		"$.store.book[?(@.price <= 8.99)].title":  `["Sayings","Moby Dick"]`,
		"$..book[?(@.price > $.expensive)].title": `["Sword","Rings"]`,
		"$..book[?(@.category == 'fiction' && @.price < 10)].title":     `["Moby Dick"]`,
		"$..book[?(@.category == \"reference\" || @.price > 20)].title": `["Sayings","Rings"]`,
		"$..book[?(@.category != 'fiction')].title":                     `["Sayings"]`,
		"$..book[?(@.author =~ /.*REES/i)].title":                       `["Sayings"]`,
		"$..book[?(@.author =~ /rees/)].title":                          `null`,
		"$..book[?(@.category in ['reference','poetry'])].title":        `["Sayings"]`,
		"$..book[?(@.category nin ['fiction'])].title":                  `["Sayings"]`,
		"$..book[?(@.tags subsetof ['a','b','c'])].title":               `["Sword","Moby Dick"]`,
		"$..book[?(@.tags anyof ['a'])].title":                          `["Sword"]`,
		"$..book[?(@.tags noneof ['a'])].title":                         `["Moby Dick"]`,
		"$..book[?(@.tags size 2)].title":                               `["Sword"]`,
		"$..book[?(@.tags empty false)].title":                          `["Sword","Moby Dick"]`,
		"$..book[?(@.title contains 'ing')].title":                      `["Sayings","Rings"]`,
		"$..book[?(@.tags contains 'a')].title":                         `["Sword"]`,
		"$..book[?(@.price == 8.95)].title":                             `["Sayings"]`,
		"$..book[?((@.price < 9) && (@.category == 'fiction'))].title":  `["Moby Dick"]`,
		"$.store.book.length()":                                         `[4]`,
		"$.store.book[*].price.max()":                                   `[22.99]`,
		"$.store.book[*].price.min()":                                   `[8.95]`,
		"$.store.book[?(@.price < 10)].length()":                        `[2]`,
		"$..book[?(@.category == 'fiction')].length()":                  `[3]`,
		"$.store.bicycle.color.length()":                                `[3]`,
		"$.store.bicycle.keys()":                                        `[["color","price"]]`,
		"$..bicycle.color":                                              `["red"]`,
		"$..color":                                                      `["red"]`,
		"$.nothing":                                                     `null`,
		"$..nothing":                                                    `null`,
		"$.store.book[9]":                                               `null`,
		"$..book[?(@.price > 100)].title":                               `null`,
	} {
		p, err := compileJSONPath(expr)
		if err != nil {
			t.Errorf("%s: %v", expr, err)
			continue
		}
		got := "null"
		if r := p.eval(doc); len(r) > 0 {
			b, _ := json.Marshal(r)
			got = string(b)
		}
		if got != want {
			t.Errorf("%s = %s, want %s", expr, got, want)
		}
	}

	for expr, want := range map[string]bool{
		"$.store.book[0].author":         true,
		"$.store.book[*].author":         false,
		"$..book[?(@.price<8.99)].price": false,
		"$..bicycle.color":               false,
		"$.a":                            true,
		"$.store.book[0,1]":              false,
		"$.store.book[0:2]":              false,
		"$.store.book.length()":          true,
	} {
		p, _ := compileJSONPath(expr)
		if p.definite() != want {
			t.Errorf("%s definite = %v, want %v", expr, p.definite(), want)
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
