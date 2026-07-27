# Work plan — self-improvement workstream

*Created 2026-07-27. Executes [`12-composition-playbook.md`](./12-composition-playbook.md) §3.
Format and rules follow [`06-work-plan.md`](./06-work-plan.md), which carried 37/37 items.*

## EXECUTION RULES

1. **Executors work in isolated worktrees**, one per item (or item group as marked). They do NOT
   edit this file — the orchestrator owns it. Surprises are reported as bullets in the final
   report, for the orchestrator to file in the Discovered Issues Log below.
2. **Run the item's validation commands verbatim before claiming done.** The orchestrator re-runs
   them after merge. "Done" without a green validation run is not done.
3. **Merges are sequential**; the orchestrator resolves conflicts and re-validates after each.
4. **The compose stack is a serial resource.** `./e2e/run-stack-e2e.sh` restarts agentd; only one
   validation run at a time, coordinated by the orchestrator. Executors may develop specs without
   the stack and must say clearly if they could not run them.
5. **Do not touch:** `.env` (holds real credentials), the shared test Postgres container
   `ao-test-pg` on `localhost:5433` (use it, never delete/recreate it), production seeding, and
   the real Anthropic API (mock mode only unless an item says otherwise).

## Standing traps (scar tissue — read before coding)

- **gorm `default:` tags**: NEVER on a column where zero/false is meaningful (GORM omits
  zero values when a default is declared). DDL defaults go in migration SQL; defaulting in
  `normalize()`/validate. See `Worker.Enabled` for the pattern.
- **Mock-script body-match leak** (`11-learning-stories.md` §3): the rule match runs against the
  whole request body, which replays prior turns — a marker emitted in a tool call matches later
  requests. Partition rules by worker name first; split before/after with `absent`; distinct
  non-substring worker names.
- **Assert on happens-after signals, never sleeps.** Poll the delivery/event/config-log record.
- **Port pool**: every job holds a container + host port (ceiling 100). Delete sessions in
  teardown; `./e2e/run-stack-e2e.sh clean` clears leftovers.
- **The stack serves a BUILT web image** — UI edits are invisible until
  `docker compose up -d --build web`.
- **Migrations**: append to `agentMigrations`, next free number (check on merge — sibling items
  may claim the same number; orchestrator renumbers on conflict).
- **Where vs when to assert**: reading back a stored value proves storage, not delivery. Behaviour
  switches prove delivery.

---

## Wave 1 — Learning stories (playbook P.5)

*Goal: the deterministic gate exists and is green offline. No schema changes, no tokens.*
*Spec: [`11-learning-stories.md`](./11-learning-stories.md). Model: `go/modelproxy/script.go`.*
*Pattern to build on: `e2e/features/acceptance-loop.spec.ts` (seeds a §8.7 org already).*

- [ ] **H0 — Harness + control + canonical story.** One composite mock script
  `e2e/mock-scripts/learning-stories.json` (worker-name-partitioned rule blocks); spec file
  `e2e/features/learning-stories.stack.spec.ts` with shared helpers (seed actor+critic+
  subscriptions into a run-scoped project; drive rounds by emitting events via `POST
  /agent/events` — never cron; await delivery outcomes). Implements **MR-3 (no ghost learning)**:
  critic disabled → actor behaviour byte-identical across rounds; and **S1 (missing title)**:
  round 0 lacks `Title:`, critic rewrites via `worker_prompt_write`, round 1 has it, config log
  holds the rewrite + rationale. File header carries the doc-11 §2 caveat: proves transmission,
  not discovery.
  *Validation:* `./e2e/run-stack-e2e.sh up mock` then
  `./e2e/run-stack-e2e.sh test mock --mock-script e2e/mock-scripts/learning-stories.json -- e2e/features/learning-stories.stack.spec.ts`
- [ ] **S2–S9 — The remaining stories**, added to the same spec + script file, one describe block
  each, isolated run-scoped projects, distinct worker names per story:
  - **S2** forgotten sign-off; **S3** missing units (doc 11 §4 rows 2–3).
  - **S4** the unasked question — round 1 calls `request_human_attention`, delivery parks at
    `awaiting_human` (non-text observable).
  - **S5** planted null — round 1 reports "no significant effect".
  - **S6** no-regression (**MR-2**): second rewrite; S1's property still holds.
  - **S8** lineage: three rewrites → config log has exactly three, ordered, each with rationale;
    folding to round *k* reproduces round *k*'s prompt.
  - **S9** capstone: three cumulative improvements (title, sign-off, units) all hold
    simultaneously in the final round.
  *Validation:* same command as H0 (whole spec file).

## Wave 2 — Frozen workers (playbook P0) — runs in parallel with Wave 1

*Spec: [`10-topology-library.md`](./10-topology-library.md) §3. Touches `go/` + `web/` only — no
overlap with Wave 1's `e2e/` files.*

- [ ] **F1 — Engine.** `Worker.Frozen bool` (NO gorm default tag), migration (next free number),
  freeze/unfreeze via the JWT-guarded HTTP API (config-logged through `WithConfigEvent`;
  `TestMutationsAreLogged` must adopt the new mutation). The core MCP server refuses
  `worker_prompt_write` / `worker_update` / `worker_delete` against a frozen worker with an error
  saying why; each refusal emits a project event `worker.freeze_refused` (C8: refusals are
  signals). Table tests + live-Postgres tests.
  *Validation:* `cd go && go build ./... && go vet ./... && go test ./...` and
  `AGENTKIT_TEST_POSTGRES_URL=postgres://postgres:test@localhost:5433/postgres?sslmode=disable go test ./agentdb/... ./cmd/agentd/... ./httpapi/... -count=1`
- [ ] **F2 — UI.** Lock badge on frozen workers ("Frozen — cannot be changed by other workers"),
  freeze/unfreeze control on the worker settings page, changelog renders freeze/unfreeze events.
  *Validation:* `cd web && npm ci && npm run typecheck && npm test`
- [ ] **S7 — Frozen-scorer story** (after F1+F2 merge + stack rebuild): critic attempts to rewrite
  a frozen worker; refused; prompt byte-identical; `worker.freeze_refused` event recorded.
  *Validation:* Wave 1's command (spec file includes S7).

## Wave 3 — Topology as data + first seeds (playbook P1–P2)

*GATED on decisions D1, D2 (below).* Itemised on unlock: T1 schema+renderer+preview,
T2 apply-as-config-batch, T3 UI flow, T4–T7 the four seeds (Solo, Actor–Critic, Supervisor,
Frozen-scorer harness), each with a learning-story-style e2e proving instantiation.

## Wave 4 — Hypothesis lab + calibration (playbook P3)

*GATED on Wave 3 and on the real-model spend decision (D4 context).* Itemised on unlock:
dataset generator + trap taxonomy, lab topology seed, calibration run design. **Hard pause for
Kai before any real-model run.**

## Wave 5+ — Remaining seeds, comparison rig, onboarding, Tier B (playbook P4–P7)

*GATED on D3 (self-organizing autonomy) for topology 9; on Wave 4 for the comparison rig.*

## Decisions needed (D1–D4)

- **D1** (blocks W3): topologies built-in-curated vs user-authorable/shareable at first?
- **D2** (blocks W3): topology bundles own their images/skills or reference existing by name?
- **D3** (blocks topology 9): how much autonomy for the self-organizing pool (`worker_create` in
  agent hands)? Existing brakes: `MaxConcurrentJobs`, daily token caps.
- **D4** (blocks W5 unfreeze design): does unfreezing need anything beyond a human JWT?

## Discovered Issues Log

- **The e2e rig already had the mock-script affordance this plan needs** —
  `run-stack-e2e.sh test --mock-script FILE` loads a script for one run and restores the plain
  model after, refusing malformed scripts loudly. Wave 1 needs no runner changes.
- **The scheduler already fires through the event spine** (`CreateProjectEvent` + shared dispatch
  gate), so simulated time needed nothing built. Recorded as standing rule C6.
