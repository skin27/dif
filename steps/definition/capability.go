package definition

import "context"

// Capability kinds: what a step can touch outside the process.
const (
	NetConnect = "net.connect" // opens a connection; the target is host:port
	NetListen  = "net.listen"  // accepts connections; the target is address:port
	FSRead     = "fs.read"     // reads a file or a directory; the target is its path
	FSWrite    = "fs.write"    // creates, changes, moves or deletes files; the target is a file or directory path
	EnvVar     = "env"         // reads an environment variable; the target is its name
)

// Capability is an outside resource a step can touch, as its definition
// declares it from the step's validated options (see Definition.Capabilities).
// It is data: the step's implementation must do no more than it declares.
//
// A target that is known only when a message arrives, such as an address with
// ${...} in it, is Dynamic: then Target is the option text with its
// placeholders, or "*" when nothing of it is known, and the step asks the
// guard of its context (Permit) with the real target before it touches it.
type Capability struct {
	Kind    string `json:"kind"`
	Target  string `json:"target"`
	Dynamic bool   `json:"dynamic,omitempty"`
}

func (c Capability) String() string {
	s := c.Kind + " " + c.Target
	if c.Dynamic {
		s += " (dynamic)"
	}
	return s
}

// CapabilityGuard decides at run time whether a step may touch a target. A
// policy is one; see WithCapabilityGuard.
type CapabilityGuard interface {
	// Permit returns nil when the step may touch c.Target, else a
	// *CapabilityError.
	Permit(c Capability) error
}

// CapabilityError is the error of a step that was not permitted to touch a
// target. It wraps nothing, so an error handler matches it by its class,
// *definition.CapabilityError.
type CapabilityError struct {
	Kind, Target string
}

func (e *CapabilityError) Error() string {
	return e.Kind + " " + e.Target + " is not permitted by the policy"
}

type guardKey struct{}

// WithCapabilityGuard returns ctx carrying g, which the steps of the flows
// started with it consult before they touch an outside resource.
func WithCapabilityGuard(ctx context.Context, g CapabilityGuard) context.Context {
	return context.WithValue(ctx, guardKey{}, g)
}

// Permit asks the guard of ctx whether the step may touch target. Without a
// guard it permits everything.
func Permit(ctx context.Context, kind, target string) error {
	if g, ok := ctx.Value(guardKey{}).(CapabilityGuard); ok && g != nil {
		return g.Permit(Capability{Kind: kind, Target: target})
	}
	return nil
}
