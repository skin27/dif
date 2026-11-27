package api

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dif/message"
)

// writeFiles writes the files into a new directory and returns it.
func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func prepare(t *testing.T, paths ...string) *PreparedProgram {
	t.Helper()
	var sources []ProgramSource
	for _, p := range paths {
		sources = append(sources, ProgramSource{Name: p, Path: p})
	}
	p, err := PrepareProgram(context.Background(), sources, ProgramLimits{})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// loadProgram loads the program into the runtime and starts its flows.
func loadProgram(t *testing.T, p *PreparedProgram, r *Runtime) map[string]*Flow {
	t.Helper()
	loaded, err := p.Load(context.Background(), r, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	flows := map[string]*Flow{}
	for _, f := range loaded.Flows {
		if err := f.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Stop() })
		flows[f.ID()] = f
	}
	return flows
}

func request(t *testing.T, f *Flow, m Message) Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := f.Request(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const ratesLib = `store rates { ttl: 3600000, maxKeys: 10 }
`

const ratesMain = `import "lib.dil" as lib

store quotes {}

flow put-rate {
  message
    -> store:lib:rates(operation: "put", key: "${header.currency}")
    -> store:quotes(operation: "put", key: "last", value: "${header.currency}")
}

flow price {
  message
    -> setbody(language: "simple", expression: "${body} at ${store:lib:rates:${header.currency}} (last ${store:quotes:last})")
}
`

func TestProgramStores(t *testing.T) {
	dir := writeFiles(t, map[string]string{"lib.dil": ratesLib, "main.dil": ratesMain})
	p := prepare(t, filepath.Join(dir, "main.dil"))

	info := p.Info()
	stores := map[string][]StoreInfo{}
	for _, d := range info.Documents {
		stores[filepath.Base(d.Name)] = d.Stores
	}
	if want := map[string][]StoreInfo{"lib.dil": {{Name: "rates", TTL: 3600000, MaxKeys: 10}}, "main.dil": {{Name: "quotes"}}}; !reflect.DeepEqual(stores, want) {
		t.Errorf("stores = %+v, want %+v", stores, want)
	}
	var refs []ReferenceInfo
	for _, f := range info.Flows {
		refs = append(refs, f.References...)
	}
	if len(refs) != 2 || refs[0] != (ReferenceInfo{Step: refs[0].Step, Kind: "store", Name: "lib:rates"}) {
		t.Errorf("references = %+v", refs)
	}

	r := runtimeNew(t, ChannelConfig{})
	flows := loadProgram(t, p, r)
	m := message.New(1.08)
	m["currency"] = "EUR"
	request(t, flows["put-rate"], m)
	m = message.New("100")
	m["currency"] = "EUR"
	if out := request(t, flows["price"], m); out[Body] != "100 at 1.08 (last EUR)" {
		t.Errorf("price = %v", out[Body])
	}
	want := []StoreStatus{{Name: "quotes", Keys: 1}, {Name: "rates", Keys: 1}}
	if got := r.ChannelStatus().Stores; !reflect.DeepEqual(got, want) {
		t.Errorf("status = %+v, want %+v", got, want)
	}
}

func TestProgramStoreErrors(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"flow f { message -> store:rates(operation: \"get\", key: \"k\") }", `store "rates" not found in dil.core.stores`},
		{"flow f { message -> setbody(language: \"simple\", expression: \"${store:rates:k}\") }", `store "rates" not found in dil.core.stores`},
		{"store rates {}\nflow f { message -> aggregate(store: \"other\") -> passthrough }", `store "other" not found in dil.core.stores`},
	} {
		dir := writeFiles(t, map[string]string{"f.dil": tc.text})
		p, err := PrepareProgram(context.Background(), []ProgramSource{{Name: "f.dil", Path: filepath.Join(dir, "f.dil")}}, ProgramLimits{})
		if err == nil {
			_, err = p.Load(context.Background(), runtimeNew(t, ChannelConfig{}), nil, nil)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", tc.text, err, tc.want)
		}
	}

	// A durable store needs a runtime with a directory; two programs that
	// declare a store differently cannot share a runtime.
	dir := writeFiles(t, map[string]string{
		"durable.dil": "store rates { durable: true }\nflow d { message -> passthrough }",
		"other.dil":   "store rates { ttl: 1 }\nflow o { message -> passthrough }",
	})
	_, err := prepare(t, filepath.Join(dir, "durable.dil")).Load(context.Background(), runtimeNew(t, ChannelConfig{}), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "configure channels.directory") {
		t.Errorf("durable store without a directory: %v", err)
	}
	r := runtimeNew(t, ChannelConfig{Directory: t.TempDir()})
	loadProgram(t, prepare(t, filepath.Join(dir, "durable.dil")), r)
	_, err = prepare(t, filepath.Join(dir, "other.dil")).Load(context.Background(), r, nil, nil)
	if err == nil || !strings.Contains(err.Error(), `store "rates" is already declared`) {
		t.Errorf("redeclared: %v", err)
	}
}

// TestTenantVariablesSurviveRestart declares the store of the default tenant
// durable: its variables are there in a new runtime on the same directory.
// Without a declaration they are in memory, as before.
func TestTenantVariablesSurviveRestart(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"set.dil": "store \"tenant.default\" { durable: true }\nflow set { message -> settenantvariable:token(value: \"${body}\") }",
		"get.dil": "store \"tenant.default\" { durable: true }\nflow get { message -> gettenantvariable:token(headerName: \"token\") }",
	})
	config := ChannelConfig{Directory: t.TempDir()}
	r := runtimeNew(t, config)
	flows := loadProgram(t, prepare(t, filepath.Join(dir, "set.dil")), r)
	request(t, flows["set"], message.New("dummy-token"))
	flows["set"].Stop()
	r.Close()

	r = runtimeNew(t, config)
	flows = loadProgram(t, prepare(t, filepath.Join(dir, "get.dil")), r)
	if out := request(t, flows["get"], message.New(nil)); out["token"] != "dummy-token" {
		t.Errorf("token after a restart = %v", out["token"])
	}
	if got := r.ChannelStatus().Stores; len(got) != 1 || got[0] != (StoreStatus{Name: "tenant.default", Durable: true, Keys: 1}) {
		t.Errorf("status = %+v", got)
	}

	// Without a declaration, the tenant's store is in memory.
	mem := runtimeNew(t, ChannelConfig{})
	undeclared := writeFiles(t, map[string]string{"set.dil": "flow set { message -> settenantvariable:token(value: \"x\") }"})
	flows = loadProgram(t, prepare(t, filepath.Join(undeclared, "set.dil")), mem)
	request(t, flows["set"], message.New(nil))
	if got := mem.ChannelStatus().Stores; len(got) != 1 || got[0] != (StoreStatus{Name: "tenant.default", Keys: 1}) {
		t.Errorf("undeclared tenant store = %+v", got)
	}
}

// TestSingleFlowStores loads one document with LoadBytes, which declares its
// stores as its steps use them.
func TestSingleFlowStores(t *testing.T) {
	r := runtimeNew(t, ChannelConfig{})
	f, err := r.LoadBytes([]byte("store counts {}\nflow one { message -> store:counts(operation: \"put\", key: \"k\") -> store:counts(operation: \"get\", key: \"k\", headerName: \"v\") }"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()
	if out := request(t, f, message.New(int64(42))); out["v"] != int64(42) {
		t.Errorf("v = %#v", out["v"])
	}
}
