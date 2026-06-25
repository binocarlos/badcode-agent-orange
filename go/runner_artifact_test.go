package agentkit

// Task 7.3 hermetic tests: onArtifactRegistered pulls artifact bytes from the
// running instance workspace (via Exec+cat) and persists them via ArtifactStore.Save.

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/bayes-price/agentkit/agentdb"
	"github.com/bayes-price/agentkit/agentkittest"
	"github.com/bayes-price/agentkit/artifacts"
	"github.com/bayes-price/agentkit/events"
	"github.com/bayes-price/agentkit/execenv"
	"github.com/bayes-price/agentkit/imageregistry"
)

// newArtifactTestRunner builds a Runner whose onArtifactRegistered default hook
// is exercised (deps.Events is nil so the runner builds the default pipeline with
// the hook wired).  Returns the runner, the mock execenv, and the mock artifact store.
func newArtifactTestRunner(t *testing.T) (*runnerImpl, *execenv.MockExecutionEnvironment, *artifacts.MockArtifactStore, *agentkittest.MemStore) {
	t.Helper()
	env := execenv.NewMock()
	reg := imageregistry.NewMock()
	store := agentkittest.NewMemStore()
	arts := artifacts.NewMock()
	runner, err := NewRunner(Deps{
		Env:       env,
		Registry:  reg,
		Store:     store,
		Artifacts: arts,
		Claims:    agentkittest.StaticClaims{Token: "test-token"},
		// Events intentionally nil → runner builds default pipeline with onArtifactRegistered wired.
		Policy: Policy{BaseImage: "agentkit-sandbox:test"},
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner.(*runnerImpl), env, arts, store
}

// ---------------------------------------------------------------------------
// Task 7.3: onArtifactRegistered pulls bytes from the instance and saves them
// (TDD: failing first — hook is a no-op stub before implementation).
// ---------------------------------------------------------------------------

func TestOnArtifactRegistered_PullsAndSavesBytes(t *testing.T) {
	ctx := context.Background()
	r, env, arts, store := newArtifactTestRunner(t)

	// Seed a session row so the runner can look it up.
	const sessionID = "art-sess-1"
	store.Seed(&agentdb.Session{ID: sessionID, Customer: "acme", Job: "j1"})

	// Provision a session instance so the runner has it tracked.
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: sessionID,
		Customer:  "acme",
		Job:       "j1",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Seed the execenv mock to return bytes for "cat /workspace/report.html"
	// when the hook exec's into the container to pull the artifact.
	artifactContent := []byte("<html><body>report</body></html>")
	env.ExecStdoutByCmd = map[string][]byte{
		"cat /workspace/report.html": artifactContent,
	}

	// Fire the onArtifactRegistered hook directly (it's package-private but
	// accessible from within package agentkit).
	q := events.QueryContext{SessionID: sessionID, QueryID: "q-art-1"}
	ev := events.Envelope{
		Type: events.ArtifactRegistered,
		Data: map[string]any{
			"filePath":     "/workspace/report.html",
			"label":        "Report",
			"artifactType": "file",
		},
	}
	r.onArtifactRegistered(ctx, q, ev)

	// Assert: ArtifactStore.Save was called with the correct bytes.
	saveCalls := arts.CallsTo("Save")
	if len(saveCalls) == 0 {
		t.Fatal("expected ArtifactStore.Save to be called, got 0 calls")
	}

	// Verify the saved artifact's content round-trips correctly via Load.
	artsListed, err := arts.List(ctx, sessionID)
	if err != nil {
		t.Fatalf("List artifacts: %v", err)
	}
	if len(artsListed) == 0 {
		t.Fatal("expected at least one artifact saved for session, got 0")
	}

	// Check the saved artifact has the right FilePath and the exact bytes.
	var found bool
	for _, a := range artsListed {
		if strings.Contains(a.FilePath, "report.html") {
			found = true
			// Load and verify the byte content matches what the execenv mock returned.
			_, rc, err := arts.Load(ctx, a.ID)
			if err != nil {
				t.Fatalf("Load artifact: %v", err)
			}
			if rc == nil {
				t.Fatal("Load returned nil reader — bytes not saved")
			}
			defer rc.Close()
			got, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("ReadAll from artifact reader: %v", err)
			}
			if !bytes.Equal(got, artifactContent) {
				t.Errorf("artifact bytes = %q, want %q", got, artifactContent)
			}
			break
		}
	}
	if !found {
		t.Errorf("no artifact found with FilePath containing 'report.html'; got: %+v", artsListed)
	}
}

// ---------------------------------------------------------------------------
// Task 7.3 variant: onArtifactRegistered should log+continue when filePath is
// missing from the event (defensive — no panic or error propagation).
// ---------------------------------------------------------------------------

func TestOnArtifactRegistered_MissingFilePathIsDefensive(t *testing.T) {
	ctx := context.Background()
	r, _, arts, store := newArtifactTestRunner(t)

	const sessionID = "art-sess-2"
	store.Seed(&agentdb.Session{ID: sessionID, Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{
		SessionID: sessionID,
		Customer:  "acme",
		Job:       "j1",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Fire hook with empty/missing filePath — must not panic or return an error.
	q := events.QueryContext{SessionID: sessionID, QueryID: "q-art-2"}
	ev := events.Envelope{
		Type: events.ArtifactRegistered,
		Data: map[string]any{
			// filePath deliberately absent.
			"label": "Missing",
		},
	}
	// Should not panic.
	r.onArtifactRegistered(ctx, q, ev)

	// No Save should have been called for the malformed event.
	if n := arts.Count("Save"); n != 0 {
		t.Errorf("expected 0 Save calls for missing filePath, got %d", n)
	}
}

// ---------------------------------------------------------------------------
// Task 7.3 variant: onArtifactRegistered should also succeed when the session
// is not currently tracked (no running instance) — log + skip gracefully.
// ---------------------------------------------------------------------------

func TestOnArtifactRegistered_UntrackedSessionIsDefensive(t *testing.T) {
	ctx := context.Background()
	r, _, arts, _ := newArtifactTestRunner(t)

	// Do NOT create a session — the runner has no tracked instance.
	q := events.QueryContext{SessionID: "nonexistent-session", QueryID: "q-art-3"}
	ev := events.Envelope{
		Type: events.ArtifactRegistered,
		Data: map[string]any{
			"filePath": "/workspace/output.json",
		},
	}
	// Should not panic.
	r.onArtifactRegistered(ctx, q, ev)

	// No Save should have been called.
	if n := arts.Count("Save"); n != 0 {
		t.Errorf("expected 0 Save calls for untracked session, got %d", n)
	}
}

func TestOnArtifactRegistered_WebappCapturesBuildDir(t *testing.T) {
	ctx := context.Background()
	r, env, arts, store := newArtifactTestRunner(t)

	const sessionID = "art-sess-webapp-1"
	store.Seed(&agentdb.Session{ID: sessionID, Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: sessionID, Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// The agent registers the entry point dist/index.html as a webapp. The runner
	// must capture the WHOLE build dir (dist/) so bundled assets are stored too —
	// it tars the parent dir: tar -cf - -C /workspace/dist .
	tarBytes := buildTar(t, map[string]string{
		"./index.html":          "<html>app</html>",
		"./assets/index-abc.js": "console.log(1)",
	})
	env.ExecStdoutByCmd = map[string][]byte{
		"tar -cf - -C /workspace/dist .": tarBytes,
	}

	q := events.QueryContext{SessionID: sessionID, QueryID: "q-webapp-1"}
	ev := events.Envelope{
		Type: events.ArtifactRegistered,
		Data: map[string]any{"filePath": "dist/index.html", "artifactType": "webapp", "label": "My App"},
	}
	r.onArtifactRegistered(ctx, q, ev)

	list, err := arts.List(ctx, sessionID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 webapp artifact, got %d: %+v", len(list), list)
	}
	got := list[0]
	if got.ArtifactType != "webapp" {
		t.Fatalf("artifactType = %q, want webapp", got.ArtifactType)
	}
	if !got.IsDir {
		t.Fatalf("expected webapp captured as a directory artifact, got IsDir=false: %+v", got)
	}
	if got.FilePath != "dist" {
		t.Fatalf("FilePath = %q, want the build dir %q", got.FilePath, "dist")
	}
	if n := len(arts.DirEntries(got.ID)); n != 2 {
		t.Fatalf("expected 2 dir entries (index.html + asset), got %d", n)
	}
}

// buildTar builds an in-memory tar (archive/tar) of relPath->content.
func buildTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestOnArtifactRegistered_DirIsTarredAndCaptured(t *testing.T) {
	ctx := context.Background()
	r, env, arts, store := newArtifactTestRunner(t)

	const sessionID = "art-sess-dir-1"
	store.Seed(&agentdb.Session{ID: sessionID, Customer: "acme", Job: "j1"})
	if _, err := r.CreateSession(ctx, CreateSessionRequest{SessionID: sessionID, Customer: "acme", Job: "j1"}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// The runner tars the dir contents via: tar -cf - -C /workspace/skills/demo .
	tarBytes := buildTar(t, map[string]string{"./SKILL.md": "hello", "./files/x.py": "y"})
	env.ExecStdoutByCmd = map[string][]byte{
		"tar -cf - -C /workspace/skills/demo .": tarBytes,
	}

	q := events.QueryContext{SessionID: sessionID, QueryID: "q-dir-1"}
	ev := events.Envelope{
		Type: events.ArtifactRegistered,
		Data: map[string]any{"filePath": "skills/demo", "isDir": true, "artifactType": "skill"},
	}
	r.onArtifactRegistered(ctx, q, ev)

	list, err := arts.List(ctx, sessionID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 artifact, got %d: %+v", len(list), list)
	}
	if !list[0].IsDir {
		t.Fatalf("expected IsDir artifact, got %+v", list[0])
	}
	if list[0].Status != artifacts.StatusExtracted {
		t.Fatalf("expected extracted, got %s", list[0].Status)
	}
	if n := len(arts.DirEntries(list[0].ID)); n != 2 {
		t.Fatalf("expected 2 dir entries, got %d", n)
	}
}
