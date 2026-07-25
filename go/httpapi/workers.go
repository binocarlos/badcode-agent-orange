package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// WorkersStore is the worker-catalogue seam the CRUD handlers need.
// *agentdb.Store implements it; hosts may substitute their own.
type WorkersStore interface {
	ListWorkers(ctx context.Context, project string) ([]*agentdb.Worker, error)
	GetWorker(ctx context.Context, project, name string) (*agentdb.Worker, error)
	UpsertWorker(ctx context.Context, w *agentdb.Worker) (*agentdb.Worker, error)
	DeleteWorker(ctx context.Context, project, name string) error
}

// workerBody is the PUT payload. PUT is create-or-replace, not patch: an absent
// field takes its default rather than keeping the stored value. MaxInstances and
// Enabled are pointers only because their zero values (0, false) are meaningful
// and would otherwise be indistinguishable from "not supplied".
type workerBody struct {
	Description  string               `json:"description"`
	SystemPrompt string               `json:"system_prompt"`
	MCPConfig    agentdb.JSONMap      `json:"mcp_config"`
	Image        string               `json:"image"`
	MaxInstances *int                 `json:"max_instances"` // nil → 1
	Briefing     agentdb.SelectorList `json:"briefing"`      // nil → NULL
	Enabled      *bool                `json:"enabled"`       // nil → true
}

// workers returns the configured store, or writes 501 and returns nil when the
// host has not wired one (mirrors the optional-Artifacts contract).
func (h *Handlers) workers(w http.ResponseWriter) WorkersStore {
	if h.cfg.Workers == nil {
		http.Error(w, "worker store not configured", http.StatusNotImplemented)
		return nil
	}
	return h.cfg.Workers
}

// ListWorkers returns every worker in the authenticated principal's project.
// Project is taken from the token (Identity.Customer) and never from the
// request — that is the whole tenancy boundary for this route set.
func (h *Handlers) ListWorkers(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.workers(w)
	if store == nil {
		return
	}
	list, err := store.ListWorkers(r.Context(), id.Customer)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"workers": list})
}

// GetWorker returns one worker. A worker in another project is reported as
// not-found, never as forbidden.
func (h *Handlers) GetWorker(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.workers(w)
	if store == nil {
		return
	}
	worker, err := store.GetWorker(r.Context(), id.Customer, r.PathValue("name"))
	if err != nil {
		writeWorkerErr(w, err)
		return
	}
	writeJSON(w, worker)
}

// PutWorker creates or replaces a worker, then echoes the stored row back
// (read-back validation, spec 05-management-tools §9).
func (h *Handlers) PutWorker(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.workers(w)
	if store == nil {
		return
	}
	var body workerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	worker := agentdb.NewWorker(id.Customer, r.PathValue("name"))
	worker.Description = body.Description
	worker.SystemPrompt = body.SystemPrompt
	worker.MCPConfig = body.MCPConfig
	worker.Image = body.Image
	worker.Briefing = body.Briefing
	if body.MaxInstances != nil {
		worker.MaxInstances = *body.MaxInstances
	}
	if body.Enabled != nil {
		worker.Enabled = *body.Enabled
	}

	stored, err := store.UpsertWorker(r.Context(), worker)
	if err != nil {
		writeWorkerErr(w, err)
		return
	}
	writeJSON(w, stored)
}

// DeleteWorker removes a worker from the caller's project.
func (h *Handlers) DeleteWorker(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.workers(w)
	if store == nil {
		return
	}
	if err := store.DeleteWorker(r.Context(), id.Customer, r.PathValue("name")); err != nil {
		writeWorkerErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeWorkerErr maps store errors onto status codes: missing rows are 404,
// validation failures are 400, everything else is 500.
func writeWorkerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agentdb.ErrWorkerNotFound):
		http.Error(w, "worker not found", http.StatusNotFound)
	case errors.Is(err, agentdb.ErrWorkerInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
