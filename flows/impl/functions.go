package impl

import (
	"context"
	"fmt"
	"strings"

	"dif/internal/mapping"
)

type dilFunction mapping.FunctionDefinition

func (f *dilFunction) UnmarshalJSON(data []byte) error {
	type plain dilFunction
	return strictDeclaration(data, (*plain)(f), "function")
}
func (d *Document) functionOwner(name string) (*Document, *dilFunction) {
	for i := range d.doc.DIL.Core.Functions.Function {
		f := &d.doc.DIL.Core.Functions.Function[i]
		if f.Name == name {
			return d, f
		}
	}
	alias, key, ok := strings.Cut(name, ":")
	if ok && d.imports[alias] != nil {
		other := d.imports[alias]
		for i := range other.doc.DIL.Core.Functions.Function {
			f := &other.doc.DIL.Core.Functions.Function[i]
			if f.Name == key {
				return other, f
			}
		}
	}
	return nil, nil
}
func (d *Document) compileFunction(ctx context.Context, name string, l mapping.Limits, stack []string) (*mapping.Function, error) {
	owner, def := d.functionOwner(name)
	if owner == nil {
		return nil, fmt.Errorf("function %q not found", name)
	}
	if owner.functions == nil {
		owner.functions = map[string]*mapping.Function{}
	}
	if f := owner.functions[def.Name]; f != nil {
		return f, nil
	}
	label := owner.origin + ":" + def.Name
	if owner.compiling == nil {
		owner.compiling = map[string]bool{}
	}
	if owner.compiling[def.Name] {
		return nil, fmt.Errorf("function cycle: %s", strings.Join(append(stack, label), " -> "))
	}
	if len(stack) >= l.CallDepth {
		return nil, fmt.Errorf("function call depth limit exceeded")
	}
	owner.compiling[def.Name] = true
	defer delete(owner.compiling, def.Name)
	declaration := mapping.FunctionDefinition(*def)
	if (declaration.Expression == "") == (declaration.SourceRef == "") {
		return nil, fmt.Errorf("function %q requires exactly one of expression and sourceRef", def.Name)
	}
	if declaration.SourceRef != "" {
		src, e := owner.resource(declaration.SourceRef)
		if e != nil {
			return nil, e
		}
		declaration.Expression = src
	}
	f, e := mapping.CompileFunction(ctx, declaration, owner.origin, l, func(n string) (*mapping.Function, error) {
		return owner.compileFunction(ctx, n, l, append(stack, label))
	})
	if e != nil {
		return nil, fmt.Errorf("function %q (%s): %w", def.Name, owner.origin, e)
	}
	owner.functions[def.Name] = f
	return f, nil
}
func (d *Document) FunctionSourceSize() (int, int, error) {
	total := 0
	for _, f := range d.doc.DIL.Core.Functions.Function {
		s := f.Expression
		if f.SourceRef != "" {
			var e error
			s, e = d.resource(f.SourceRef)
			if e != nil {
				return 0, 0, e
			}
		}
		total += len(s)
	}
	return len(d.doc.DIL.Core.Functions.Function), total, nil
}
func (d *Document) PrepareFunctions(ctx context.Context, l mapping.Limits) error {
	var e error
	l, e = l.Normalize()
	if e != nil {
		return e
	}
	d.functionLimits = l
	count, size, e := d.FunctionSourceSize()
	if e != nil {
		return e
	}
	if count > l.Functions || size > l.TotalSourceBytes {
		return fmt.Errorf("function declaration/source limit exceeded")
	}
	for _, f := range d.doc.DIL.Core.Functions.Function {
		if _, e = d.compileFunction(ctx, f.Name, l, nil); e != nil {
			return e
		}
	}
	if e = d.prepareProcessors(l); e != nil {
		return e
	}
	return d.prepareInterceptors()
}
func (d *Document) functionBindings() *mapping.Functions {
	out := map[string]*mapping.Function{}
	for k, f := range d.functions {
		out[k] = f
	}
	for _, i := range d.Imports() {
		if dep := d.imports[i.Namespace]; dep != nil {
			for k, f := range dep.functions {
				name := i.Namespace + ":" + k
				if out[name] == nil {
					out[name] = f
				}
			}
		}
	}
	return mapping.NewFunctions(out, d.functionLimits)
}
func (d *Document) FunctionInfo() []mapping.FunctionInfo {
	out := []mapping.FunctionInfo{}
	for _, def := range d.doc.DIL.Core.Functions.Function {
		if f := d.functions[def.Name]; f != nil {
			out = append(out, f.Info())
		}
	}
	return out
}
