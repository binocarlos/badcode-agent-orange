package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/bayes-price/agentkit/artifacts"
)

// artifactsConfigured reports whether an ArtifactStore is wired. When it is nil
// it writes a 501 and returns false; otherwise it returns true and writes nothing.
//
// TODO: an artifact *download* route (GET by artifact ID, backed by
// ArtifactStore.Load) is intentionally deferred — that is why stubArtifacts.Load
// is currently unexercised by these handlers.
func (h *Handlers) artifactsConfigured(w http.ResponseWriter) bool {
	if h.cfg.Artifacts == nil {
		http.Error(w, "artifacts not configured", http.StatusNotImplemented)
		return false
	}
	return true
}

// Artifacts returns the list of artifacts for a session.
func (h *Handlers) Artifacts(w http.ResponseWriter, r *http.Request) {
	_, ok := h.identify(w, r)
	if !ok {
		return
	}
	if !h.artifactsConfigured(w) {
		return
	}
	sid := r.PathValue("id")
	list, err := h.cfg.Artifacts.List(r.Context(), sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []*artifacts.Artifact{}
	}
	writeJSON(w, list)
}

type createArtifactBody struct {
	Label        string `json:"label"`
	Path         string `json:"path"`
	ArtifactType string `json:"type"`
	MimeType     string `json:"mimeType"`
}

// CreateArtifact saves metadata-only artifact for a session (no bytes).
func (h *Handlers) CreateArtifact(w http.ResponseWriter, r *http.Request) {
	_, ok := h.identify(w, r)
	if !ok {
		return
	}
	if !h.artifactsConfigured(w) {
		return
	}
	sid := r.PathValue("id")
	var body createArtifactBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	art := &artifacts.Artifact{
		ID:           newID(),
		SessionID:    sid,
		FilePath:     body.Path,
		Label:        body.Label,
		ArtifactType: body.ArtifactType,
		MimeType:     body.MimeType,
		Source:       "upload",
	}
	saved, err := h.cfg.Artifacts.Save(r.Context(), art, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, saved)
}

// Upload saves an artifact with its content bytes for a session. The request
// body is read directly as the artifact content (binary-safe). Clients should
// pass metadata via query parameters: ?label=...&path=...&type=...
func (h *Handlers) Upload(w http.ResponseWriter, r *http.Request) {
	_, ok := h.identify(w, r)
	if !ok {
		return
	}
	if !h.artifactsConfigured(w) {
		return
	}
	sid := r.PathValue("id")
	q := r.URL.Query()
	art := &artifacts.Artifact{
		ID:           newID(),
		SessionID:    sid,
		FilePath:     q.Get("path"),
		Label:        q.Get("label"),
		ArtifactType: q.Get("type"),
		MimeType:     r.Header.Get("Content-Type"),
		Source:       "upload",
	}
	// Save must consume the reader synchronously: net/http closes r.Body once this
	// handler returns, so the store cannot defer reading the bytes.
	saved, err := h.cfg.Artifacts.Save(r.Context(), art, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, saved)
}
