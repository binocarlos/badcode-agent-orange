package watchapi

import "net/http"

// Trigger serves POST /api/trigger: fire one ManagerExchange now (else cron drives
// it). It binds to the frozen orchestrator.Triggerer (method Tick). Fire-and-forget
// from the operator's view: 202 Accepted; 500 if dispatch was halted (e.g. spend
// ceiling).
func (h *Handlers) Trigger(w http.ResponseWriter, r *http.Request) {
	if err := h.cfg.Trigger.Tick(r.Context()); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"triggered": true})
}
