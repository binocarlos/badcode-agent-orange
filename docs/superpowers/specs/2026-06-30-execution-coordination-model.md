# Execution & Coordination Model — Addendum

**Date:** 2026-06-30
**Status:** Design addendum from the red-team/brainstorm session. **Amends** `ARCHITECTURE.md`
§6 (manager loop / fire-and-forget), §6A (event substrate), §6D (primitives), §7 (Scope +
WorkerRuntime seam). Not yet implemented. Where this conflicts with §7's "replay is enough"
framing, read the reconciliation in §5 below.
**Reading order:** `docs/AGENTS_DESIGN.md` → `ARCHITECTURE.md` §6/§6D/§7 → this.

This addendum resolves *how sessions are dispatched, how they wait on each other, and how they
are paused/resumed* — the execution mechanics under the §6D primitive set. It was grounded
against the actual agentkit code (`go/runner.go`), not impressions.

---

## 1. One dispatch mechanism, three trigger sources

Everything that runs is dispatched by a single kernel-level driver:

> **A thread finishes → emits `thread.finished{output}` → the bus dispatches whatever was
> waiting on it, passing `output` as the next input.**

There are three *sources* that feed this one mechanism (they do not need three implementations):

1. **Event→subscription matching** — code routes a bus event to a standing subscription.
2. **Model-initiated tool call** — a scope calls `spawn(...)` or `run_pipeline(...)`; the
   structured tool call *is* a dispatch act (the "third side of the triangle": *compose* builds
   the prompt → model emits a structured call → that call dispatches). Recursion is free because
   spawner and spawnee are the same primitive (a manager may `spawn` a manager).
3. **Pipeline advancement** — a finished stage triggers the next stage with its output as input.

"Who was waiting on this finished thread?" has three flavours, all served by the one driver:
a **standing subscription** (e.g. archive every finished thread), the **next pipeline stage**,
or a **parent manager's continuation**.

## 2. Fire-and-forget everywhere; request-reply is emergent

**No thread ever blocks in-process on another thread.** Blocking chains are fragile: one crash
anywhere collapses the chain. Instead, request-reply is *reconstructed* from fire-and-forget +
the event bus + statelessness (the actor / durable-workflow pattern; cf. Temporal `await`):

- A manager that needs a child's result **spawns (fire-and-forget), records "waiting on child
  X", then calls `finished` and ends.**
- When child X emits `thread.finished`, a continuation **re-dispatches a fresh manager scope**
  with X's output as input. The manager is stateless between triggers (principle #3); the
  "reply" is just another event that wakes it.

This makes "pause for an unknown, possibly very long time" **free for ephemeral threads** — a
dormant manager costs **zero compute** (it is destroyed and later reconstructed from the log, a
continuation, *not* a resident PID holding a stack).

The new failure surface is **lossy resume**: every await is teardown + re-spawn + state
re-derivation from board+memory. Invest in "persist enough breadcrumbs to resume coherently" —
that is where this model's real fragility lives (it moved from *process-crash* to *lossy-resume*).
This is acceptable here because the workloads are minutes-to-days, so cold-start latency is noise.

## 3. Standing subscriptions = policy; continuations = work state

| | Standing subscription | Continuation / await |
|---|---|---|
| Example | "archive every finished thread" | "wake manager M when session X finishes" |
| Keyed on | `event_type` (broadcast) | correlation id `(session_id)` or `(pipeline_run_id, stage)` |
| Lifecycle | long-lived | one-shot |
| Persistence | **versioned board** (policy, §6D) | **work state** (ephemeral, like `pipeline_runs`) |
| Gated? | optional | no |

**Gap to close:** `board_subscriptions` currently has only `event_type` — no correlation/target-id
column. Targeted continuations (the await + pipeline-advance cases) need a correlation key, and
they must **not** be written into the versioned policy log (they would fill board history with
per-execution noise). They are work state.

## 4. Escalation = block + emit + end (to human *or* parent)

A stuck thread does **not** open a live back-channel. It calls `escalate` (= `finished` with a
"blocked, need answer" status + a question payload) and **ends**. The answer — from a manager
scope *or* a human — is another event that **re-dispatches (resumes)** it. Escalation-to-parent
and escalation-to-human are the *same* pattern, routed to different reactors (§6's "persist a
ticket + end, resumed by the reply event", generalized).

Consequence: we do **not** use the Claude Agent SDK's in-process sub-agents for *cross-thread*
org coordination (they are live/in-process/fragile). In-thread sub-agents for a single scope's
*own* decomposition are fine; cross-thread coordination lives on the durable bus. That boundary
is *why Agent Orange is a layer above the SDK, not a fight with it.*

## 5. Session persistence: `ephemeral | snapshot` (replay is the spine)

Grounded against `runner.go:766–855`: agentkit already resumes a session by stitching **two
independent sources** — filesystem from a Docker **snapshot image** (`Materialize`) + conversation
from **Postgres** (`rehydrateConversation` → `/load-conversation`). The code comment
(`runner.go:796–803`) states the key fact: `docker commit` captures the **filesystem, not** the
harness's in-process conversation; the conversation is always rebuilt from the event log.

Therefore the resume model is **not** "replay vs snapshot" — it is:

- **Conversation-replay-from-log is the mandatory universal spine.** Every thread needs it.
- **Snapshot is an opt-in filesystem layer**, only for scopes that build disk state.

Make persistence a **property of the Scope**:

| Mode | For | Resume | Image | Tenancy |
|---|---|---|---|---|
| **ephemeral** (default) | managers, routers, summary bots — no disk state | fresh container from **base image** + replay | shared **base/core** image (cheap, re-pullable, no GC) | may run **shared** (dense, many-per-container) |
| **snapshot** (opt-in) | workers with disk artifacts | **Materialize(snapshot)** + replay | per-session **committed** image (registry + GC) | must run **per-session** (one container each) |

**Two image kinds (do not conflate):** a *base/core image* is shared, already-present, never
session-specific; a *snapshot image* is committed from one session's container, pushed to the
registry, must be GC'd. The ephemeral path escapes the **per-session snapshot image and its
registry**, not Docker itself.

**Persistence ↔ tenancy is one axis viewed twice** (`runner.go:299–306`, `680–684`): snapshot is
banned on shared-tenancy (a filesystem diff is not attributable to one of many co-tenant sessions).
So `ephemeral ⇒ shareable/dense` and `snapshot ⇒ per-session/isolated` — the cheap threads pack
tight, the heavyweight threads get isolation. A free, clean alignment.

**This revises §7's "replay is enough" bet:** the snapshot machinery already *exists and works* in
agentkit — it is not "build later", it is "already here; decide per-scope whether to invoke it."
The only cost of keeping it is operational weight (registry storage, GC, per-session tenancy) — pay
it only on disk-touching scopes.

**Small missing branch:** today the destroyed/archived restore path *requires* a snapshot handle
(`runner.go:780–782`: no snapshot → "must be re-created"). The ephemeral path (provision from
**base image** + rehydrate, no snapshot) is **not built** — it is a small addition reusing the
existing `rehydrateConversation` machinery, branching to `Policy.BaseImage` when there is no handle.

## 6. Fail loud on conversation-rehydrate (reverse agentkit's best-effort)

`runner.go:805–807` makes rehydrate **best-effort** — on failure the session resumes *usable but
with no prior context*. **Reverse this for the autonomous org.** A manager resumed without its
conversation wakes with amnesia and makes decisions (`spawn`/`emit`/`update_ticket`) on no context;
with no human watching that thread and no test oracle, the bad decision propagates **silently** —
the exact failure mode we are most exposed to. Conversation history is load-bearing; if it cannot
be restored, the resume **fails**.

- **Distinguish** legitimately-empty (`len(msgs)==0`, brand-new session — fine) from
  **failed-to-load** an existing non-empty history (the fail case).
- **Fail = retry-then-quarantine,** not crash-loop: on persistent failure mark the session
  **failed / needs-human** (§6), surfacing "couldn't restore session X's memory" instead of an
  amnesiac agent quietly doing damage.

agentkit chose best-effort because it was a *human-interactive* chat tool (a human would notice
amnesia). The autonomous org has no such observer, so the silent path is dangerous.

## 7. Recursion controls — the resource floor (the one non-editable mechanism, §6D)

Three independent controls; you need all three (each bounds a different dimension):

1. **Depth** — `session.depth = parent.depth + 1`, hard cap. Bounds tree **height** (runaway
   recursion). Already half-present: §7 `Scope.Budget` is a "token/spend/**depth** ceiling".
2. **Per-session fan-out budget** — "this scope may spawn ≤ N sessions / ≤ M pipelines". Bounds
   branching **factor**.
3. **Tree-global budget** — a shared counter decremented down the whole goal-tree. Bounds total
   **node count / spend** — the one that actually caps cost (depth×fan-out can still be huge).

These are instances of §6D's non-editable resource floor ("loop-depth / budget / concurrency
caps"). The floor is the *only* thing not subject to policy edits — the system cannot
runaway-edit its own cost limits.

## 8. Pipelines are built-in mechanism (definition stays text)

Consistent with the dispatch-vs-compose razor and §6D:

- **Pipeline definition** = guidance **text** (a `fragments` entry) — *compose*.
- **Pipeline execution / advancement** = **built-in mechanism** (`run_pipeline` syscall;
  `pipeline_runs` work-state row tracking current stage + status) — *dispatch*.
- **Advancement is a targeted, one-shot subscription** keyed to `(pipeline_run_id, stage)` —
  **not** a broadcast `event_type` sub. This reuses the bus mechanism (DRY) while preserving
  §6B's guarantee that a pipeline is an *ordered* sequence, not emergent broadcast soup.

Every thread must eventually call `finished(output)`. A **reaper** force-finishes idle threads
(e.g. > N minutes no activity) to unlock stuck pipelines — **but** it must stamp the output with a
`reaped` status, and downstream stages / verify logic must branch on it. Otherwise a reaped
(empty) output silently flows into the next stage ("rewrite to be funny" runs on an empty string,
emails the boss nothing) — the same silent-degradation class as §6.

## 9. The learning loop — feedback and the Consultant are one primitive

The system's *learning* mechanism reduces to a single loop:

> **trigger → scope → `write_fragment(...)`**

**Human feedback and the Consultant are the same loop**, differing in only two slots:

| | trigger | input | action |
|---|---|---|---|
| **Human feedback** | `human-feedback` event | an explicit note (diagnosis *given*) | `write_fragment` |
| **Consultant** | cron | mined telemetry (`search_memory` over labelled decision summaries) | `write_fragment` |

This is §6D's "the Consultant is a pattern, not a primitive" made concrete: it is the
human-feedback loop with the *trigger* and *input* swapped. All machinery (the fragment write
path, versioning, pinning) is shared.

**Build consequence (resolves the hardest-slice-first risk):** ship the **human-feedback loop
first** — safe (the human is the judge), trivial (no autonomous diagnosis), immediately useful,
and immediately content-rich ("watch me give it a note, watch its behaviour change"). The
Consultant is then a near-free later variation: swap the trigger to a cron and the input to
telemetry. You do **not** build a scary new subsystem last; you swap two slots on a loop you
already shipped.

**The trap — same loop, very different difficulty.** The loop is identical at the
`write_fragment` end; it is *not* identical at the **diagnosis** end:
- Human feedback hands the system a **completed credit-assignment** ("too try-hard, be drier") —
  the scope only translates a clear note into a fragment edit. Reliable.
- The Consultant must **do the credit-assignment itself** — the drift-prone step. It is the same
  loop wrapped around the **hardest** thing in the project.

So "same loop" reassures about *build cost*, not *reliability*. Build the feedback loop as
production; treat the Consultant as a research bet, not a refactor.

### The board is never empty — seed fragments
Cold-start ("what do you do with an empty board?") dissolves: the board is **seeded, not
empty.** Startup = human-authored **seed fragments** (role guidance, "kinds of specialists",
"useful pipelines", routing guidance — the platform guidance) **+ a first vague goal**. The
manager reads the seed fragments to turn the vague goal into a spec; human-feedback and the
Consultant then *evolve* the seeds. The whole system is a machine for editing its seed guidance
over time — which is also the cleanest content/teaching framing ("here's what I seeded; here's
how it changed").

### No questions in v1 (autonomous spec, after-the-fact correction)
v1 has **no interview / no question-asking**: the manager autonomously turns the vague goal into
a spec. The trade, stated honestly: **all correction moves from upfront (interview) to
after-the-fact (feedback).** The manager *will* guess the spec wrong; the human corrects via the
feedback primitive. For a precision/oracle project this is reckless; for a **narrative/taste**
project it is *better* — more autonomous (better story), it **dogfoods the feedback loop**, and
"guess wrong then learn from my note" beats "interrogated me for ten minutes" as a demo. Defer
the adversarial-question-answerer (a `question` tool that wakes an adversarial AI instead of a
human) — a v2 flourish.

### Two risks this introduces
1. **Multi-editor fragment coherence.** Humans + Consultant + many feedback events all
   `write_fragment` to the same blobs → thrash and "context collapse" (§10's ACE risk): "be
   funnier" + "be more professional" → mush. The versioned log gives attribution and rollback,
   **not semantic coherence** — two reasonable notes can be jointly incoherent. Bake in from line
   one: **delta edits, never rewrites**; a length cap; and an arbitration decision (a
   reconciliation pass, or one human as final editor early).
2. **Feedback needs a target.** "Easy feedback" still needs an **anchor**: the primitive is
   **(target_ref, note) → `human-feedback` event** — the human points at a thing (a session, a
   board decision, a published post) and types a note; the subscriber gets *the critiqued thing*
   + *the note* → `write_fragment`. This is the **same correlation-key infrastructure** missing
   from `board_subscriptions` (§3) — load-bearing across continuations, pipeline-advance, *and*
   feedback. Build it once.

## 10. Deferred / open

- Correlation-key column on the subscription/continuation model (§3) — pinned down with the bus impl.
- `pipeline_runs` advancement-driver wiring (§8) — with `run_pipeline`.
- Tree-global budget representation (§7.3) — a counter threaded through `spawn`.
- Whether `ephemeral` shared-tenancy packing is worth it at Slice 0, or per-session-for-all is
  simpler first (premature-density risk, §14.3).
