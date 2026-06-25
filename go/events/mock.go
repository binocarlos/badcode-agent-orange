package events

import (
	"context"
	"sync"

	"github.com/bayes-price/agentkit/internal/recorder"
)

// MockSink is an in-memory Sink that records persisted events and tracks the
// flush counter, so tests can assert both what was persisted and that the
// flush-guard discipline (BeginFlush/EndFlush around every persist) was honoured.
type MockSink struct {
	recorder.Recorder

	mu        sync.Mutex
	persisted map[string][]Envelope // queryID -> events
	searchTxt map[string]string     // queryID -> search text
	pending   int                   // current in-flight flush count
	maxFlush  int                   // high-water mark

	// Err, if set, is returned by the next PersistQueryEvents.
	Err error
}

// NewMockSink returns an empty in-memory sink.
func NewMockSink() *MockSink {
	return &MockSink{persisted: map[string][]Envelope{}, searchTxt: map[string]string{}}
}

func (m *MockSink) BeginFlush(sessionID string) {
	m.Record("BeginFlush", sessionID)
	m.mu.Lock()
	m.pending++
	if m.pending > m.maxFlush {
		m.maxFlush = m.pending
	}
	m.mu.Unlock()
}

func (m *MockSink) EndFlush(sessionID string) {
	m.Record("EndFlush", sessionID)
	m.mu.Lock()
	if m.pending > 0 {
		m.pending--
	}
	m.mu.Unlock()
}

func (m *MockSink) PersistQueryEvents(ctx context.Context, sessionID, queryID string, events []Envelope, searchText string) error {
	m.Record("PersistQueryEvents", sessionID, queryID, len(events))
	if m.Err != nil {
		err := m.Err
		m.Err = nil
		return err
	}
	m.mu.Lock()
	cp := make([]Envelope, len(events))
	copy(cp, events)
	m.persisted[queryID] = cp
	m.searchTxt[queryID] = searchText
	m.mu.Unlock()
	return nil
}

// Persisted returns the events stored for a query.
func (m *MockSink) Persisted(queryID string) []Envelope {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.persisted[queryID]
}

// MaxConcurrentFlushes is the high-water mark of in-flight flushes — useful to
// assert the guard never let an archive slip through mid-flush.
func (m *MockSink) MaxConcurrentFlushes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxFlush
}

// PendingFlushes returns the current in-flight count (should be 0 at rest).
func (m *MockSink) PendingFlushes() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending
}

var _ Sink = (*MockSink)(nil)
