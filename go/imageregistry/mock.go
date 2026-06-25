package imageregistry

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"sync"

	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/internal/recorder"
)

// MockImageRegistry is an in-memory ImageRegistry. It round-trips Snapshot refs
// through Persist/Materialize in memory so suspend/restore is testable with no
// Docker and no blob storage. Records every call.
type MockImageRegistry struct {
	recorder.Recorder

	mu         sync.Mutex
	persist    map[string]execenv.ImageRef // handle ref -> image ref
	present    map[execenv.ImageRef]bool
	resolveMap map[string]execenv.ImageRef // content-hash -> image ref (Resolve cache)
	seq        int

	// Err, if set, is returned by the next call (failure injection).
	Err error
}

// NewMock returns an empty in-memory registry.
func NewMock() *MockImageRegistry {
	return &MockImageRegistry{
		persist:    map[string]execenv.ImageRef{},
		present:    map[execenv.ImageRef]bool{},
		resolveMap: map[string]execenv.ImageRef{},
	}
}

func (m *MockImageRegistry) EnsurePresent(ctx context.Context, ref execenv.ImageRef) error {
	m.Record("EnsurePresent", string(ref))
	if sink := ProgressSinkFromContext(ctx); sink != nil {
		sink.Bytes(100, 100, []LayerProgress{{ID: "mock-layer", Current: 100, Total: 100, Status: "Pull complete"}})
	}
	if m.Err != nil {
		return m.takeErr()
	}
	m.mu.Lock()
	m.present[ref] = true
	m.mu.Unlock()
	return nil
}

func (m *MockImageRegistry) Build(ctx context.Context, spec BuildSpec) (execenv.ImageRef, error) {
	m.Record("Build", string(spec.BaseImage), spec.Tag)
	if m.Err != nil {
		return "", m.takeErr()
	}
	ref := execenv.ImageRef(spec.Tag)
	if ref == "" {
		ref = "mock-built-image"
	}
	m.mu.Lock()
	m.present[ref] = true
	// Also cache in the resolve map so a subsequent Resolve hits.
	m.resolveMap[contentHash(spec)] = ref
	m.mu.Unlock()
	return ref, nil
}

func (m *MockImageRegistry) Resolve(ctx context.Context, spec BuildSpec) (execenv.ImageRef, bool, error) {
	m.Record("Resolve", string(spec.BaseImage), spec.Tag, spec.SourceKey)
	if m.Err != nil {
		return "", false, m.takeErr()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	h := contentHash(spec)
	if ref, ok := m.resolveMap[h]; ok {
		return ref, true, nil
	}
	return "", false, nil
}

func (m *MockImageRegistry) Persist(ctx context.Context, ref execenv.ImageRef, opts PersistOptions) (Handle, error) {
	m.Record("Persist", string(ref), opts.SessionID, opts.PreferDiff)
	if sink := ProgressSinkFromContext(ctx); sink != nil {
		sink.Bytes(100, 100, []LayerProgress{{ID: "mock-layer", Current: 100, Total: 100, Status: "Pushed"}})
	}
	if m.Err != nil {
		return Handle{}, m.takeErr()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	hRef := fmt.Sprintf("mock-handle:%s:%d", opts.SessionID, m.seq)
	m.persist[hRef] = ref
	return Handle{Kind: "mock", Ref: hRef, Meta: map[string]string{"session": opts.SessionID}}, nil
}

func (m *MockImageRegistry) Materialize(ctx context.Context, h Handle) (execenv.ImageRef, error) {
	m.Record("Materialize", h.Ref)
	if sink := ProgressSinkFromContext(ctx); sink != nil {
		sink.Bytes(100, 100, []LayerProgress{{ID: "mock-layer", Current: 100, Total: 100, Status: "Pull complete"}})
	}
	if m.Err != nil {
		return "", m.takeErr()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ref, ok := m.persist[h.Ref]
	if !ok {
		return "", fmt.Errorf("mock registry: handle %q not found", h.Ref)
	}
	m.present[ref] = true
	return ref, nil
}

func (m *MockImageRegistry) Remove(ctx context.Context, h Handle) error {
	m.Record("Remove", h.Ref)
	if m.Err != nil {
		return m.takeErr()
	}
	m.mu.Lock()
	delete(m.persist, h.Ref)
	m.mu.Unlock()
	return nil
}

func (m *MockImageRegistry) Capabilities() Capabilities {
	return Capabilities{
		SupportsDiff:    true,
		SupportsBuild:   true,
		SupportsRemote:  false,
		PortableHandles: true,
	}
}

func (m *MockImageRegistry) takeErr() error {
	err := m.Err
	m.Err = nil
	return err
}

// contentHash computes a deterministic content-hash key for a BuildSpec.
// Used by Build to populate the resolve cache and by Resolve to look up a hit.
// sha256(base + sorted overlays + sorted build args + layer + sourceKey).
func contentHash(spec BuildSpec) string {
	h := sha256.New()
	fmt.Fprintf(h, "base:%s\n", spec.BaseImage)
	fmt.Fprintf(h, "layer:%s\n", spec.Layer)
	fmt.Fprintf(h, "sourcekey:%s\n", spec.SourceKey)

	// Sort overlay sources for determinism.
	srcs := make([]string, len(spec.Overlays))
	for i, o := range spec.Overlays {
		srcs[i] = o.Source + ":" + o.Target
	}
	sort.Strings(srcs)
	for _, s := range srcs {
		fmt.Fprintf(h, "overlay:%s\n", s)
	}

	// Sort build args for determinism.
	argKeys := make([]string, 0, len(spec.BuildArgs))
	for k := range spec.BuildArgs {
		argKeys = append(argKeys, k)
	}
	sort.Strings(argKeys)
	for _, k := range argKeys {
		fmt.Fprintf(h, "arg:%s=%s\n", k, spec.BuildArgs[k])
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// Compile-time assertion.
var _ ImageRegistry = (*MockImageRegistry)(nil)
