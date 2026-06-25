// Package agentkittest provides in-memory host-extension implementations so a
// host can integration-test its agent flows against agentkit with no database,
// no blob backend, and no auth server. Pair these with execenv.NewMock() and
// imageregistry.NewMock() for a fully hermetic Runner.
package agentkittest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// MemStore is an in-memory RunnerStore and fleet.WorkerStore for tests.
type MemStore struct {
	mu             sync.Mutex
	sessions       map[string]*agentdb.Session
	queryEvents    map[string][]events.Envelope // key: sessionID+"\x00"+queryID
	workerBindings map[string]string            // sessionID -> workerID
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		sessions:       map[string]*agentdb.Session{},
		queryEvents:    map[string][]events.Envelope{},
		workerBindings: map[string]string{},
	}
}

// Seed inserts a session row (the host would normally persist it before CreateSession).
func (s *MemStore) Seed(sess *agentdb.Session) {
	s.mu.Lock()
	cp := *sess
	s.sessions[sess.ID] = &cp
	s.mu.Unlock()
}

func (s *MemStore) GetSession(ctx context.Context, id string) (*agentdb.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return nil, fmt.Errorf("memstore: session %q not found", id)
	}
	cp := *sess
	return &cp, nil
}

func (s *MemStore) UpdateSession(ctx context.Context, sess *agentdb.Session) (*agentdb.Session, error) {
	s.mu.Lock()
	cp := *sess
	s.sessions[sess.ID] = &cp
	s.mu.Unlock()
	return &cp, nil
}

func (s *MemStore) PersistQueryEventsFlat(ctx context.Context, sessionID, queryID string, evs []events.Envelope, searchText string) error {
	s.mu.Lock()
	cp := make([]events.Envelope, len(evs))
	copy(cp, evs)
	s.queryEvents[sessionID+"\x00"+queryID] = cp
	s.mu.Unlock()
	return nil
}

func (s *MemStore) ListQueryEventsFlat(ctx context.Context, sessionID string) ([]events.Envelope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []events.Envelope
	for k, evs := range s.queryEvents {
		if strings.HasPrefix(k, sessionID+"\x00") {
			out = append(out, evs...)
		}
	}
	return out, nil
}

func (s *MemStore) GetSnapshotHandle(ctx context.Context, sessionID string) (imageregistry.Handle, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok || sess.SnapshotHandle == "" {
		return imageregistry.Handle{}, false, nil
	}
	var h imageregistry.Handle
	if err := json.Unmarshal([]byte(sess.SnapshotHandle), &h); err != nil {
		return imageregistry.Handle{}, false, err
	}
	return h, true, nil
}

func (s *MemStore) SetSnapshotHandle(ctx context.Context, sessionID string, h imageregistry.Handle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return fmt.Errorf("memstore: session %q not found", sessionID)
	}
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	sess.SnapshotHandle = string(b)
	return nil
}

func (s *MemStore) GetWorkerBinding(ctx context.Context, sessionID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wid, ok := s.workerBindings[sessionID]
	return wid, ok, nil
}

func (s *MemStore) SetWorkerBinding(ctx context.Context, sessionID, workerID string) error {
	s.mu.Lock()
	s.workerBindings[sessionID] = workerID
	s.mu.Unlock()
	return nil
}

func (s *MemStore) ClearWorkerBinding(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	delete(s.workerBindings, sessionID)
	s.mu.Unlock()
	return nil
}

// MemBlobs is an in-memory extension.BlobStore using single-key addressing.
type MemBlobs struct {
	mu   sync.Mutex
	data map[string][]byte
}

// NewMemBlobs returns an empty in-memory blob store.
func NewMemBlobs() *MemBlobs { return &MemBlobs{data: map[string][]byte{}} }

func (b *MemBlobs) Write(ctx context.Context, key string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.data[key] = data
	b.mu.Unlock()
	return nil
}

func (b *MemBlobs) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, ok := b.data[key]
	if !ok {
		return nil, fmt.Errorf("memblobs: %q not found", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (b *MemBlobs) Exists(ctx context.Context, key string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.data[key]
	return ok, nil
}

func (b *MemBlobs) Delete(ctx context.Context, key string) error {
	b.mu.Lock()
	delete(b.data, key)
	b.mu.Unlock()
	return nil
}

func (b *MemBlobs) List(ctx context.Context, prefix string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for k := range b.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

// MemBlobsFactory is an in-memory extension.BlobStoreFactory. It stores all
// blobs in a single flat MemBlobs using "namespace/key" addressing.
type MemBlobsFactory struct {
	blobs *MemBlobs
}

// NewMemBlobsFactory returns a new factory backed by a shared MemBlobs.
func NewMemBlobsFactory() *MemBlobsFactory {
	return &MemBlobsFactory{blobs: NewMemBlobs()}
}

// Blobs returns the underlying flat MemBlobs so tests can seed data directly.
// Use Write(ctx, "namespace/path", ...) to match what ForSession/Global will see.
func (f *MemBlobsFactory) Blobs() *MemBlobs { return f.blobs }

// namespaceBlobs is a BlobStore view scoped to a namespace prefix.
type namespaceBlobs struct {
	ns    string
	blobs *MemBlobs
}

func (n *namespaceBlobs) Write(ctx context.Context, key string, r io.Reader) error {
	return n.blobs.Write(ctx, n.ns+"/"+key, r)
}
func (n *namespaceBlobs) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	return n.blobs.Read(ctx, n.ns+"/"+key)
}
func (n *namespaceBlobs) Exists(ctx context.Context, key string) (bool, error) {
	return n.blobs.Exists(ctx, n.ns+"/"+key)
}
func (n *namespaceBlobs) Delete(ctx context.Context, key string) error {
	return n.blobs.Delete(ctx, n.ns+"/"+key)
}
func (n *namespaceBlobs) List(ctx context.Context, prefix string) ([]string, error) {
	return n.blobs.List(ctx, n.ns+"/"+prefix)
}

// ForSession returns a BlobStore scoped to the given sessionID.
func (f *MemBlobsFactory) ForSession(_ context.Context, sessionID string) (extension.BlobStore, error) {
	return &namespaceBlobs{ns: "session/" + sessionID, blobs: f.blobs}, nil
}

// Global returns a BlobStore scoped to the given namespace.
func (f *MemBlobsFactory) Global(namespace string) extension.BlobStore {
	return &namespaceBlobs{ns: namespace, blobs: f.blobs}
}

// StaticClaims is an extension.ScopedClaimsIssuer that returns a fixed token.
type StaticClaims struct{ Token string }

// Issue returns the static token.
func (c StaticClaims) Issue(ctx context.Context, scope extension.ContextScope, sessionID string) (string, error) {
	return c.Token, nil
}

// Compile-time assertions.
var (
	_ extension.BlobStore          = (*MemBlobs)(nil)
	_ extension.BlobStoreFactory   = (*MemBlobsFactory)(nil)
	_ extension.ScopedClaimsIssuer = StaticClaims{}
)
