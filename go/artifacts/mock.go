package artifacts

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/bayes-price/agentkit/internal/recorder"
)

// MockArtifactStore is an in-memory ArtifactStore that enforces the SAME status
// semantics as the real impl (dedup on session+path, never regress
// extracted->live, write-once Source, MarkLost promotes when bytes exist), so
// tests catch regressions in those rules without Postgres or a blob backend.
type MockArtifactStore struct {
	recorder.Recorder

	mu      sync.Mutex
	byID    map[string]*Artifact
	byKey   map[string]string     // sessionID+"\x00"+filePath -> id
	content map[string][]byte     // id -> bytes (file artifacts); id+"\x00"+rel -> bytes (dir artifacts)
	dirs    map[string][]DirEntry // id -> manifest (dir artifacts)
	seq     int
}

// NewMock returns an empty in-memory artifact store.
func NewMock() *MockArtifactStore {
	return &MockArtifactStore{
		byID:    map[string]*Artifact{},
		byKey:   map[string]string{},
		content: map[string][]byte{},
		dirs:    map[string][]DirEntry{},
	}
}

func key(sessionID, filePath string) string { return sessionID + "\x00" + filePath }

func (m *MockArtifactStore) Save(ctx context.Context, art *Artifact, content io.Reader) (*Artifact, error) {
	m.Record("Save", art.SessionID, art.FilePath)
	m.mu.Lock()
	defer m.mu.Unlock()

	k := key(art.SessionID, art.FilePath)
	stored := *art // copy

	if existingID, ok := m.byKey[k]; ok {
		// Upsert: merge onto the existing row, applying the invariants.
		prev := m.byID[existingID]
		stored.ID = prev.ID
		// Never regress extracted -> live.
		if prev.Status == StatusExtracted && stored.Status == StatusLive {
			stored.Status = StatusExtracted
		}
		// Preserve a blob path the caller didn't supply.
		if stored.BlobPath == "" {
			stored.BlobPath = prev.BlobPath
		}
		// Source is write-once.
		if prev.Source != "" {
			stored.Source = prev.Source
		}
	} else {
		if stored.ID == "" {
			m.seq++
			stored.ID = fmt.Sprintf("artifact-%d", m.seq)
		}
		m.byKey[k] = stored.ID
	}

	if content != nil {
		if stored.IsDir {
			var total int64
			entries, err := WriteTarToBlobs(ctx, content, func(rel string, r io.Reader) error {
				data, err := io.ReadAll(r)
				if err != nil {
					return err
				}
				m.content[stored.ID+"\x00"+rel] = data
				total += int64(len(data))
				return nil
			})
			if err != nil {
				return nil, err
			}
			m.dirs[stored.ID] = entries
			stored.FileSize = total
			stored.Status = StatusExtracted
			if stored.BlobPath == "" {
				stored.BlobPath = "mock-blob/" + stored.ID // prefix
			}
			if stored.Meta == nil {
				stored.Meta = map[string]string{}
			}
			stored.Meta["dirDigest"] = DirDigest(entries)
		} else {
			data, err := io.ReadAll(content)
			if err != nil {
				return nil, err
			}
			m.content[stored.ID] = data
			stored.FileSize = int64(len(data))
			stored.Status = StatusExtracted
			if stored.BlobPath == "" {
				stored.BlobPath = "mock-blob/" + stored.ID
			}
		}
	}

	cp := stored
	m.byID[stored.ID] = &cp
	out := stored
	return &out, nil
}

func (m *MockArtifactStore) Load(ctx context.Context, artifactID string) (*Artifact, io.ReadCloser, error) {
	m.Record("Load", artifactID)
	m.mu.Lock()
	defer m.mu.Unlock()
	art, ok := m.byID[artifactID]
	if !ok {
		return nil, nil, fmt.Errorf("mock artifact store: %q not found", artifactID)
	}
	out := *art
	if data, ok := m.content[artifactID]; ok && out.Status != StatusLost {
		return &out, io.NopCloser(bytes.NewReader(data)), nil
	}
	return &out, nil, nil
}

func (m *MockArtifactStore) List(ctx context.Context, sessionID string) ([]*Artifact, error) {
	m.Record("List", sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*Artifact{}
	for _, a := range m.byID {
		if a.SessionID == sessionID {
			cp := *a
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (m *MockArtifactStore) MarkLost(ctx context.Context, sessionID string) error {
	m.Record("MarkLost", sessionID)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.byID {
		if a.SessionID != sessionID || a.Status != StatusLive {
			continue
		}
		if a.BlobPath != "" {
			a.Status = StatusExtracted // bytes are safe — promote, don't lose
		} else {
			a.Status = StatusLost
		}
	}
	return nil
}

// CaptureFolder implements the generalised folder/file-set capture.
// It saves the full content (typically a tar stream) as a single artifact with
// FilePath = name and ArtifactType = "folder-capture".  All status/dedup/
// never-regress rules apply via the underlying Save call.
func (m *MockArtifactStore) CaptureFolder(ctx context.Context, sessionID, name string, content io.Reader) (*Artifact, error) {
	m.Record("CaptureFolder", sessionID, name)
	return m.Save(ctx, &Artifact{
		SessionID:    sessionID,
		FilePath:     name,
		ArtifactType: "folder-capture",
		Status:       StatusLive,
		Source:       "capture",
	}, content)
}

// DirEntries returns the captured manifest for a directory artifact (test helper).
func (m *MockArtifactStore) DirEntries(artifactID string) []DirEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dirs[artifactID]
}

var _ ArtifactStore = (*MockArtifactStore)(nil)
