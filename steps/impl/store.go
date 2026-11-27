package impl

import (
	"context"
	"fmt"
	"strings"

	"dif/internal/channels"
	"dif/message"
	stepdef "dif/steps/definition"
)

// Stores are named key-value stores of the channel runtime that a program
// declares in dil.core.stores. The store step gets, puts and removes an entry;
// ${store:<name>:<key>} reads one in the simple language.

// storeConfig is the runtime's configuration of a declared store.
func storeConfig(d stepdef.Store) channels.StoreConfig {
	return channels.StoreConfig{Durable: d.Durable, TTLMS: d.TTL.Milliseconds(), MaxKeys: d.MaxKeys}
}

// declaredStores returns the stores a loader says the step may refer to, by
// the names it uses; false when the step was made without a loader.
func declaredStores(p stepdef.Params) (map[string]stepdef.Store, bool) {
	stores, ok := p[stepdef.StoresBinding].(map[string]stepdef.Store)
	return stores, ok
}

// openStore returns the store name in the step's channel runtime: the one
// the program declares, or, without a loader, one the runtime has.
func openStore(p stepdef.Params, name string) (*channels.Store, error) {
	rt := channelRuntime(p)
	if stores, ok := declaredStores(p); ok {
		d, ok := stores[name]
		if !ok {
			return nil, fmt.Errorf("store %q not found in dil.core.stores", name)
		}
		return rt.DeclareStore(d.Name, storeConfig(d))
	}
	if s := rt.Store(name); s != nil {
		return s, nil
	}
	return nil, fmt.Errorf("store %q is not declared", name)
}

// storeAction is the store step: operation get, put or remove on the entry
// whose key is an expression.
type storeAction struct {
	store     *channels.Store
	operation string
	key       expression
	value     expression  // put
	header    string      // get: the header that gets the value; "" for the body
	fallback  *expression // get: the value of a missing key; nil: nothing changes
}

func newStoreAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	name, _ := p["path"].(string)
	if name == "" {
		return nil, fmt.Errorf("no store name: want store:<name>")
	}
	s, err := openStore(p, name)
	if err != nil {
		return nil, err
	}
	a := &storeAction{store: s, operation: p["operation"].(string), header: optString(p, "headerName")}
	flow := flowOf(p)
	if a.key, err = compileExpressionIn(flow, "simple", p["key"].(string)); err != nil {
		return nil, fmt.Errorf("option key: %w", err)
	}
	if a.operation == "put" {
		if a.value, err = compileExpressionIn(flow, "simple", p["value"].(string)); err != nil {
			return nil, fmt.Errorf("option value: %w", err)
		}
	}
	if a.header != "" {
		if err := checkHeaderName(a.header); err != nil {
			return nil, err
		}
	}
	if def, ok := p["default"].(string); ok {
		x, err := compileExpressionIn(flow, "simple", def)
		if err != nil {
			return nil, fmt.Errorf("option default: %w", err)
		}
		a.fallback = &x
	}
	return a, nil
}

func (a *storeAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	key, err := a.key.eval(m, ctx)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("store %s: the key is empty", a.store.Name())
	}
	switch a.operation {
	case "put":
		v, err := a.value.value(m, ctx)
		if err == nil {
			err = a.store.Put(key, plainValue(v))
		}
		if err != nil {
			return nil, err
		}
		return m, nil
	case "remove":
		if err := a.store.Remove(key); err != nil {
			return nil, err
		}
		return m, nil
	}
	v, found, err := a.store.Get(key)
	if err != nil {
		return nil, err
	}
	if !found {
		if a.fallback == nil {
			return m, nil
		}
		if v, err = a.fallback.value(m, ctx); err != nil {
			return nil, err
		}
	}
	if a.header != "" {
		m[a.header] = v
	} else {
		m[message.Body] = v
	}
	return m, nil
}

// plainValue makes the lists and maps of the simple language plain ones,
// which a store keeps.
func plainValue(v any) any {
	switch x := v.(type) {
	case jlist:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = plainValue(item)
		}
		return out
	case jmap:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[k] = plainValue(item)
		}
		return out
	}
	return v
}

// storeReference compiles ${store:<name>:<key>}, which reads the entry key of
// the store: nil when it is not set. The key is the rest of the block, a
// template that may hold blocks, as in ${store:rates:${header.currency}}; the
// name is written as it is. A loader's flow knows its stores, so a name it
// does not declare fails here; otherwise the store is looked up when the
// expression runs.
func (c *compiler) storeReference(b *block, rest string) (evalFn, error) {
	name, keyText, ok := strings.Cut(rest, ":")
	var stores map[string]stepdef.Store
	rt := defaultChannels
	if c.flow != nil {
		stores = c.flow.stores
		if c.flow.channels != nil {
			rt = c.flow.channels
		}
	}
	// <namespace>:<name>:<key> when the flow sees a store by that name.
	if second, after, more := strings.Cut(keyText, ":"); ok && more && stores != nil {
		if _, declared := stores[name+":"+second]; declared {
			name, keyText = name+":"+second, after
		}
	}
	if !ok || name == "" || keyText == "" || strings.ContainsAny(name, string([]rune{phOpen, phClose})) {
		return nil, unsupported(b, "write ${store:<name>:<key>}, with the store's name as it is")
	}
	key, err := c.template(b, keyText)
	if err != nil {
		return nil, err
	}
	lookup := func() (*channels.Store, error) {
		if s := rt.Store(name); s != nil {
			return s, nil
		}
		return nil, fmt.Errorf("%s: store %q is not declared", b.src, name)
	}
	if stores != nil {
		d, declared := stores[name]
		if !declared {
			return nil, fmt.Errorf("%s: store %q not found in dil.core.stores", b.src, name)
		}
		s, err := rt.DeclareStore(d.Name, storeConfig(d))
		if err != nil {
			return nil, err
		}
		lookup = func() (*channels.Store, error) { return s, nil }
	}
	return func(e *env) (any, error) {
		s, err := lookup()
		if err != nil {
			return nil, err
		}
		k, err := key(e)
		if err != nil {
			return nil, err
		}
		v, _, err := s.Get(render(k))
		return v, err
	}, nil
}

// storeEffect is what the store step does, for the static checks: put and
// remove keep the message; get sets its header (only may, without a default)
// or makes a body nothing is known of.
func storeEffect(p stepdef.Params) *stepdef.Effect {
	e := keepAll
	e.Reads = readsIn("simple", optString(p, "key"), false)
	def, hasDefault := p["default"].(string)
	switch op := optString(p, "operation"); {
	case op == "put":
		e.Reads = append(e.Reads, readsIn("simple", optString(p, "value"), false)...)
	case op == "get":
		if hasDefault {
			e.Reads = append(e.Reads, readsIn("simple", def, false)...)
		}
		if header := optString(p, "headerName"); header == "" {
			e.KeepBody = false
		} else if hasDefault {
			e.Sets = []string{header}
		} else {
			e.MaySet = []string{header}
		}
	case op != "remove":
		return nil
	}
	return &e
}
