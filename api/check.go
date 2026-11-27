package api

import (
	"strings"

	flowdef "dif/flows/definition"
	flowimpl "dif/flows/impl"
	"dif/internal/flowcheck"
	"dif/internal/mapping"
	"dif/message"
	stepdef "dif/steps/definition"
)

// Diagnostic is a finding of the static checks of a flow (see Inspect): what
// is known about the message on every link, checked against what each step
// needs. Severity "error" means every message that reaches the step fails
// there; "warning" that the flow probably does not do what it means to.
type Diagnostic = flowcheck.Diagnostic

// Diagnostic severities.
const (
	SeverityError   = flowcheck.Error
	SeverityWarning = flowcheck.Warning
	SeverityInfo    = flowcheck.Info
)

// checkFlow runs the static checks on f. They never stop a flow from loading:
// should the Effect of a custom step panic, the flow has no diagnostics.
func checkFlow(f *flowdef.Flow) (ds []Diagnostic) {
	defer func() {
		if recover() != nil {
			ds = nil
		}
	}()
	return flowcheck.Check(f, stepEffect)
}

// stepEffect is what the step of n does to a message: from the core map or
// service it is bound to, else from its registered step. A source bound to a
// service contract makes the message the contract's input describes.
func stepEffect(n *flowdef.Node) *stepdef.Effect {
	if p, ok := n.Bindings[mapping.BindingKey].(*mapping.Plan); ok && p != nil {
		return mapperEffect(p)
	}
	if n.Kind == stepdef.Source {
		e := steps.Effect(n)
		if sb, ok := n.Bindings[flowimpl.FlowServiceBindingKey].(*mapping.ServiceBinding); ok && sb != nil && sb.Plan != nil {
			return contractSourceEffect(sb.Plan, e)
		}
		return e
	}
	if sb, ok := n.Bindings[mapping.ServiceBindingKey].(*mapping.ServiceBinding); ok && sb != nil && sb.Plan != nil {
		return serviceCallEffect(sb.Plan)
	}
	return steps.Effect(n)
}

// family returns the body family of a format's media type.
func family(media string) string {
	switch media {
	case "application/json":
		return stepdef.FamilyJSON
	case "application/xml":
		return stepdef.FamilyXML
	case "text/csv":
		return stepdef.FamilyCSV
	case "text/plain":
		return stepdef.FamilyText
	}
	return ""
}

// bodyOf is a body of the format named name (as a step refers to it, such as
// common:Order) that the document origin defines, with media type media.
func bodyOf(media, name, origin string) stepdef.BodyType {
	if media == "" {
		return stepdef.BodyType{}
	}
	// The same format has one identity however a document refers to it.
	if i := strings.LastIndexByte(name, ':'); i >= 0 {
		name = name[i+1:]
	}
	if origin != "" {
		name += " (" + origin + ")"
	}
	return stepdef.BodyType{Family: family(media), Media: media, Format: name}
}

// mapperEffect: a mapper checks the Content-Type and decodes the body by its
// input format, and sets the body and Content-Type of its output format.
func mapperEffect(p *mapping.Plan) *stepdef.Effect {
	info := p.Info()
	return &stepdef.Effect{
		Requires: bodyOf(p.InputMedia(), info.Input, info.InputOrigin),
		Body:     bodyOf(p.ContentType(), info.Output, info.OutputOrigin),
		Headers:  stepdef.HeadersKeep,
	}
}

// serviceCallEffect: a service call checks its input against the service's
// input format; InOut its reply, checked against the output format, replaces
// the message, InOnly the message goes on as it is.
func serviceCallEffect(p *mapping.ServicePlan) *stepdef.Effect {
	info := p.Info()
	e := &stepdef.Effect{Requires: bodyOf(p.InputMedia(), info.Input, info.InputOrigin)}
	if p.ExchangePattern() != message.InOut {
		e.KeepBody, e.Headers = true, stepdef.HeadersKeep
		return e
	}
	e.Body = bodyOf(p.OutputMedia(), info.Output, info.OutputOrigin)
	for _, h := range info.OutputHeaders {
		if h.Required || h.HasDefault {
			e.Sets = append(e.Sets, h.Name)
		}
	}
	return e
}

// contractSourceEffect: a flow bound to a service contract takes only the
// messages its input contract accepts: the body has the input format, and the
// declared input headers are set (required or with a default) or may be. The
// contract does not reject other headers; the checks take the contract as the
// interface of the flow and treat those as absent, besides the headers the
// transport source itself sets.
func contractSourceEffect(p *mapping.ServicePlan, transport *stepdef.Effect) *stepdef.Effect {
	info := p.Info()
	e := &stepdef.Effect{Headers: stepdef.HeadersKeep, Body: bodyOf(p.InputMedia(), info.Input, info.InputOrigin)}
	if transport != nil {
		e.Sets = append(e.Sets, transport.Sets...)
		e.MaySet = append(e.MaySet, transport.MaySet...)
	}
	for _, h := range info.InputHeaders {
		if h.Required || h.HasDefault {
			e.Sets = append(e.Sets, h.Name)
		} else {
			e.MaySet = append(e.MaySet, h.Name)
		}
	}
	if p.ExchangePattern() == message.InOut {
		e.ReplyNeeds = bodyOf(p.OutputMedia(), info.Output, info.OutputOrigin)
	}
	return e
}
