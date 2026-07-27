// Package agentdb — point-in-time replay of the config log (spec §15.6/§15.7,
// docs/product/09-config-log.md).
//
// # The fold (§15.6)
//
// The project's configuration at any historical instant T is reconstructible by
// folding `config_events` from t₀ to T: iterate in order, keep the newest
// payload per (entity kind, entity key), and treat delete actions as tombstones
// that remove the key. The result is exactly the projection tables as they
// stood at T.
//
// That is the whole algorithm. It is a map-assignment loop with no merge
// algebra, no rebase semantics and no way for one corrupt record to poison the
// records after it — which is the entire reason §15.2 stores the FULL new state
// in every payload rather than a diff.
//
// ORDER. The spec says "created_at/id order". `created_at` is a millisecond
// wall clock and `id` is a random uuid, so that order is not total: two writes
// to the same key inside one millisecond would fold in an arbitrary order and
// the fold could contradict the projection it exists to reproduce. This
// implementation therefore orders by `seq`, the monotonic per-project sequence
// allocated inside the config-event transaction (migration 027) — seq order is
// commit order. `at` still filters on `created_at`, because "what did the org
// look like on Tuesday" is a question about the clock.
//
// BOUNDARY. This replays *configuration*, not the world (§15.6): memories
// written after T are still there, image blobs a fold references may have been
// reaped by `snapshot_ttl_days`, and nothing outside the system rewinds.
//
// # Restore is a forward operation (§15.7)
//
// There is deliberately NO restore function in this file, and no
// `restore_project` verb anywhere in v1 (work plan, "Deferred"). Restoring the
// configuration to T is: fold to T, compare with now, and append ordinary
// compensating mutations — the same `UpsertWorker` / prompt-write /
// `CreateSubscription` calls any other change uses, each carrying a rationale
// that names the config event being restored.
//
// Git revert, never git reset. A destructive restore would erase the most
// instructive part of the record: that something was tried, regretted and
// reverted. TestRestoreIsForward pins the property that a restore only ever
// ADDS events.
//
// WORKED EXAMPLE — restoring a worker's prompt to revision ce_41 (§15.7):
//
//	// 1. Find the revision: newest-first prompt writes for this worker.
//	evs, _ := store.ListConfigEvents(ctx, agentdb.ConfigEventQuery{
//		Project: "acme", Action: agentdb.ActionWorkerPromptWrite, Limit: 10,
//	})
//	target := evs[3] // the one whose rationale the human recognised
//
//	// 2. Take the prompt out of its payload — the payload is the full row.
//	prompt, _ := target.PayloadString("system_prompt")
//
//	// 3. Append the restore as an ORDINARY mutation. Nothing is rewritten,
//	//    nothing is truncated; the regression and its rationale stay in the log.
//	w, _ := store.GetWorker(ctx, "acme", "email-answerer")
//	w.SystemPrompt = prompt
//	store.UpsertWorker(ctx, w, agentdb.ConfigWrite{
//		Worker:  "marketing-manager",
//		Session: "s-1043",
//		Rationale: "restore to " + target.ID + ": the later rewrite regressed tone",
//	})
//
// Step 3 writes a NEW config event whose payload is the full restored row. A
// fold to any earlier T still reproduces exactly what was live then — including
// the week the organisation was wrong.
package agentdb

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ── Entity identity (§15.6) ─────────────────────────────────────────────────

// EntityKind is the kind half of the fold key. The list is closed because the
// action vocabulary is closed (§15.3).
type EntityKind string

const (
	EntityWorker          EntityKind = "worker"
	EntitySubscription    EntityKind = "subscription"
	EntitySchedule        EntityKind = "schedule"
	EntityImage           EntityKind = "image"
	EntitySkill           EntityKind = "skill"
	EntityProjectSettings EntityKind = "project-settings"
	EntityProjectPrompt   EntityKind = "project-prompt"
)

// EntityRef identifies one folded entity: its kind plus the key that is unique
// within that kind — worker name, subscription id, schedule id, image
// `name:version`, skill name. The two singletons (project settings, the project
// prompt) carry an empty key.
//
// The rendered form is the `entity` filter of `config_history` (§15.9):
// "worker:email-answerer", "schedule:sch-7", "project-settings".
type EntityRef struct {
	Kind EntityKind
	Key  string
}

// String renders the reference in the §15.9 `entity` form.
func (r EntityRef) String() string {
	if r.Key == "" {
		return string(r.Kind)
	}
	return string(r.Kind) + ":" + r.Key
}

// EntityKinds is the complete set of fold kinds, in §15.3 order. Closed for the
// same reason the action vocabulary is.
var EntityKinds = []EntityKind{
	EntityWorker,
	EntitySubscription,
	EntitySchedule,
	EntityImage,
	EntitySkill,
	EntityProjectSettings,
	EntityProjectPrompt,
}

// ParseEntityRef reads the rendered form back — "worker:email-answerer",
// "image:toolbox:2", "project-settings". It is the parser behind
// `config_history`'s `entity` filter (§15.9).
//
// The kind is everything before the FIRST colon, so an image's `name:version`
// key survives intact. An unknown kind is an error rather than a filter that
// matches nothing: a typo must not read as "this worker has no history".
func ParseEntityRef(s string) (EntityRef, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return EntityRef{}, fmt.Errorf("agentdb: entity reference is empty")
	}
	kindPart, key, _ := strings.Cut(trimmed, ":")
	kind := EntityKind(kindPart)
	known := false
	for _, k := range EntityKinds {
		if k == kind {
			known = true
			break
		}
	}
	if !known {
		names := make([]string, 0, len(EntityKinds))
		for _, k := range EntityKinds {
			names = append(names, string(k))
		}
		return EntityRef{}, fmt.Errorf(
			"agentdb: %q is not a known entity kind (want one of %s, e.g. \"worker:email-answerer\")",
			kindPart, strings.Join(names, ", "))
	}
	switch kind {
	case EntityProjectSettings, EntityProjectPrompt:
		if key != "" {
			return EntityRef{}, fmt.Errorf(
				"agentdb: %q is a singleton — it takes no key (write %q)", kindPart, kindPart)
		}
	default:
		if strings.TrimSpace(key) == "" {
			return EntityRef{}, fmt.Errorf(
				"agentdb: entity %q needs a key, e.g. %q", trimmed, kindPart+":<name>")
		}
	}
	return EntityRef{Kind: kind, Key: key}, nil
}

// ActionsForEntityKind lists the §15.3 actions that write one kind of entity —
// the SQL narrowing behind the `entity` filter. Order is the vocabulary's.
func ActionsForEntityKind(kind EntityKind) []string {
	out := make([]string, 0, 6)
	for _, a := range ConfigActions {
		if entityKindForAction[a] == kind {
			out = append(out, a)
		}
	}
	return out
}

// entityKindForAction maps the closed §15.3 vocabulary onto fold keys. A new
// action MUST be added here: an unmapped action makes the fold incomplete, and
// the fold failing loudly is far better than a snapshot that silently omits an
// entity kind.
var entityKindForAction = map[string]EntityKind{
	ActionWorkerCreate:       EntityWorker,
	ActionWorkerUpdate:       EntityWorker,
	ActionWorkerEnable:       EntityWorker,
	ActionWorkerDisable:      EntityWorker,
	ActionWorkerFreeze:       EntityWorker,
	ActionWorkerUnfreeze:     EntityWorker,
	ActionWorkerDelete:       EntityWorker,
	ActionWorkerPromptWrite:  EntityWorker,
	ActionProjectPromptWrite: EntityProjectPrompt,
	ActionProjectSettingsPut: EntityProjectSettings,
	ActionSubscriptionCreate: EntitySubscription,
	ActionSubscriptionUpdate: EntitySubscription,
	ActionSubscriptionDelete: EntitySubscription,
	ActionScheduleCreate:     EntitySchedule,
	ActionScheduleUpdate:     EntitySchedule,
	ActionScheduleDelete:     EntitySchedule,
	ActionImageCreate:        EntityImage,
	ActionSkillCreate:        EntitySkill,
}

// deleteActions are the tombstones: the record stays, the key goes (§15.3
// rule 2). Note what is NOT here — images and skills have no delete verb at all
// because they are append-only at the tool surface (§13, §14), and the two
// singletons cannot be deleted, only rewritten.
var deleteActions = map[string]bool{
	ActionWorkerDelete:       true,
	ActionSubscriptionDelete: true,
	ActionScheduleDelete:     true,
}

// IsDeleteAction reports whether an action is a tombstone in the fold.
func IsDeleteAction(action string) bool { return deleteActions[action] }

// PayloadString reads a string field out of the full-state payload. It is the
// accessor a restore uses to lift the old value out of a historical event
// (§15.7 step 2) without every caller re-implementing the type assertion.
func (e *ConfigEvent) PayloadString(field string) (string, bool) {
	if e == nil || e.Payload == nil {
		return "", false
	}
	v, ok := e.Payload[field].(string)
	return v, ok
}

// EntityRefFor derives the fold key of one config event from its action and its
// full-state payload.
//
// The key always comes from the PAYLOAD, never from a separate column: the
// payload is by construction the row as it stood, so the key a fold uses is the
// key the projection table used. An event whose payload is missing its own
// identity is a corrupt record and says so.
func EntityRefFor(ev *ConfigEvent) (EntityRef, error) {
	if ev == nil {
		return EntityRef{}, fmt.Errorf("agentdb: cannot key a nil config event")
	}
	kind, ok := entityKindForAction[ev.Action]
	if !ok {
		return EntityRef{}, fmt.Errorf(
			"agentdb: config event %s has action %q, which the fold does not know (§15.6). "+
				"The §15.3 vocabulary is closed: adding a verb means teaching entityKindForAction about it",
			ev.ID, ev.Action)
	}

	switch kind {
	case EntityProjectSettings, EntityProjectPrompt:
		// Singletons: one per project, so the kind IS the key.
		return EntityRef{Kind: kind}, nil
	case EntityImage:
		// §13.2 identity is `name:version` — every version is its own entity,
		// never overwritten and never deleted.
		name, err := payloadKeyField(ev, "name")
		if err != nil {
			return EntityRef{}, err
		}
		version, err := payloadIntField(ev, "version")
		if err != nil {
			return EntityRef{}, err
		}
		return EntityRef{Kind: kind, Key: fmt.Sprintf("%s:%d", name, version)}, nil
	case EntityWorker, EntitySkill:
		name, err := payloadKeyField(ev, "name")
		if err != nil {
			return EntityRef{}, err
		}
		return EntityRef{Kind: kind, Key: name}, nil
	default: // subscriptions and schedules are keyed by their generated id
		id, err := payloadKeyField(ev, "id")
		if err != nil {
			return EntityRef{}, err
		}
		return EntityRef{Kind: kind, Key: id}, nil
	}
}

func payloadKeyField(ev *ConfigEvent, field string) (string, error) {
	v, ok := ev.PayloadString(field)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf(
			"agentdb: config event %s (%s) has no %q in its payload — the payload must be the full row (§15.2)",
			ev.ID, ev.Action, field)
	}
	return v, nil
}

// payloadIntField reads a numeric payload field. jsonb round-trips numbers as
// float64, so both forms are accepted.
func payloadIntField(ev *ConfigEvent, field string) (int, error) {
	if ev.Payload != nil {
		switch v := ev.Payload[field].(type) {
		case float64:
			return int(v), nil
		case int:
			return v, nil
		case int64:
			return int(v), nil
		}
	}
	return 0, fmt.Errorf(
		"agentdb: config event %s (%s) has no numeric %q in its payload — the payload must be the full row (§15.2)",
		ev.ID, ev.Action, field)
}

// ── The snapshot ────────────────────────────────────────────────────────────

// FoldedEntity is one entity as it stood at the fold instant: its reference and
// the event that last wrote it. The event carries the full state, the actor and
// the rationale, so a caller holding a FoldedEntity can both read the row and
// say who decided it and why.
type FoldedEntity struct {
	Ref   EntityRef
	Event *ConfigEvent
}

// Payload is the entity's full state at the fold instant.
func (f FoldedEntity) Payload() JSONMap {
	if f.Event == nil {
		return nil
	}
	return f.Event.Payload
}

// ConfigSnapshot is the projection state at an instant — "the tables as they
// stood". It is a value, not a store: nothing writes it back, because writing a
// fold back over the projections would be exactly the destructive restore
// §15.7 forbids.
type ConfigSnapshot struct {
	// Project is the namespace the fold was taken in (P5).
	Project string
	// At is the instant folded to, unix ms; 0 means "the whole log".
	At int64
	// Entities holds the live entities, keyed by EntityRef.String().
	Entities map[string]FoldedEntity
	// Deleted holds the tombstones: the delete event for each key that was
	// removed and not recreated. Its payload is the row as it last stood, which
	// is what makes "restore a deleted schedule" a lookup rather than
	// archaeology (§15.7).
	Deleted map[string]*ConfigEvent
	// Folded counts the events consumed — the receipt of the replay.
	Folded int
}

// Get returns the entity live at the fold instant, if any.
func (s *ConfigSnapshot) Get(ref EntityRef) (FoldedEntity, bool) {
	if s == nil || s.Entities == nil {
		return FoldedEntity{}, false
	}
	e, ok := s.Entities[ref.String()]
	return e, ok
}

// Worker is sugar for the commonest lookup.
func (s *ConfigSnapshot) Worker(name string) (FoldedEntity, bool) {
	return s.Get(EntityRef{Kind: EntityWorker, Key: name})
}

// OfKind returns every live entity of one kind, ordered by key so callers and
// tests see a stable sequence.
func (s *ConfigSnapshot) OfKind(kind EntityKind) []FoldedEntity {
	if s == nil {
		return nil
	}
	var out []FoldedEntity
	for _, e := range s.Entities {
		if e.Ref.Kind == kind {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.Key < out[j].Ref.Key })
	return out
}

// WasDeleted reports whether the key was removed by a tombstone at the fold
// instant, returning the delete event that carries its final state.
func (s *ConfigSnapshot) WasDeleted(ref EntityRef) (*ConfigEvent, bool) {
	if s == nil || s.Deleted == nil {
		return nil, false
	}
	ev, ok := s.Deleted[ref.String()]
	return ev, ok
}

// ── The fold (§15.6) ────────────────────────────────────────────────────────

// FoldTo reconstructs the projection state of a project at instant `at` (unix
// milliseconds; 0 or negative = the whole log, i.e. "now").
//
// It reads the log in seq order and keeps the newest payload per
// (entity kind, entity key); a delete action removes the key and is remembered
// as a tombstone. Nothing here writes: a fold is a value a human or a worker
// looks at, and turning one back into the live tables is a run of ordinary
// forward mutations (§15.7), never a bulk overwrite.
//
// This is history and audit only — no runtime path folds (§15.4). Composition
// reads the projection tables, and a session's `composed_prompt` already pins
// what a given transcript actually ran with (§6.2).
func (s *Store) FoldTo(ctx context.Context, project string, at int64) (*ConfigSnapshot, error) {
	if strings.TrimSpace(project) == "" {
		return nil, fmt.Errorf("agentdb: FoldTo requires a project (P5: the namespace is never inferred)")
	}

	db := s.gdb.WithContext(ctx).Model(&ConfigEvent{}).Where("project = ?", project)
	if at > 0 {
		db = db.Where("created_at <= ?", at)
	}
	var evs []*ConfigEvent
	// Ascending seq: the fold is last-writer-wins, so it must run forwards.
	if err := db.Order("seq ASC").Find(&evs).Error; err != nil {
		return nil, fmt.Errorf("agentdb: fold config log: %w", err)
	}

	snap := &ConfigSnapshot{
		Project:  project,
		At:       at,
		Entities: map[string]FoldedEntity{},
		Deleted:  map[string]*ConfigEvent{},
	}
	for _, ev := range evs {
		ref, err := EntityRefFor(ev)
		if err != nil {
			return nil, err
		}
		key := ref.String()
		if IsDeleteAction(ev.Action) {
			delete(snap.Entities, key)
			snap.Deleted[key] = ev
			continue
		}
		// A re-create after a delete is an ordinary write: the key comes back and
		// the tombstone stops applying. (§15.7's "restore a deleted schedule is
		// schedule_create with the payload from its delete event".)
		delete(snap.Deleted, key)
		snap.Entities[key] = FoldedEntity{Ref: ref, Event: ev}
	}
	snap.Folded = len(evs)
	return snap, nil
}
