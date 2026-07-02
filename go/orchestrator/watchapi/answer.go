package watchapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

type answerBody struct {
	Text string `json:"text"`
}

// Answer serves POST /api/tickets/{id}/answer (§10c §E — the escalation resume):
// the operator answers an escalated needs_human ticket and it re-enters the queue
// with the answer in its AttemptNotes. Unlike a reject note, an answer is REQUIRED
// text (400 when empty) — the answer IS the action. Unknown ticket or an invalid
// state (not needs_human, or holding a PendingPost that must be approved/rejected
// instead) surfaces as 404, mirroring reject's shape.
func (h *Handlers) Answer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing ticket id")
		return
	}
	var body answerBody
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if strings.TrimSpace(body.Text) == "" {
		writeErr(w, http.StatusBadRequest, "missing answer text")
		return
	}
	if err := h.cfg.Answerer.Answer(r.Context(), id, body.Text); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "answered"})
}
