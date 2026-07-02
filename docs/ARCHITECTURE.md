# Agent Orange — Architecture Guide

**Date:** 2026-06-26
**Status:** Authoritative architecture overview. Consolidates and supersedes (as the thing to build
against) the scattered research/decision docs:
the design seed, the stack decision, and four exhaustive landscape surveys (~1,100 systems). Those
remain the evidence; this is the plan. **Doc map + reading order: [`AGENTS_DESIGN.md`](AGENTS_DESIGN.md).**

---

## 1. What Agent Orange is now

**Agent Orange is an opinionated, single-tenant MANAGER-as-HR layer (in Go) that runs an autonomous
agent organization for general-purpose business goals — over a pluggable worker runtime.**

A human sets a vague goal ("grow engagement for the art project"). A continuously-running **manager**
interviews them into a spec, decomposes it into a **kanban board** of tickets, and — like an HR
function — **provisions each worker agent**: composing its system prompt, granting it scoped skills
and memory access, launching it in a sandbox, reviewing its output, and escalating to the human when
it must. Over time the manager *learns* — curating a labeled memory, growing a skill library, and
keeping the documentation that trains future workers up to date.

### What changed (the pivot)

We were migrating Platinum's `agentkit` runtime wholesale (the `phase4-gcp-seams` branch: DinD, image
registry, blobarchive snapshots). Three findings redirected us:

1. **Single-tenancy deletes the heavy machinery.** One trusted org running many projects means we
   need *namespacing*, not multi-tenant hardening — so DinD isolation, image GC, egress allowlists,
   and snapshot pipelines are undifferentiated weight we can shed.
2. **The worker runtime is now a commodity.** OpenHands (and others) provide the containerized
   harness + pause/resume + Docker-image env off the shelf. We *adopt* layer 1 instead of finishing
   the hoist.
3. **The memory/HR substrate is also a commodity; only its governance is novel.** A 227-system
   survey showed the store, MCP surface, skill loaders, and learning patterns all exist. The
   integrated *manager-as-HR governance loop* is the genuine white space — and that's what we build.

Net: **buy the substrate, steal the patterns, build the governance.**

---

## 2. Design principles

1. **Scopes, not personas.** One agent archetype, recursively invoked under many *scopes* (objective
   + context slice + tools + model + budget). The manager is the same archetype holding the board.
2. **Single-tenant by project.** A **Project** namespaces everything (board, memory namespace, base
   images, tool/credential policy, goals). Cross-project access is simply not wired — no defended
   trust boundary needed.
3. **Stateless between triggers.** The manager holds no long-lived in-process state. Every trigger
   re-derives all state from the board + memory, acts, and persists. A crash loses nothing.
4. **Fire-and-forget workers.** The manager enqueues work and reacts to results *between* runs, never
   supervising a live session. (Decided — §6.)
5. **Buy the substrate, behind a seam.** Runtime and memory store are adopted third-party components
   behind narrow Go interfaces (`WorkerRuntime`, `MemoryStore`) — one implementation now, swap later.
6. **The manager proposes, a gate disposes.** Every self-modifying act (prompt docs, labels, skills)
   is versioned, pinned per session, and passes an eval/approval gate before it can affect workers.
7. **Verify before promote.** Self-authored skills and graduated memories must pass validation
   (sandbox/eval/security-scan) before workers may use them.
8. **Build the novel/risky parts last, on a proven base** (per the staged roadmap, §12).
9. **Mechanism vs. policy.** The event *substrate* (emitters/subscribers + the ability to run a
   scoped worker) is fixed and cheap; the event→reaction *bindings* are **learned and chosen at
   runtime**, never authored as static DAGs (§6A).

---

## 3. The system at a glance

```
            ┌────────────────────────────────────────────────────────────────┐
   HUMAN ──▶│ TRIGGER (Go): cron schedules + event intake (webhook/email/queue)│
   goal     │   each fire → "run one MANAGER exchange"                          │
            └───────────────────────────┬────────────────────────────────────┘
                                         ▼
   ┌──────────────────────────────────────────────────────────────────────────────┐
   │ MANAGER  (Go, the part we BUILD) — one archetype, holds the goal                │
   │   • interview→spec · decompose→tickets · Task Ledger + Progress Ledger          │
   │   • HR: compile worker prompts · assign skills · curate labeled-memory taxonomy │
   │   • verify In-Review tickets · escalate_to_human · re-plan on failure           │
   │   reads/writes ▼            enqueues ▼                 reads/writes ▼           │
   │   ┌──────────────┐   ┌──────────────────────┐   ┌──────────────────────────┐  │
   │   │ BOARD (Pg)   │   │ WORK QUEUE           │   │ MemoryStore SEAM (adopt) │  │
   │   │ tickets+state│   │ fire-and-forget jobs │   │ Zep/mcp-memory-service…  │  │
   │   └──────────────┘   └──────────┬───────────┘   │ + Skill registry (SKILL  │  │
   └─────────────────────────────────┼───────────────│   .md) + Prompt fragments│  │
                                      ▼               └────────────┬─────────────┘  │
   ┌──────────────────────────────────────────────────────────┐   │ exposed via MCP │
   │ WorkerRuntime SEAM (Go iface) — adapters consume the queue │   ▼ to all agents  │
   │   Spawn(Scope)→Session · Resume(id) · Events · Pause · Result                   │
   │   impl #1: agentkit-runtime   impl #2 (later): OpenHands   adapters: Hermes/E2B │
   └───────────────────────────┬────────────────────────────────────────────────────┘
                               ▼
   ┌──────────────────────────────────────────────────────────┐
   │ WORKER (a sandboxed agent session)                        │
   │   runs the Scope: bash/files/code/MCP tools (incl. memory)│
   │   on finish → writes {result, trace, memory_writes} back  │
   └──────────────────────────────────────────────────────────┘
```

**What we BUILD:** the manager loop, the board, the HR governance (taxonomy curator, skill assignment,
prompt composition), the trigger intake, the verification harness, and the two seams.
**What we ADOPT:** the worker runtime (agentkit-impl first, OpenHands later), the memory store +
MCP surface, the SKILL.md skill standard + loader.
**What we STEAL (patterns, not code):** A-MEM auto-labeling, AWM/ReasoningBank consolidation, Voyager
skill-validation, ACE delta-prompt-updates, Zep/yantrikdb provenance, Magentic-One ledgers.

---

## 4. The Project (namespace)

Everything lives under a **Project**. A Project owns: one **board**, a **memory namespace**, its
**base image(s)**, its **tool/credential policy**, its **skill library subset**, and its **goal(s)**.
This is the single-tenant boundary — namespacing, not hardened isolation. (Maps onto the engine's
existing `ContextScope{Customer, Job, …}`, with Project as the top scope.)

---

## 5. The Board (the manager's durable state)

The board **is** the persisted state of the manager (enabling principle #3). Tickets flow:

```
Backlog → Todo → In Progress → In Review → Done
                     │
                     ├──▶ Blocked       (a depends_on ticket isn't Done)
                     └──▶ Needs-Human    (escalation lane)
```

```
Ticket {
  id, project_id, title
  objective        // the narrowed spec slice (= Scope.objective)
  acceptance       // verifiable acceptance criteria, written at PLAN time (the non-code oracle)
  depends_on []    // DAG, cycle-checked
  status           // state above
  scope            // how to invoke the worker (§7)
  result           // structured result + trace ref
  memory_writes [] // what the worker learned (graduated per §8)
  budget           // token/spend/depth ceilings
  parent           // workers may create subtasks → hierarchy
  attempts         // re-plan (don't retry-in-place) past a cap
}
```

Roles: **Human** (sets goals, answers escalations), **Manager/Leader** (plans, assigns, verifies),
**Worker** (archetype under a scope). Stale workers are reaped (mark offline after a timeout).

---

## 6. The Manager loop + the fire-and-forget handoff

**The exchange** (what every trigger runs — cron or event resolve to the same thing):

```
load(board, memory)                 # re-derive all state; nothing held across triggers
→ reconcile progress ledger         # reap stale workers, advance blocked→todo as deps clear
→ verify In-Review tickets          # check result vs ticket.acceptance → Done | re-plan
→ choose next scope(s)              # depth-first by default; parallel only for independent/read-only
→ compile + enqueue worker jobs     # HR: build prompt, assign skills (§9/§10), push to WORK QUEUE
→ persist(board, memory)            # exchange ends; manager goes idle
```

The manager runs **Magentic-One's dual ledgers**: a **Task Ledger** (the spec + board / what's known)
and a **Progress Ledger** (live status). On failure it **re-plans** rather than retrying in place.

**Fire-and-forget handoff (decided).** The manager never supervises a live session:

1. Manager enqueues `Job{ticket_id, scope}` onto the **work queue**.
2. A **runtime adapter** (worker pool) dequeues, calls `WorkerRuntime.Spawn(scope)`, drains the
   session's `Events()` into the event log (for the live UI/observability only), waits for `Result()`,
   then writes `{result, trace, memory_writes}` back and sets the ticket **In-Review**.
3. The manager sees the result on its **next tick** and verifies it.

The manager can *observe* the stream and *cancel* a ticket (the adapter checks and pauses/kills), but
it makes **decisions between runs, not during them**. A worker that gets stuck **escalates** (writes a
Needs-Human ticket) and ends — escalation is "persist a ticket + end," resumed by the human's reply
event. This keeps the system durable, restart-safe, and free of fragile long-lived coordination.

---

## 6A. Event sources, dynamic workflows & the self-improving manager (the Consultant)

This is the layer that makes the org *self-improving* rather than merely *automated*. It rests on
principle #9: **separate the mechanism (what the system *can* do when things happen) from the policy
(what it *chooses* to do) — fix the first, learn the second.**

### Event sources & the event bus
Everything that happens is an **event** on a bus, produced by an **EventSource** and consumed by
**subscribers**. Two families:
- *External* events: `human-goal`, `email-arrived`, `webhook`, `metric-threshold`, `schedule-tick`,
  `human-reply`.
- *Lifecycle* events the system emits about itself: `session.completed`, `ticket.done`,
  `summary.completed`, `escalation.raised`.

A **subscription** says "event E *may* trigger reaction R" — but a subscription is a *candidate*, not
a hardwired rule. The actual binding is decided at runtime (below).

### Mechanism vs. policy — why we never compile static DAGs
Hardcoding "on event E run pipeline P" is just writing software to anticipate every scenario — the
rigid-waterfall trap (MetaGPT/ChatDev). Instead:
- **Mechanism (fixed, cheap):** the bus, the catalogue of possible scopes, and the ability to run a
  scoped worker (memory access + composed prompt + tools) in response to an event. This is the
  substrate — the same one a company boss has: "when X happens, I can put someone with this brief on it."
- **Policy (learned, dynamic):** a **routing scope** decides, per event, *which* reaction to run, by
  **consulting the history of past events, reactions, and outcomes** — "what did we do last time
  something like this arrived, and did it work?" This is textbook **Case-Based Reasoning**
  (Retrieve → Reuse → Revise → Retain). The DAG is therefore **emergent** — a runtime trace of
  event→route→scope→event→… — not an authored graph. We never fix the DAG; we **populate a history of
  choices and let the routing layer choose afresh** each time.

```
event ──▶ [routing scope] ──consults──▶ event/reaction/outcome history (CBR case base)
              │ picks a reaction (reuse a winning approach; avoid a known-bad one)
              ▼
          run scoped worker ──emits──▶ lifecycle event ──▶ [routing scope] ──▶ …   (chain = emergent DAG)
```

### Post-event summary memories & summary bots
A concrete, high-value subscriber: on `session.completed`, run a **summary bot** — a *cheap-model
scope* (e.g. Sonnet) with a tight toolset (`read_memories`, `add_memory`) whose objective is *"here is
what happened in this thread; extract the valuable, reusable insights and record them"* — not dump raw
events. This is the §8 learn-from-session loop, expressed as just another event-triggered scope.
- **What decides the labels?** The summary bot **proposes labels conditioned on the curated controlled
  vocabulary** (A-MEM neighbourhood-conditioned generation, §8) — not free-form per memory. Novel
  labels are *suggestions* the taxonomy curator reconciles/merges and the gate ratifies into the
  vocabulary; importance-gating decides *whether* a memory is worth keeping at all.
- **Flavours:** several summary-bot scopes can exist with different foci — general insight,
  **documentation extraction**, strategy extraction. New flavours are introduced by the Consultant
  (below), not hardcoded.
- **Chaining:** the summary bot emits `summary.completed`, which other subscribers can route on — so a
  DAG forms by *composition*, not declaration.

### The Consultant (the self-improvement persona)
Because every event and reaction is recorded, a meta-scope — **the Consultant** — periodically (cron,
or on accumulated-signal events) **mines the event/reaction/outcome history for patterns and proposes
process changes.** The motivating example (the feature that started this whole project): the Consultant
notices *"three times we re-did work because we failed to find existing documentation,"* diagnoses it,
and proposes *"add a documentation-extraction summary-bot subscription on `session.completed`."*

Crucially, **the Consultant proposes; it does not act directly.** Every proposal — a new subscription,
a new summary-bot flavour, a prompt-fragment edit, a skill change, a taxonomy/doc update — goes through
the **gate** (eval/approval/versioning/rollback, principle #6). This is "the manager proposes, a gate
disposes" applied to *the process itself* — how the org rewrites its own playbook safely.

### The decision substrate
A persisted `events` + `reactions` + `outcomes` store (Postgres) is the **CBR case base**: every event,
the route chosen, the scope run, and the eventual outcome. The routing scope queries it to *choose*;
the Consultant mines it to *improve*; the §11 verification harness scores outcomes so "did it work?"
is answerable (proxy/judge, since non-code).

### Risks specific to this layer (see also §14)
- **Credit assignment** (the killer): attributing an outcome to a strategy with no clean oracle.
  Mitigate with proxy metrics + logged confounders; accept it's statistical.
- **Ossification / overfit:** always reusing the known-good strategy stops discovery — keep an
  explicit explore-vs-exploit budget (CBR's *Revise*).
- **Runaway self-modification:** a Consultant editing the process unchecked is the dynamic-hiring
  failure surface in new clothes — the gate + budgets are mandatory, and the Consultant comes *last*.
- **Event storms / loops:** emergent chaining can cycle (A emits→B emits→A…) — need loop detection,
  per-chain depth/budget caps, and idempotent subscriptions.

### Prior-art blueprints (from `AGENTS_SELF_IMPROVING_LANDSCAPE.md`)
A 248-system survey confirmed every pillar exists, but **the intersection — CBR-routed,
Consultant-driven self-improvement for a *general (non-code)* org — is empty.** Adopt/steal map:
- **Event bus/log:** *adopt* **Temporal** (Go SDK, event-sourced, durable replay) or Kafka/Pulsar;
  *steal* **StreamNative**'s "agents declare produced/consumed events; topology **emerges** from
  subscriptions" — exactly our "attach a summary/Consultant bot to a running stream later."
- **CBR router:** *build in Go* on the **CASCADE** blueprint — the 4R cycle with a **contextual-bandit
  retrieval policy** (principled explore/exploit) that **learns only the retrieval policy, model
  frozen** (small, auditable learning surface, no oracle required).
- **Summary bots:** the **ReasoningBank** spec — distil *titled, typed* reusable items from
  **successes *and* failures**; mint negative "when X, don't do Y" memories, each keyed with an
  **applicability condition** so the router matches on event context, not just embedding similarity.
- **Consultant:** **ADAS** archive (propose against prior designs; pre-score before expensive eval) +
  **ACE** discipline (evolve a *playbook*, never code; separate generator/reflector/curator roles) +
  **Darwin Gödel Machine** (keep an archive; retain only empirically-validated, reversible changes).
- **The load-bearing caveat:** every adoptable self-improvement result silently assumes a
  **verifier/oracle** (tests, benchmark score) we do **not** have for non-code goals — the field's
  wins are coding/benchmark-scoped. So **credit-assignment is *the* engineering problem**: invest
  disproportionately there (LLM-judge with self-contrast, **executor ≠ distiller ≠ verifier** role
  separation, downstream proxies + human spot-checks, and store each outcome's *uncertainty* with the
  case).

---

## 6B. The operating model: Routing Manager, pub/sub vs pipelines, staff & the architecture board

§6 (the loop) and §6A (the event substrate) describe the machinery; this is the concrete operating
model that runs on them, in the primitives we'll actually build.

> **Superseded in part by §6D (2026-06-29).** The *staff-template* and *pipeline-definition* nouns
> below were a useful first pass, but **§6D collapses them** (and the prompt-fragment / label-vocab /
> event-taxonomy structures) into a single versioned text **`fragments` KV**, keeping only what the
> runtime dispatches on (subscriptions, memory labels, enforced spawn caps) as structured. Read §6D
> for the authoritative primitive set; the framing here remains valid, the rigid schemas do not.

### The low-level primitive (recap)
Everything the org does is **an agentic session** = a prompt + a **tool config** (which *is* its
memory access, skills, model, budget — the Scope of §7). Two coordination tools, exposed via MCP:
- **`emit_event(type, payload)`** — fire an event onto the bus. A worker's prompt tells it what to
  emit when done ("once you've produced the draft, `emit_event('draft.ready', {ref})`"). **What a
  session emits is purely a function of its prompt** — so wiring lives in prompts, not code.
- **`run_pipeline(stages)`** — run an explicit ordered sequence of sessions, each stage's output
  feeding the next.

### Two coordination modes — and when each fits
- **Pub/sub (broadcast)** = *standing, org-wide* reactions. "Whenever `thread.complete` fires, the
  archival expert reacts." Emitter doesn't know who listens (loose coupling). For *"we always do X
  when Y happens."*
- **Pipeline** = a *job-specific ordered sequence*, output→input. "For *this* event:
  research → draft → review." Tight coupling, one job.

Both exist; a pipeline can also *emerge* from pub/sub (a stage emits, the next subscribes), but when a
guaranteed sequence is needed for one job, `run_pipeline` is the explicit construct. **Sequences are
never forced through broadcast — the Routing Manager picks the right primitive per event.**

### The Routing Manager (the single triage brain = the CBR router)
There is **one** Routing Manager (keep it simple). It's an agentic session triggered on **any** event.
Its prompt: *"You are an expert operations manager. Given this event, the organization's current
configuration, and our history, decide how to handle it — coordinating which resources in which
sequence."* Decision space: **ignore/archive**, **single worker** (`emit_event` to wake one staff
member; tell it what to emit when done), or **pipeline** (`run_pipeline([...])`).

It decides by **Case-Based Reasoning**: it's given the **history of past (event → decision → outcome)
cases** and reuses what worked / avoids what didn't (§6A; CASCADE blueprint). When a `human-goal`
event arrives, "handle it" routes into the §6 planning exchange (interview→spec→board) — so the §6
manager loop is *one of the things the Routing Manager can invoke*, not a separate brain.
Inputs: the event, the **architecture board** (below), the live subscriber map, the decision+outcome
history. Primitives: `emit_event`, `run_pipeline`.

### Staff members (specialists = reusable scope templates)
A **staff member** is **not a persistent process** — it's a *named, reusable Scope template*
(consistent with "scopes, not personas"): `{role prompt, skill set, model tier, memory
filter/namespace, event subscriptions, self-archiving strategy}`. Each time a staff member acts, a
fresh session is spawned from its template. Two things make it a *specialist*:
1. **A focused memory view + self-curated archiving.** Each staff member decides what's worth
   remembering *for itself*. The legal expert's archiving prompt: *"if this isn't law-related, ignore
   it; if it is, extract the case, jurisdiction, holding…"* — so each expert maintains its own
   personalised slice of the one shared, provenance-tracked store.
2. **Event subscriptions** — which events it reacts to, and how.

*Worked example (the archival expert):* a staff member subscribed to `thread.complete`, prompt:
*"retrieve your top-5 closest past archiving decisions, look at the labels you used, archive this
accordingly."* That is a **CBR loop at the staff level** — it gets better at labeling as its own
decision-history grows. (The §6A summary-bot, realized as a learning specialist.)

### The Architecture Board (the live org config)
The **architecture board** is the registry of *how the org is currently wired*: which staff members
exist, what each subscribes to, what pipelines are defined, and the event taxonomy. The Routing
Manager **reads** it to decide; the Consultant **proposes changes** to it (through the gate). It is
the org chart + subscription map *as data* — the thing the self-improvement loop actually edits.

### The Consultant watches the Routing Manager
The §6A Consultant's subject is now concrete: it continuously reviews the Routing Manager's stream of
**(event → decision → outcome)** cases, asks *"is this arrangement of staff and pipelines working?"*,
and feeds guidance back into the Routing Manager's context + proposes architecture-board changes —
all through the gate. The top-level heartbeat = the Consultant on a cron.

### The outcome signal (bootstrap the missing oracle)
CBR routing and the Consultant both need to know *"did it work?"* — our hardest problem (§11/§14).
**We bootstrap with a human-supplied numeric quality score** per resolved ticket/event: a small,
enforced, manual signal that *manufactures* the oracle the field's self-improvement methods assume. We
proceed on the basis that a numeric score exists; proxy/LLM-judge signals (§11) layer on later to
reduce the human burden.

---

## 6C. The board as GitOps: the policy-tuning loop

The architecture board (§6B) is managed **GitOps-style** — we steal the *mechanism* (declarative,
versioned, reviewed, revertible, branchable), **not** the *philosophy* (config = known-good truth).
The board is a **best-guess playbook**, not a spec.

### Config in Git, telemetry in the log
- **Board = a version-controlled config repo (declarative, slow, reviewed):** staff templates,
  subscriptions, pipeline definitions, event taxonomy, prompt fragments — the handbook the Routing
  Manager reads. The org's **desired policy**.
- **Telemetry = append-only log (fast, never reviewed):** events, routing decisions, outcomes, quality
  scores, traces — what actually happened; the CBR case base the Consultant mines.

Config is reviewed and revertible; telemetry is immutable data. Keep them separate.

### The loop
```
BOARD (git: declarative policy)
   │ Routing Manager reads it per event   (fast inner loop)
   ▼
decisions + outcomes + quality scores ──▶ TELEMETRY LOG
   ▲                                            │ mined by
   │ merged PR changes future decisions         ▼
   └────────── gate review ◀── PR ◀──────── CONSULTANT   (slow outer loop)
```

### What GitOps buys us (the mechanism)
- **PR-as-gate:** the Consultant (or a human) never mutates the running org — it opens a **PR** against
  the board; the gate (eval ± human) reviews and merges. Structurally eliminates runaway
  self-modification.
- **Commit message = the *why*:** the Consultant's reasoning is the commit body; the git log is the
  audit trail of how operating procedure evolved.
- **Rollback = `git revert`:** a bad change is one revert away (the §10 versioned/rollback requirement,
  for free).
- **SHA-pinning = reproducibility + attribution:** every routing decision records the board commit it
  ran against, so the Consultant can correlate *"after board version X, did outcomes improve?"*
- **Branches = canary org-policy:** propose a change on a branch, run it in shadow / on a slice of
  events, compare outcomes vs `main`, merge only if it wins. A/B testing how the company operates.
- **One change stream for humans + AI:** humans guiding the AI just commit/PR to the same board.

### Two-timescale control (why this is the crucial loop)
A control hierarchy: a **fast inner loop** (Routing Manager executes the board) and a **slow outer
loop** (Consultant tunes the board) — the same separation as policy-vs-action in RL and
operations-vs-management in a real company. The board is the *policy*; GitOps is its safe editor. The
loops run at deliberately different cadences and are never collapsed.

### Caveats GitOps does NOT fix (carry forward)
1. **Desired state is a hypothesis, not truth** — the board is the current best guess; we use the
   mechanism, not the "config = correct" assumption.
2. **No reconciliation oracle** — "is the board good?" is fuzzy; GitOps makes changes safe and
   reversible but says nothing about whether one *helped*. That still rests entirely on the **human
   quality score** (§11). GitOps de-risks change; it does not measure it.
3. **The board is guidance, not a deterministic spec** — the Routing Manager *interprets* it and makes
   novel decisions; DAGs emerge. We do "GitOps for the policy," not "GitOps for the behaviour" —
   don't over-declare and kill the emergence that is the point.

### Concrete stack (from `AGENTS_POLICY_AS_CODE_LANDSCAPE.md`, 212 systems)
The survey confirmed: the *mechanisms* are all mature; **the loop assembled for a general non-code org
is empty space.** Verdicts:
- **Board format/store — literal git + a Postgres projection (open decision resolved).** Board config
  lives in a **real git repo** (PRs via branch-protection/CODEOWNERS = the gate, `git revert` =
  rollback, branches = canary — all free); a **derived Postgres projection** is rebuilt on merge for
  the fast queries the Routing Manager needs ("who subscribes to event X?"). Config in git; the
  queryable index is a cache.
  **Update (2026-06-29):** for the initial build we defer literal git and make
  Postgres the source of truth behind a `BoardStore` seam, using an immutable
  event-sourced revision log that preserves the GitOps *properties* (pinnable
  revisions, rollback, review-before-apply). Literal git becomes a later export
  or backing-store swap when the Consultant lands. See
  `docs/superpowers/specs/2026-06-29-architecture-board-data-model-design.md`.
- **Pre-merge validation — adopt OPA + Conftest** (Rego rules over the board — the canonical config
  gate) **+ promptfoo** (git-diffable eval suites that gate prompt-fragment changes in CI). A board PR
  and its eval changes review together.
- **Action-time guardrails — adopt OPA/Cedar** as a Policy Decision Point *outside* the worker loop
  (workers propose actions; an independent engine allows/denies against the pinned board).
- **Canary — steal GrowthBook's** ramp + guardrail + kill-switch, mapped onto board branches (% of
  routing decisions pinned to branch-SHA vs main-SHA); SHA-pinning + drift reconciliation from
  Argo CD / OPAL.
- **Human-approval gate — HumanLayer** is the clean library abstraction of "human approves before it
  takes effect"; **risk-tier it** (cheap changes auto-merge on eval pass; high-blast-radius needs a
  human) so review scales to Consultant commit volume.
- **Build (novel):** the **board schema** (staff/scope/subscriptions/pipelines/taxonomy for a non-code
  org), the **Consultant→PR→gate→canary loop**, and the **small-n, delayed, gameable, human-bootstrapped
  evaluation gate** — which every adoptable canary/policy tool quietly assumes away.

---

## 6D. The agentic-OS model: primitives, dispatch-vs-compose, gate-as-subscription

**Status: this is the consolidated operating model and it sharpens §6B.** §6B framed the org as
**staff templates + pipeline definitions + a taxonomy** — useful first-pass nouns, but on reflection
several of them bake in opinion the system doesn't need. This section replaces those rigid structures
with a smaller, composable primitive set under an explicit **operating-system lens**, and demotes the
**Consultant (§6A)** and the **gate (§6C)** from *primitives* to *patterns built from the primitives.*

### The OS lens
| OS concept | Agent Orange |
|---|---|
| process / thread | a **session** (a prompt + tools + model + budget = a Scope, §7) |
| kernel / scheduler | the **Routing Manager** (one scope, triggered per event) |
| kernel state (protected) | **policy config** — subscriptions + the fragment KV |
| message bus | the **event bus** |
| syscalls | the **system tools** every thread gets |
| `init` / cron | **event sources** (triggers, timers) — core mechanism |
| an enabled daemon | a **standing subscription** (event → reaction) |

### The load-bearing rule: dispatch vs. compose
The question that decides whether something is a structured table or just text is **does the runtime
ever query or dispatch on it?**
- **Dispatch → structured.** Code routes on it, so it needs real fields: `subscription.event_type`
  (the bus matches on it), `memory.labels` (you filter on them), `ticket` fields, `pipeline_run`
  status, and the **enforced execution caps** on a spawn (tool allowlist, model tier, budget).
- **Compose → text.** It is only ever pasted into a prompt, so it is just a named text value: role
  prompts, "kinds of specialists" guidance (was: staff templates), "useful pipelines" guidance (was:
  pipeline definitions), the **label vocabulary**, and the **event-type vocabulary**. The runtime
  never `WHERE model_tier='cheap'`s a staff template — it reads it and composes it into a prompt.

This is the balance between *generically useful* and *over-opinionated*: structure only what executes;
leave everything the model merely *reads* as fluid text the Consultants curate.

### The collapse
By that rule, **staff-templates + pipeline-definitions + prompt-fragments + label-vocab +
event-taxonomy-docs all become one versioned key-value store of named text — the `fragments` KV** —
curated in the background. Two elegant symmetries: *event vocabulary* and *label vocabulary* are both
just documentation fragments, while their **dispatch** counterparts are the structured
`subscription.event_type` and `memory.labels` fields. Same pattern, twice.

**The one boundary where "just text" breaks — enforced execution scoping.** You cannot tell a leaf
worker "don't use the destructive tool" in *prose* and trust it. Tool allowlist, model tier, and
budget are **structured arguments to `spawn`**, enforced by the container/mechanism — not hoped-for in
the prompt. So a "staff member" splits: its *role/guidance* → a fragment (collapse); its *enforced
caps* → structured spawn args (§7). No staff *table*; `spawn` keeps structured caps. This preserves
blast-radius safety (spend/irreversible tools never reach leaves) without a rigid template schema.

### The revised primitive set (the syscalls)
- **Thread syscalls** (every scope): `emit_event(type, text)`, `search_memory(query, labels)` /
  `add_memory(text, labels)`, `job_finished(result)`, `escalate_to_human(text)`.
- **Orchestration syscalls** (manager/privileged — *acting*, ungated): `spawn(prompt, tools, model,
  budget)` (run one stage, fire-and-forget), `run_pipeline(name|stages)` (ordered spawns — sugar over
  `spawn`), `query_subscriptions(event)`, `query_tickets` / `create_ticket` / `update_ticket`.
- **Policy syscalls** (*rewiring* — versioned write, review optional): `write_fragment(id, text)` and
  `write_subscription(...)`.

Two non-primitives, explicitly dropped: **`draft_prompt`** (the manager just writes the stage prompt
as text inline when it spawns) and **`query_history`** (past decisions are memories the archival bot
labeled — it's just `search_memory`). Payloads are **opaque text**; the emitter writes them, the
consumer interprets them, and "input" (human goal / event payload / prior-stage output) is one concept:
text templated into the next prompt.

### Three kinds of state — gating tracks *policy*, not *persistence*
| Category | Examples | Mutability | Gated? |
|---|---|---|---|
| **Policy / config** | subscriptions, the `fragments` KV | read-write, **versioned** | optional (below) |
| **Work state** | `tickets`, `pipeline_runs` | read-write | no |
| **Telemetry** | events, routing decisions, outcomes, memories | append-only | n/a |

Persistence was never the axis. Creating/updating a **ticket** is *acting* (ungated) even though it is
durable — "customer emails a bug → manager files or updates a ticket" is a free act, like running a
pipeline. Only **policy** (the org's standing wiring) is subject to review.

### The gate is itself a subscription (opt-in), with a resource-only floor
There is **no hardcoded human checkpoint.** A policy write is **versioned** (so any decision can pin
the fragment/subscription version it ran against, and a bad edit is one `revert` away — rollback and
attribution are independent of gating). *Whether* a write is reviewed is itself a **subscription**: a
policy write emits a change event; if a review subscription exists, a **reviewer scope** runs and may
auto-approve or call `escalate_to_human`. So the gate, the Consultant, archival, and "staff templates"
are all **patterns over the primitives**, and *how strict the org governs itself is tunable org policy.*

The **one assumption baked in as non-editable mechanism is resource safety** — loop-depth, budget, and
concurrency caps on event chains — so the system cannot runaway-edit its own cost limits. Human review
is *not* baked in; it is an opt-in reviewer subscription.

### Pipelines: definition vs. run vs. execute
A pipeline stage is a container that may run for minutes, so **the manager must never block on it**
(consistent with the fire-and-forget handoff of §6). Pipelines split three ways: the *definition* is
guidance **text** (a fragment); **executing** one is the `run_pipeline` syscall; and an *in-flight run*
is a persisted **`pipeline_runs`** record (current stage + status) — **work state**, like a ticket —
that emits `pipeline.completed` for the manager to react to on a later tick.

### Worked examples — everything is built from primitives
- **Archival (dogfooded, not core code).** A core event source emits `conversation.ended` after N
  minutes of inactivity (mechanism). A **subscription** routes it to an **archival-bot scope** whose
  *prompt* says "summarise this conversation, then label it (`conversation-summary`; add `manager` if
  it is a manager thread)." The labeling scheme lives in the bot's prompt — a fragment — not in code.
- **Self-improving manager (the Consultant, as a pattern).** A scope on a cron runs
  `search_memory(labels=[conversation-summary, manager], limit=5)`, studies the recent manager
  decisions, and `write_fragment("routing-guidance", …)` (≤ a length cap). Every manager scope
  templates `{{fragment:routing-guidance}}` into its prompt — so the org continuously tunes its own
  routing policy, built entirely from `scope + cron + search_memory + write_fragment`.

---

## 7. The Scope contract + WorkerRuntime seam

A **Scope** is the contract for one worker invocation; it maps directly onto a runtime session.

```go
type Scope struct {
    Objective string        // narrowed task
    Context   ContextSlice  // parent-trace excerpt + memory retrieved for THIS objective (§8)
    Prompt    PromptSpec    // composed system prompt + pinned fragment versions (§10)
    Skills    []SkillRef    // assigned skill/tool allowlist (§9) — NEVER spend/irreversible at leaves
    Model     ModelTier     // full | mid | cheap — downgrade toward leaves
    BaseImage ImageRef      // runtime-adapter detail; bakes in deps (resume = re-pull, not overlay)
    Budget    Budget        // token/spend/depth ceiling
    Return    ResultSchema  // structured result the manager expects
}

type WorkerRuntime interface {
    Spawn(ctx, Scope) (Session, error)      // provision sandbox+harness, start the scope
    Resume(ctx, sessionID) (Session, error) // rehydrate a paused session
}
type Session interface {
    Events() <-chan Event             // canonical event stream (reuse go/events vocab)
    Pause(ctx) error
    Result(ctx) (Result, error)       // structured result + full-trace ref + memory_writes
    ID() string
}
```

This is essentially `agentkit`'s existing `Runner`/session shape — so **we keep agentkit's interface
+ event vocabulary, drop its heavy implementation, and the runtime becomes a swappable impl**:

- **impl #1 (now): agentkit-runtime** — reuses the Claude-Agent-SDK harness + a lightweight container
  exec (no DinD/registry/blobarchive). Fastest path to a working slice; known-good loop behavior.
- **impl #2 (later): OpenHands** — Go manager drives OpenHands' in-container agent-server over HTTP.
- **future adapters: Hermes / E2B / Morph** — rented sandboxes; the adapter drops a harness inside.
  Build none until needed; they exist to prove the seam earns its keep.

Resume fidelity (decided): **conversation replay + re-pull base image** is enough — no full-overlay
snapshot. Therefore a Scope **bakes required deps into its base image** rather than relying on
mid-session installs surviving a resume.

---

## 8. Memory subsystem (ADOPT behind a seam + BUILD the governance)

**Substrate: adopt** (decided), behind a thin seam so it's swappable:

```go
type MemoryStore interface {
    Write(ctx, MemoryItem) error                 // item carries provenance (below)
    Query(ctx, MemoryQuery) ([]MemoryItem, error)// vector + label filter + scoring
}
```

Default substrate candidate: **Zep/Graphiti** (temporal provenance fits the poisoning risk) or
**mcp-memory-service** (simplest self-host). The store is **exposed to all agents via MCP** so every
worker can `search / label / query` memory as a first-class tool. **We do not write the store.**

**Provenance on every item** (steal from Zep/yantrikdb/Collaborative-Memory — our defense against
memory poisoning, a *known-unsolved* risk):

```
MemoryItem { text, labels[], embedding,
             source_session, confidence, valid_window, supersedes, why_retrieved }
```
Retrieval explains itself (`why_retrieved`); contradictions are *surfaced*, not silently overwritten;
a memory graduates from "claimed" → "trusted" only with corroboration; worker write privileges scoped.

**Governance we BUILD (the white space):**

- **Governed taxonomy curator.** Labels are auto-generated *conditioned on the existing taxonomy*
  (A-MEM pattern) and reconciled against a **manager-curated controlled vocabulary** — top-down
  taxonomy + bottom-up suggestion, never pure free-for-all (kills tag-soup). A scheduled
  merge/decay pass collapses synonyms (SwiftMem) and prunes low-value labels (Ebbinghaus-style).
- **Self-documenting labels.** On each taxonomy change the manager **regenerates the human-readable
  label documentation**, which is injected into workers (versioned/pinned — §10). This "curate the
  vocabulary *and* keep its docs in sync" loop is genuinely novel.
- **Learn-from-session.** On session end, an *importance-gated* job distills the trace into
  **titled, typed, deduplicated procedure items** (AWM/Memp/ReasoningBank pattern — learn from
  failures too), labeled into the taxonomy. Raw events are retained so summaries are re-derivable.
- **Per-staff-member memory views.** Each staff member (the specialists of §6B) curates its *own*
  focused slice of the shared store via its self-archiving strategy (the legal expert keeps only
  law-relevant extractions) — personalised retrieval over one shared, provenance-tracked store.

**Read-time injection ("compile the worker"):** at dispatch the manager assembles a worker's
`ContextSlice` by querying labeled memory by recency·importance·relevance for that ticket's objective.

---

## 9. Skill subsystem (ADOPT the standard + loader, BUILD the assignment)

- **Adopt** the **SKILL.md** standard + a cross-runtime loader (OpenSkills) + skill routing
  (eagle-eye style: trigger→BM25→embedding fusion). Don't build skill plumbing.
- **Build** the HR decision: the manager **assigns which skills each worker gets by role** (and,
  later, by *learned* worker↔skill fit from session outcomes). Defaulting to "all skills to all
  workers" is acceptable early; scoping matters for blast radius (spend/irreversible tools never
  reach leaves).
- **Manager can create new skills** — behind **verify-before-promote** (Voyager): an induced skill
  must pass sandbox execution / eval (hermes-eval-style regression + drift check) and a security scan
  before it enters the assignable registry. **Never auto-promote an unverified self-authored skill.**

---

## 10. Prompt composition (BUILD, with blast-radius guards)

The manager composes each worker's system prompt at dispatch from **versioned prompt fragments**
(role guidance + current label docs + relevant procedures). Guards (steal from ACE/GEPA/DSPy, the
field's hard-won fixes for prompt drift):

- **Delta updates, never monolithic rewrites** (ACE "context collapse").
- **Pin per session** — a session records exactly which fragment versions it ran (reproducibility).
- **Eval/approval gate + rollback** — a fragment change ships only if it beats baseline on an eval
  set; one bad nightly rewrite cannot silently degrade the whole fleet.

This is the operational meaning of principle #6: *the manager proposes, a gate disposes.*

---

## 11. Verification harness (BUILD — the hardest gap)

Our deliverables are **non-code**, so there's no test-as-oracle, and the prior art doesn't provide
one. Three mechanisms:

1. **Acceptance criteria at plan time.** Each ticket gets verifiable `acceptance` written *before*
   the work, by the manager/planner — and checked by a **separate verify-scope**, not the worker that
   did the job (criteria set by a different scope than executes).
2. **Proxy + judge evaluation** for the fuzzy artifacts (labels, summaries, prompts): downstream-task
   proxy metrics (did sessions retrieving memory-item-X succeed more?), LLM-judge rubrics with
   provenance, golden-label regression sets, and periodic human spot-audit of the taxonomy. Accept
   that verification here is **statistical/proxy, not pass/fail.**
3. **Human numeric quality score (the bootstrap oracle, §6B).** Start with a small, enforced,
   human-supplied numeric score per resolved ticket/event — the reliable outcome signal the CBR
   router and Consultant learn from. This *manufactures* the oracle that every self-improvement method
   in the field silently assumes; the proxy/judge signals above layer on later to reduce human burden.

---

## 12. Staged roadmap (thin slices, each proves out)

- **Slice 0 — the loop, no cleverness.** One schedule → manager exchange → enqueue one worker
  (agentkit-runtime) → fuzzy task → result on the board. Memory = adopted store via MCP, dumb
  append + query. **No** self-rewriting prompts, dynamic skills, or auto-labeling. Proves the
  plumbing + the fire-and-forget queue handoff. *If this isn't reliable, nothing above it is.*
- **Slice 1 — labeled memory + read-time injection.** Workers write labeled memories; the manager
  composes `ContextSlice` at dispatch. Labels into a small **human-ratified** controlled vocabulary.
- **Slice 2 — learn-from-session.** Importance-gated distill→titled-procedure→label→store
  (AWM/ReasoningBank patterns).
- **Slice 3 — the HR moves, gated.** Versioned/pinned prompt composition + eval gate; skill library
  with verify-before-promote. The self-modifying parts come last.
- **Slice 4 — event bus + dynamic routing + the Consultant.** Lifecycle events
  (`session.completed`, `summary.completed`) on a bus; summary-bot subscribers; the manager routes
  events by consulting the event/reaction/outcome history (CBR-style); the **Consultant** mines that
  history and proposes process changes (new subscriptions, summary-bot flavours, prompt/skill edits)
  through the gate. The self-modifying layer — last, behind budgets. (§6A)
- **Later — OpenHands runtime impl; bounded parallel fan-out; learned worker↔skill fit; dynamic
  scope templates ("hiring").**

---

## 13. Technology choices (summary)

| Concern | Choice | Stance |
|---|---|---|
| Manager / orchestration | **Go** (reuse `go/events`, store seams) | build |
| Worker runtime | `WorkerRuntime` seam; **agentkit-impl now**, OpenHands later | keep iface / adopt impl |
| Worker↔manager handoff | **work queue, fire-and-forget** | build |
| Board store | **Postgres** (reuse `go/agentdb`) | build (thin) |
| Memory substrate | **adopt** (Zep/Graphiti or mcp-memory-service) behind `MemoryStore` | adopt |
| Memory governance | taxonomy curator, provenance, learn-from-session | build |
| Skills | **SKILL.md** + loader (adopt); assignment + verify-before-promote (build) | adopt + build |
| Prompts | dispatch-time composition, delta+gate+pin | build |
| Event bus / triggering | **Temporal** (Go SDK, event-sourced, durable replay) as bus+trigger substrate — *candidate, decide*; hand-rolled cron+events as minimal fallback | adopt (decide) |
| CBR routing layer | **build in Go**; blueprint = **CASCADE** (4R + contextual-bandit retrieval, model frozen) | build |
| Summary bots | **steal ReasoningBank** (distil labeled insight from successes *and* failures) | build (thin) |
| Consultant (self-improvement) | **steal** ADAS archive + ACE playbook-scope + role separation; propose→gate→reversible | build |
| Verification / credit-assignment | acceptance-criteria + proxy/LLM-judge (executor≠judge); **the load-bearing layer** | build |
| Routing Manager | single CBR router; tools = `emit_event` + `run_pipeline`; reads architecture board + decision/outcome history | build |
| Coordination | **pub/sub** (standing org-wide reactions) + **pipelines** (job-specific ordered sequences); worker `emit_event` tool | build |
| Staff members | named reusable scope templates (prompt+skills+memory filter+subscriptions+self-archiving) | build |
| Architecture board | registry of staff + subscriptions + pipelines (the live org config the Consultant edits via the gate) | build |
| Board management | **literal git** for config (PR=gate via branch-protection/CODEOWNERS, `revert`=rollback, branch=canary) + **Postgres projection** rebuilt on merge for fast queries; telemetry in the append-only log (§6C) | build + adopt |
| Pre-merge validation | **adopt OPA+Conftest** (Rego config gate) + **promptfoo** (eval-gating); risk-tiered approval (HumanLayer) | adopt |
| Canary mechanics | **steal GrowthBook** (ramp+guardrail+kill-switch) over board branches; SHA-pin + drift-reconcile ← Argo CD/OPAL | steal |
| Outcome signal | **human numeric quality score** first (bootstrap oracle), proxies/judge later | build |

---

## 14. Standing risks / open problems

1. **Memory poisoning** — field-unsolved. Mitigate (not eliminate) via provenance, confidence,
   conflict-surfacing, scoped writes, corroboration-before-trust.
2. **Non-code verification** — no oracle; proxy/judge/golden-set/human-audit only. Our deepest gap.
3. **Premature abstraction** — discipline: **one** impl per seam until a real need forces a second.
4. **Scope spiral** — the HR/memory vision is a research hole; the slice roadmap is the guardrail.
5. **Credit assignment** — attributing outcomes to strategies/workflows without a clean oracle is
   the load-bearing weakness of the self-improving layer (§6A); verification is statistical, not
   pass/fail.
6. **Runaway self-modification & event loops** — the Consultant editing the process, and emergent
   event chains, both need the gate, budgets, depth caps, and loop detection (§6A).
7. **Agent commit-volume overwhelms review** — human approval won't scale to Consultant PR rates;
   use risk-tiered gating (auto-merge cheap eval-passing changes; humans only for high-blast-radius) +
   automated pre-merge validation (Conftest/promptfoo). Beware prompt-injection via PR content (§6C).
8. **Canary without an oracle** — feature-flag ramp/guardrail/kill-switch assume a dense, clean metric;
   ours is tiny-n/delayed/gameable. Borrow the mechanism, not the confidence model — hold-outs, human
   spot-audit, regress-to-`main` on ambiguity; and squash/semantically-version the board to stop the
   Consultant's PR history sprawling past auditability (§6C).

---

## 15. Open decisions still pending

1. **Which adopted memory store** — Zep/Graphiti (provenance) vs mcp-memory-service (simplicity);
   decide at Slice-0 adoption.
2. **Spec-interview channel** — existing `web/` chat vs the out-of-band escalation channel
   (email/Slack).
3. **Verify-scope vs manager-inline verification** — when a delegated write deserves its own
   independent verify-scope.
4. **What survives from agentkit** beyond the interface/event vocabulary (e.g. model proxy).

---

## 16. Relationship to existing code & `MIGRATION.md`

- **Keep:** `go/events` vocabulary; host-store seams (`go/agentdb`, Postgres); the
  `Runner`/session/execenv **interface shape** (it becomes `WorkerRuntime`); optionally the model
  proxy.
- **Drop / stop porting:** DinD, image-registry GC, blobarchive snapshot, fleet placement,
  multi-tenant isolation tests — the `phase4-gcp-seams` machinery.
- **Reframe `MIGRATION.md`:** GCP is still wanted, but to run the **manager + board + adopted memory
  store + a worker runtime**, not an image-burning registry pipeline.
- **Nothing gets deleted** until the corresponding adopted component is proven in a slice.
