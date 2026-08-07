package topology

// registry.go — the built-in topology catalogue (D1: code-defined, versioned).
//
// Registry is a plain struct so tests can build throwaway instances; the
// package-level functions front one shared instance holding the built-ins,
// which register themselves in init(). Registration failures panic, mirroring
// mcpserver.register's posture: two topologies quietly shadowing one another
// would be a silently wrong system, and a bad definition is a programming
// error visible the first time the binary starts.

import (
	"fmt"
	"sync"
)

// Registry holds named, versioned topologies. Zero value is not usable — call
// NewRegistry.
type Registry struct {
	mu    sync.RWMutex
	byRef map[string]*Topology
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byRef: map[string]*Topology{}}
}

// Register adds a topology. It panics on a nil or invalid definition and on a
// duplicate name+version — boot-time programming errors, not runtime
// conditions.
func (r *Registry) Register(t *Topology) {
	if err := validateTopology(t); err != nil {
		panic(fmt.Sprintf("topology: %v", err))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ref := t.Ref()
	if _, dup := r.byRef[ref]; dup {
		panic(fmt.Sprintf("topology: duplicate registration of %s", ref))
	}
	r.byRef[ref] = t
}

// Get returns the topology registered as name@version, if any.
func (r *Registry) Get(name, version string) (*Topology, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byRef[name+"@"+version]
	return t, ok
}

// List returns every registered topology, ordered by name then numeric
// version. The slice is fresh on every call; the *Topology values are shared
// (definitions are immutable by convention — nothing in this package mutates
// one after Register).
func (r *Registry) List() []*Topology {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Topology, 0, len(r.byRef))
	for _, t := range r.byRef {
		out = append(out, t)
	}
	sortTopologies(out)
	return out
}

// builtins is the shared catalogue the package-level functions front. Seed
// topologies add themselves in their files' init() — solo.go is the pattern.
var builtins = NewRegistry()

// Register adds a topology to the built-in catalogue. Same panics as
// Registry.Register.
func Register(t *Topology) { builtins.Register(t) }

// Get looks up name@version among the built-ins.
func Get(name, version string) (*Topology, bool) { return builtins.Get(name, version) }

// List returns the built-in catalogue, ordered by name then numeric version.
func List() []*Topology { return builtins.List() }
