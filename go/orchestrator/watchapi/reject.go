package watchapi

import (
	"encoding/json"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

type rejectBody struct {
	Note string `json:"note"`
}

// Reject serves POST /api/tickets/{id}/reject: discards a drafted post via the
// injected Rejecter (Slice-D ApprovalService.Reject clears the PendingPost and
// returns the ticket to Todo for a re-draft — nothing is published). §10c I-7
// ordering: if the human left a note, it is applied through the FeedbackApplier
// BEFORE the reject, so a feedback failure returns 500 with the pending post
// INTACT — the whole action is retryable and the lesson is never lost against an
// already-consumed post. The handler constructs the ticket-targeted HumanFeedback
// itself (same shape ApprovalService.Reject returns). Returns
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
	var revision string
	if body.Note != "" {
		fb := orchestrator.HumanFeedback{TargetRef: "ticket:" + id, Note: body.Note}
		rev, err := h.cfg.Feedback.Apply(r.Context(), fb)
		if err != nil {
			// Pending post untouched → the operator can retry the whole reject.
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		revision = rev
	}
	if _, err := h.cfg.Rejecter.Reject(r.Context(), id, body.Note); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rejected", "revision": revision})
}
