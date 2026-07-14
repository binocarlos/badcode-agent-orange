package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// newRootMux mounts the daemon's cockpit routes over the watchapi surface.
// Go 1.22 pattern precedence lets the specific daemon paths win while
// everything else — /api/tickets, /api/trigger, the web UI — falls through to
// watchapi (which guards itself with the same token).
func newRootMux(d *Daemon, watch http.Handler, token string) *http.ServeMux {
	m := http.NewServeMux()
	m.Handle("GET /api/goal", guard(token, d.handleGetGoal))
	m.Handle("POST /api/goal", guard(token, d.handleSetGoal))
	m.Handle("GET /api/status", guard(token, d.handleStatus))
	m.Handle("POST /api/consultant/review", guard(token, d.handleReview))
	m.Handle("/", watch)
	return m
}

// guard is watchapi's shared-bearer-token convention ("" disables, local dev).
func guard(token string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	})
}

func (d *Daemon) handleGetGoal(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"goal": d.Goal()})
}

func (d *Daemon) handleSetGoal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Goal string `json:"goal"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	rev, err := d.SetGoal(r.Context(), body.Goal)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"goal":     strings.TrimSpace(body.Goal),
		"revision": rev,
		"note":     "picked up on the next tick (or POST /api/trigger to run one now)",
	})
}

func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.Status(r.Context()))
}

func (d *Daemon) handleReview(w http.ResponseWriter, r *http.Request) {
	rev, advised, skipped, err := d.Review(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"advised": advised, "skipped": skipped, "revision": rev,
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
