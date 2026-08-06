package httpapi

// Artifact bytes over HTTP (T8 of design/2026-08-06-embeddable-agent-orange.md).
//
//	GET /agent/artifacts/{id}/download
//	GET /agent/sessions/by-name/{name}/artifacts
//	GET /agent/sessions/by-name/{name}/artifacts/file?path=…
//
// ArtifactStore.Load has been implemented in every backend since the beginning
// and called from nothing: the download route was the TODO at the top of
// artifacts_handler.go. Meanwhile web/ has been requesting it from three
// components across seven call sites (ArtifactViewer.tsx:167,208;
// ArtifactPreviewDialog.tsx:103,156; InlineArtifactPreview.tsx:52,89,112), so
// every artifact preview and every download button in the console has been
// answering 404. This file is that route.
//
// The two by-name routes exist for a different caller: an embedding application
// that knows a session by the name it chose (`hypothesis-a`) and wants "the
// current summary.md" without ever holding a uuid. Artifacts dedup on
// (session_id, file_path), so a path IS a stable logical handle — it upserts
// rather than accumulating.

import (
	"context"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
)

// ArtifactPathStore resolves the (session id, file path) dedup key to an
// artifact row — the index behind ?path=. It is a separate seam from
// Config.Artifacts because the artifacts.ArtifactStore interface deliberately
// has no by-path accessor: it is keyed by artifact id, and the path index is a
// property of the durable metadata store.
//
// Note the shape of the tenancy hazard this seam carries: GetArtifactByPath
// takes a session id and has NO customer parameter, so it will happily read any
// project's artifact given the right id. Every caller here therefore resolves a
// session NAME first (resolveSessionByName, which IS project-scoped) and passes
// the id that came back. No handler in this package may pass a session id that
// came from a request.
type ArtifactPathStore interface {
	GetArtifactByPath(ctx context.Context, sessionID, filePath string) (*agentdb.Artifact, error)
}

// The concrete store must always satisfy the seam.
var _ ArtifactPathStore = (*agentdb.Store)(nil)

// DownloadArtifact serves GET /agent/artifacts/{id}/download.
//
// Auth is whatever identify() produced — a project API key, a console JWT, or an
// embed token; the difference is settled by ownsSession, which enforces both the
// project and (for an embed token) the single-session scope.
func (h *Handlers) DownloadArtifact(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if !h.artifactsConfigured(w) {
		return
	}
	h.serveArtifactBytes(w, r, id, r.PathValue("id"), "")
}

// SessionArtifactsByName serves GET /agent/sessions/by-name/{name}/artifacts —
// the by-name twin of the Artifacts list route.
func (h *Handlers) SessionArtifactsByName(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if !h.artifactsConfigured(w) {
		return
	}
	sess, ok := h.resolveSessionByName(w, r, id, r.PathValue("name"))
	if !ok {
		return
	}
	list, err := h.cfg.Artifacts.List(r.Context(), sess.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []*artifacts.Artifact{}
	}
	writeJSON(w, list)
}

// SessionArtifactFileByName serves
// GET /agent/sessions/by-name/{name}/artifacts/file?path=…: the bytes of one
// artifact addressed by the two things an integrator actually chose — the
// session name and the file path.
func (h *Handlers) SessionArtifactFileByName(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	if !h.artifactsConfigured(w) {
		return
	}
	if h.cfg.ArtifactPaths == nil {
		// The sqlite fallback keeps its artifact index in an in-process map
		// (extension/blobartifacts) and has nothing to query by path.
		http.Error(w, "artifact path lookup is not configured on this host", http.StatusNotImplemented)
		return
	}
	sess, ok := h.resolveSessionByName(w, r, id, r.PathValue("name"))
	if !ok {
		return
	}
	// Only ?path is read. A session id in the query is ignored on purpose: see
	// the ArtifactPathStore doc comment.
	want := r.URL.Query().Get("path")
	if strings.TrimSpace(want) == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	var row *agentdb.Artifact
	for _, candidate := range slashVariants(want) {
		found, err := h.cfg.ArtifactPaths.GetArtifactByPath(r.Context(), sess.ID, candidate)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if found != nil {
			row = found
			break
		}
	}
	if row == nil {
		http.Error(w, "artifact not found", http.StatusNotFound)
		return
	}
	// The row came from a lookup already scoped to the resolved session, so
	// wantSession pins the answer rather than re-deriving tenancy: if the store
	// ever hands back a row from elsewhere, this is a 404 and not a leak.
	h.serveArtifactBytes(w, r, id, row.ID, sess.ID)
}

// slashVariants returns the spellings of a stored FilePath that mean the same
// file. Artifacts are written from several places and they disagree about the
// leading slash — the capture path stores "/report.md" while an upload stores
// what the query said — and GetArtifactByPath is an exact match. Rather than
// rewrite the stored rows, ask for both.
func slashVariants(p string) []string {
	if strings.HasPrefix(p, "/") {
		return []string{p, strings.TrimPrefix(p, "/")}
	}
	return []string{p, "/" + p}
}

// serveArtifactBytes is the one place artifact bytes leave this package.
//
// wantSession, when non-empty, is the session the artifact must belong to — the
// by-name routes have already established tenancy by resolving the name, and
// this is the equality check that keeps that guarantee. Empty means "authorize
// whichever session it turns out to belong to", which is the by-id route's job
// and is what ownsSession does.
func (h *Handlers) serveArtifactBytes(w http.ResponseWriter, r *http.Request, id Identity, artifactID, wantSession string) {
	art, rc, err := h.cfg.Artifacts.Load(r.Context(), artifactID)
	if rc != nil {
		defer rc.Close() //nolint:errcheck // read-only reader
	}
	if err != nil || art == nil {
		// Every backend reports an unknown id as an error rather than a nil
		// artifact (extension/dbartifacts/dbartifacts.go:176-180), and the
		// ArtifactStore contract offers no sentinel to tell that apart from a
		// backend fault. 404 is the right answer for the overwhelmingly common
		// case and the required answer for a foreign id, so it wins over
		// reporting a store error that would also be an existence oracle.
		//
		// The message is ownsSession's, byte for byte, and must stay that way:
		// a distinguishable "artifact not found" would tell an embed token that
		// the id it guessed exists but belongs to a sibling session.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if wantSession != "" {
		if art.SessionID != wantSession {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
	} else if !h.ownsSession(w, r, id, art.SessionID) {
		return
	}
	if rc == nil {
		code, msg := artifactUnavailable(art)
		http.Error(w, msg, code)
		return
	}

	ct := art.MimeType
	if ct == "" {
		// Never let the browser guess. An artifact is agent-produced content
		// served from the console's own origin.
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// attachment, not inline, and for a security reason rather than a UX one:
	// an agent can write an artifact containing HTML, and rendering it inline
	// on the console's origin would be scripting with the console's session in
	// reach. Every console caller fetches this into a blob URL anyway
	// (ArtifactViewer.tsx:174), so nothing is lost.
	if name := path.Base(art.FilePath); name != "" && name != "." && name != "/" {
		if cd := mime.FormatMediaType("attachment", map[string]string{"filename": name}); cd != "" {
			w.Header().Set("Content-Disposition", cd)
		} else {
			w.Header().Set("Content-Disposition", "attachment")
		}
	}
	// No Content-Length: FileSize is metadata written by a different call than
	// the bytes, and a stale value would truncate the response or break the
	// connection. Chunked transfer costs nothing at artifact scale.
	if _, err := io.Copy(w, rc); err != nil {
		// The status line is long gone; there is nothing to say to the client
		// that it will not already have noticed as a short read.
		return
	}
}

// artifactUnavailable explains a nil reader. docs/06-artifacts.md is explicit
// that Load reports state without repairing it and returns (metadata, nil, nil)
// for every one of these — "that mapping is a host decision nobody has made in
// this repo". This is the decision.
//
// Order matters: a directory is checked before the live case because retrying a
// directory will never produce a single byte stream, so 202 would be a lie; and
// after lost/extraction_failed, which say something stronger than "this is not
// a file".
func artifactUnavailable(art *artifacts.Artifact) (int, string) {
	switch {
	case art.Status == artifacts.StatusLost:
		// The container was destroyed before the bytes were extracted. The row
		// is all that survives, and no retry will change that.
		return http.StatusGone, "artifact bytes were lost before extraction"
	case art.Status == artifacts.StatusExtractionFailed:
		return http.StatusConflict, "artifact extraction failed; there are no bytes to serve"
	case art.IsDir:
		// BlobPath is a PREFIX here, one blob per file. Listing it is a
		// different request, so say which one.
		return http.StatusConflict, "artifact is a directory; list its session's artifacts at GET /agent/session/{id}/artifacts"
	case art.Status == artifacts.StatusLive:
		// Registered but not yet extracted — the one case where the client
		// should come back. web/ already treats a failure here as transient
		// while status != extracted (InlineArtifactPreview.tsx:96-104).
		return http.StatusAccepted, "artifact is still being extracted; retry once its status is extracted"
	default:
		// Extracted, but the blob is not in the store. The bytes existed and no
		// longer do, which is exactly what 410 means.
		return http.StatusGone, "artifact bytes are no longer available"
	}
}
