// Package dbartifacts is an artifacts.ArtifactStore whose BYTES live in a
// BlobStore and whose METADATA lives in Postgres (`agent_artifacts`, migration
// 002 + 033).
//
// # Why this package exists
//
// The previous wiring, extension/blobartifacts, keeps the index in an
// in-process map. Bytes survived an agentd restart; the rows did not, so every
// artifact a session had ever produced became an unlisted, unloadable blob in
// the bucket. Worse, the map's ID counter restarts at 1, so the first artifact
// written after a restart takes the blob key of the first artifact written
// before it and overwrites those bytes.
//
// The index is NOT in the blob store. An index in object storage cannot be
// queried, and every concurrent write becomes a read-modify-write race on one
// JSON object — that is a database built on a bucket. `agent_artifacts` is a
// real table, indexed, already migrated, and scoped by customer.
//
// # What is unchanged
//
// The byte path and the observable contract are the ones extension/blobartifacts
// shipped, deliberately preserved:
//
//   - blob keys: "_artifacts/bytes/<id>" for a file, "_artifacts/dirs/<id>/…"
//     for the one-blob-per-file layout of a directory artifact;
//   - Load returns metadata and a NIL READER when the bytes are gone, a real
//     error only when the backend itself fails. Callers must check reader != nil;
//   - Load on a directory artifact returns metadata with a nil reader (there is
//     no single stream; callers list BlobPath);
//   - Save dedups on (SessionID, FilePath), never regresses extracted → live,
//     preserves an unsupplied BlobPath, and treats Source as write-once;
//   - MarkLost promotes to extracted anything that already has a BlobPath.
//
// # What is new
//
// Each row is stamped with the session's `customer`, so artifacts are scoped to
// a project like every other product-layer table (§12). List and Load below are
// session-keyed because the ArtifactStore interface is; the project-scoped
// reads live on the store as ListArtifactsForCustomer / GetArtifactForCustomer,
// and the HTTP layer checks session ownership before calling in.
package dbartifacts

import (
	"context"
	"fmt"
	"io"
	"path"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/google/uuid"
)

// newID mints an artifact ID. UUIDs, not the sequential counter the in-process
// store used: that counter restarted at 1 on every boot, so the first artifact
// after a restart reused the blob key of the first artifact before it.
func newID() string { return uuid.New().String() }

// Store implements artifacts.ArtifactStore over a Postgres metadata index and
// any BlobStore for bytes.
type Store struct {
	db    *agentdb.Store
	blobs extension.BlobStore
}

// New returns a metadata-durable ArtifactStore. Both arguments are required;
// hosts without a database keep extension/blobartifacts (see cmd/agentd).
func New(db *agentdb.Store, blobs extension.BlobStore) *Store {
	return &Store{db: db, blobs: blobs}
}

// Compile-time assertion.
var _ artifacts.ArtifactStore = (*Store)(nil)

// blobKey returns the BlobStore key for a file artifact's bytes. Identical to
// extension/blobartifacts so an existing bucket keeps the same layout.
func blobKey(id string) string { return "_artifacts/bytes/" + id }

// dirPrefix returns the BlobStore key prefix for a directory artifact's blobs.
func dirPrefix(id string) string { return "_artifacts/dirs/" + id }

// countingReader counts bytes read, so FileSize is recorded without re-statting
// the backend (which a generic BlobStore cannot do).
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// Save upserts artifact metadata, optionally uploading bytes. When content is
// non-nil the bytes are written FIRST and the row only afterwards: a failed
// upload must not leave a row promising bytes that are not there.
func (s *Store) Save(ctx context.Context, art *artifacts.Artifact, content io.Reader) (*artifacts.Artifact, error) {
	if art == nil {
		return nil, fmt.Errorf("dbartifacts: artifact is required")
	}
	stored := *art // copy so we don't mutate the caller's struct

	// Resolve the ID before touching the BlobStore: the blob key is derived from
	// it, and an upsert must reuse the existing row's ID so a re-save overwrites
	// the same bytes instead of orphaning them.
	prev, err := s.db.GetArtifactByPath(ctx, stored.SessionID, stored.FilePath)
	if err != nil {
		return nil, fmt.Errorf("dbartifacts: Save lookup: %w", err)
	}
	if prev != nil {
		stored.ID = prev.ID
		// Preserve a blob path the caller did not supply BEFORE the byte write,
		// exactly as the in-process store did: the content branches below only
		// fill BlobPath when it is still empty, so doing this later would let a
		// re-save rewrite a caller-chosen path.
		if stored.BlobPath == "" {
			stored.BlobPath = prev.AzureBlobPath
		}
	} else if stored.ID == "" {
		stored.ID = newID()
	}

	if content != nil {
		if stored.IsDir {
			prefix := dirPrefix(stored.ID)
			entries, err := artifacts.WriteTarToBlobs(ctx, content, func(rel string, r io.Reader) error {
				return s.blobs.Write(ctx, prefix+"/"+rel, r)
			})
			if err != nil {
				return nil, fmt.Errorf("dbartifacts: Save dir: %w", err)
			}
			var total int64
			for _, e := range entries {
				total += e.Size
			}
			stored.FileSize = total
			stored.Status = artifacts.StatusExtracted
			if stored.BlobPath == "" {
				stored.BlobPath = prefix
			}
			if stored.Meta == nil {
				stored.Meta = map[string]string{}
			}
			stored.Meta["dirDigest"] = artifacts.DirDigest(entries)
		} else {
			bk := blobKey(stored.ID)
			cr := &countingReader{r: content}
			if err := s.blobs.Write(ctx, bk, cr); err != nil {
				return nil, fmt.Errorf("dbartifacts: Save bytes: %w", err)
			}
			stored.FileSize = cr.n
			stored.Status = artifacts.StatusExtracted
			if stored.BlobPath == "" {
				stored.BlobPath = bk
			}
		}
	}

	row := toRow(&stored)
	row.Customer = s.customerFor(ctx, stored.SessionID)
	saved, err := s.db.SaveArtifactRecord(ctx, row)
	if err != nil {
		return nil, fmt.Errorf("dbartifacts: Save metadata: %w", err)
	}
	return fromRow(saved), nil
}

// Load returns metadata and an open reader for the bytes.
//
// A nil reader with a nil error means "metadata only": the artifact is Lost,
// has no blob path, is a directory, or its bytes have gone missing from the
// backend. That is the shipped contract — callers MUST check reader != nil.
// Only a backend failure is reported as an error.
func (s *Store) Load(ctx context.Context, artifactID string) (*artifacts.Artifact, io.ReadCloser, error) {
	row, err := s.db.GetArtifact(ctx, artifactID)
	if err != nil {
		return nil, nil, fmt.Errorf("dbartifacts: artifact %q not found: %w", artifactID, err)
	}
	out := fromRow(row)

	if out.IsDir {
		// One blob per file under BlobPath (a prefix); there is no single byte
		// stream. Callers materialize a dir by listing BlobStore.List(BlobPath).
		return out, nil, nil
	}
	if out.Status == artifacts.StatusLost || out.BlobPath == "" {
		return out, nil, nil
	}

	bk := blobKey(artifactID)
	exists, err := s.blobs.Exists(ctx, bk)
	if err != nil {
		return nil, nil, fmt.Errorf("dbartifacts: Load exists: %w", err)
	}
	if !exists {
		return out, nil, nil
	}
	rc, err := s.blobs.Read(ctx, bk)
	if err != nil {
		return nil, nil, fmt.Errorf("dbartifacts: Load bytes: %w", err)
	}
	return out, rc, nil
}

// List returns all artifacts for a session. The ArtifactStore interface is
// session-keyed; project scoping is enforced by the caller that holds the
// project claim (httpapi checks session ownership) and, at the table, by
// agentdb.ListArtifactsForCustomer.
func (s *Store) List(ctx context.Context, sessionID string) ([]*artifacts.Artifact, error) {
	// agentdb.ListArtifacts treats "" as "every session"; the ArtifactStore
	// contract does not, and answering a blank session ID with every project's
	// artifacts is exactly the leak this package is scoped against.
	if sessionID == "" {
		return []*artifacts.Artifact{}, nil
	}
	rows, err := s.db.ListArtifacts(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("dbartifacts: List: %w", err)
	}
	out := make([]*artifacts.Artifact, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, nil
}

// ListForCustomer is the project-scoped read: another project's session yields
// an empty list, never a row. Not part of artifacts.ArtifactStore (which has no
// project parameter) — callers that hold a project claim reach for this.
func (s *Store) ListForCustomer(ctx context.Context, customer, sessionID string) ([]*artifacts.Artifact, error) {
	rows, err := s.db.ListArtifactsForCustomer(ctx, customer, sessionID)
	if err != nil {
		return nil, fmt.Errorf("dbartifacts: ListForCustomer: %w", err)
	}
	out := make([]*artifacts.Artifact, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, nil
}

// LoadForCustomer is Load with a project check: another project's artifact
// reads as not-found, the same answer as a nonexistent ID.
func (s *Store) LoadForCustomer(ctx context.Context, customer, artifactID string) (*artifacts.Artifact, io.ReadCloser, error) {
	if _, err := s.db.GetArtifactForCustomer(ctx, customer, artifactID); err != nil {
		return nil, nil, fmt.Errorf("dbartifacts: artifact %q not found: %w", artifactID, err)
	}
	return s.Load(ctx, artifactID)
}

// MarkLost flags still-Live artifacts as Lost — but promotes to Extracted any
// that already have a BlobPath (the bytes are safe even though the instance is
// gone). One UPDATE per branch, in the table, rather than a read-modify-write.
func (s *Store) MarkLost(ctx context.Context, sessionID string) error {
	if err := s.db.MarkArtifactsLost(ctx, sessionID); err != nil {
		return fmt.Errorf("dbartifacts: MarkLost: %w", err)
	}
	return nil
}

// CaptureFolder saves the full content (typically a tar stream) as a single
// artifact with FilePath=name and ArtifactType="folder-capture".
func (s *Store) CaptureFolder(ctx context.Context, sessionID, name string, content io.Reader) (*artifacts.Artifact, error) {
	return s.Save(ctx, &artifacts.Artifact{
		SessionID:    sessionID,
		FilePath:     name,
		ArtifactType: "folder-capture",
		Status:       artifacts.StatusLive,
		Source:       "capture",
	}, content)
}

// customerFor resolves the project that owns a session, so the artifact row
// carries the same tenancy stamp as its session. A session that cannot be read
// yields "" rather than an error: an artifact must not be lost because the
// session row moved, and the row is still reachable session-keyed.
func (s *Store) customerFor(ctx context.Context, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	sess, err := s.db.GetSession(ctx, sessionID)
	if err != nil || sess == nil {
		return ""
	}
	return sess.Customer
}

// toRow maps the portable type onto the table. BlobPath lands in
// `azure_blob_path` — the column has carried the blob path since migration 002
// under a name inherited from the original host; renaming it is a migration
// this change does not need.
func toRow(a *artifacts.Artifact) *agentdb.Artifact {
	row := &agentdb.Artifact{
		ID:            a.ID,
		SessionID:     a.SessionID,
		FilePath:      a.FilePath,
		FileName:      path.Base(a.FilePath),
		FileSize:      a.FileSize,
		MimeType:      a.MimeType,
		Label:         a.Label,
		Description:   a.Description,
		ArtifactType:  a.ArtifactType,
		Source:        a.Source,
		AzureBlobPath: a.BlobPath,
		Status:        string(a.Status),
		IsDir:         a.IsDir,
	}
	if len(a.Meta) > 0 {
		row.Meta = agentdb.JSONMap{}
		for k, v := range a.Meta {
			row.Meta[k] = v
		}
	}
	return row
}

// fromRow maps the table back onto the portable type. Non-string Meta values
// cannot occur through toRow, so a value that is not a string is dropped rather
// than stringified into something a caller would compare against.
func fromRow(r *agentdb.Artifact) *artifacts.Artifact {
	a := &artifacts.Artifact{
		ID:           r.ID,
		SessionID:    r.SessionID,
		FilePath:     r.FilePath,
		ArtifactType: r.ArtifactType,
		Status:       artifacts.Status(r.Status),
		BlobPath:     r.AzureBlobPath,
		Label:        r.Label,
		Description:  r.Description,
		MimeType:     r.MimeType,
		FileSize:     r.FileSize,
		Source:       r.Source,
		IsDir:        r.IsDir,
	}
	if len(r.Meta) > 0 {
		a.Meta = map[string]string{}
		for k, v := range r.Meta {
			if sv, ok := v.(string); ok {
				a.Meta[k] = sv
			}
		}
		if len(a.Meta) == 0 {
			a.Meta = nil
		}
	}
	return a
}
