package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
)

// --- fixtures -----------------------------------------------------------------

// hostedArtifact is an ArtifactStore that serves exactly one artifact. An empty
// body is how every "no bytes" case arrives: Load returns metadata and a NIL
// reader, never an error (docs/06-artifacts.md, "Download"). An unknown id, by
// contrast, IS an error in both shipped backends
// (extension/dbartifacts/dbartifacts.go:176-180), so the stub errors too.
func hostedArtifact(art *artifacts.Artifact, body string) *stubArtifacts {
	return &stubArtifacts{
		loadFn: func(_ context.Context, id string) (*artifacts.Artifact, io.ReadCloser, error) {
			if id != art.ID {
				return nil, nil, fmt.Errorf("artifact %q not found", id)
			}
			if body == "" {
				return art, nil, nil
			}
			return art, io.NopCloser(strings.NewReader(body)), nil
		},
		listFn: func(_ context.Context, sid string) ([]*artifacts.Artifact, error) {
			if sid != art.SessionID {
				return nil, nil
			}
			return []*artifacts.Artifact{art}, nil
		},
	}
}

// stubArtifactPaths is the (session, path) index the by-name file route reads.
// It records every lookup so a test can prove WHICH session id was asked for —
// the whole tenancy of that route is that the id came from the resolved name
// and not from the request.
type stubArtifactPaths struct {
	rows  map[string]*agentdb.Artifact // key: sessionID + "\x00" + filePath
	calls []string
}

func (s *stubArtifactPaths) GetArtifactByPath(_ context.Context, sessionID, filePath string) (*agentdb.Artifact, error) {
	s.calls = append(s.calls, sessionID+"|"+filePath)
	// (nil, nil) for a miss, exactly as agentdb.Store does — a missing row is
	// not an error there (agentdb/artifacts_durable.go:35-50).
	return s.rows[sessionID+"\x00"+filePath], nil
}

func getDownload(h *Handlers, artifactID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/agent/artifacts/"+artifactID+"/download", nil)
	req.SetPathValue("id", artifactID)
	rec := httptest.NewRecorder()
	h.DownloadArtifact(rec, req)
	return rec
}

// ownedBy is a RunnerStore whose every session belongs to one project — the
// cheapest way to make ownsSession say yes or no.
func ownedBy(customer string) stubStore {
	return stubStore{getSessionFn: func(_ context.Context, id string) (*agentdb.Session, error) {
		return &agentdb.Session{ID: id, Customer: customer}, nil
	}}
}

// --- GET /agent/artifacts/{id}/download ----------------------------------------

func TestDownloadArtifactServesBytes(t *testing.T) {
	art := &artifacts.Artifact{
		ID: "a1", SessionID: "s-acme", FilePath: "/reports/summary.md",
		Status: artifacts.StatusExtracted, MimeType: "text/markdown", FileSize: 5,
	}
	h := newHandlers(t, Config{
		Runner:    stubRunner{},
		Store:     ownedBy("acme"),
		Artifacts: hostedArtifact(art, "hello"),
		Identity:  okIdentity, // Customer: "acme"
	})

	rec := getDownload(h, "a1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	if rec.Body.String() != "hello" {
		t.Fatalf("body = %q, want %q", rec.Body, "hello")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/markdown" {
		t.Fatalf("Content-Type = %q, want the artifact's MimeType", ct)
	}
	// The filename is the BASE of FilePath: the portable artifacts.Artifact has
	// no FileName field (only the agentdb row does), and a client that saved
	// "reports/summary.md" as a filename would write a path.
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "summary.md") || strings.Contains(cd, "reports") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	// Agent-produced bytes are served from the console's own origin, so the
	// browser must not be allowed to guess a type we did not send.
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing X-Content-Type-Options: nosniff")
	}
}

// A MIME type nobody recorded must not become one the browser invents.
func TestDownloadArtifactFallsBackToOctetStream(t *testing.T) {
	art := &artifacts.Artifact{ID: "a1", SessionID: "s-acme", FilePath: "blob.bin", Status: artifacts.StatusExtracted}
	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: ownedBy("acme"),
		Artifacts: hostedArtifact(art, "\x00\x01"), Identity: okIdentity,
	})
	rec := getDownload(h, "a1")
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", ct)
	}
}

// The nil-reader cases of docs/06-artifacts.md, each mapped to the status that
// says WHY there are no bytes. They are all "200 with an empty body" without
// this mapping, which is the worst possible answer: a client caches nothing and
// learns nothing.
func TestDownloadArtifactMapsEveryNilReaderCase(t *testing.T) {
	cases := []struct {
		name string
		art  artifacts.Artifact
		want int
	}{
		{"lost", artifacts.Artifact{Status: artifacts.StatusLost}, http.StatusGone},
		{"extraction failed", artifacts.Artifact{Status: artifacts.StatusExtractionFailed}, http.StatusConflict},
		{"directory", artifacts.Artifact{Status: artifacts.StatusExtracted, IsDir: true}, http.StatusConflict},
		{"still live", artifacts.Artifact{Status: artifacts.StatusLive}, http.StatusAccepted},
		// Extracted, but the blob is gone from the backend: the bytes existed
		// and no longer do, which is what 410 means.
		{"bytes vanished", artifacts.Artifact{Status: artifacts.StatusExtracted, BlobPath: "b/1"}, http.StatusGone},
		// A live directory is still a directory: retrying will never produce a
		// single byte stream, so 202 ("come back later") would be a lie.
		{"live directory", artifacts.Artifact{Status: artifacts.StatusLive, IsDir: true}, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			art := tc.art
			art.ID, art.SessionID, art.FilePath = "a1", "s-acme", "out"
			h := newHandlers(t, Config{
				Runner: stubRunner{}, Store: ownedBy("acme"),
				Artifacts: hostedArtifact(&art, ""), Identity: okIdentity,
			})
			rec := getDownload(h, "a1")
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body)
			}
		})
	}
}

// The directory refusal has to tell the caller where the contents ARE, or the
// 409 is just a dead end.
func TestDownloadDirectoryArtifactPointsAtTheListRoute(t *testing.T) {
	art := &artifacts.Artifact{ID: "a1", SessionID: "s-acme", FilePath: "site", IsDir: true, Status: artifacts.StatusExtracted}
	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: ownedBy("acme"),
		Artifacts: hostedArtifact(art, ""), Identity: okIdentity,
	})
	if body := getDownload(h, "a1").Body.String(); !strings.Contains(body, "/artifacts") {
		t.Fatalf("the directory 409 does not name the list route: %q", body)
	}
}

// §12 at the HTTP edge: the ArtifactStore is session-keyed and takes no project,
// so the gate is ownsSession on the session the artifact turns out to belong to.
func TestDownloadArtifactIsProjectScoped(t *testing.T) {
	art := &artifacts.Artifact{
		ID: "a1", SessionID: "s-globex", FilePath: "secret.md",
		Status: artifacts.StatusExtracted, MimeType: "text/markdown",
	}
	h := newHandlers(t, Config{
		Runner:    stubRunner{},
		Store:     ownedBy("globex"), // the session belongs to globex...
		Artifacts: hostedArtifact(art, "the secret"),
		Identity:  okIdentity, // ...and the token says acme
	})

	rec := getDownload(h, "a1")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "the secret") {
		t.Fatal("another project's artifact bytes were written to the response")
	}
}

// An embed token may download the artifacts of the one session it was minted
// for, and nothing else in the project.
func TestDownloadArtifactHonoursTheSessionScope(t *testing.T) {
	scoped := func(*http.Request) (Identity, error) {
		return Identity{UserEmail: "embed", Customer: "acme", SessionScope: "s-mine"}, nil
	}
	mine := &artifacts.Artifact{ID: "a-mine", SessionID: "s-mine", FilePath: "ok.md", Status: artifacts.StatusExtracted}
	other := &artifacts.Artifact{ID: "a-other", SessionID: "s-other", FilePath: "no.md", Status: artifacts.StatusExtracted}

	store := &stubArtifacts{loadFn: func(_ context.Context, id string) (*artifacts.Artifact, io.ReadCloser, error) {
		for _, a := range []*artifacts.Artifact{mine, other} {
			if a.ID == id {
				return a, io.NopCloser(strings.NewReader("bytes")), nil
			}
		}
		return nil, nil, fmt.Errorf("no such artifact %q", id)
	}}
	h := newHandlers(t, Config{Runner: stubRunner{}, Store: ownedBy("acme"), Artifacts: store, Identity: scoped})

	if rec := getDownload(h, "a-mine"); rec.Code != http.StatusOK {
		t.Fatalf("the embed token was refused its own artifact: %d %s", rec.Code, rec.Body)
	}
	sibling := getDownload(h, "a-other")
	absent := getDownload(h, "a-nope")
	if sibling.Code != http.StatusNotFound {
		t.Fatalf("a scoped credential downloaded a sibling session's artifact: %d", sibling.Code)
	}
	if sibling.Body.String() != absent.Body.String() {
		t.Fatalf("a scoped credential can tell a sibling (%q) from an absent artifact (%q)", sibling.Body, absent.Body)
	}
}

func TestDownloadUnknownArtifactIsNotFound(t *testing.T) {
	art := &artifacts.Artifact{ID: "a1", SessionID: "s-acme", FilePath: "x", Status: artifacts.StatusExtracted}
	h := newHandlers(t, Config{
		Runner: stubRunner{}, Store: ownedBy("acme"),
		Artifacts: hostedArtifact(art, "bytes"), Identity: okIdentity,
	})
	if rec := getDownload(h, "nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
}

func TestDownloadArtifactWithoutAStoreIsNotImplemented(t *testing.T) {
	h := newHandlers(t, Config{Runner: stubRunner{}, Store: ownedBy("acme"), Identity: okIdentity})
	if rec := getDownload(h, "a1"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", rec.Code, rec.Body)
	}
}

// --- the by-name artifact routes ------------------------------------------------

// artifactNamingHandlers wires the by-name stack: one recordingStore as both the
// RunnerStore and the SessionNameStore (the shape agentd has, where they are the
// same *agentdb.Store), plus the artifact seams.
func artifactNamingHandlers(t *testing.T, ident IdentityFunc, arts artifacts.ArtifactStore, paths ArtifactPathStore) (*Handlers, *recordingStore) {
	t.Helper()
	store := newRecordingStore()
	h := newHandlers(t, Config{
		Runner:        stubRunner{},
		Store:         store,
		SessionNames:  store,
		Artifacts:     arts,
		ArtifactPaths: paths,
		Identity:      ident,
	})
	return h, store
}

func getByNameArtifacts(h *Handlers, name string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/agent/sessions/by-name/"+name+"/artifacts", nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	h.SessionArtifactsByName(rec, req)
	return rec
}

func getByNameFile(h *Handlers, name, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/agent/sessions/by-name/"+name+"/artifacts/file?"+query, nil)
	req.SetPathValue("name", name)
	rec := httptest.NewRecorder()
	h.SessionArtifactFileByName(rec, req)
	return rec
}

func TestSessionArtifactsByNameListsThem(t *testing.T) {
	art := &artifacts.Artifact{ID: "a1", SessionID: "s-1", FilePath: "summary.md", Status: artifacts.StatusExtracted}
	h, store := artifactNamingHandlers(t, okIdentity, hostedArtifact(art, "hello"), nil)
	store.rows["s-1"] = &agentdb.Session{ID: "s-1", Customer: "acme", Name: "hypothesis-a"}

	rec := getByNameArtifacts(h, "hypothesis-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0]["id"] != "a1" {
		t.Fatalf("body = %v", out)
	}
}

// The whole tenancy of the by-name routes is the name resolution: a foreign
// name must never reach the artifact store at all.
func TestSessionArtifactsByNameDoesNotCrossProjects(t *testing.T) {
	var reached bool
	arts := &stubArtifacts{listFn: func(context.Context, string) ([]*artifacts.Artifact, error) {
		reached = true
		return []*artifacts.Artifact{{ID: "secret"}}, nil
	}}
	h, store := artifactNamingHandlers(t, okIdentity, arts, nil) // Customer: "acme"
	store.rows["s-globex"] = &agentdb.Session{ID: "s-globex", Customer: "globex", Name: "hypothesis-a"}

	foreign := getByNameArtifacts(h, "hypothesis-a")
	absent := getByNameArtifacts(h, "no-such-name")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", foreign.Code, foreign.Body)
	}
	if absent.Body.String() != foreign.Body.String() {
		t.Fatalf("absent (%q) is distinguishable from foreign (%q)", absent.Body, foreign.Body)
	}
	if reached {
		t.Fatal("the artifact store was reached for another project's session")
	}
}

// Stored paths carry a leading slash sometimes and not others (the capture path
// and the upload route disagree), so both spellings have to resolve — an
// integrator holding the name "summary.md" cannot be expected to know which.
func TestSessionArtifactFileByNameNormalizesTheLeadingSlash(t *testing.T) {
	for _, tc := range []struct{ stored, asked string }{
		{"/summary.md", "summary.md"},
		{"summary.md", "/summary.md"},
		{"summary.md", "summary.md"},
		{"/summary.md", "/summary.md"},
	} {
		t.Run(tc.stored+" as "+tc.asked, func(t *testing.T) {
			art := &artifacts.Artifact{
				ID: "a1", SessionID: "s-1", FilePath: tc.stored,
				Status: artifacts.StatusExtracted, MimeType: "text/markdown",
			}
			paths := &stubArtifactPaths{rows: map[string]*agentdb.Artifact{
				"s-1\x00" + tc.stored: {ID: "a1", SessionID: "s-1", FilePath: tc.stored},
			}}
			h, store := artifactNamingHandlers(t, okIdentity, hostedArtifact(art, "the summary"), paths)
			store.rows["s-1"] = &agentdb.Session{ID: "s-1", Customer: "acme", Name: "hypothesis-a"}

			rec := getByNameFile(h, "hypothesis-a", "path="+tc.asked)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
			}
			if rec.Body.String() != "the summary" {
				t.Fatalf("body = %q", rec.Body)
			}
		})
	}
}

// GetArtifactByPath takes a session ID and has NO customer parameter, so a
// session id accepted from the request would be a project-wide read of anyone's
// artifacts. The only id that may reach it is the one the name resolved to.
func TestSessionArtifactFileByNameIgnoresASessionIDInTheQuery(t *testing.T) {
	paths := &stubArtifactPaths{rows: map[string]*agentdb.Artifact{
		"s-globex\x00secret.md": {ID: "a-globex", SessionID: "s-globex", FilePath: "secret.md"},
	}}
	art := &artifacts.Artifact{ID: "a-globex", SessionID: "s-globex", FilePath: "secret.md", Status: artifacts.StatusExtracted}
	h, store := artifactNamingHandlers(t, okIdentity, hostedArtifact(art, "the secret"), paths)
	store.rows["s-1"] = &agentdb.Session{ID: "s-1", Customer: "acme", Name: "hypothesis-a"}

	rec := getByNameFile(h, "hypothesis-a", "path=secret.md&session=s-globex&session_id=s-globex&id=s-globex")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
	for _, call := range paths.calls {
		if !strings.HasPrefix(call, "s-1|") {
			t.Fatalf("the path lookup used a session id from the request: %q", call)
		}
	}
}

func TestSessionArtifactFileByNameRequiresAPath(t *testing.T) {
	h, store := artifactNamingHandlers(t, okIdentity, &stubArtifacts{}, &stubArtifactPaths{})
	store.rows["s-1"] = &agentdb.Session{ID: "s-1", Customer: "acme", Name: "hypothesis-a"}
	if rec := getByNameFile(h, "hypothesis-a", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body)
	}
}

func TestSessionArtifactFileByNameUnknownPathIsNotFound(t *testing.T) {
	h, store := artifactNamingHandlers(t, okIdentity, &stubArtifacts{}, &stubArtifactPaths{})
	store.rows["s-1"] = &agentdb.Session{ID: "s-1", Customer: "acme", Name: "hypothesis-a"}
	if rec := getByNameFile(h, "hypothesis-a", "path=nope.md"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body)
	}
}

// The sqlite fallback has no artifact index to query by path.
func TestSessionArtifactFileByNameWithoutTheIndexIsNotImplemented(t *testing.T) {
	h, store := artifactNamingHandlers(t, okIdentity, &stubArtifacts{}, nil)
	store.rows["s-1"] = &agentdb.Session{ID: "s-1", Customer: "acme", Name: "hypothesis-a"}
	if rec := getByNameFile(h, "hypothesis-a", "path=summary.md"); rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (body %s)", rec.Code, rec.Body)
	}
}

// Three routes that must be mounted, not merely written — the console has been
// calling the download one across seven call sites into a 404.
func TestArtifactDownloadRoutesAreRegistered(t *testing.T) {
	art := &artifacts.Artifact{
		ID: "a1", SessionID: "s-1", FilePath: "summary.md",
		Status: artifacts.StatusExtracted, MimeType: "text/markdown",
	}
	paths := &stubArtifactPaths{rows: map[string]*agentdb.Artifact{
		"s-1\x00summary.md": {ID: "a1", SessionID: "s-1", FilePath: "summary.md"},
	}}
	h, store := artifactNamingHandlers(t, okIdentity, hostedArtifact(art, "hello"), paths)
	store.rows["s-1"] = &agentdb.Session{ID: "s-1", Customer: "acme", Name: "hypothesis-a"}

	for _, url := range []string{
		"/agent/artifacts/a1/download",
		"/agent/sessions/by-name/hypothesis-a/artifacts",
		"/agent/sessions/by-name/hypothesis-a/artifacts/file?path=summary.md",
	} {
		rec := httptest.NewRecorder()
		h.Mux().ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (body %s)", url, rec.Code, rec.Body)
		}
	}
	// And the plain by-name lookup still resolves alongside its two new
	// children — the patterns must not have shadowed each other.
	rec := httptest.NewRecorder()
	h.Mux().ServeHTTP(rec, httptest.NewRequest("GET", "/agent/sessions/by-name/hypothesis-a", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the by-name lookup broke: %d %s", rec.Code, rec.Body)
	}
}
