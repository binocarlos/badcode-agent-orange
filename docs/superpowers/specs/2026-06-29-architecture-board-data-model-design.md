# Architecture-Board Data Model — Design

**Date:** 2026-06-29
**Status:** First pass IMPLEMENTED and merged (`go/agentdb/board*.go`, migrations `020`/`021`).
**Partly superseded** by the second-pass revision below — see **§0. Second-pass revision** before
building further.
**Context:** `docs/ARCHITECTURE.md` **§6D (authoritative primitive set)**, §6B (operating model),
§6C (board as GitOps), §10 (prompt composition), §12 (roadmap). Reading order: `docs/AGENTS_DESIGN.md`.

---

## 0. Second-pass revision (2026-06-29) — the agentic-OS collapse

After the first pass shipped, the design converged on the **agentic-OS model (`ARCHITECTURE.md` §6D)**.
The governing rule is **dispatch vs. compose**: structure only what the runtime queries or dispatches
on; leave everything the model merely *reads into a prompt* as fluid text the Consultants curate. That
**collapses** most of the first-pass schema. This section is the authoritative delta; §3–§6 below are
the first-pass design, kept for rationale/history.

**What survives unchanged:**
- **`board_revisions` + `board_head`** — the immutable, versioned changeset log (now versions the
  `fragments` KV + subscriptions). Rollback + version-pinning are retained and are **independent of
  gating**.
- **`board_subscriptions`** — real dispatch (the bus matches `event_type`). Keep as built.

**What collapses into one `fragments` KV** (named text values, versioned, Consultant-curated — only
ever composed into prompts, never dispatched on):
- `board_staff` → **removed**. A "staff member" is *role guidance text* (a fragment) + **enforced caps
  as structured `spawn` args** (tool allowlist, model tier, budget — these are dispatch/enforcement,
  not prose; they live on the Scope/`spawn` call of §7, not in a table).
- `board_pipelines` (definitions) → **removed**. Pipeline *definition* = guidance text; *running* one
  is the `run_pipeline` syscall; an *in-flight run* is the new `pipeline_runs` work-state table.
- `board_event_types` → **removed** as a table. Event types are strings on `board_subscriptions`; the
  *vocabulary* is a documentation fragment (symmetric with the label vocabulary).
- `board_prompt_fragments` → **generalised** into the `fragments` KV holding *all* guidance: routing
  guidance, role prompts, useful-pipelines guidance, label vocab, event vocab.
- `payload_schema` (on the removed `board_event_types`) → **gone**; payloads are opaque text (§6D).

**What is added (work state — read-write, ungated; gating tracks policy, not persistence):**
- **`tickets`** — the kanban work items (the manager files/updates them as a free act). See
  `ARCHITECTURE.md` §5 for the ticket shape. **Not** part of the versioned board log.
- **`pipeline_runs`** — fire-and-forget in-flight pipeline state (current stage + status), emits
  `pipeline.completed`. **Not** part of the versioned board log.

**Memory labels:** the *applied* labels are a structured field on memory items (a memory-store concern,
not the board); the label *vocabulary* is a `fragments` entry.

**Gating (no schema needed):** `status: proposed → applied` on `board_revisions` is retained but
**optional** — a policy write is versioned and applies immediately unless a *review subscription*
routes its change event to a reviewer scope (which may `escalate_to_human`). The only non-editable
floor is **resource safety** (loop-depth / budget / concurrency caps), enforced in mechanism.

> **Implementation status:** the first-pass tables are merged but **not deployed/depended-on**, so this
> collapse is a cheap revise. A follow-up implementation spec will: generalise `board_prompt_fragments`
> → `fragments`, drop `board_staff` / `board_pipelines` / `board_event_types`, and add `tickets` /
> `pipeline_runs`. Until then, treat §6D + this §0 as the target.

---

## 1. Scope

This spec defines **the architecture-board data model only**: the entities, their fields, the
immutable revision model, the Postgres schema, and the pinning contract. It is the foundation the
Routing Manager *reads* and the Consultant *edits* (in later slices).

**Explicitly out of scope** (consume this model in their own specs):
- The Routing Manager, the Consultant, the gate/approval flow.
- The worker runtime, the memory store, the telemetry/CBR case-base store.
- The Go **loader/apply/fold code** — we define the schema and the `BoardStore` seam *shape*, not
  its implementation.

## 2. The pivot from §6C (decided in brainstorm)

§6C specified **literal git + a Postgres projection**. We are **deferring literal git**:

- Git's concrete payoffs (PR-as-gate, `git revert`, branch-canary, shared human/AI change stream)
  are all **Consultant-era** features, and the Consultant is the *last* slice (§12). For Slices 0–3
  there is no Consultant; board edits are made by a human or the manager directly. Until then,
  literal git is machinery for a use case that doesn't exist yet, at the cost of two-store sync.
- What §6C actually *needs early* are the **properties**, not git: immutable addressable versions
  (pin "I ran against revision R"), rollback, and review-before-apply. Those are cheap now and
  expensive to retrofit.

**Decision:** Postgres is the single source of truth, behind a `BoardStore` seam, with an
**immutable, event-sourced revision model** built from day one. Git becomes a later **export** or a
**swapped backing store** behind the seam — never a thing we sync to now.

This preserves §6C's mental model exactly — *immutable config log + fast queryable view* — with both
layers in Postgres.

> **The one trap avoided:** Postgres with *mutable* board rows + versioning bolted on later. That
> migration is the painful one. The immutability lives in the log from the start.

## 3. Conceptual model: two layers, one store

### 3.1 The changeset log — source of truth (append-only, immutable)

Each **revision** is a *changeset*: an ordered list of ops (`add` / `update` / `remove`) over board
entities, plus metadata (parent, author, `message` = the *why*, status). This is the event source.
It is what is immutable, what gets **pinned**, what a proposal/PR-equivalent is, and what a `revert`
appends an inverse of. (`status: proposed → applied → reverted` models the gate without implementing
it.)

### 3.2 The typed "current" tables — derived cache (mutable, rebuilt on apply)

Five typed tables hold the *current* folded state with real columns and indexes, so the Routing
Manager answers "who reacts to `session.completed`?" with a plain indexed `SELECT`. Each row carries
`last_changed_in` (the revision that last touched it) for attribution. These are a **cache**: fully
reconstructable by folding the log, droppable and rebuildable.

This is §6C's "immutable config log + fast projection" split, both in Postgres.

### 3.3 Reads

- **Hot reads** (current state) hit the typed tables directly; never fold.
- **Point-in-time reads** ("board as of revision R", for repro/audit) fold the log up to R. The
  board is kilobytes and these reads are rare, so folding is cheap.

### 3.4 Pinning contract

A consumer (a routing decision, a worker session) records **one token**: `board_revision_id` — the
head changeset id at dispatch. That deterministically resolves the entire board, including every
prompt fragment (§10 "a session records exactly which fragment versions it ran" — satisfied by the
single revision id, since the revision folds to an exact board state). The telemetry store that
*holds* that token is a different store (§6C) and is out of scope; this spec only defines the token
the board exposes and guarantees its determinism.

## 4. Entities

Stable **string-slug ids**, referenced across revisions (renaming/replacing is itself a changeset).
The org **event taxonomy** here is the bus vocabulary (`human-goal`, `session.completed`,
`ticket.done`, `summary.completed`, …) and is **distinct from** the intra-session SSE vocabulary in
`go/events` (`message_start`, `tool_use_start`, …). The board models the bus taxonomy.

| Entity | Fields |
|---|---|
| **staff** | `id`, `role_fragments[]` (fragment ids), `skills[]`, `model_tier` (full\|mid\|cheap), `memory_namespace`, `self_archiving` (fragment ref or inline), `budget?`. **No subscriptions field** — subscriptions are standalone. |
| **subscription** | `id`, `event_type`, `reaction{kind: staff\|pipeline, ref}`, `applicability_condition` (the ReasoningBank "when X" match key, §6A), `enabled`. The Consultant's primary edit surface. |
| **pipeline** | `id`, `description`, `stages[]` (each: staff ref or inline scope + input mapping + optional emit-on-done). |
| **event_type** | `id`, `kind` (external\|lifecycle), `description`, `payload_schema` (JSON Schema; empty `{}` = no schema). |
| **prompt_fragment** | `id`, `kind` (role\|label_docs\|procedure\|self_archiving), `body` (markdown). |

**Why subscriptions are standalone** (decided): a subscription is a *candidate binding* `event →
reaction` (§6A), where a reaction may be a staff member *or* a pipeline. Modeling it as its own
entity gives the loosest coupling (emitter-agnostic pub/sub), handles both reaction kinds uniformly,
and is exactly the Consultant's edit surface.

## 5. Postgres schema

Follows the `go/agentdb` idiom: numbered idempotent SQL migrations, gorm/Postgres, BIGINT epoch
timestamps, JSONB bodies, VARCHAR ids. New migrations append after the existing `019_*`.

### 5.1 The log

```sql
-- 020_board_revisions
CREATE TABLE IF NOT EXISTS board_revisions (
    id          VARCHAR(36) PRIMARY KEY,                -- revision id = the pin token
    parent_id   VARCHAR(36) DEFAULT '',                 -- prior revision (linear history)
    seq         BIGSERIAL UNIQUE,                       -- monotonic order for folding
    status      VARCHAR(20) NOT NULL DEFAULT 'applied', -- proposed | applied | reverted
    author      VARCHAR(255) NOT NULL DEFAULT '',       -- human email or 'consultant'
    message     TEXT NOT NULL DEFAULT '',               -- the *why* (commit-message analog)
    ops         JSONB NOT NULL DEFAULT '[]',            -- [{op,entity_type,entity_id,body}]
    created_at  BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_board_revisions_status ON board_revisions(status);

-- single-row head pointer (which applied revision is live)
CREATE TABLE IF NOT EXISTS board_head (
    singleton   BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    revision_id VARCHAR(36) NOT NULL REFERENCES board_revisions(id)
);
```

### 5.2 The typed current cache

```sql
-- 021_board_current
CREATE TABLE IF NOT EXISTS board_staff (
    id               VARCHAR(64) PRIMARY KEY,
    role_fragments   JSONB NOT NULL DEFAULT '[]',   -- []fragment_id
    skills           JSONB NOT NULL DEFAULT '[]',
    model_tier       VARCHAR(20) NOT NULL DEFAULT 'mid',
    memory_namespace VARCHAR(255) NOT NULL DEFAULT '',
    self_archiving   JSONB NOT NULL DEFAULT '{}',   -- {fragment_id} | {inline}
    budget           JSONB NOT NULL DEFAULT '{}',
    last_changed_in  VARCHAR(36) NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS board_event_types (
    id              VARCHAR(64) PRIMARY KEY,
    kind            VARCHAR(20) NOT NULL DEFAULT 'lifecycle', -- external | lifecycle
    description     TEXT NOT NULL DEFAULT '',
    payload_schema  JSONB NOT NULL DEFAULT '{}',       -- empty {} = no schema
    last_changed_in VARCHAR(36) NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS board_subscriptions (
    id                      VARCHAR(64) PRIMARY KEY,
    event_type              VARCHAR(64) NOT NULL,      -- -> board_event_types.id (logical)
    reaction_kind           VARCHAR(20) NOT NULL,      -- staff | pipeline
    reaction_ref            VARCHAR(64) NOT NULL,      -- -> staff.id | pipeline.id
    applicability_condition TEXT NOT NULL DEFAULT '',  -- ReasoningBank "when X" key
    enabled                 BOOLEAN NOT NULL DEFAULT TRUE,
    last_changed_in         VARCHAR(36) NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_board_subs_event ON board_subscriptions(event_type, enabled);

CREATE TABLE IF NOT EXISTS board_pipelines (
    id              VARCHAR(64) PRIMARY KEY,
    description     TEXT NOT NULL DEFAULT '',
    stages          JSONB NOT NULL DEFAULT '[]',      -- [{staff_ref|scope,input,emit}]
    last_changed_in VARCHAR(36) NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS board_prompt_fragments (
    id              VARCHAR(64) PRIMARY KEY,
    kind            VARCHAR(20) NOT NULL DEFAULT 'role', -- role|label_docs|procedure|self_archiving
    body            TEXT NOT NULL DEFAULT '',
    last_changed_in VARCHAR(36) NOT NULL DEFAULT ''
);
```

### 5.3 Stated invariants (not DB constraints)

- **Cross-references are logical, not FK-enforced.** `reaction_ref`, `event_type`, and
  `role_fragments` are validated **before apply** (a gate/Conftest concern), not by FK constraints —
  FK enforcement would fight the event-sourced apply order and the eventual git-backed swap. The
  validator (later spec) guarantees: every `subscription.event_type` exists in `board_event_types`;
  every `reaction_ref` exists in the named entity table; every `role_fragment` id exists.
- **The typed tables are a cache.** `board_revisions.ops` folded in `seq` order is the authority; the
  typed tables are reconstructable from it and may be dropped/rebuilt.
- **One head.** `board_head` is a single row; `revision_id` always points at an `applied` revision.

## 6. The `BoardStore` seam (shape only — no implementation here)

A narrow Go interface wraps the model so a later git-backed implementation is a swap. Indicative
shape (final signatures land with the implementation spec):

```go
type BoardStore interface {
    // Read paths (hot = current; AsOf folds the log).
    Current(ctx) (Board, error)
    AsOf(ctx, revisionID string) (Board, error)
    Head(ctx) (revisionID string, err error)

    // Write path: append a changeset. Apply moves head; Propose leaves status=proposed.
    Append(ctx, Changeset) (revisionID string, err error)
}
```

The Postgres impl is implemented in a later spec; a git-backed impl is a future swap.

## 7. Relationship to the roadmap (§12)

- **Slice 0** needs almost none of this — a board can start as a near-empty revision. The model is
  built ahead so the revision/pin contract exists before anything depends on it.
- **Slices 1–3** populate fragments, staff, and the eval/pin path.
- **Slice 4** (Consultant) is where `status: proposed` + the gate + canary land, and where the git
  export/swap becomes worth its weight. The model already expresses all of it.

## 8. Open items (deferred, not blocking)

1. **`stages[]` inner schema** for pipelines — pinned down when `run_pipeline` is specified.
2. **`applicability_condition` representation** — free text now; may become structured when the CBR
   router is built.
3. **Canary pointers** — a second head for branch-canary (§6C) — added when the Consultant lands.
4. **Git export/swap** — the literal-git backing or export, when Consultant-era PR ergonomics are
   wanted.
