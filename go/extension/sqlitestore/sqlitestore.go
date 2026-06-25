// Package sqlitestore is a SQLite-backed reference implementation of
// agentkit.RunnerStore. Uses modernc.org/sqlite (pure-Go driver, no CGO) so
// it works on any platform without a C toolchain.
//
// For dev/local/examples use — not a high-throughput production store.
// Each method opens no extra connections; all operations go through the single
// *sql.DB opened in Open.
//
// Schema:
//
//	sessions         (id PK, customer, job, user_email, persona, status, snapshot_handle, worker_id)
//	query_events     (session_id, query_id, payload TEXT, search_text, PRIMARY KEY(session_id,query_id))
//
// Blobs are stored in a filesblob.BlobStore rooted next to the DB file
// (at <dbdir>/blobs).
package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // register "sqlite" driver

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/extension/filesblob"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// Store is a SQLite-backed RunnerStore.
type Store struct {
	db    *sql.DB
	blobs *filesblob.BlobStore
}

// Open opens (or creates) a SQLite database at path, creates tables if they
// don't exist, and returns a ready Store. The caller must call Close when done.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: open %s: %w", path, err)
	}
	if err := createTables(db); err != nil {
		db.Close()
		return nil, err
	}
	blobsDir := filepath.Join(filepath.Dir(path), "blobs")
	if err := os.MkdirAll(blobsDir, 0o755); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlitestore: create blobs dir: %w", err)
	}
	return &Store{db: db, blobs: filesblob.NewBlobStore(blobsDir)}, nil
}

// Close shuts down the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Blobs returns the filesystem BlobStore rooted next to the DB file.
func (s *Store) Blobs() extension.BlobStore { return s.blobs }

// ---- schema ------------------------------------------------------------------

func createTables(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS sessions (
	id              TEXT PRIMARY KEY,
	customer        TEXT NOT NULL DEFAULT '',
	job             TEXT NOT NULL DEFAULT '',
	user_email      TEXT NOT NULL DEFAULT '',
	persona         TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL DEFAULT '',
	snapshot_handle TEXT NOT NULL DEFAULT '',
	worker_id       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS query_events (
	session_id  TEXT NOT NULL,
	query_id    TEXT NOT NULL,
	payload     TEXT NOT NULL,
	search_text TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (session_id, query_id)
);
`
	_, err := db.Exec(ddl)
	if err != nil {
		return fmt.Errorf("sqlitestore: createTables: %w", err)
	}
	return nil
}

// ---- RunnerStore methods -----------------------------------------------------

// GetSession returns the session row for id.
func (s *Store) GetSession(ctx context.Context, id string) (*agentdb.Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, customer, job, user_email, persona, status, snapshot_handle, worker_id FROM sessions WHERE id=?`, id)
	var sess agentdb.Session
	if err := row.Scan(&sess.ID, &sess.Customer, &sess.Job, &sess.UserEmail, &sess.Persona, &sess.Status, &sess.SnapshotHandle, &sess.WorkerID); err != nil {
		return nil, fmt.Errorf("sqlitestore: GetSession %q: %w", id, err)
	}
	return &sess, nil
}

// UpdateSession upserts the session row.
func (s *Store) UpdateSession(ctx context.Context, sess *agentdb.Session) (*agentdb.Session, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(id, customer, job, user_email, persona, status, snapshot_handle, worker_id)
		 VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET
		   customer        = CASE WHEN excluded.customer        != '' THEN excluded.customer        ELSE customer        END,
		   job             = CASE WHEN excluded.job             != '' THEN excluded.job             ELSE job             END,
		   user_email      = CASE WHEN excluded.user_email      != '' THEN excluded.user_email      ELSE user_email      END,
		   persona         = CASE WHEN excluded.persona         != '' THEN excluded.persona         ELSE persona         END,
		   status          = CASE WHEN excluded.status          != '' THEN excluded.status          ELSE status          END,
		   snapshot_handle = excluded.snapshot_handle,
		   worker_id       = excluded.worker_id`,
		sess.ID, sess.Customer, sess.Job, sess.UserEmail, sess.Persona, sess.Status, sess.SnapshotHandle, sess.WorkerID,
	)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: UpdateSession %q: %w", sess.ID, err)
	}
	return sess, nil
}

// SetSnapshotHandle persists a snapshot handle for a session.
func (s *Store) SetSnapshotHandle(ctx context.Context, sessionID string, h imageregistry.Handle) error {
	blob, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("sqlitestore: SetSnapshotHandle marshal: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sessions(id, snapshot_handle) VALUES(?,?)
		 ON CONFLICT(id) DO UPDATE SET snapshot_handle=excluded.snapshot_handle`,
		sessionID, string(blob),
	)
	if err != nil {
		return fmt.Errorf("sqlitestore: SetSnapshotHandle %q: %w", sessionID, err)
	}
	return nil
}

// GetSnapshotHandle retrieves the latest snapshot handle for a session.
// ok=false if none has been persisted.
func (s *Store) GetSnapshotHandle(ctx context.Context, sessionID string) (imageregistry.Handle, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT snapshot_handle FROM sessions WHERE id=?`, sessionID)
	var raw string
	if err := row.Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return imageregistry.Handle{}, false, nil
		}
		return imageregistry.Handle{}, false, fmt.Errorf("sqlitestore: GetSnapshotHandle %q: %w", sessionID, err)
	}
	if raw == "" {
		return imageregistry.Handle{}, false, nil
	}
	var h imageregistry.Handle
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return imageregistry.Handle{}, false, fmt.Errorf("sqlitestore: GetSnapshotHandle unmarshal: %w", err)
	}
	return h, true, nil
}

// PersistQueryEventsFlat upserts the event payload for (sessionID, queryID).
func (s *Store) PersistQueryEventsFlat(ctx context.Context, sessionID, queryID string, evs []events.Envelope, searchText string) error {
	blob, err := json.Marshal(evs)
	if err != nil {
		return fmt.Errorf("sqlitestore: PersistQueryEventsFlat marshal: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO query_events(session_id,query_id,payload,search_text) VALUES(?,?,?,?)
		 ON CONFLICT(session_id,query_id) DO UPDATE SET payload=excluded.payload, search_text=excluded.search_text`,
		sessionID, queryID, string(blob), searchText,
	)
	if err != nil {
		return fmt.Errorf("sqlitestore: PersistQueryEventsFlat %q/%q: %w", sessionID, queryID, err)
	}
	return nil
}

// ListQueryEventsFlat returns all events for a session as a flat slice.
func (s *Store) ListQueryEventsFlat(ctx context.Context, sessionID string) ([]events.Envelope, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT payload FROM query_events WHERE session_id=? ORDER BY rowid`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sqlitestore: ListQueryEventsFlat query: %w", err)
	}
	defer rows.Close()

	var out []events.Envelope
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("sqlitestore: ListQueryEventsFlat scan: %w", err)
		}
		var evs []events.Envelope
		if err := json.Unmarshal([]byte(payload), &evs); err != nil {
			return nil, fmt.Errorf("sqlitestore: ListQueryEventsFlat unmarshal: %w", err)
		}
		out = append(out, evs...)
	}
	return out, rows.Err()
}

// GetWorkerBinding returns the worker ID sticky-bound to sessionID.
func (s *Store) GetWorkerBinding(ctx context.Context, sessionID string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT worker_id FROM sessions WHERE id=?`, sessionID)
	var wid string
	if err := row.Scan(&wid); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("sqlitestore: GetWorkerBinding %q: %w", sessionID, err)
	}
	if wid == "" {
		return "", false, nil
	}
	return wid, true, nil
}

// SetWorkerBinding records the sticky session→worker placement.
func (s *Store) SetWorkerBinding(ctx context.Context, sessionID, workerID string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(id, worker_id) VALUES(?,?)
		 ON CONFLICT(id) DO UPDATE SET worker_id=excluded.worker_id`,
		sessionID, workerID,
	)
	if err != nil {
		return fmt.Errorf("sqlitestore: SetWorkerBinding %q: %w", sessionID, err)
	}
	return nil
}

// ClearWorkerBinding removes the sticky binding for sessionID.
func (s *Store) ClearWorkerBinding(ctx context.Context, sessionID string) error {
	return s.SetWorkerBinding(ctx, sessionID, "")
}
