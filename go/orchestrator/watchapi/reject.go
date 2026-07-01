package watchapi

import (
	"encoding/json"
	"net/http"
)

type rejectBody struct {
	Note string `json:"note"`
}

// Reject serves POST /api/tickets/{id}/reject: discards a drafted post via the
// injected Rejecter (Slice-D ApprovalService.Reject clears the PendingPost and
// returns the ticket to Todo for a re-draft — nothing is published). If the human
// left a note, the returned HumanFeedback is applied through the FeedbackApplier
// so the rejection teaches (the teacher's desk). Returns
// {"status":"rejected","revision":<rev-or-empty>}.
func (h *Handlers) Reject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing ticket id")
		return
	}
	var body rejectBody
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // empty/absent body = no note
	}
	fb, err := h.cfg.Rejecter.Reject(r.Context(), id, body.Note)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	var revision string
	if fb.Note != "" {
		rev, err := h.cfg.Feedback.Apply(r.Context(), fb)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		revision = rev
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected", "revision": revision})
}
