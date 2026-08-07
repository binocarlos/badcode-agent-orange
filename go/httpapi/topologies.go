package httpapi

// topologies.go — T2's HTTP surface over the topology library (go/topology)
// and the transactional apply (agentdb.ApplyTopology). Three routes, all on
// the JWT path, project always from the token's Customer claim (P5):
//
//	GET  /agent/topologies          — the built-in catalogue (D1)
//	POST /agent/topologies/preview  — render + diff + preconditions; writes NOTHING
//	POST /agent/topologies/apply    — the same computation, then the atomic apply
//
// Preview and apply share one computation (previewTopology), so what apply
// refuses is exactly what preview flagged. The handler-side check is advisory
// — ApplyTopology re-checks inside its transaction and its sentinels map to
// the same 409 — which closes the race between a preview and a concurrent
// mutation without ever trusting the client.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/topology"
)

// TopologyStore is the store seam the topology routes need: the reads that
// power the preview diff, and the ONE write — the atomic apply, which routes
// every row through the existing config-logged mutations. *agentdb.Store
// implements it; New() fills it from AgentDB, and without one the routes
// answer 501 like every other product-layer seam.
type TopologyStore interface {
	ListWorkers(ctx context.Context, project string) ([]*agentdb.Worker, error)
	GetProjectSettings(ctx context.Context, project string) (*agentdb.ProjectSettings, error)
	ResolveCustomImage(ctx context.Context, project, ref string) (*agentdb.CustomImage, error)
	GetProjectSkill(ctx context.Context, project, name string) (*agentdb.Skill, error)
	ApplyTopology(ctx context.Context, app agentdb.TopologyApplication, cw agentdb.ConfigWrite) (*agentdb.TopologyApplyResult, error)
}

// topologies returns the configured store, or writes 501 and returns nil
// (mirrors the workers/schedules contract).
func (h *Handlers) topologies(w http.ResponseWriter) TopologyStore {
	if h.cfg.Topologies == nil {
		http.Error(w, "topology store not configured", http.StatusNotImplemented)
		return nil
	}
	return h.cfg.Topologies
}

// topologyBody is the preview/apply payload: which topology, and the answers.
type topologyBody struct {
	Name    string         `json:"name"`
	Version string         `json:"version"`
	Answers map[string]any `json:"answers"`
	// Rationale is the operator's why, threaded like every other human edit
	// (E3). Absent is fine: ApplyTopology defaults it to "seeded from
	// name@version", which is the honest reason for a seeded row anyway.
	Rationale string `json:"rationale,omitempty"`
}

// topologyRouteSummary names one would-be subscription in the diff.
type topologyRouteSummary struct {
	EventType string `json:"event_type"`
	Worker    string `json:"worker"`
}

// topologyCronSummary names one would-be schedule in the diff.
type topologyCronSummary struct {
	Cron   string `json:"cron"`
	Worker string `json:"worker"`
	Input  string `json:"input"`
}

// topologyDiff is the preview against the project's CURRENT config: what would
// be created, and which worker names are already taken. Subscriptions and
// schedules are id-keyed rows, so they are always new; workers are the one
// name-keyed kind and the one that can collide.
type topologyDiff struct {
	NewWorkers       []string               `json:"new_workers"`
	CollidingWorkers []string               `json:"colliding_workers"`
	NewSubscriptions []topologyRouteSummary `json:"new_subscriptions"`
	NewSchedules     []topologyCronSummary  `json:"new_schedules"`
	// SettingsFields are the project-settings fields the bundle's patch would
	// set — computed by the SAME overlay apply performs (TopologySettingsOverlay).
	SettingsFields []string `json:"settings_fields"`
	MemorySeeds    int      `json:"memory_seeds"`
}

// topologyPreview is the whole preview response. Applicable is the one-word
// verdict: no collisions and no missing preconditions.
type topologyPreview struct {
	Topology      *topology.Topology `json:"topology"`
	Bundle        *topology.Bundle   `json:"bundle"`
	Diff          topologyDiff       `json:"diff"`
	MissingImages []string           `json:"missing_images"`
	MissingSkills []string           `json:"missing_skills"`
	Applicable    bool               `json:"applicable"`
}

// ListTopologies returns the built-in catalogue: name, version, description
// and the question list — everything a UI needs to interview the operator.
func (h *Handlers) ListTopologies(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.identify(w, r); !ok {
		return
	}
	writeJSON(w, map[string]any{"topologies": topology.List()})
}

// previewTopology is the shared computation. It performs reads only — the
// property PreviewTopology's tests pin — and every error it returns is already
// mapped to a status code.
func (h *Handlers) previewTopology(ctx context.Context, store TopologyStore, project string, body topologyBody) (*topologyPreview, *topology.Topology, *topology.Bundle, int, error) {
	t, ok := topology.Get(body.Name, body.Version)
	if !ok {
		return nil, nil, nil, http.StatusNotFound, errors.New("topology not found")
	}
	bundle, err := t.Instantiate(topology.Answers(body.Answers))
	if err != nil {
		// Both wraps are the caller's answers being wrong (400 by the topology
		// package's contract); anything else would be a registry bug.
		if errors.Is(err, topology.ErrBadAnswers) || errors.Is(err, topology.ErrRender) {
			return nil, nil, nil, http.StatusBadRequest, err
		}
		return nil, nil, nil, http.StatusInternalServerError, err
	}

	existing, err := store.ListWorkers(ctx, project)
	if err != nil {
		return nil, nil, nil, http.StatusInternalServerError, err
	}
	taken := make(map[string]bool, len(existing))
	for _, w := range existing {
		taken[w.Name] = true
	}

	diff := topologyDiff{
		NewWorkers:       []string{},
		CollidingWorkers: []string{},
		NewSubscriptions: []topologyRouteSummary{},
		NewSchedules:     []topologyCronSummary{},
		SettingsFields:   []string{},
		MemorySeeds:      len(bundle.MemorySeeds),
	}
	for _, bw := range bundle.Workers {
		if taken[bw.Name] {
			diff.CollidingWorkers = append(diff.CollidingWorkers, bw.Name)
		} else {
			diff.NewWorkers = append(diff.NewWorkers, bw.Name)
		}
	}
	for _, sub := range bundle.Subscriptions {
		diff.NewSubscriptions = append(diff.NewSubscriptions, topologyRouteSummary{
			EventType: sub.EventType, Worker: sub.Worker,
		})
	}
	for _, sch := range bundle.Schedules {
		diff.NewSchedules = append(diff.NewSchedules, topologyCronSummary{
			Cron: sch.Cron, Worker: sch.Worker, Input: sch.Input,
		})
	}
	if bundle.SettingsPatch != nil {
		current, err := store.GetProjectSettings(ctx, project)
		if err != nil {
			return nil, nil, nil, http.StatusInternalServerError, err
		}
		_, diff.SettingsFields = agentdb.TopologySettingsOverlay(current, bundle.SettingsPatch)
	}

	missingImages := []string{}
	for _, ref := range bundle.Preconditions.Images {
		if _, err := store.ResolveCustomImage(ctx, project, ref); err != nil {
			if errors.Is(err, agentdb.ErrCustomImageNotFound) || errors.Is(err, agentdb.ErrCustomImageInvalid) {
				missingImages = append(missingImages, ref)
				continue
			}
			return nil, nil, nil, http.StatusInternalServerError, err
		}
	}
	missingSkills := []string{}
	for _, name := range bundle.Preconditions.Skills {
		if _, err := store.GetProjectSkill(ctx, project, name); err != nil {
			if errors.Is(err, agentdb.ErrSkillNotFound) || errors.Is(err, agentdb.ErrSkillInvalid) {
				missingSkills = append(missingSkills, name)
				continue
			}
			return nil, nil, nil, http.StatusInternalServerError, err
		}
	}

	preview := &topologyPreview{
		Topology:      t,
		Bundle:        bundle,
		Diff:          diff,
		MissingImages: missingImages,
		MissingSkills: missingSkills,
		Applicable:    len(diff.CollidingWorkers) == 0 && len(missingImages) == 0 && len(missingSkills) == 0,
	}
	return preview, t, bundle, 0, nil
}

// PreviewTopology renders the bundle for {name, version, answers} and answers
// with the diff and the unmet preconditions. It writes NOTHING — it is the
// look-before-you-leap half of apply.
func (h *Handlers) PreviewTopology(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.topologies(w)
	if store == nil {
		return
	}
	var body topologyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	preview, _, _, status, err := h.previewTopology(r.Context(), store, id.Customer, body)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, preview)
}

// ApplyTopologyHandler runs the same computation as preview and, when the
// verdict is applicable, performs the atomic apply. A collision or an unmet
// precondition is a 409 that changes nothing — refused here when the preview
// already shows it, and refused inside the store's transaction when a
// concurrent mutation created it after this check.
func (h *Handlers) ApplyTopologyHandler(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.topologies(w)
	if store == nil {
		return
	}
	var body topologyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	preview, t, bundle, status, err := h.previewTopology(r.Context(), store, id.Customer, body)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	if !preview.Applicable {
		refusal := "topology not applicable:"
		for _, name := range preview.Diff.CollidingWorkers {
			refusal += " worker " + name + " already exists;"
		}
		for _, ref := range preview.MissingImages {
			refusal += " image " + ref + " not in the project's catalogue;"
		}
		for _, name := range preview.MissingSkills {
			refusal += " skill " + name + " not in the project's catalogue;"
		}
		http.Error(w, refusal+" nothing was changed", http.StatusConflict)
		return
	}

	// Record the RESOLVED answers (defaults applied) — what was actually
	// rendered, not just what was typed. Instantiate already validated them.
	resolved, err := topology.ResolveAnswers(t.Questions, topology.Answers(body.Answers))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	app := agentdb.TopologyApplication{
		Project:        id.Customer,
		Topology:       t.Ref(),
		Answers:        agentdb.JSONMap(resolved),
		SettingsPatch:  bundle.SettingsPatch,
		RequiredImages: bundle.Preconditions.Images,
		RequiredSkills: bundle.Preconditions.Skills,
	}
	for i := range bundle.Workers {
		app.Workers = append(app.Workers, &bundle.Workers[i])
	}
	for i := range bundle.Subscriptions {
		app.Subscriptions = append(app.Subscriptions, &bundle.Subscriptions[i])
	}
	for i := range bundle.Schedules {
		app.Schedules = append(app.Schedules, &bundle.Schedules[i])
	}
	for i := range bundle.MemorySeeds {
		app.MemorySeeds = append(app.MemorySeeds, &bundle.MemorySeeds[i])
	}

	result, err := store.ApplyTopology(r.Context(), app, humanEditBecause(body.Rationale))
	if err != nil {
		writeTopologyApplyErr(w, err)
		return
	}
	writeJSON(w, result)
}

// writeTopologyApplyErr maps the store's refusals onto status codes: the two
// not-in-that-state sentinels are 409, validation failures are 400, everything
// else is 500.
func writeTopologyApplyErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agentdb.ErrTopologyNameCollision),
		errors.Is(err, agentdb.ErrTopologyPreconditionUnmet):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, agentdb.ErrWorkerInvalid),
		errors.Is(err, agentdb.ErrScheduleInvalid),
		errors.Is(err, agentdb.ErrInvalidProjectSettings):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
