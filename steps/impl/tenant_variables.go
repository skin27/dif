package impl

import (
	"context"
	"fmt"
	"strings"

	"dif/internal/channels"
	"dif/message"
	stepdef "dif/steps/definition"
)

// Tenant variables are named values that the flows of a tenant share, such as
// a token one flow fetches and others use. The Java platform keeps them in the
// tenant's database; DIF keeps the variables of a tenant in the store
// tenant.<tenant> of the channel runtime the flow is loaded in. A program may
// declare that store in dil.core.stores, durable to keep the variables over a
// restart; without a declaration it is in memory, and the variables are lost
// when dif exits.

// tenantStore holds the variables of one tenant by name: the seam between the
// steps and where the variables are kept, the tenant's store.
type tenantStore struct{ store *channels.Store }

// tenantVariables returns the variables of tenant for the step: the store
// tenant.<tenant> of its runtime, as a program declares it, else in memory.
func tenantVariables(p stepdef.Params, tenant string) (tenantStore, error) {
	rt, name := channelRuntime(p), "tenant."+tenant
	if s := rt.Store(name); s != nil {
		return tenantStore{s}, nil
	}
	d := stepdef.Store{Name: name}
	if stores, _ := declaredStores(p); stores != nil {
		for _, declared := range stores {
			if declared.Name == name {
				d = declared
			}
		}
	}
	s, err := rt.DeclareStore(d.Name, storeConfig(d))
	return tenantStore{s}, err
}

func (t tenantStore) get(name string) (string, bool, error) {
	v, ok, err := t.store.Get(name)
	return text(v), ok, err
}

func (t tenantStore) set(name, value string) error { return t.store.Put(name, value) }
func (t tenantStore) remove(name string) error     { return t.store.Remove(name) }

// tenantVariable is the variable a step works on: its tenant's variables (the
// option tenantDbName) and its name (the URI path).
type tenantVariable struct {
	vars tenantStore
	name string
}

func newTenantVariable(p stepdef.Params) (tenantVariable, error) {
	name, _ := p["path"].(string)
	if name == "" {
		return tenantVariable{}, fmt.Errorf("no variable name: want <step>:<name>")
	}
	vars, err := tenantVariables(p, p["tenantDbName"].(string))
	return tenantVariable{vars, name}, err
}

// setTenantVariableAction sets the variable to the value of an expression.
type setTenantVariableAction struct {
	v     tenantVariable
	value expression
}

func newSetTenantVariableAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	v, err := newTenantVariable(p)
	if err != nil {
		return nil, err
	}
	value, err := compileValue(flowOf(p), p["language"].(string), p["value"].(string), false)
	if err != nil {
		return nil, fmt.Errorf("option value: %w", err)
	}
	return setTenantVariableAction{v, value}, nil
}

func (a setTenantVariableAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	s, err := a.value.eval(m, ctx)
	if err != nil {
		return nil, err
	}
	if err := a.v.vars.set(a.v.name, s); err != nil {
		return nil, err
	}
	return m, nil
}

// getTenantVariableAction sets a header to the variable's value, or to ""
// when the variable is not set.
type getTenantVariableAction struct {
	v      tenantVariable
	header string
}

func newGetTenantVariableAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	v, err := newTenantVariable(p)
	if err != nil {
		return nil, err
	}
	header := p["headerName"].(string)
	if err := checkHeaderName(header); err != nil {
		return nil, err
	}
	return getTenantVariableAction{v, header}, nil
}

func (a getTenantVariableAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	v, _, err := a.v.vars.get(a.v.name)
	if err != nil {
		return nil, err
	}
	m[a.header] = v
	return m, nil
}

// removeTenantVariableAction removes the variable.
type removeTenantVariableAction struct{ v tenantVariable }

func newRemoveTenantVariableAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	v, err := newTenantVariable(p)
	if err != nil {
		return nil, err
	}
	return removeTenantVariableAction{v}, nil
}

func (a removeTenantVariableAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	if err := a.v.vars.remove(a.v.name); err != nil {
		return nil, err
	}
	return m, nil
}

// expandTenantVariables replaces every @{name} in s with the value of the
// tenant variable name, as the Java platform does in option values. A step
// calls it when it uses the option, not when it is created, so a value that
// another flow refreshes (such as an access token) is current.
func expandTenantVariables(vars tenantStore, s string) (string, error) {
	var b strings.Builder
	for {
		start := strings.Index(s, "@{")
		if start < 0 {
			return b.String() + s, nil
		}
		end := strings.IndexByte(s[start:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated @{ in option value")
		}
		name := s[start+2 : start+end]
		v, ok, err := vars.get(name)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("tenant variable %q of tenant %q is not set", name, strings.TrimPrefix(vars.store.Name(), "tenant."))
		}
		b.WriteString(s[:start])
		b.WriteString(v)
		s = s[start+end+1:]
	}
}
