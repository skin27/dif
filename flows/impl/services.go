package impl

import (
	"context"
	"fmt"
	"strings"

	flowdef "dif/flows/definition"
	"dif/internal/mapping"
	"dif/message"
)

const FlowServiceBindingKey = "dif.internal.flowService"

type dilService mapping.ServiceDefinition

func (s *dilService) UnmarshalJSON(data []byte) error {
	type plain dilService
	return strictDeclaration(data, (*plain)(s), "service")
}

// BindScopeWithKey connects immutable documents with both display origin and canonical scope key.
func (d *Document) BindScopeWithKey(origin, scopeKey string, imports map[string]*Document) {
	d.origin = origin
	d.scopeKey = scopeKey
	d.imports = imports
}

func serviceCanonicalID(scopeKey, name string) string {
	if scopeKey == "" {
		return name
	}
	return scopeKey + "#" + name
}

func (d *Document) PrepareServices(ctx context.Context, l mapping.Limits) error {
	if d.compiledServices != nil {
		return nil
	}
	compiled := map[string]*mapping.ServiceBinding{}
	scopeKey := d.scopeKey
	if scopeKey == "" {
		scopeKey = d.origin
	}
	for _, s := range d.doc.DIL.Core.Services.Service {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.Name == "" || compiled[s.Name] != nil {
			return fmt.Errorf("empty or duplicate service name %q", s.Name)
		}
		var fmts mapping.ServiceFormats
		if s.Input != "" {
			in, err := d.format(s.Input)
			if err != nil {
				return fmt.Errorf("service %q: %w", s.Name, err)
			}
			fmts.Input = &in
		}
		if s.Output != "" {
			out, err := d.format(s.Output)
			if err != nil {
				return fmt.Errorf("service %q: %w", s.Name, err)
			}
			fmts.Output = &out
		}
		if s.Error != "" {
			errFmt, err := d.format(s.Error)
			if err != nil {
				return fmt.Errorf("service %q: %w", s.Name, err)
			}
			fmts.Error = &errFmt
		}
		plan, err := mapping.CompileService(ctx, mapping.ServiceDefinition(s), fmts, d.origin, l)
		if err != nil {
			return fmt.Errorf("service %q: %w", s.Name, err)
		}
		compiled[s.Name] = &mapping.ServiceBinding{
			ID:   serviceCanonicalID(scopeKey, s.Name),
			Name: s.Name,
			Plan: plan,
		}
	}
	d.compiledServices = compiled
	return nil
}

func (d *Document) serviceBindings() map[string]*mapping.ServiceBinding {
	out := map[string]*mapping.ServiceBinding{}
	for k, sb := range d.compiledServices {
		out[k] = sb
	}
	for _, i := range d.Imports() {
		dep := d.imports[i.Namespace]
		if dep == nil {
			continue
		}
		for k, sb := range dep.compiledServices {
			name := i.Namespace + ":" + k
			if out[name] == nil {
				out[name] = sb
			}
		}
	}
	return out
}

func (d *Document) ServiceInfo() []mapping.ServiceInfo {
	out := []mapping.ServiceInfo{}
	for _, s := range d.doc.DIL.Core.Services.Service {
		if sb := d.compiledServices[s.Name]; sb != nil {
			out = append(out, sb.Plan.Info())
		}
	}
	return out
}

func newFlowContract(sb *mapping.ServiceBinding) *flowdef.Contract {
	p := sb.Plan
	info := p.Info()
	return &flowdef.Contract{
		Name:            info.Name,
		Origin:          info.Origin,
		ExchangePattern: info.ExchangePattern,
		HasErrorFormat:  p.HasErrorFormat(),
		ValidateInput: func(ctx context.Context, m message.Message, wireHeaders bool) error {
			return p.ValidateInput(ctx, map[string]any(m), wireHeaders)
		},
		ValidateOutput: func(ctx context.Context, m message.Message) error {
			return p.ValidateOutput(ctx, map[string]any(m))
		},
		ValidateError: func(ctx context.Context, m message.Message) error {
			return p.ValidateError(ctx, map[string]any(m))
		},
	}
}

func checkServiceCallerStep(kind string, opts map[string]any, dynamic map[string]string, sb *mapping.ServiceBinding) error {
	if kind == flowdef.Router {
		return fmt.Errorf("service step cannot be a router")
	}
	if sb.Plan.ExchangePattern() == message.InOut {
		if kind != flowdef.Action {
			return fmt.Errorf("InOut service %q requires an action step, not %s", sb.Name, kind)
		}
		if dynamic["delivery"] != "" {
			return fmt.Errorf("InOut service %q does not allow parameterized delivery", sb.Name)
		}
		if del, ok := opts["delivery"].(string); ok && strings.EqualFold(del, "enqueue") {
			return fmt.Errorf("InOut service %q does not support delivery=enqueue", sb.Name)
		}
	}
	return nil
}

func checkSourceServiceMEP(source *flowdef.Node, mep string) error {
	scheme, _, _ := strings.Cut(source.URI, ":")
	switch scheme {
	case "https", "rest":
		optMEP, _ := source.Options["exchangePattern"].(string)
		srcInOnly := strings.EqualFold(optMEP, message.InOnly)
		if mep == message.InOut && srcInOnly {
			return fmt.Errorf("source %q has exchangePattern InOnly, incompatible with InOut service", source.ID)
		}
		if mep == message.InOnly && !srcInOnly {
			return fmt.Errorf("source %q has exchangePattern InOut, incompatible with InOnly service", source.ID)
		}
	case "as2":
		if mep == message.InOnly {
			return fmt.Errorf("source %q is request-reply, incompatible with InOnly service", source.ID)
		}
	case "timer", "repeater", "quartz", "counter", "file", "ftp", "ftps", "sftp", "smb", "imaps", "googledrive", "topic", "event", "reply":
		if mep == message.InOut {
			return fmt.Errorf("one-way source %q (%s) cannot implement InOut service", source.ID, scheme)
		}
	}
	return nil
}
