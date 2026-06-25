// agent-library/go/progress.go
package agentkit

import (
	"sync"
	"time"

	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// LayerProg is the per-layer byte detail surfaced to clients (mirror of
// imageregistry.LayerProgress, kept in this package so the public SessionStatus
// payload has no imageregistry dependency).
type LayerProg struct {
	ID      string `json:"id"`
	Current int64  `json:"current"`
	Total   int64  `json:"total"`
	Status  string `json:"status"`
}

// OpProgress is the live progress of a snapshot or restore operation for one
// session. BytesTotal is 0 during phases Docker cannot quantify (committing,
// starting) — clients render an indeterminate bar + elapsed time in that case.
type OpProgress struct {
	Op         string      `json:"op"`    // "snapshot" | "restore"
	Phase      string      `json:"phase"` // committing|uploading|downloading|starting|done|failed
	BytesDone  int64       `json:"bytesDone"`
	BytesTotal int64       `json:"bytesTotal"`
	Layers     []LayerProg `json:"layers,omitempty"`
	StartedAt  time.Time   `json:"startedAt"`
	Err        string      `json:"err,omitempty"`
}

type progressEntry struct {
	p      OpProgress
	doneAt time.Time // zero until finished
}

// progressStore holds the live OpProgress per session. Entries linger for ttl after
// completion so at least one poll observes the terminal done/failed state, then purge.
type progressStore struct {
	mu  sync.Mutex
	m   map[string]*progressEntry
	now func() time.Time
	ttl time.Duration
}

func newProgressStore() *progressStore {
	return &progressStore{m: map[string]*progressEntry{}, now: time.Now, ttl: 10 * time.Second}
}

func (s *progressStore) begin(sid, op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[sid] = &progressEntry{p: OpProgress{Op: op, StartedAt: s.now()}}
}

func (s *progressStore) phase(sid, phase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.m[sid]; e != nil {
		e.p.Phase = phase
	}
}

func (s *progressStore) bytes(sid string, done, total int64, layers []LayerProg) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.m[sid]; e != nil {
		e.p.BytesDone, e.p.BytesTotal, e.p.Layers = done, total, layers
	}
}

func (s *progressStore) finish(sid, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.m[sid]; e != nil {
		if errMsg != "" {
			e.p.Phase, e.p.Err = "failed", errMsg
		} else {
			e.p.Phase = "done"
		}
		e.doneAt = s.now()
	}
}

func (s *progressStore) get(sid string) (OpProgress, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.m[sid]
	if e == nil {
		return OpProgress{}, false
	}
	if !e.doneAt.IsZero() && s.now().Sub(e.doneAt) > s.ttl {
		delete(s.m, sid)
		return OpProgress{}, false
	}
	out := e.p
	if out.Layers != nil {
		cp := make([]LayerProg, len(out.Layers))
		copy(cp, out.Layers)
		out.Layers = cp
	}
	return out, true
}

// sessionProgressSink adapts a session's slot in the progressStore to the
// imageregistry.ProgressSink interface, so the ociregistry adapter can report
// push/pull bytes without importing this package.
type sessionProgressSink struct {
	store *progressStore
	sid   string
}

func (s sessionProgressSink) Bytes(done, total int64, layers []imageregistry.LayerProgress) {
	lp := make([]LayerProg, len(layers))
	for i, l := range layers {
		lp[i] = LayerProg{ID: l.ID, Current: l.Current, Total: l.Total, Status: l.Status}
	}
	s.store.bytes(s.sid, done, total, lp)
}
