package definition

import "time"

// StoresBinding is the node binding under which a loader puts the stores a
// step may refer to, a map[string]Store by the name the step uses (for a store
// of an imported document, <namespace>:<name>).
const StoresBinding = "dif.internal.stores"

// Store is a store that a program declares: a named key-value store of the
// channel runtime. Name is its name in the runtime, the name it is declared
// with; TTL 0 keeps entries for ever, MaxKeys 0 is the runtime's default.
type Store struct {
	Name    string
	Durable bool
	TTL     time.Duration
	MaxKeys int
}
