# Agent Orange — Steward & Board Orchestration Design

**Date:** 2026-06-25
**Status:** Approved design (brainstorming thread). Ready for implementation planning (Phase A first).
**Foundation:** Built on the **agentkit** engine in this repo (`github.com/binocarlos/badcode-agent-orange`) — the "floor" (Layer 1). This spec designs the layers agentkit deliberately lacks: triggering, the board, the dispatch loop, engage, and the steward.
**Relationship to other docs:**
- Extends the **master spec** (`badcode/docs/superpowers/specs/2026-06-20-agent-orange-design.md`) — keeps its locked vocabulary, "scopes-not-personas" thesis, reliability rules, and conscience mechanism; concretizes its abstract coordination layer (§6) into a board + dispatch loop.
- Grounded in the **research substrate** (`badcode/docs/AGENTS_RESEARCH.md`) and a verified 2026 ecosystem scan whose load-bearing reference is **Hermes** (NousResearch) — its board/dispatcher/worker-protocol design, adapted to canon and to containerized (not host-PID) workers.
- Written against the real engine seams in this repo (`go/agentkit.go`, `go/runner.go`, `go/agentdb/`, `go/events/`, `sandbox/`); see Appendix B for file references.

> **Provenance correction vs. the master spec / research seed.** The research seed (`AGENTS_RESEARCH.md` §5) called dynamic spawning + a manager-over-board "genuinely novel / under-served." The 2026 ecosystem scan refuted that: the pattern ships in Hermes, agent-kanban, agent-board, Operator, and others. We are *not* inventing it — we are adopting the verified design and building only our differentiators (scopes-not-personas curation, the conscience mechanism, containerized workers on agentkit, env-gated keys).

---

## 0. What this is

A design for turning agentkit's **single-session container runtime** into a **board-driven autonomous workforce**: a top-level **steward** (an LLM orchestrator) reads a board, decides priorities, and posts **mandates**; a **dispatch loop** launches each mandate as an Agent Orange session; workers do the work, **refer up** for consent, and write results back to the board; everything is durable in Postgres so any component can crash and restart as a non-event.

The design scope is the **full** system (steward + board + engage). Implementation is **phased** (§6.2) — this is more than one implementation plan; the first plan covers Phase A.

---

## 1. Architecture & component model

**The one rule:** the **board** (Postgres, alongside agentkit's `agentdb`) is the only durable coordination state. It holds **the plan** (mandates awaiting/under work) and the **ledger** (the append-only work record). Nothing else holds authoritative state in memory across a tick or a turn. Every component is a thin, *stateless* loop or client over the board, so crashes and restarts are non-events.

```
        ┌─────────────────────────── the board (Postgres) ───────────────────────────┐
        │  mandates (the plan) · mandate_runs · mandate_deps · ledger · memory         │
        └──▲───────────▲────────────────────▲────────────────────▲───────────────────┘
           │ read/write │ read/write          │ claim/update        │ append
    ┌──────┴─────┐ ┌────┴──────────┐  ┌───────┴────────┐   ┌────────┴─────────┐
    │ the steward│ │ workers       │  │ the dispatch   │   │ shifts & calls   │
    │ (a worker, │ │ (Agent Orange │  │ loop (dumb     │   │ (cron + event    │
    │ orch-tier  │ │  sessions —   │  │ daemon, NO LLM)│   │  intake; both →  │
    │  keys)     │ │  the floor)   │  │ promote·claim· │   │  "make ready")   │
    └────────────┘ └───────▲───────┘  │ launch·reclaim·│   └──────────────────┘
                           │ launches │ enforce budget │
                           └──────────┤ via Runner     │
                                      └────────────────┘ (embeds agentkit Runner = the floor)
```

- **The board** — durable truth. A mandate moves through a state machine (§2). The ledger is append-only; memory is a separate vectorized stream.
- **The dispatch loop** — a host-side daemon, **no LLM**, embedding agentkit's `Runner`. Each tick: promote → atomically claim → launch as a session → heartbeat/reclaim stale or crashed (via `Runner.Status`/`RunningSessions`) → enforce budgets. This is the piece agentkit lacks and the piece that must be boringly reliable.
- **Workers** — ordinary Agent Orange sessions (the **floor**). A worker is the archetype under a **mandate** (brief, keys, model, budget). It orients from the board, works, heartbeats, and ends by **completing** or **referring up**.
- **The steward** — *not special*: a worker under an **orchestrator-tier mandate** (keys to create/prioritise/assign mandates, never to do the work). The loop launches it on a **shift**. It reads the board, decides the next mandates, posts them, ends its turn. This is scopes-not-personas in its purest form: the same archetype, given the orchestrator's keys.
- **Shifts & calls** — triggering. A **shift** (cron) and a **call** (event/webhook) both resolve to the same act: make a mandate `ready`. They are board-writers; they hold no state and never launch sessions directly.

### Canon naming (fixed for this spec)

| Concept | Term |
|---|---|
| The Postgres coordination surface | **the board** (= the plan + the ledger) |
| A unit of work + its scope (≈ a ticket) | **mandate** |
| Post a mandate to the board | **engage** |
| The orchestrator-tier worker | **the steward** |
| Cron trigger / event trigger | **shift** / **call** |
| Escalate to the human | **refer up** / **consent** |
| The env-gated tool allowlist a mandate grants | **the keys** |
| An agentkit session / its container | **the floor** |
| Verify a delegated write before it counts as done | **attest** |
| Token/spend/depth bounds | **budget** `{tokens, spend, depth}` |
| Acting beyond the mandate | **overreach** |
| The human | **the owner** |

(Full master-spec glossary in Appendix A.)

---

## 2. The board: data model & state machine

Five tables, added to agentkit's Postgres, scoped by the same `customer`/`job` keys agentkit already uses (so a board is namespaced per goal/project for free).

### `mandates` — the plan + the fully-specified unit of work

A mandate carries its **own** scope; there are no personas to look up.

```
id · customer · job                      -- identity + board scope (ContextScope)
title · brief                            -- the narrowed objective + context slice
keys        jsonb                        -- tool allowlist + env gates (the keys)
model       text                         -- tier: full | mid | cheap
image / custom_image_id                  -- the floor to launch on
budget      jsonb  {tokens, spend, depth}-- bounds the labor
lane        text                         -- optional grouping label, for concurrency caps
priority    int  · depth int             -- ordering; depth for overreach caps
attest      text                         -- attest policy: none | auto | worker | owner
status      text                         -- the state machine (below)
claim_lock · claim_expires               -- the lease (atomic claim)
session_id · current_run_id              -- the agentkit session once launched
attempts · max_retries                   -- circuit breaker
blocked_reason                           -- set when needs-consent
scheduled_at · idempotency_key           -- delayed/shift mandates; dedup
created_by · created_at · updated_at     -- which mandate/steward posted it
```

### `mandate_runs` — one row per attempt (the audit of labor)

```
id · mandate_id · session_id · started_at · ended_at · heartbeat_at
outcome   -- completed | needs_consent | crashed | timed_out | reclaimed | failed | overreach
summary   -- 1–3 sentence human handoff
result    jsonb  -- structured handoff (the parent-handoff payload downstream mandates read)
attested_by · attested_at  -- who attested this write before it entered the ledger
exit_code · error
```

### `mandate_deps` — the plan DAG

`parent_id → child_id`. A child promotes to `ready` only when **all** parents are `done`; cycles are detected on insert (Hermes' proven rule). The **handoff**: a child reads each parent's last completed run's `summary` + `result`.

### `ledger` — append-only work record (the "progress ledger")

```
id · when · customer · job · by (mandate/run)
kind   -- transition (status change) | note (a progress comment for a human/steward)
text   -- natural language
ref    -- mandate_id / run_id
```

Bounded, structural, ticket-shaped. The dispatch loop and the plan-DAG depend on it.

### `memory` — the vectorized observation stream (separate from the ledger, by design)

A memory is a learned observation; one mandate can produce **many** memories, and a memory may belong to **none** (something learned about the world, not a ticket). It is *not* 1:1 with a mandate — which is exactly why it is its own table.

```
id · when · customer · job
text · weight (importance 1–10) · embedding (vector, pgvector)
source -- nullable ref: run_id | mandate_id | null
-- recall = recency · importance · relevance, scored per a worker's brief at engage time
```

### The state machine

```
        steward creates                deps done
  (—) ──────────────▶ triage ──────────────────▶ ready ◀───────────────┐
                        │ (steward holds/edits)     │                    │ reclaim (no penalty,
                        ▼                           ▼ loop claims         │ attempts<max)
                    abandoned ◀── budget/overreach claimed ──launch──▶ working
                    (or owner-cancel)        kill   │                    │
                                                    │ crash/stale ───────┘
                        ┌───────────────────────────┤
                  refer up │                         │ worker completes
                           ▼                         ▼
                     needs-consent              attesting ──reject──▶ ready (re-organise)
                     (await a call)                  │                or failed
                           │ owner consents          │ attested
                           └──────▶ ready             ▼
                                                    done ──▶ (promotes children)

   attempts==max  ·  protocol-violation (session ended w/o complete|refer-up)  ──▶ failed
```

### Invariants (enforced in SQL / the loop, not prose)

- **Atomic claim** = `UPDATE … SET status='claimed', claim_lock=$me, claim_expires=now()+ttl WHERE id=$id AND status='ready' AND claim_lock IS NULL`. Affected-rows is the compare-and-swap; at most one tick wins a mandate.
- **Attest before done** — a mandate that produced a write cannot reach `done` without `attested_by` (master spec §7.4). Unattested → back to `ready` (re-organise) or `failed`.
- **Budget/depth caps live on the row** — the loop kills to `abandoned` on breach (overreach is structural, not vibes). Depth is checked at create time.
- **Reclaim is penalty-free** (re-queue to `ready`) until `attempts==max_retries`, then `failed` (benign-reclaim vs. circuit-breaker split).
- **Everything durable** — a worker (incl. the steward) holds nothing across a turn; on relaunch it re-derives from its mandate row + parent handoffs + memory recall.

---

## 3. The dispatch loop + turning a mandate into a worker

### The loop (a host-side daemon embedding `Runner`, no LLM)

Each tick is **stateless** — it re-derives everything from the board, so the daemon can die and restart mid-tick with no corruption. Ordered steps (Hermes-proven sequencing):

```
every N seconds (default 60, configurable):
  1. RECLAIM CRASHED   — claimed/working mandates: ask Runner.RunningSessions()/Status();
                         session gone/destroyed/error → re-queue to ready (attempts++,
                         penalty-free), Runner.Stop the husk.
  2. RECLAIM WEDGED    — claim_expires < now AND no heartbeat in window → same re-queue.
  3. PROTOCOL VIOLATION— session reached query_complete/destroyed while mandate still 'working'
                         and the worker never completed/referred-up → outcome=crashed,
                         mandate → failed (or re-queue w/ note) up to max_retries.
  4. PROMOTE           — triage mandates whose deps are all 'done' → ready (cycle-safe).
  5. CLAIM + LAUNCH    — ready mandates in priority order, respecting per-lane caps and
                         Policy.MaxConcurrent: atomic CAS claim → build request → launch.
  6. BUDGET/TIMEOUT    — working mandates over {tokens|spend|runtime} → Runner.Stop → abandoned.
```

**Crash detection is better here than in Hermes.** Hermes guesses liveness from host-local PIDs (which breaks for containers — the reason we don't run it). agentkit gives `RunningSessions()`/`Status()` as an **authoritative** liveness probe over containerized sessions. Heartbeat-staleness is kept only to catch the *wedged-but-alive* worker (stuck in a long subprocess that still looks "running").

### Mandate → `CreateSessionRequest` (the launch)

```
CreateSessionRequest{
  SessionID:    deterministic(mandate.id, run.id)    // idempotent ⇒ resume just works
  SystemPrompt: workerProtocol + mandate.brief        // protocol block (§4) + the brief
  Model:        mandate.model                          // tier
  Image / CustomImageID: mandate.image                 // the floor, tools baked in
  Customer/Job: mandate.customer/job                   // board scope = ContextScope
  MaxTurns:     from mandate.budget                    // coarse turn bound
  Harness:      claude-agent-sdk
  Env:          { AO_MANDATE: mandate.id, ...keyGates } // ← the keys (see below)
}
```

Then `Runner.SendMessage(session, "take up your mandate")` and **do not block** — the worker runs autonomously. The loop records `session_id` + opens a `mandate_runs` row, and moves on. The worker's terminal state lands on the board *by the worker's own hand* (it calls `mandate_complete` / `refer_up`); the loop notices next tick.

### The keys = env-gated tools

Two mechanisms combine; **both are net-new in agentkit**:

1. **Per-session env injection.** Today agentkit injects env host-wide (`Policy.SessionEnv`); `CreateSessionRequest` has no per-session `Env` map. We add one — a small, clean Runner addition (the provision path already takes an env map; we thread per-session values through).
2. **Conditional tool registration in the sandbox harness.** Today the in-image harness loads *all* plugins from `PRODUCT_PLUGINS_DIR` — no env gate. We add the Hermes `check_fn` pattern: each tool plugin declares `requiresEnv`, and the registrar omits it from the model's schema unless that env var is set. So:
   - `AO_MANDATE=<id>` set → the **worker-tier** board tools appear.
   - The steward's **orchestrator-tier** board tools appear only under the orchestrator env flag (3-tier visibility: worker / steward / none).
   - `mandate.keys` → per-tool env flags → exactly the entrusted product tools appear, and no others. Spend/credential tools are simply never gated on at a leaf — "the keys = blast radius," enforced by **absence from the schema**, not by asking the model nicely.

> Gate against the *resolved* tool list, not the raw config (the Hermes `["all"]`-alias bug — Appendix C).

### Worker → board writes: through an API, not direct SQL

The worker's board tools (an MCP server baked into the image) call a small **board HTTP API** authenticated by the **scoped session token agentkit already issues** — *not* a direct Postgres connection. This keeps DB credentials out of the container (the same philosophy as agentkit's model proxy), and lets the API authorize every write against the caller's mandate (a worker can only complete/heartbeat/refer-up *its own* mandate; only orchestrator-tier can create/assign).

---

## 4. The worker lifecycle protocol + the board tool surface

### Board tools (MCP, baked into the image, gated by env tier)

**Worker-tier** (appear when `AO_MANDATE` is set):

| Tool | Does |
|---|---|
| `mandate_show()` | **Orient.** Returns the brief, the keys/budget, each parent's last-completed-run handoff (`summary`+`result`), prior attempts, and **memory recalled for this brief** (recency·importance·relevance). Ground truth. |
| `mandate_heartbeat(note?)` | "Still alive" — refresh the lease during long work. |
| `mandate_complete(summary, result, artifacts?)` | **Terminal.** Structured handoff. Moves mandate → `attesting` (or `done` if attest policy is `none`). |
| `refer_up(ask, options?, blocking?)` | **Consent gate.** Needs a credential / money / a decision beyond the mandate → mandate → `needs-consent`, owner notified out-of-band. Backed by agentkit `ask_user`. |
| `record(text)` | Append a progress note to the **ledger** (work record). |
| `note(text, weight?)` | Append an observation to the **memory** stream (recalled for future mandates). |

**Steward-tier** (appear only under the orchestrator env flag — hidden from workers):

| Tool | Does |
|---|---|
| `mandate_create(title, brief, keys, model, image, budget, parents?, lane?, priority?, attest?)` | **Engage.** Post a fully-specified mandate. Returns its id. |
| `mandate_link(parent, child)` | Add a dependency edge (cycle-checked). |
| `mandate_list(filter)` | Survey the board. |
| `mandate_prioritise(id, priority)` / `mandate_abandon(id, reason)` | Re-organise the plan; cancel overreaching/obsolete work. |

**Owner actions are not agent tools** — consent (answer a `refer_up`), cancel, and edit arrive as **calls** (from the UI or an out-of-band reply); the loop resumes the mandate. An agent can never grant its own consent.

### Attestation is just another mandate (no special subsystem)

When a completing mandate's policy is —
- **`auto`** → the loop runs a mechanical check (tests/validation), writes the verdict.
- **`worker`** → the loop **engages a verify-worker**: an ordinary mandate whose brief is "attest this output against this brief," whose keys are read-only + `attest_verdict(pass|fail, reason)`. A leaf worker like any other.
- **`owner`** → it becomes a `refer_up` to the owner.

Pass → `done` (children promote). Fail → back to `ready` with the reason, to re-organise. No bespoke review engine — attestation reuses the board, the loop, and the archetype. This keeps "one archetype."

**Attest ≠ refer-up.** Attest is a *quality gate on output already produced* ("is this work correct?"). Refer-up is a *permission gate on an action not yet taken* ("may I spend / use this credential / post publicly?"). A mandate can hit both: refer-up during the work, attest after.

### The protocol injected into every worker (canon voice — lore all the way to the API)

Prepended to the worker's system prompt; functional *and* the conscience mechanism in motion.

```
You hold ONE mandate. It is in your tools as AO_MANDATE.

  Orient.   Call mandate_show() first. The brief, your parents' handoffs, and what
            you've been given to remember are your ground truth.
  Work.     Inside your floor, with the keys you were entrusted — only those. If a
            tool you want isn't here, you were not given it. That is deliberate.
  Heartbeat.On long work, mandate_heartbeat() — or you'll be reclaimed and lose progress.
  Refer up. Need a credential, money, or a decision that isn't yours to make?
            refer_up() and stop. Do not guess. Do not act in the owner's name.
  Finish.   End with mandate_complete(summary, result). Your result is the handoff the
            next worker reads — make it true. If it still needs eyes you don't control,
            say so plainly; don't bless your own work.
  Don't sprawl. Follow-up work you can see but weren't asked to do: name it in your
            result (or, if you hold the steward's keys, mandate_create it for the right
            hands). Never wander outside your brief — that is overreach.
  Remember. note() what's worth keeping. The board is your only memory; you may be
            snapshotted or reclaimed at any moment.
```

---

## 5. The steward, shifts & calls, refer-up, engage, reliability

### The steward = a standing mandate on a shift

The steward is the archetype under a long-lived **orchestrator mandate** that a **shift** re-engages on a schedule. Each turn is stateless:

```
mandate_show()           -- my orchestrator mandate carries the goal G
+ mandate_list(filter)   -- survey the board: done / in-flight / blocked / failed
+ memory recall          -- what I've learned toward G
→ decide the next mandates (depth-first; fan out only independent/read-only work)
→ mandate_create / link / prioritise / abandon
→ mandate_complete(summary of decisions)   -- end the turn; hold NO state
```

The board *is* its plan (master spec §6 statelessness mitigation, realized). Two on-message properties fall out: the steward **re-organises on failure** (it *sees* the failed mandate next turn and re-plans, rather than blind-retrying), and the steward **cannot act in the owner's name** — its keys are plan-shaping tools only. Even the manager can't spend or post; only the owner can, via consent.

### Shifts & calls are board-writers (one launch path)

Neither launches a session directly — they only make a mandate `ready`; the loop does the rest:

- **Shift** (cron) → at time T, mark the due mandate `ready` (or open a steward turn). A tiny scheduler; writes the board, nothing more.
- **Call** (webhook/email/queue) → an HTTP intake translating the event into a board write: *create* a mandate ("a comment arrived — respond"), *resume* a `needs-consent` mandate (the owner's reply), or wake the steward.

Everything funnels through `ready → loop claims → launches`. One launch path is why the system stays reliable: exactly one way work starts.

### Refer-up / consent (the agentkit payoff)

1. Worker `refer_up(...)` → mandate `needs-consent`, `blocked_reason` set, ledger note, **owner notified out-of-band** (a small host notify concern).
2. **The waiting worker is snapshotted and its container reaped** (agentkit idle-reaper) — a blocked mandate costs nothing while it waits days for the owner. The mandate holds the state.
3. Owner answers via a **call** → mandate resumes. Primary path: **`Runner.Resume` restores the snapshot and delivers the answer as the `ask_user` reply — the worker continues with full in-progress context.** Fallback (snapshot gone): re-engage a fresh worker with the answer in its brief.

Suspend-a-blocked-worker-for-days-then-resume-it-exactly is something Hermes structurally cannot do (it respawns fresh). It is the concrete payoff of building consent on agentkit's snapshot/resume. **Mandatory** refer-up for: credentials, spending money, irreversible/external actions, out-of-mandate decisions (master spec §7.5).

### Engage is async, via the board

`engage` = the steward calls `mandate_create` — it **posts a row, it does not block**. The loop claims and launches the child. The parent gets the child's work back **through the board**: the child's completed run (`summary`+`result`) is read by the steward on a later turn, or as a **parent handoff** to a dependent mandate.

This is a deliberate divergence from the master spec's §5.1, which had `engage` return `{result, trace, ledgerWrites}` *synchronously*. We chose Approach A (board-centric), so the trace flows back **asynchronously through the board**. Caps still hold at create time: child `depth = parent.depth + 1` (rejected past the cap), child budget drawn from the parent's allocation, child keys ⊆ what the steward may grant — spend/credential keys cannot be granted to a leaf.

> **Deferred (Approach C):** a *synchronous in-run subagent* (for the steward's own short, read-only decomposition reasoning) would need a real engine addition — `EngageSubagent` + parent/child session linkage. Out of scope until the async board path is proven.

### Reliability rules (master spec §7) — where each is enforced

| Rule | Enforced by | Mechanism |
|---|---|---|
| 1. One archetype, many mandates | structural | steward, workers, verify-workers are all the archetype under different keys |
| 2. Depth-first; parallel only independent/read-only | steward + loop | DAG sequencing + per-lane concurrency caps |
| 3. Share full traces | the board | child run + ledger + `result` handoff, readable by parent/steward |
| 4. Attest every delegated write | the `attesting` state | cannot reach `done` unattested (auto / verify-worker / owner) |
| 5. Keys = blast radius | env-gated tools | spend/credential absent from a leaf's schema → bubble to refer-up |
| 6. Bounded autonomy | the mandate row + loop | `{tokens, spend, depth}` budgets; loop kills breach → `abandoned`/overreach |
| 7. Downgrade only at verifiable leaves | steward policy | `model` tier paired with an `attest` policy |

Each default is disabled **only by a loud, logged override** — the conscience mechanism (a budgetless mandate, or spend-keys at a leaf, logs that the owner chose the bad branch, on the record).

---

## 6. Net-new vs. reused, phasing, open questions

### 6.1 What we build vs. what agentkit gives us

**Pure reuse (no changes):** the `Runner` surface (`CreateSession/SendMessage/Stream/Snapshot/Resume/Stop/Status/RunningSessions`) = the floor · session lifecycle + **snapshot/resume** (powers suspend-while-blocked) · the event pipeline + `agentdb`/Postgres persistence · `ask_user` (backs `refer_up`) · `ContextScope` (board namespacing) · the harness + model proxy + scoped session tokens.

**Net-new (the build list):**
1. **Board schema** — 5 Postgres tables alongside `agentdb`.
2. **Board HTTP API** — token-authed write surface; authorizes by mandate + tier.
3. **Board MCP toolset** — the `mandate_*` tools, baked into the image, env-tier-gated.
4. **Env-gated tool registration** in the sandbox harness (the `check_fn` pattern) — *engine/sandbox change*.
5. **Per-session `Env`** on `CreateSessionRequest` — small Runner addition.
6. **The dispatch loop daemon** — promote/claim/launch/reclaim/budget. The dumb control plane.
7. **The scheduler (shifts)** — tiny cron→board-writer.
8. **The call intake** — HTTP→board-writer + out-of-band notify.
9. **The steward** — orchestrator mandate prompt + keys (mostly content).
10. **Memory store + recall** — pgvector; recency·importance·relevance scoring.
11. **Attest machinery** — auto-check runner + verify-worker brief template + `attest_verdict`.

**Deferred (named, not built):** synchronous in-run subagents (Approach C — `EngageSubagent` + parent/child linkage); full art-voice polish of every error/log line (layer once functional).

### 6.2 Phasing (one coherent design, *not* one build)

- **Phase A — Board + one worker, hand-driven.** Tables + API + MCP toolset + env-gated keys + per-session `Env`. The owner posts a mandate by hand; the worker orients/works/completes/refers-up; consent→resume works. No loop, no steward — **the owner is the dispatcher.** Proves the board↔worker contract, the keys, and consent end-to-end. *(≈ master Phase 0, re-grounded on agentkit.)*
- **Phase B — The dispatch loop.** The dumb daemon; mandates dispatch themselves. Add `auto` attest. *(the missing control plane.)*
- **Phase C — Shifts & calls.** Scheduler + call intake + notify. Runs continuously. *(≈ master Phase 0 triggering.)*
- **Phase D — Steward + engage + memory.** Orchestrator mandate, the DAG/handoffs, memory recall, verify-worker attest. Depth-first, sequential. *(≈ master Phase 1.)*
- **Phase E — Bounded fan-out.** Parallelize independent/read-only mandates via lanes/caps. *(≈ master Phase 2.)*

(Runtime "hiring" / dynamic mandate templates — master Phase 3 — stays out of this spec.)

The first implementation plan covers **Phase A** (and likely **B**).

### 6.3 Open questions (resolve before/while building)

1. **Board scope granularity** — one board per `(customer, job)`? A "goal" = a long-lived steward mandate within a job-scoped board?
2. **Consent/notify channel** — likely the existing agentkit chat UI *is* the consent surface (the `ask_user` card already renders there; a call = the UI reply). Confirm, plus any out-of-band nudge (email/Slack) for long waits.
3. **Spend accounting** — token usage comes from agentkit's `TokenUsageLogger`; external $ spend (paid tool/API costs) needs a reporting convention from tools.
4. **Memory policy** — who scores importance (worker via `note(weight)` vs. an LLM pass), recall budget per engage, dedup.
5. **Home of the orchestration layer** — the engine (`go/`) must stay generic (liftability invariant). The dispatch loop + board API + steward are a **host app** — a new package under `examples/`, or its own repo depending on the engine? (The two Runner additions — per-session `Env`, later `EngageSubagent` — *do* land in the engine; they're generic, so the invariant holds.)
6. **Attest defaults** — default policy per mandate kind, and which mandates demand `owner` attest.

---

## Appendix A — Canon glossary (from the master spec §4)

| Engineering concept | Agent Orange term |
|---|---|
| The agent archetype | **worker** |
| Scope contract | **mandate** |
| Spawn / delegate a child | **engage** (Phase 3: *hire*) |
| Orchestrator | **the steward** |
| Plan / what's known | **the plan** |
| Progress record | **the ledger** |
| Long-term memory stream | **memory** (broken out from the ledger — see §2) |
| Verify-before-accept | **attest** |
| Escalate to human | **refer up** / **consent** |
| Tool scope = blast radius | **the keys** |
| Budget | **budget** `{tokens, spend, depth}` |
| Runaway autonomy | **overreach** |
| The container / Linux env | **the floor** |
| Cron trigger | **shift** |
| Event trigger | **call** |
| The user | **the owner** |

## Appendix B — agentkit seam references (this repo)

- **Runner API** — `go/agentkit.go` (`Runner` interface, `CreateSessionRequest`, `Policy`), `go/runner.go` (`CreateSession`/`Resume`/`Snapshot`/idle archive loop).
- **Persistence** — `go/agentdb/` (`Store` over Postgres/GORM; `Session`/`Message`/`QueryEvents`/`Artifact` types), `RunnerStore`/`extension.SessionStore` seams.
- **Events** — `go/events/events.go` (canonical vocabulary incl. `ask_user`, `subagent_event`, `query_complete`), `EventPipeline`.
- **Human-in-loop** — `sandbox/src/tools/builtin/ask_user.ts` (the `ask_user` tool + marker → SSE event → UI card → resume via `SendMessage`).
- **In-image harness / tool loading** — `sandbox/src/config.ts` (env-driven config), `sandbox/src/tools/load-plugins.ts` (plugin dir loading — **no env gate today**; gap #4), `sandbox/src/harness/bootstrap.ts` (`resolveHarness` credential gating — the pattern env-gating mirrors).
- **Multi-tenancy** — `go/extension/extension.go` (`ContextScope{Customer, Job, Persona, UserEmail}`).
- **Triggers** — *confirmed absent* in `go/`; the host calls the Runner. The dispatch loop, scheduler, and call intake are net-new host components.

## Appendix C — Sources

- Hermes (NousResearch) — board/dispatcher/worker-protocol reference: `https://hermes-agent.nousresearch.com/docs/user-guide/features/kanban`; source `github.com/NousResearch/hermes-agent` (`kanban_db.py`, `gateway/kanban_watchers.py`, `tools/kanban_tools.py`, `toolsets.py`, `agent/prompt_builder.py`). The `["all"]`-alias gate bug: issue #35581.
- Master spec — `badcode/docs/superpowers/specs/2026-06-20-agent-orange-design.md`.
- Research substrate + verified findings (MAST, Cognition, Magentic-One, MemGPT, Generative Agents) — `badcode/docs/AGENTS_RESEARCH.md`.
