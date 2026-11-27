package api

import (
	"context"
	"errors"
	"strings"

	flowdef "dif/flows/definition"
	"dif/internal/flowcheck"
	"dif/internal/policy"
	stepdef "dif/steps/definition"
)

// Capability is an outside resource a step can touch: an address it connects
// to or listens on, a file it reads or writes, an environment variable it
// reads; see stepdef.Capability. The capabilities of the steps of a program are
// its manifest, in NodeInfo.Capabilities.
type Capability = stepdef.Capability

// CapabilityError is the error of a step that the policy did not permit to
// touch a target at run time.
type CapabilityError = stepdef.CapabilityError

// Policy grants capabilities; it denies what it does not grant. See
// NewPolicy, Runtime.SetPolicy and WithPolicy.
type Policy = policy.Policy

// CodeNotPermitted is the code of the error diagnostic of a step whose
// capabilities a policy does not grant.
const CodeNotPermitted = "not-permitted"

// NewPolicy returns the policy of grants, by kind (net.connect, net.listen,
// fs.read, fs.write, env); see package internal/policy for how they match. An
// empty map grants nothing.
func NewPolicy(grants map[string][]string) (*Policy, error) { return policy.New(grants) }

// WithPolicy returns ctx carrying p as the guard of the flows started with it
// (Flow.StartContext, engine.StartGroup): a step whose target is known only per
// message asks it before it touches the target, and a refusal fails the
// message with a *CapabilityError. A nil p changes nothing.
func WithPolicy(ctx context.Context, p *Policy) context.Context {
	if p == nil {
		return ctx
	}
	return stepdef.WithCapabilityGuard(ctx, p)
}

// stepCapabilities is the manifest entry of the step of n; declared is false
// for a step that declares none. Should the Capabilities of a custom step
// panic, it is undeclared.
func stepCapabilities(n *flowdef.Node) (caps []Capability, declared bool) {
	defer func() {
		if recover() != nil {
			caps, declared = nil, false
		}
	}()
	return steps.Capabilities(n)
}

// CheckPolicy checks the capability manifest of the program against p: it adds
// a not-permitted error diagnostic to the flow (and to the program) for each
// capability p does not grant and for each step that declares none, and
// returns them. A dynamic capability passes when p grants anything of its
// kind; it is checked when the step runs. A nil p checks nothing.
func (info *ProgramInfo) CheckPolicy(p *Policy) []Diagnostic {
	if p == nil {
		return nil
	}
	var all []Diagnostic
	for _, f := range info.Flows {
		if f.Flow == nil {
			continue
		}
		ds := checkFlowPolicy(f.Flow, p)
		f.Flow.Diagnostics = append(f.Flow.Diagnostics, ds...)
		all = append(all, ds...)
	}
	info.Diagnostics = append(info.Diagnostics, all...)
	return all
}

func checkFlowPolicy(f *FlowInfo, p *Policy) []Diagnostic {
	var uses []policy.Use
	for _, s := range f.Steps {
		if s.CapabilitiesUndeclared {
			uses = append(uses, policy.Use{Flow: f.ID, Step: s.ID, Undeclared: true})
		}
		for _, c := range s.Capabilities {
			uses = append(uses, policy.Use{Flow: f.ID, Step: s.ID, Capability: c})
		}
	}
	var ds []Diagnostic
	for _, v := range p.Check(uses) {
		ds = append(ds, Diagnostic{Flow: v.Flow, Step: v.Step, Severity: flowcheck.Error, Code: CodeNotPermitted, Message: v.Reason})
	}
	return ds
}

// policyError is the error of a load that a policy refuses.
func policyError(ds []Diagnostic) error {
	lines := make([]string, len(ds))
	for i, d := range ds {
		lines[i] = d.String()
	}
	return errors.New("the policy does not permit what the program can do:\n  " + strings.Join(lines, "\n  "))
}
