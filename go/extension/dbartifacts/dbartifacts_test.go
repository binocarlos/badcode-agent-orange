package dbartifacts

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// memBlobs is an in-memory BlobStore. It stands in for the real bucket: the
// point of every test here is that the blobs SURVIVE while the process does
// not, so this is deliberately shared across "restarts".
type memBlobs struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemBlobs() *memBlobs { return &memBlobs{data: map[string][]byte{}} }

func (m *memBlobs) Write(_ context.Context, key string, r io.Reader) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = b
	return nil
}

func (m *memBlobs) Read(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, fmt.Errorf("no blob %q", key)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (m *memBlobs) Exists(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *memBlobs) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memBlobs) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

// dbFile returns a sqlite path that outlives a single Store, so a test can
// close one Store and open another over the SAME database — the simulated
// agentd restart.
func dbFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "artifacts.sqlite")
}

// openStore opens a Store over the given sqlite file. Calling it twice with the
// same path is the restart: a brand-new process-level object, same durable
// storage. The production schema comes from the numbered Postgres migrations,
// which cannot run on sqlite, so the two tables are AutoMigrated instead.
func openStore(t *testing.T, path string, blobs *memBlobs) *Store {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&agentdb.Artifact{}, &agentdb.Session{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	return New(agentdb.NewStore(gdb), blobs)
}

// seedSession writes the session row an artifact hangs off, so the store can
// resolve the owning project.
func seedSession(t *testing.T, s *Store, id, customer string) {
	t.Helper()
	if err := s.db.DB().Create(&agentdb.Session{
		ID: id, Customer: customer, UserEmail: "a@b.c", WorkflowID: "chat",
	}).Error; err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
}

// ── the bug this package exists to fix ──────────────────────────────────────

// TestMetadataSurvivesRestart is the regression test for the durability bug:
// with the in-process index (extension/blobartifacts) the bytes stayed in the
// bucket and every row vanished on restart. Here the second Store shares only
// the sqlite file and the blob store — no Go state at all — and must see the
// artifact whole.
func TestMetadataSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	path := dbFile(t)

	before := openStore(t, path, blobs)
	seedSession(t, before, "s1", "acme")
	saved, err := before.Save(ctx, &artifacts.Artifact{
		SessionID:    "s1",
		FilePath:     "/workspace/report.md",
		ArtifactType: "file",
		Label:        "Report",
		Description:  "quarterly",
		MimeType:     "text/markdown",
		Status:       artifacts.StatusLive,
		Source:       "tool",
	}, strings.NewReader("hello world"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Status != artifacts.StatusExtracted {
		t.Fatalf("status after save with bytes = %q, want extracted", saved.Status)
	}

	// ---- restart ----
	after := openStore(t, path, blobs)

	list, err := after.List(ctx, "s1")
	if err != nil {
		t.Fatalf("list after restart: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("after restart: got %d artifacts, want 1 — metadata was lost", len(list))
	}
	got := list[0]
	if got.ID != saved.ID || got.Label != "Report" || got.Description != "quarterly" ||
		got.MimeType != "text/markdown" || got.Source != "tool" ||
		got.ArtifactType != "file" || got.FileSize != int64(len("hello world")) ||
		got.Status != artifacts.StatusExtracted || got.BlobPath != saved.BlobPath {
		t.Fatalf("metadata did not round-trip: %+v", got)
	}

	art, rc, err := after.Load(ctx, saved.ID)
	if err != nil {
		t.Fatalf("load after restart: %v", err)
	}
	if rc == nil {
		t.Fatalf("after restart: no reader for %s", saved.ID)
	}
	defer rc.Close() //nolint:errcheck
	b, _ := io.ReadAll(rc)
	if string(b) != "hello world" {
		t.Fatalf("bytes = %q", string(b))
	}
	if art.Label != "Report" {
		t.Fatalf("label = %q, want Report", art.Label)
	}
}

// TestRestartDoesNotReuseBlobKeys pins the second half of the old bug. The
// in-process store minted "art-1", "art-2", … from a counter that restarted at
// 1 on every boot, so the first artifact written after a restart overwrote the
// bytes of the first artifact written before it. IDs are now database-unique.
func TestRestartDoesNotReuseBlobKeys(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	path := dbFile(t)

	before := openStore(t, path, blobs)
	seedSession(t, before, "s1", "acme")
	first, err := before.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "/a.txt", Status: artifacts.StatusLive,
	}, strings.NewReader("first"))
	if err != nil {
		t.Fatalf("save first: %v", err)
	}

	after := openStore(t, path, blobs)
	second, err := after.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "/b.txt", Status: artifacts.StatusLive,
	}, strings.NewReader("second"))
	if err != nil {
		t.Fatalf("save second: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("ID reused across restart: %q", first.ID)
	}

	_, rc, err := after.Load(ctx, first.ID)
	if err != nil || rc == nil {
		t.Fatalf("first artifact unreadable after restart: rc=%v err=%v", rc, err)
	}
	defer rc.Close() //nolint:errcheck
	b, _ := io.ReadAll(rc)
	if string(b) != "first" {
		t.Fatalf("first artifact's bytes were overwritten: %q", string(b))
	}
}

// ── project isolation (§12) ─────────────────────────────────────────────────

// TestProjectIsolation is the negative test §12 requires on every table: one
// project must not be able to list or load another's artifacts.
func TestProjectIsolation(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbFile(t), newMemBlobs())
	seedSession(t, s, "s-acme", "acme")
	seedSession(t, s, "s-globex", "globex")

	acme, err := s.Save(ctx, &artifacts.Artifact{
		SessionID: "s-acme", FilePath: "/secret.txt", Status: artifacts.StatusLive,
	}, strings.NewReader("acme confidential"))
	if err != nil {
		t.Fatalf("save acme: %v", err)
	}
	if _, err := s.Save(ctx, &artifacts.Artifact{
		SessionID: "s-globex", FilePath: "/own.txt", Status: artifacts.StatusLive,
	}, strings.NewReader("globex")); err != nil {
		t.Fatalf("save globex: %v", err)
	}

	// The row carries its project.
	row, err := s.db.GetArtifact(ctx, acme.ID)
	if err != nil {
		t.Fatalf("get row: %v", err)
	}
	if row.Customer != "acme" {
		t.Fatalf("artifact customer = %q, want acme", row.Customer)
	}

	// NEGATIVE: globex asking for acme's session gets nothing — not an error,
	// so it cannot probe for the existence of another project's session ID.
	cross, err := s.ListForCustomer(ctx, "globex", "s-acme")
	if err != nil {
		t.Fatalf("cross-project list: %v", err)
	}
	if len(cross) != 0 {
		t.Fatalf("globex listed %d of acme's artifacts: %+v", len(cross), cross)
	}

	// NEGATIVE: globex asking for acme's artifact by ID gets not-found and no
	// reader — the bytes must not be reachable through a foreign ID either.
	art, rc, err := s.LoadForCustomer(ctx, "globex", acme.ID)
	if err == nil {
		t.Fatalf("globex loaded acme's artifact: %+v", art)
	}
	if rc != nil {
		rc.Close() //nolint:errcheck
		t.Fatal("globex got a reader for acme's bytes")
	}

	// POSITIVE control: acme still sees its own, and globex sees only its own.
	own, err := s.ListForCustomer(ctx, "acme", "s-acme")
	if err != nil || len(own) != 1 {
		t.Fatalf("acme should see its own artifact: %d (%v)", len(own), err)
	}
	mine, err := s.ListForCustomer(ctx, "globex", "")
	if err != nil || len(mine) != 1 || mine[0].FilePath != "/own.txt" {
		t.Fatalf("globex should see exactly its own artifact: %+v (%v)", mine, err)
	}
	if _, _, err := s.LoadForCustomer(ctx, "acme", acme.ID); err != nil {
		t.Fatalf("acme should load its own artifact: %v", err)
	}
}

// ── the shipped contract, unchanged ─────────────────────────────────────────

// TestLoadReturnsNilReaderWhenBytesAreGone pins the contract callers depend on:
// losing the bytes is metadata + NIL READER, never an error. A caller that
// forgets to check reader != nil is the bug this shape is designed to expose,
// and "improving" it into an error would break every such caller silently.
func TestLoadReturnsNilReaderWhenBytesAreGone(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	s := openStore(t, dbFile(t), blobs)
	seedSession(t, s, "s1", "acme")

	saved, err := s.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "/gone.txt", Status: artifacts.StatusLive,
	}, strings.NewReader("bytes"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// The bucket loses the object; the row stays.
	if err := blobs.Delete(ctx, blobKey(saved.ID)); err != nil {
		t.Fatalf("delete blob: %v", err)
	}

	art, rc, err := s.Load(ctx, saved.ID)
	if err != nil {
		t.Fatalf("Load must NOT error when the bytes are gone: %v", err)
	}
	if rc != nil {
		rc.Close() //nolint:errcheck
		t.Fatal("expected a nil reader when the bytes are gone")
	}
	if art == nil || art.ID != saved.ID {
		t.Fatalf("expected metadata back, got %+v", art)
	}
}

// TestLoadMetadataOnlyAndLost covers the other two nil-reader cases: an
// artifact with no blob path at all, and one explicitly Lost.
func TestLoadMetadataOnlyAndLost(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbFile(t), newMemBlobs())
	seedSession(t, s, "s1", "acme")

	meta, err := s.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "/pending.txt", Status: artifacts.StatusLive,
	}, nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if meta.Status != artifacts.StatusLive {
		t.Fatalf("a save with no content must stay live, got %q", meta.Status)
	}
	if _, rc, err := s.Load(ctx, meta.ID); err != nil || rc != nil {
		t.Fatalf("metadata-only Load: rc=%v err=%v", rc, err)
	}

	// MarkLost with no bytes → lost, still metadata + nil reader.
	if err := s.MarkLost(ctx, "s1"); err != nil {
		t.Fatalf("marklost: %v", err)
	}
	art, rc, err := s.Load(ctx, meta.ID)
	if err != nil || rc != nil {
		t.Fatalf("lost Load: rc=%v err=%v", rc, err)
	}
	if art.Status != artifacts.StatusLost {
		t.Fatalf("status = %q, want lost", art.Status)
	}
}

// TestLoadUnknownIDErrors — an ID that was never stored is an error, not a nil
// reader. The two must stay distinguishable.
func TestLoadUnknownIDErrors(t *testing.T) {
	s := openStore(t, dbFile(t), newMemBlobs())
	if _, _, err := s.Load(context.Background(), "nope"); err == nil {
		t.Fatal("expected an error for an unknown artifact ID")
	}
}

// TestListWithEmptySessionIsEmpty — agentdb.ListArtifacts reads "" as "every
// session". Through the ArtifactStore seam that would hand a caller who lost
// track of the session ID every project's artifacts, so it is refused here.
func TestListWithEmptySessionIsEmpty(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbFile(t), newMemBlobs())
	seedSession(t, s, "s1", "acme")
	if _, err := s.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "/a.txt", Status: artifacts.StatusLive,
	}, strings.NewReader("x")); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := s.List(ctx, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a blank session ID listed %d artifacts: %+v", len(got), got)
	}
}

// TestSaveInvariants pins the upsert rules: dedup on (session, path), no
// extracted → live regression, blob path preserved, Source write-once.
func TestSaveInvariants(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbFile(t), newMemBlobs())
	seedSession(t, s, "s1", "acme")

	first, err := s.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "/x.txt", Status: artifacts.StatusLive, Source: "tool",
	}, strings.NewReader("v1"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// A later metadata-only write with Status=live and a different Source.
	second, err := s.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "/x.txt", Status: artifacts.StatusLive,
		Source: "upload", Label: "renamed",
	}, nil)
	if err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("dedup failed: %q != %q", second.ID, first.ID)
	}
	if second.Status != artifacts.StatusExtracted {
		t.Fatalf("status regressed to %q", second.Status)
	}
	if second.BlobPath != first.BlobPath {
		t.Fatalf("blob path lost: %q != %q", second.BlobPath, first.BlobPath)
	}
	if second.Source != "tool" {
		t.Fatalf("Source is write-once: got %q, want tool", second.Source)
	}
	if second.Label != "renamed" {
		t.Fatalf("ordinary fields must update: label = %q", second.Label)
	}

	list, err := s.List(ctx, "s1")
	if err != nil || len(list) != 1 {
		t.Fatalf("expected one row after upsert, got %d (%v)", len(list), err)
	}
}

// TestMarkLostPromotesExtracted — bytes already in the bucket survive the
// instance, so MarkLost promotes rather than loses them.
func TestMarkLostPromotesExtracted(t *testing.T) {
	ctx := context.Background()
	s := openStore(t, dbFile(t), newMemBlobs())
	seedSession(t, s, "s1", "acme")

	withBytes, err := s.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "/kept.txt", Status: artifacts.StatusLive,
	}, strings.NewReader("kept"))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	// Force it back to live so MarkLost has something to decide about.
	if err := s.db.DB().Model(&agentdb.Artifact{}).
		Where("id = ?", withBytes.ID).Update("status", "live").Error; err != nil {
		t.Fatalf("force live: %v", err)
	}
	noBytes, err := s.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "/pending.txt", Status: artifacts.StatusLive,
	}, nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	if err := s.MarkLost(ctx, "s1"); err != nil {
		t.Fatalf("marklost: %v", err)
	}
	kept, _, err := s.Load(ctx, withBytes.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if kept.Status != artifacts.StatusExtracted {
		t.Fatalf("artifact with bytes = %q, want extracted", kept.Status)
	}
	pending, _, err := s.Load(ctx, noBytes.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if pending.Status != artifacts.StatusLost {
		t.Fatalf("artifact without bytes = %q, want lost", pending.Status)
	}
}

// TestDirArtifactSurvivesRestart covers the directory path: one blob per file
// under a prefix, a nil reader from Load, and Meta["dirDigest"] — the only
// field of the portable type with no column of its own, which migration 033
// exists for.
func TestDirArtifactSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	path := dbFile(t)

	before := openStore(t, path, blobs)
	seedSession(t, before, "s1", "acme")
	saved, err := before.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "skills/demo", IsDir: true, Status: artifacts.StatusLive,
	}, tarOf(map[string]string{"SKILL.md": "hello", "sub/x.txt": "world"}))
	if err != nil {
		t.Fatalf("save dir: %v", err)
	}
	if saved.Meta["dirDigest"] == "" {
		t.Fatal("expected dirDigest in Meta")
	}
	if saved.FileSize != int64(len("hello")+len("world")) {
		t.Fatalf("FileSize = %d", saved.FileSize)
	}

	after := openStore(t, path, blobs)
	art, rc, err := after.Load(ctx, saved.ID)
	if err != nil {
		t.Fatalf("load dir after restart: %v", err)
	}
	if rc != nil {
		rc.Close() //nolint:errcheck
		t.Fatal("a directory artifact has no single byte stream — reader must be nil")
	}
	if !art.IsDir {
		t.Fatal("IsDir lost across restart")
	}
	if art.Meta["dirDigest"] != saved.Meta["dirDigest"] {
		t.Fatalf("dirDigest lost across restart: %q != %q", art.Meta["dirDigest"], saved.Meta["dirDigest"])
	}
	keys, err := blobs.List(ctx, art.BlobPath)
	if err != nil || len(keys) != 2 {
		t.Fatalf("expected 2 blobs under %q, got %v (%v)", art.BlobPath, keys, err)
	}
}

// TestCaptureFolderSurvivesRestart — the CaptureFolder degenerate case (a
// single blob named by the caller) is durable too.
func TestCaptureFolderSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	path := dbFile(t)

	before := openStore(t, path, blobs)
	seedSession(t, before, "s1", "acme")
	saved, err := before.CaptureFolder(ctx, "s1", "workspace-files", strings.NewReader("tarbytes"))
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if saved.ArtifactType != "folder-capture" || saved.Source != "capture" {
		t.Fatalf("unexpected capture shape: %+v", saved)
	}

	after := openStore(t, path, blobs)
	got, rc, err := after.Load(ctx, saved.ID)
	if err != nil || rc == nil {
		t.Fatalf("load after restart: rc=%v err=%v", rc, err)
	}
	defer rc.Close() //nolint:errcheck
	if got.FilePath != "workspace-files" {
		t.Fatalf("FilePath = %q", got.FilePath)
	}
	b, _ := io.ReadAll(rc)
	if string(b) != "tarbytes" {
		t.Fatalf("bytes = %q", string(b))
	}
}

// TestSaveDoesNotWriteRowWhenBytesFail — a failed upload must not leave a row
// promising bytes that are not there.
func TestSaveDoesNotWriteRowWhenBytesFail(t *testing.T) {
	ctx := context.Background()
	blobs := newMemBlobs()
	s := openStore(t, dbFile(t), blobs)
	seedSession(t, s, "s1", "acme")

	if _, err := s.Save(ctx, &artifacts.Artifact{
		SessionID: "s1", FilePath: "/boom.txt", Status: artifacts.StatusLive,
	}, errReader{}); err == nil {
		t.Fatal("expected the save to fail")
	}
	list, err := s.List(ctx, "s1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a failed byte write left a row behind: %+v", list)
	}
}

// errReader always fails, standing in for a bucket that rejects an upload.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, fmt.Errorf("upload exploded") }

// tarOf builds a tar stream from name → content.
func tarOf(files map[string]string) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	return &buf
}
