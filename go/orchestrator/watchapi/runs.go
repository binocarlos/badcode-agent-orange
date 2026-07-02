package watchapi

import "net/http"

// RunDTO is the JSON projection of a telemetry Run (scope, board_rev, output —
// the "show your work" substrate the story is told from). orchestrator.Run has no
// json tags, so we project to snake_case here.
type RunDTO struct {
	ID        string `json:"id"`
	Scope     string `json:"scope"`
	BoardRev  string `json:"board_rev"`
	Prompt    string `json:"prompt"`
	Output    string `json:"output"`
	TicketID  string `json:"ticket_id"`  // §10c §C: the ticket this run served ("" for exchange-level runs)
	SessionID string `json:"session_id"` // §10c §C: the worker session id ("" for non-worker runs)
	Seq       int    `json:"seq"`
}

// Runs serves GET /api/runs — the append-only run log (contracts §10b E-1:
// ctx+error, so telemetry loss fails loud).
func (h *Handlers) Runs(w http.ResponseWriter, r *http.Request) {
	runs, err := h.cfg.Telemetry.Runs(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]RunDTO, 0, len(runs))
	for _, rn := range runs {
		out = append(out, RunDTO{
			ID: rn.ID, Scope: rn.Scope, BoardRev: rn.BoardRevision,
			Prompt: rn.Prompt, Output: rn.Output,
			TicketID: rn.TicketID, SessionID: rn.SessionID, Seq: rn.Seq,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
