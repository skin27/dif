package impl

import (
	"reflect"
	"strings"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	stepdef "dif/steps/definition"
)

// storeDoc is a flow whose action b has the URI and options, with the stores
// as dil.core.stores.
func storeDoc(uri, options, stores string) string {
	steps := `{"id":"a","type":"source","links":{"link":{"id":"b","bound":"out"}}},` +
		`{"id":"b","type":"action","uri":"` + uri + `","options":` + options + `,"links":{"link":[{"id":"b","bound":"in"},{"id":"c","bound":"out"}]}},` +
		`{"id":"c","type":"sink","uri":"log","links":{"link":{"id":"c","bound":"in"}}}`
	return `{"dil":{"integrations":{"integration":{"flows":{"flow":{"id":"f","steps":{"step":[` + steps + `]}}}}},"core":{"stores":` + stores + `}}}`
}

func TestStoresAreBound(t *testing.T) {
	// One object or a list; numbers and booleans may be text, as from XML.
	for _, stores := range []string{
		`{"store":{"name":"rates","durable":true,"ttl":3600000,"maxKeys":10}}`,
		`{"store":[{"name":"rates","durable":"true","ttl":"3600000","maxKeys":"10"}]}`,
	} {
		nodes, err := parseNodes(t, storeDoc("store:rates", `{"operation":"get","key":"k"}`, stores))
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"a", "b", "c"} {
			got, _ := nodes[id].Bindings[stepdef.StoresBinding].(map[string]stepdef.Store)
			want := map[string]stepdef.Store{"rates": {Name: "rates", Durable: true, TTL: time.Hour, MaxKeys: 10}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: stores = %v, want %v", id, got, want)
			}
		}
	}
	d, err := DecodeDocument([]byte(storeDoc("log", `{}`, `{"store":[{"name":"a"},{"name":"tenant.default","durable":true}]}`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := d.StoreInfo(); !reflect.DeepEqual(got, []StoreInfo{{Name: "a"}, {Name: "tenant.default", Durable: true}}) {
		t.Errorf("info = %+v", got)
	}
}

func TestStoreDeclarationErrors(t *testing.T) {
	for stores, want := range map[string]string{
		`{"store":{"durable":true}}`:                            "empty store name",
		`{"store":[{"name":"a"},{"name":"a"}]}`:                 `duplicate store "a"`,
		`{"store":{"name":"a:b"}}`:                              `store "a:b": the name must be`,
		`{"store":{"name":"a","durable":"yes"}}`:                `store "a": durable must be true or false`,
		`{"store":{"name":"a","ttl":-1}}`:                       `store "a": ttl`,
		`{"store":{"name":"a","maxKeys":1.5}}`:                  `store "a": maxKeys`,
		`{"store":{"name":"a","capacity":1}}`:                   `store "a": unknown field capacity`,
		`{"store":{"name":"a","ttl":"9223372036854775807000"}}`: `store "a": ttl`,
	} {
		if _, err := DecodeDocument([]byte(storeDoc("log", `{}`, stores))); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want containing %q", stores, err, want)
		}
	}
	_, err := parseNodes(t, storeDoc("store:other", `{"operation":"get","key":"k"}`, `{"store":{"name":"rates"}}`))
	if err == nil || !strings.Contains(err.Error(), `store "other" not found in dil.core.stores`) {
		t.Errorf("undeclared store: %v", err)
	}
}

func TestImportedStores(t *testing.T) {
	lib, err := DecodeDocument([]byte(`{"dil":{"core":{"stores":{"store":[{"name":"rates","ttl":1000},{"name":"local"}]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	main, err := DecodeDocument([]byte(`{"dil":{"imports":{"import":{"path":"lib.json","namespace":"lib"}},` +
		`"core":{"stores":{"store":{"name":"local","durable":true}}},` +
		`"integrations":{"integration":{"flows":{"flow":{"id":"f","steps":{"step":[` +
		`{"id":"a","type":"source","uri":"timer:t","links":{"link":{"id":"b","bound":"out"}}},` +
		`{"id":"b","type":"action","uri":"store:lib:rates","options":{"operation":"get","key":"k"},"links":{"link":[{"id":"b","bound":"in"},{"id":"c","bound":"out"}]}},` +
		`{"id":"c","type":"sink","uri":"log","links":{"link":{"id":"c","bound":"in"}}}]}}}}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	main.BindScopeWithKey("main.json", "main.json", map[string]*Document{"lib": lib})
	plans, err := main.Plans(map[string]*Document{"lib": lib})
	if err != nil {
		t.Fatal(err)
	}
	if got := plans[0].References; !reflect.DeepEqual(got, []ReferenceInfo{{Step: "b", Kind: "store", Name: "lib:rates"}}) {
		t.Errorf("references = %+v", got)
	}
	var stores map[string]stepdef.Store
	if _, err := plans[0].Build(func(n *flowdef.Node) (stepdef.Processor, error) {
		stores, _ = n.Bindings[stepdef.StoresBinding].(map[string]stepdef.Store)
		return noop{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]stepdef.Store{
		"local":     {Name: "local", Durable: true},
		"lib:rates": {Name: "rates", TTL: time.Second},
		"lib:local": {Name: "local"},
	}
	if !reflect.DeepEqual(stores, want) {
		t.Errorf("stores = %v, want %v", stores, want)
	}
}
