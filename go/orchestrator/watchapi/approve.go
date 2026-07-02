package watchapi

import "net/http"

// Approve serves POST /api/tickets/{id}/approve: the single human click that lets
// a drafted post reach the world. It calls the injected Approver (the Slice-D
// approval→publish flow) and returns the published ref. It NEVER touches a
// Connector directly — that is the un-bypassable publish-approval floor. A publish
// failure surfaces as 502 (the channel refused), not a generic 500.
func (h *Handlers) Approve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "missing ticket id")
		return
	}
	ref, err := h.cfg.Approver.Approve(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ref": ref})
}
