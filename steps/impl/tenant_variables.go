package impl

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"dif/message"
	stepdef "dif/steps/definition"
)

// Tenant variables are named values that the flows of a tenant share, such as
// a token one flow fetches and others use. The Java platform keeps them in the
// tenant's database; DIF keeps them in memory, shared by all flows of the
// process, and loses them when dif exits.

// tenantStore holds tenant variables by tenant and name. It is the seam for
// persisting them in a later iteration.
type tenantStore interface {
	get(tenant, name string) (string, bool)
	set(tenant, name, value string)
	remove(tenant, name string)
}

// memTenantStore is the in-memory tenantStore.
type memTenantStore struct {
	mu   sync.RWMutex
	vars map[[2]string]string // by tenant and name
}

func (s *memTenantStore) get(tenant, name string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.vars[[2]string{tenant, name}]
	return v, ok
}

func (s *memTenantStore) set(tenant, name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vars[[2]string{tenant, name}] = value
}

func (s *memTenantStore) remove(tenant, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vars, [2]string{tenant, name})
}

var tenantVariables tenantStore = &memTenantStore{vars: map[[2]string]string{}}

// tenantVariable is the variable a step works on: its tenant (the option
// tenantDbName) and name (the URI path).
type tenantVariable struct{ tenant, name string }

func newTenantVariable(p stepdef.Params) (tenantVariable, error) {
	name, _ := p["path"].(string)
	v := tenantVariable{tenant: p["tenantDbName"].(string), name: name}
	if v.name == "" {
		return v, fmt.Errorf("no variable name: want <step>:<name>")
	}
	return v, nil
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
	value, err := compileExpression(p["language"].(string), p["value"].(string))
	if err != nil {
		return nil, fmt.Errorf("option value: %w", err)
	}
	return setTenantVariableAction{v, value}, nil
}

func (a setTenantVariableAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	s, err := a.value.eval(m)
	if err != nil {
		return nil, err
	}
	tenantVariables.set(a.v.tenant, a.v.name, s)
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

func (a getTenantVariableAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	m[a.header], _ = tenantVariables.get(a.v.tenant, a.v.name)
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

func (a removeTenantVariableAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	tenantVariables.remove(a.v.tenant, a.v.name)
	return m, nil
}

// expandTenantVariables replaces every @{name} in s with the value of the
// tenant variable name, as the Java platform does in option values. A step
// calls it when it uses the option, not when it is created, so a value that
// another flow refreshes (such as an access token) is current.
func expandTenantVariables(tenant, s string) (string, error) {
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
		v, ok := tenantVariables.get(tenant, name)
		if !ok {
			return "", fmt.Errorf("tenant variable %q of tenant %q is not set", name, tenant)
		}
		b.WriteString(s[:start])
		b.WriteString(v)
		s = s[start+end+1:]
	}
}
