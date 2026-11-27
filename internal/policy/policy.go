// Package policy decides which capabilities a program may use: it parses the
// grants of a policy, matches a capability against them, and checks the
// capability manifest of a program. It depends on neither the engine, the
// steps' implementation nor DIL; it works on stepdef.Capability, which the
// steps declare.
//
// Grants per kind:
//
//   - net.connect, net.listen: host:port. The port may be *; a host *.example.com
//     covers its subdomains (not example.com itself), and * alone covers every
//     host and port. Host names compare without regard to case and are never
//     resolved: an IP address must be granted as it is written.
//   - fs.read, fs.write: a file or directory, which covers itself and
//     everything under it. Paths are made absolute (relative to the working
//     directory) and cleaned, lexically: symbolic links are not followed.
//     fs.write does not imply fs.read.
//   - env: a variable name; a grant of NAME also covers its NAME_FILE
//     companion, which names the file a secret is read from.
//
// * alone grants everything of a kind.
package policy

import (
	"fmt"
	"maps"
	"net"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	stepdef "dif/steps/definition"
)

// Kinds are the capability kinds a policy grants.
var Kinds = []string{stepdef.NetConnect, stepdef.NetListen, stepdef.FSRead, stepdef.FSWrite, stepdef.EnvVar}

// Policy is a set of grants. It denies everything it does not grant. It is
// safe for concurrent use; a nil *Policy is no policy and permits everything.
type Policy struct {
	grants map[string][]string // by kind; paths absolute and cleaned
}

// New returns the policy of grants, by kind. An empty or nil map is a policy
// that grants nothing (a sandbox).
func New(grants map[string][]string) (*Policy, error) {
	p := &Policy{grants: map[string][]string{}}
	for _, kind := range slices.Sorted(maps.Keys(grants)) {
		for _, g := range grants[kind] {
			if err := p.add(kind, g); err != nil {
				return nil, err
			}
		}
	}
	return p, nil
}

func (p *Policy) add(kind, grant string) error {
	if !slices.Contains(Kinds, kind) {
		return fmt.Errorf("permissions: unknown kind %q; want one of %s", kind, strings.Join(Kinds, ", "))
	}
	g := strings.TrimSpace(grant)
	bad := func(want string) error { return fmt.Errorf("permissions: %s grant %q: want %s", kind, grant, want) }
	switch {
	case g == "":
		return bad("a non-empty grant")
	case g == "*":
	case kind == stepdef.NetConnect || kind == stepdef.NetListen:
		host, port, err := net.SplitHostPort(g)
		if err != nil || host == "" || host != "*" && strings.Contains(strings.TrimPrefix(host, "*."), "*") || !validPort(port) {
			return bad("host:port, with * for the port or *.domain for subdomains")
		}
		g = net.JoinHostPort(strings.ToLower(host), port)
	case kind == stepdef.FSRead || kind == stepdef.FSWrite:
		abs, err := filepath.Abs(g)
		if err != nil {
			return bad("a path")
		}
		g = abs
	case kind == stepdef.EnvVar:
		if strings.ContainsAny(g, "= ") {
			return bad("a variable name")
		}
	}
	p.grants[kind] = append(p.grants[kind], g)
	return nil
}

func validPort(port string) bool {
	if port == "*" {
		return true
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// Grants returns the grants of kind, in the form they are matched in.
func (p *Policy) Grants(kind string) []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.grants[kind]...)
}

// Allows reports whether the policy grants c, as a load check does: a dynamic
// capability, whose target is known only per message, is allowed when its
// kind has a grant at all; the step is then checked when it runs (Permit).
func (p *Policy) Allows(c stepdef.Capability) bool {
	if p == nil {
		return true
	}
	if c.Dynamic {
		return len(p.grants[c.Kind]) > 0
	}
	return p.matches(c.Kind, c.Target)
}

// Permit checks c.Target, the target a step is about to touch, at run time.
// It implements stepdef.CapabilityGuard.
func (p *Policy) Permit(c stepdef.Capability) error {
	if p == nil || p.matches(c.Kind, c.Target) {
		return nil
	}
	return &stepdef.CapabilityError{Kind: c.Kind, Target: c.Target}
}

func (p *Policy) matches(kind, target string) bool {
	for _, g := range p.grants[kind] {
		if g == "*" {
			return true
		}
		switch kind {
		case stepdef.NetConnect, stepdef.NetListen:
			if matchAddress(g, target) {
				return true
			}
		case stepdef.FSRead, stepdef.FSWrite:
			abs, err := filepath.Abs(target)
			if err == nil && covers(g, abs, runtime.GOOS == "windows") {
				return true
			}
		case stepdef.EnvVar:
			if g == target || g+"_FILE" == target {
				return true
			}
		}
	}
	return false
}

// matchAddress reports whether grant, host:port as add normalized it, covers
// target, host:port.
func matchAddress(grant, target string) bool {
	gh, gp, err := net.SplitHostPort(grant)
	if err != nil {
		return false
	}
	th, tp, err := net.SplitHostPort(target)
	if err != nil {
		return false
	}
	if gp != "*" && gp != tp {
		return false
	}
	th = strings.ToLower(strings.TrimSuffix(th, "."))
	switch {
	case gh == "*":
		return true
	case strings.HasPrefix(gh, "*."):
		return strings.HasSuffix(th, gh[1:])
	}
	return gh == th
}

// covers reports whether the absolute path grant covers the absolute path
// target: they are the same, or target is under grant. With windows, \ is a
// separator too and case does not count. Both are cleaned first.
func covers(grant, target string, windows bool) bool {
	if windows {
		grant = strings.ToLower(strings.ReplaceAll(grant, `\`, "/"))
		target = strings.ToLower(strings.ReplaceAll(target, `\`, "/"))
	}
	grant, target = path.Clean(grant), path.Clean(target)
	return target == grant || strings.HasPrefix(target, strings.TrimSuffix(grant, "/")+"/")
}

// Use is a capability a step of a flow declares; a step that declares nothing
// is Undeclared.
type Use struct {
	Flow, Step string
	Capability stepdef.Capability
	Undeclared bool
}

// Violation is a use the policy does not allow, and why.
type Violation struct {
	Use
	Reason string
}

// Check returns the uses the policy does not allow, in order: an undeclared
// step, a target that is not granted, and a dynamic target of a kind nothing
// is granted of.
func (p *Policy) Check(uses []Use) []Violation {
	if p == nil {
		return nil
	}
	var out []Violation
	for _, u := range uses {
		c := u.Capability
		switch {
		case u.Undeclared:
			out = append(out, Violation{u, "the step declares no capabilities, so the policy cannot tell what it touches"})
		case p.Allows(c):
		case c.Dynamic:
			out = append(out, Violation{u, fmt.Sprintf("%s %s is known only per message, and nothing of %s is granted", c.Kind, c.Target, c.Kind)})
		default:
			out = append(out, Violation{u, fmt.Sprintf("%s %s is not granted", c.Kind, c.Target)})
		}
	}
	return out
}
