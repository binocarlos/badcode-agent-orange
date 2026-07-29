package agentdb

// topology_apply.go — the write half of T2 (docs/product/13-work-plan
// item T2; the rendering half lives in go/topology).
//
// ApplyTopology lands one rendered topology bundle in one project, in ONE
// database transaction, writing every row through the EXISTING config-logged
// store mutations (UpsertWorker, CreateSubscription, CreateSchedule,
// PutProjectSettings, CreateMemory) and finishing with a single
// `topology_apply` config event that names topology@version and the answers.
// There is deliberately no new row-write path here: the method is composition
// plus a bracket, so every invariant the individual mutations enforce — the
// config-event dual write, validation, read-back — holds unchanged inside an
// apply.
//
// # Why one transaction (the T2 "transactionality" decision)
//
// A half-applied topology must be impossible, and validate-then-write cannot
// promise that: the process can die between row three and row four. What makes
// one transaction HONEST here is that every store mutation already funnels its
// writes through WithConfigEvent on `s.gdb` — so a Store clone whose gdb is
// the outer *transaction* handle makes each inner WithConfigEvent nest as a
// SAVEPOINT (gorm's nested-transaction behaviour on both backends), and either
// the whole apply commits or none of it exists. Seq allocation stays correct:
// each nested event reads MAX(seq) inside the same outer transaction, so the
// bracket event always carries the highest seq of its own apply.
//
// The one thing that must NOT ride the savepoints is `config.changed`
// emission: the post-commit hook fires when an inner savepoint releases, which
// is long before the outer commit — an emitted event for a change that could
// still roll back would violate §15.4. So the clone's hook COLLECTS the
// committed records, and ApplyTopology replays them through the store's real
// hook only after the outer transaction has landed, in seq order. A crash in
// that window is repaired by the ordinary EmittedAt sweep, exactly as a crash
// after any single mutation is.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// Topology-apply errors. Sentinels so the HTTP layer can map both onto a 409
// without string-matching. Both mean "the project is not in the state the
// bundle assumes", and both leave the database untouched.
var (
	// ErrTopologyPreconditionUnmet: a referenced image or skill is not in the
	// project's catalogue (D2: a topology names assets, it never creates them).
	ErrTopologyPreconditionUnmet = errors.New("agentdb: topology precondition unmet")
	// ErrTopologyNameCollision: a bundle worker's name is already taken.
	// Hiring is not overwriting — same posture as the MCP worker_create.
	ErrTopologyNameCollision = errors.New("agentdb: topology name collision")
)

// TopologyApplication is one rendered bundle, stamped with the project it is
// being applied to. Rows arrive project-agnostic (T1 leaves Project/IDs/
// timestamps zero); ApplyTopology stamps Project itself and lets each store
// mutation allocate ids and timestamps as it always does.
type TopologyApplication struct {
	// Project is the hard namespace (P5) — required, never inferred.
	Project string
	// Topology is the applied identity, "name@version" (topology.Ref()).
	Topology string
	// Answers are the RESOLVED answers the bundle was rendered from — defaults
	// applied — recorded verbatim in the `topology_apply` payload so the log
	// says what was actually rendered, not just what the caller typed.
	Answers JSONMap
	// The bundle rows. Any slice may be empty; SettingsPatch may be nil.
	Workers       []*Worker
	Subscriptions []*Subscription
	Schedules     []*Schedule
	// SettingsPatch is zero-means-keep: non-zero fields overlay the project's
	// CURRENT settings and the merged object is written whole, because
	// PutProjectSettings has no patch semantics (§5; T1's discovered issue).
	SettingsPatch *ProjectSettings
	MemorySeeds   []*Memory
	// RequiredImages/RequiredSkills are the bundle's preconditions (D2),
	// re-checked inside the transaction. Images may be `name` or
	// `name:version` (§13.3); skills are names.
	RequiredImages []string
	RequiredSkills []string
}

// TopologyApplyResult is everything the apply created, read back (§9), plus
// the bracketing config event.
type TopologyApplyResult struct {
	Workers       []*Worker        `json:"workers"`
	Subscriptions []*Subscription  `json:"subscriptions"`
	Schedules     []*Schedule      `json:"schedules"`
	Settings      *ProjectSettings `json:"settings,omitempty"`
	Memories      []*Memory        `json:"memories,omitempty"`
	// Event is the committed `topology_apply` record — the receipt.
	Event *ConfigEvent `json:"event"`
}

// TopologySettingsOverlay merges a zero-means-keep patch onto the current
// settings and returns the merged copy plus the names of the fields the patch
// set. It is exported because preview (httpapi) must describe exactly the
// overlay apply will perform — one implementation, no drift.
//
// Corollary of the shape (T1's discovered issue): fields whose zero value is
// meaningful (daily_tokens_*, snapshot_ttl_days) cannot be set to zero through
// a patch. No current topology needs to; the limit is recorded there.
func TopologySettingsOverlay(current, patch *ProjectSettings) (*ProjectSettings, []string) {
	merged := *current
	fields := []string{}
	if patch == nil {
		return &merged, fields
	}
	if patch.BaseImage != "" {
		merged.BaseImage = patch.BaseImage
		fields = append(fields, "base_image")
	}
	if patch.SystemPrompt != "" {
		merged.SystemPrompt = patch.SystemPrompt
		fields = append(fields, "system_prompt")
	}
	if patch.MCPConfig != nil {
		merged.MCPConfig = patch.MCPConfig
		fields = append(fields, "mcp_config")
	}
	if patch.AttentionChannel != nil {
		merged.AttentionChannel = patch.AttentionChannel
		fields = append(fields, "attention_channel")
	}
	if patch.MaxConcurrentJobs != 0 {
		merged.MaxConcurrentJobs = patch.MaxConcurrentJobs
		fields = append(fields, "max_concurrent_jobs")
	}
	if patch.DailyTokensSoft != 0 {
		merged.DailyTokensSoft = patch.DailyTokensSoft
		fields = append(fields, "daily_tokens_soft")
	}
	if patch.DailyTokensHard != 0 {
		merged.DailyTokensHard = patch.DailyTokensHard
		fields = append(fields, "daily_tokens_hard")
	}
	if patch.BriefingMaxBytes != 0 {
		merged.BriefingMaxBytes = patch.BriefingMaxBytes
		fields = append(fields, "briefing_max_bytes")
	}
	if patch.SnapshotTTLDays != 0 {
		merged.SnapshotTTLDays = patch.SnapshotTTLDays
		fields = append(fields, "snapshot_ttl_days")
	}
	return &merged, fields
}

// txStore returns a Store bound to the transaction handle whose config-event
// hook appends committed records to sink instead of emitting them — the two
// halves of the one-transaction design described in the file header. The clone
// is used by a single goroutine for the life of one transaction and never
// escapes it.
func (s *Store) txStore(tx *gorm.DB, sink *[]*ConfigEvent) *Store {
	clone := &Store{gdb: tx}
	clone.configHook = func(_ context.Context, ev *ConfigEvent) { *sink = append(*sink, ev) }
	return clone
}

// ApplyTopology applies one rendered bundle to app.Project atomically.
//
// Inside the transaction it re-checks (authoritatively — any handler-side
// preview is advisory) that no bundle worker name is taken and that every
// required image and skill resolves in the project's catalogues; a failed
// check returns ErrTopologyNameCollision / ErrTopologyPreconditionUnmet and
// writes NOTHING. Then, in order: workers, subscriptions, schedules, the
// settings overlay, memory seeds — each through its ordinary config-logged
// mutation — and finally the `topology_apply` bracket event, which therefore
// carries the highest seq of the apply.
//
// cw is the who/why; a human/API edit passes the zero value. Memory seeds
// require Postgres (the memory store's standing rule); a bundle without seeds
// applies on either backend.
func (s *Store) ApplyTopology(ctx context.Context, app TopologyApplication, cw ConfigWrite) (*TopologyApplyResult, error) {
	if strings.TrimSpace(app.Project) == "" {
		return nil, fmt.Errorf("agentdb: ApplyTopology requires a project (P5: the namespace is never inferred)")
	}
	if strings.TrimSpace(app.Topology) == "" {
		return nil, fmt.Errorf("agentdb: ApplyTopology requires the topology identity (name@version)")
	}
	answers := app.Answers
	if answers == nil {
		answers = JSONMap{}
	}

	// A seeded row's reason is knowable without asking anyone: it exists because
	// this topology was applied. doc 10 §2 wrote the sentence — the changelog
	// should read "seeded from hypothesis-lab@v1" — and without it the first
	// thing a new operator ever sees is a Desk full of "(no reason given)",
	// which reads as sloppiness on the one screen K2 exists to keep honest.
	// An explicit rationale from the caller always wins.
	if strings.TrimSpace(cw.Rationale) == "" {
		cw.Rationale = "seeded from " + app.Topology
	}

	var deferred []*ConfigEvent
	result := &TopologyApplyResult{
		Workers:       []*Worker{},
		Subscriptions: []*Subscription{},
		Schedules:     []*Schedule{},
	}

	err := s.gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txs := s.txStore(tx, &deferred)

		// 1. Refuse before any write: collisions, then preconditions.
		var taken []string
		for _, w := range app.Workers {
			if w == nil {
				return fmt.Errorf("%w: nil worker row", ErrWorkerInvalid)
			}
			_, err := txs.GetWorker(ctx, app.Project, w.Name)
			switch {
			case err == nil:
				taken = append(taken, w.Name)
			case errors.Is(err, ErrWorkerNotFound):
				// free — the good case
			case errors.Is(err, ErrWorkerInvalid):
				return err // a malformed name is the caller's error, not a free slot
			default:
				return err
			}
		}
		if len(taken) > 0 {
			sort.Strings(taken)
			return fmt.Errorf("%w: worker %s already exists in project %s (hiring is not overwriting)",
				ErrTopologyNameCollision, strings.Join(taken, ", "), app.Project)
		}

		var unmet []string
		for _, ref := range app.RequiredImages {
			if _, err := txs.ResolveCustomImage(ctx, app.Project, ref); err != nil {
				if errors.Is(err, ErrCustomImageNotFound) || errors.Is(err, ErrCustomImageInvalid) {
					unmet = append(unmet, "image "+ref)
					continue
				}
				return err
			}
		}
		for _, name := range app.RequiredSkills {
			if _, err := txs.GetProjectSkill(ctx, app.Project, name); err != nil {
				if errors.Is(err, ErrSkillNotFound) || errors.Is(err, ErrSkillInvalid) {
					unmet = append(unmet, "skill "+name)
					continue
				}
				return err
			}
		}
		if len(unmet) > 0 {
			sort.Strings(unmet)
			return fmt.Errorf("%w: %s not in project %s's catalogues (a topology references assets by name, it never creates them — D2)",
				ErrTopologyPreconditionUnmet, strings.Join(unmet, ", "), app.Project)
		}

		// 2. The rows, each through its ordinary config-logged mutation.
		for _, w := range app.Workers {
			w.Project = app.Project
			stored, err := txs.UpsertWorker(ctx, w, cw)
			if err != nil {
				return err
			}
			result.Workers = append(result.Workers, stored)
		}
		for _, sub := range app.Subscriptions {
			if sub == nil {
				return fmt.Errorf("agentdb: nil subscription row")
			}
			sub.Project = app.Project
			stored, err := txs.CreateSubscription(ctx, sub, cw)
			if err != nil {
				return err
			}
			result.Subscriptions = append(result.Subscriptions, stored)
		}
		for _, sch := range app.Schedules {
			if sch == nil {
				return fmt.Errorf("%w: nil schedule row", ErrScheduleInvalid)
			}
			sch.Project = app.Project
			stored, err := txs.CreateSchedule(ctx, sch, cw)
			if err != nil {
				return err
			}
			result.Schedules = append(result.Schedules, stored)
		}
		if app.SettingsPatch != nil {
			current, err := txs.GetProjectSettings(ctx, app.Project)
			if err != nil {
				return err
			}
			merged, _ := TopologySettingsOverlay(current, app.SettingsPatch)
			stored, err := txs.PutProjectSettings(ctx, merged, cw)
			if err != nil {
				return err
			}
			result.Settings = stored
		}
		for _, m := range app.MemorySeeds {
			if m == nil {
				return fmt.Errorf("agentdb: nil memory seed")
			}
			m.Project = app.Project
			stored, err := txs.CreateMemory(ctx, m, nil)
			if err != nil {
				return err
			}
			result.Memories = append(result.Memories, stored)
		}

		// 3. The bracket. Written LAST so its seq caps the apply: a log reader
		// (and the newest-first changelog) sees "applied topology X" above the
		// rows it created, and its presence proves every row event landed.
		ev, err := txs.WithConfigEvent(ctx, ConfigChange{
			Project: app.Project,
			Action:  ActionTopologyApply,
			Payload: JSONMap{"topology": app.Topology, "answers": map[string]any(answers)},
			Write:   cw,
		}, func(*gorm.DB) error { return nil })
		if err != nil {
			return err
		}
		result.Event = ev
		return nil
	})
	if err != nil {
		return nil, err
	}

	// AFTER the commit, never inside it (§15.4): replay the collected records
	// through the real hook, in the order they were written.
	if hook := s.configEventHook(); hook != nil {
		emitCtx := context.WithoutCancel(ctx)
		for _, ev := range deferred {
			hook(emitCtx, ev)
		}
	}
	return result, nil
}
