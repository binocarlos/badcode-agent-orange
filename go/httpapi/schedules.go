package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ── The schedule routes (spec §8.6) ─────────────────────────────────────────
//
//	/agent/schedules        GET (list) POST (create)
//	/agent/schedules/{id}   GET PUT DELETE
//
// Cron is a core primitive, so schedules are ordinary project configuration a
// human edits in the UI and a worker edits with the `schedule_*` tools (§9).
// Both paths land on the same store methods and therefore the same config-log
// records (§15) — there is no "the UI can do things the tools cannot".
//
// Project scoping is the same rule as everywhere else: the project comes from
// the authenticated Identity's Customer claim and never from the request, so a
// row belonging to another project is simply never found.
//
// The scheduler loop that turns these rows into jobs is H1 and lives in agentd;
// these handlers only write the rows it polls.

// ScheduleStore is the slice of agentdb.Store the schedule routes need. Hosts
// may substitute their own; *agentdb.Store satisfies it (asserted below).
type ScheduleStore interface {
	CreateSchedule(ctx context.Context, sch *agentdb.Schedule, cw agentdb.ConfigWrite) (*agentdb.Schedule, error)
	GetSchedule(ctx context.Context, project, id string) (*agentdb.Schedule, error)
	ListSchedules(ctx context.Context, project string) ([]*agentdb.Schedule, error)
	UpdateSchedule(ctx context.Context, sch *agentdb.Schedule, cw agentdb.ConfigWrite) (*agentdb.Schedule, error)
	DeleteSchedule(ctx context.Context, project, id string, cw agentdb.ConfigWrite) error
}

var _ ScheduleStore = (*agentdb.Store)(nil)

// scheduleBody is the wire shape. Enabled is a pointer so an absent field means
// "the default" (true on create, unchanged on update) rather than false.
//
// Rationale is optional here and threaded into the config event (§15.5 requires
// it only on the two prompt writes). It exists so a human edit can carry a
// commit message when the operator has one — an empty one is a fine record of a
// UI tweak, not an omission.
type scheduleBody struct {
	Worker    string `json:"worker"`
	Cron      string `json:"cron"`
	Input     string `json:"input"`
	Enabled   *bool  `json:"enabled"`
	Rationale string `json:"rationale"`
}

// schedules returns the configured store, or writes 501 and returns nil when the
// host wired no database.
func (h *Handlers) schedules(w http.ResponseWriter) ScheduleStore {
	if h.cfg.Schedules == nil {
		http.Error(w, "schedules are not configured on this host", http.StatusNotImplemented)
		return nil
	}
	return h.cfg.Schedules
}

// Schedules serves GET (list) and POST (create) on /agent/schedules.
func (h *Handlers) Schedules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listSchedules(w, r)
	case http.MethodPost:
		h.createSchedule(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Schedule serves GET/PUT/DELETE on /agent/schedules/{id}.
func (h *Handlers) Schedule(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getSchedule(w, r)
	case http.MethodPut, http.MethodPatch:
		h.updateSchedule(w, r)
	case http.MethodDelete:
		h.deleteSchedule(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handlers) listSchedules(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.schedules(w)
	if store == nil {
		return
	}
	list, err := store.ListSchedules(r.Context(), id.Customer)
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"schedules": list})
}

func (h *Handlers) createSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.schedules(w)
	if store == nil {
		return
	}
	if id.Customer == "" {
		http.Error(w, "no project in token", http.StatusForbidden)
		return
	}
	var body scheduleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	sch := agentdb.NewSchedule(id.Customer, strings.TrimSpace(body.Worker), strings.TrimSpace(body.Cron), body.Input)
	if body.Enabled != nil {
		sch.Enabled = *body.Enabled
	}
	// Read-back validation (§9): the caller sees exactly what persisted.
	created, err := store.CreateSchedule(r.Context(), sch, scheduleWrite(body))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, created)
}

func (h *Handlers) getSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.schedules(w)
	if store == nil {
		return
	}
	sch, err := store.GetSchedule(r.Context(), id.Customer, r.PathValue("id"))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	writeJSON(w, sch)
}

func (h *Handlers) updateSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.schedules(w)
	if store == nil {
		return
	}
	// Read through the project filter first: this is what makes a cross-project
	// PUT a 404 instead of a write.
	existing, err := store.GetSchedule(r.Context(), id.Customer, r.PathValue("id"))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	var body scheduleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if v := strings.TrimSpace(body.Worker); v != "" {
		existing.Worker = v
	}
	if v := strings.TrimSpace(body.Cron); v != "" {
		existing.Cron = v
	}
	// Input is the instruction the trigger delivers, and "" is a meaningful new
	// value, so it is only left alone when the field is absent from the body.
	if body.Input != "" {
		existing.Input = body.Input
	}
	if body.Enabled != nil {
		existing.Enabled = *body.Enabled
	}
	updated, err := store.UpdateSchedule(r.Context(), existing, scheduleWrite(body))
	if err != nil {
		writeScheduleErr(w, err)
		return
	}
	writeJSON(w, updated)
}

func (h *Handlers) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.schedules(w)
	if store == nil {
		return
	}
	if err := store.DeleteSchedule(r.Context(), id.Customer, r.PathValue("id"), humanEdit()); err != nil {
		writeScheduleErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"deleted": true})
}

// scheduleWrite is humanEdit() plus the optional rationale from the body: no
// acting worker and no acting session, because who was at the keyboard is the
// login audit's business, not the config log's (§15.2).
func scheduleWrite(body scheduleBody) agentdb.ConfigWrite {
	cw := humanEdit()
	cw.Rationale = strings.TrimSpace(body.Rationale)
	return cw
}

// writeScheduleErr maps store errors onto status codes: missing rows are 404,
// validation failures (including an unparseable cron) are 400.
func writeScheduleErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agentdb.ErrScheduleNotFound):
		http.Error(w, "schedule not found", http.StatusNotFound)
	case errors.Is(err, agentdb.ErrScheduleInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
