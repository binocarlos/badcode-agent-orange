package watchapi

import (
	"encoding/json"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/orchestrator"
)

// Feedback serves POST /api/feedback: a targeted note that becomes a
// write_fragment board revision (the learning loop). target_ref is
// "ticket:<id>" | "run:<id>" | "fragment:<id>"; both fields are required. The
// ref→fragment resolution is the applier's concern (Slice C). The reject-note is
// the same call from a different trigger.
func (h *Handlers) Feedback(w http.ResponseWriter, r *http.Request) {
	// The frozen orchestrator.HumanFeedback carries no json tags, so we bind the
	// snake_case §8 wire shape via a local request struct.
	var body struct {
		TargetRef string `json:"target_ref"`
		Note      string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if body.TargetRef == "" || body.Note == "" {
		writeErr(w, http.StatusBadRequest, "target_ref and note are required")
		return
	}
	fb := orchestrator.HumanFeedback{TargetRef: body.TargetRef, Note: body.Note}
	rev, err := h.cfg.Feedback.Apply(r.Context(), fb)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"revision": rev})
}
