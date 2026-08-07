# Work plan — production readiness (executing doc 22)

*Created 2026-08-06. Executes the unticked items of [`22-readiness.md`](./22-readiness.md) — the
production-readiness audit. Format and rules follow [`16`](./16-work-plan-operator-console.md) and
[`23`](./23-work-plan-console-remaining.md); their Discovered Issues Logs and Standing traps are
**binding scar tissue** and bind every item here.*

*Doc 22 is the evidence; this file is the execution. Where they disagree on **what** is wrong, doc
22 wins. Where they disagree on **sequencing or validation**, this file wins. Item text is written
so an executor with no context beyond the repo, doc 22's matching RD entry, and the files named can
implement it.*

**Ordering principle: user harm × likelihood, not effort.** Doc 22's thesis governs every judgement
call — *the enemy is silent success, not crashes*. An item is not done when the code changes; it is
done when something would now **tell us** if it broke.

---

## EXECUTION RULES

1. **Executors work in isolated worktrees**, one per item (or group as marked). They do NOT edit
   this file — the orchestrator owns the tick-boxes and the Discovered Issues Log. Surprises go in
   the final report as bullets; the orchestrator files them below.
2. **Verify your base first.** Workflow worktrees have repeatedly come up on a stale pre-product-
   layer commit. First command in every worktree: `git log --oneline -1`, and confirm it matches
   the base sha in your brief. If it does not, `git reset --hard <base-sha>` before anything else.
3. **This branch is shared with other live sessions.** NEVER `git add -A`, NEVER `git commit
   --amend`, NEVER `git stash` (it is shared across worktrees and has swapped two executors' work),
   NEVER rebase. Stage explicit paths only. If `git status` shows files you did not touch, leave
   them alone and say so in your report.
4. **Run the item's validation commands verbatim before claiming done.** The orchestrator re-runs
   them at merge. "Done" without a green validation run is not done.
5. **Prove non-vacuity.** Every item here fixes something that was silently wrong, so every test
   added must be shown to fail against the unfixed code (revert the fix, run, record the failure
   count and headline, restore). A test that passes both ways is worse than no test — it is a
   second silent success. Report the revert evidence.
6. **The compose stack is a serial resource.** Only the orchestrator runs `docker compose up`/
   `run-stack-e2e.sh`. The e2e rig may hold 8080; the main stack runs on `WEB_PORT=8081` with the
   mock-mode overrides in `README-stack.md` ("If you have a real `.env`: two traps"). Executors
   develop against vitest/jsdom and `go test`, and must say clearly when a claim is untested.
7. **Do not touch:** `.env` (real credentials — read the README section, never edit the file),
   `e2e/experiments/`, `sandbox/` *except where an item names it explicitly*, the shared test
   Postgres `ao-test-pg` on `localhost:5433` (use, never delete or recreate), and the real
   Anthropic API.
8. **Search hygiene:** three `web/src` files still contain literal NUL bytes and grep as binary —
   **always `grep -an` in `web/src`**. Doc 22's RD25 standing rule applies everywhere: *a tool
   reporting "no matches" is not evidence of absence.* Verify a negative with a second,
   differently-built method before believing it.

## CONFLICT WATCH (read before touching Go HTTP/auth code)

**This plan executes on the `readiness` branch, in a worktree.** `product-layer` belongs to the
parallel **embeddable-singleton** session (`design/2026-08-06-embeddable-agent-orange.md`, 5 of 19
tickets done as of `2c38be2`). The orchestrator merges `product-layer` → `readiness` before each
wave, and merges each finished wave back to `product-layer`. Executors never touch `product-layer`.

Their landed work (`45eba55` T5, `c960e84` T1–T4) changed the ground under several items here:
`ownsSession` is now the single tenancy gate on every session-by-ID route, `httpapi.Identity` gains
`SessionScope`, and an `X-API-Key` header now authenticates. Read those commits before touching
`go/httpapi/`. Files they actively own: `go/cmd/agentd/{auth,googleauth,apikey,main}.go`,
`go/extension/devclaims/`, `go/httpapi/{httpapi,lifecycle,stream,history,session}.go`. Items
touching them are marked **⚠ CONFLICT-WATCH** — keep the edit minimal and say so in the report.

**The interleave contract (agreed with Kai, 2026-08-06) — binding in both directions:**

| Ordering | Why |
| --- | --- |
| **D2 and B1 land before their T12** (the embed page) | `Runner.Stream` serves `/reconnect` as a bare `io.Copy` that persists nothing, and a dropped SSE stream reports the turn finished. An iframe embedding a live session in a customer product would ship both. |
| **S3 lands before their T18** (full-content memory over HTTP) | `memory_create` reports `embedded:true` while storing no vector whenever pgvector is absent — the managed-Postgres case. T18 would faithfully serve silently keyword-only memory. |
| **D1 waits for their T6** (session names) | Same table, same migration file, same `sessions.go`/`types.go`. |
| **S2 and B3 wait for their T9** (session-mode schedules) | Both rewrite `scheduler.go:203-301`; T9 adds `target_session` to `Schedule`, which B3's key-set guard would otherwise trip inside *their* thread. |
| **Migration numbers**: they own **035** (T6) and **036** (T9); this plan takes **037+** | Highest today is `034_workers_frozen`. Reserved, so no renumbering at merge. |

## Validation shorthand

- **GO** = `cd go && go build ./... && go vet ./... && go test ./...`
- **GOPG** = `cd go && AGENTKIT_TEST_POSTGRES_URL='postgres://postgres:test@localhost:5433/postgres?sslmode=disable' go test ./...`
  *(Go items run **both**. A green GO alone does not prove the Postgres paths — they skip silently.)*
- **WEB** = `cd web && npm ci && npm run typecheck && npm test`
- **SHELL** = `cd examples/web && yarn && yarn typecheck && yarn build` (**yarn only** in that package)
- **E2E** = `./e2e/run-stack-e2e.sh up mock`, then
  `./e2e/run-stack-e2e.sh test mock -- e2e/features/<spec>` (orchestrator-run, serial, mock mode)

## Standing traps carried in (from docs 16, 21, 23 — do not relearn these)

- **gorm `default:` tags: NEVER** on a column where zero/false is meaningful. DDL defaults belong in
  the migration SQL; defaulting belongs in `normalize()`.
- **Migrations**: append to `agentMigrations`, next free number. Sibling items in this plan claim
  numbers too — the orchestrator renumbers on conflict at merge.
- **`PUT` is create-or-replace, not patch.** Any read-modify-write of a worker/subscription/settings
  row must carry ALL fields including `frozen`.
- **`web/` is a component library**: no router, no new runtime dependencies, every export through
  `web/src/index.ts`, query state through the History API.
- **The stack serves a BUILT web image** — UI edits are invisible in the browser until
  `docker compose up -d --build web` (orchestrator only).
- **Assert on happens-after signals, never sleeps.** Delete sessions in e2e teardown (the port pool
  is 100 and a leaked session holds one). Worker names must not be substrings of each other.
- **`web/` pins the config-action vocabulary by count** (`configLog.test.ts`, `toHaveLength(19)`).
  Items D1 and I1 add config verbs — update that count and say so.
- **Config events are ms; project events and catalogue rows are SECONDS.** Any join hits this.

---

## Wave 0 — the two red tests (merge first; nothing else is trusted until the suite is green)

*`e2e/features/product-ui.stack.spec.ts` has exactly two failing tests and they have been red for
weeks (doc 23 DIL proved them pre-existing by bisecting the built image). While they are red, no
one can read a red product-ui run as a signal about anything else — which is precisely the
"silent success" shape this plan exists to kill, one level up.*

- [x] **P1 — "project settings: edit, save, and survive a reload": the defect is the TEST.**
  *Diagnosed by the orchestrator against the source; doc 23's DIL hypothesis (the settings fetch
  re-seeding the dirty baseline) is **wrong** and should not be pursued.* The Save button is
  `disabled={!s.canSave || !s.dirty}` (`web/src/components/ProjectSettingsPage.tsx:167`), and
  `canSave` requires `rationale.trim() !== ''` (`web/src/useProjectSettings.ts:142-148`). The test
  (`e2e/features/product-ui.stack.spec.ts:26-52`) fills the prompt and the base image and clicks
  Save without ever filling the **"Why?"** field (`ProjectSettingsPage.tsx:151-160`,
  `inputProps={{ 'aria-label': 'Why?' }}`). So the button is correctly disabled forever and the
  test times out at 240s. The test predates decision **K2** ("every human edit carries a required
  one-line reason", doc 15 / item E3) and was never updated when K2 landed.
  **Fix:** in that test, fill `page.getByLabel('Why?')` with a real reason before clicking Save.
  Do NOT relax the product's requirement — K2 is a settled decision (doc 16, "Decisions carried
  in"). Check the two sibling tests in the same file for the same omission (the worker tests use
  `WorkerEditor`'s own Why? field — verify whether they pass because they fill it or because the
  worker route differs, and say which in your report).
  While there: confirm the final assertion `configActions(api) == ['project_settings_put']` still
  holds with a rationale attached (the rationale rides the body, not the action).
  *Validation:* E2E on `product-ui.stack.spec.ts` — the settings test green, twice in a row.

- [x] **P2 — "a turn interrupted by a reload is still persisted": diagnose before fixing.**
  `e2e/features/product-ui.stack.spec.ts:151-174` clicks `new-session`
  (`examples/web/src/Sidebar.tsx:66-75`) then waits for `page.locator('textarea').first()` to be
  enabled, and it never is within 120s. **Decide TEST or PRODUCT with evidence before changing a
  line**, and state the evidence in your report. The composer textarea is disabled only while
  `isStreaming` (`web/src/components/AgentChat.tsx:692`, `isStreaming` resolved at `:137`), so
  there are three live hypotheses and they have different fixes:
  (a) **selector rot** — `textarea` `.first()` matches some other textarea now that the shell has
      eight views and the Desk is the landing view (K1); the chat composer may not even be mounted
      when the locator resolves. → test fix: scope to the composer by placeholder
      (`Type a message...`) or add a `data-testid`.
  (b) **the session never becomes usable** — creation fails or the row stays `creating`, so the
      view sits in a state that never enables input. → **product** defect, and it is doc 22's
      RD20 (`create_error` has zero consumers in `web/src`, so the UI renders a bare error): the
      user sees exactly what this test sees — nothing, forever. Fix the test to assert the error
      surfaces, and hand the product half to item **F4**.
  (c) `isStreaming` latches true from a stream that never completes → product, and adjacent to
      RD26 (item B1).
  Get the evidence cheaply: run the spec headed or with a trace, dump the DOM at the timeout, and
  read `docker compose logs agentd` for the session id. **Report which hypothesis the evidence
  supports; if it is (b) or (c), fix the product defect here only if it is small, otherwise fix the
  test to fail *loudly and fast* with the real reason and file the product half as a new item.**
  *Validation:* E2E on `product-ui.stack.spec.ts` — both tests green, twice in a row, and the whole
  file green (it is the shared-stack canary for every later wave).

---

## Wave 1 — the first hour (highest likelihood: every first user walks these)

*Doc 22's first-run audit: "the journey is polished right up to the point that matters, and then
stops." These four items are that point. All four are parallel — disjoint files — except F4.*

- [x] **F1 — RD17: build the one button that makes something happen.** *The blocker.* There is no
  way to trigger a job from the UI: no `POST` to `/agent/events` anywhere in `web/src` or
  `examples/web/src`. 11 of 14 built-in topologies are event-driven only; the other three fire
  hourly at :00 at best. The topology interview *asks* which event type wakes the worker and then
  offers no way to send one. The server route exists and is registered
  (`go/httpapi/httpapi.go:307`, `IngestEvent: "POST /agent/events"`) — **this is a browser-side gap
  only**. The design already ordered it: `docs/product/15-operator-console-design.md:258-266`
  ("after applying a topology whose only clock is cron, an operator has no way to make anything
  happen… **One button.**").
  **Fix:** add "Emit this event" to `web/src/components/EventReplayPanel.tsx`, posting the draft the
  panel already composes (it currently prints a curl command for the user to run by hand, `:91`).
  Behind a confirm dialog whose text says plainly that this writes a **real** event into the
  project and may wake workers — that is the footgun the existing comments (`web/src/events.ts:33`,
  `EventReplayPanel.tsx:4-8`) were guarding against, and a labelled confirm is the answer, not
  silence. Rewrite both comments: the UI now POSTs, deliberately, in one place. Export any new
  props through `web/src/index.ts`. Show the server's own error text on failure (the
  `configApi.request` convention), and show the created event id on success so the user can follow
  it into Events.
  **Tests:** vitest — the panel posts the composed body to the right endpoint, does nothing until
  the confirm is accepted, and renders the server's error text on a 400.
  *Validation:* WEB + SHELL. Then E2E: extend `e2e/features/console.stack.spec.ts` (or add to
  `product-ui`) with *apply a topology → emit its event from the UI → a delivery appears and goes
  `ok`*, which is the first-run path end to end and readiness bar #4.

- [x] **F2 — RD19: an event that matches nothing must say so.** Three silent no-ops, one item:
  (a) `go/cmd/agentd/router.go:235-247` loops subscriptions, `continue`s on non-matches and marks
      the event delivered — **zero matches is byte-identical to a healthy fan-out**: no log, no
      row, no annotation. "My worker didn't wake up" has no observable signal at all.
  (b) `go/agentdb/events.go:477-498` validates the event type's *shape* at write time but never
      that the named worker exists or the type is one anything subscribes to, so `worker.faild` is
      accepted silently; envelope filter keys are unvalidated and any key the envelope lacks
      returns false (`router.go:417-420`).
  (c) `go/compose.go:224-232` skips an empty briefing selector silently, so a typo and "nothing
      written yet" are indistinguishable and the job runs with a quietly thinner prompt.
  **Fix:** one log line at `router.go:247` when zero subscriptions matched, naming project, event
  type, and how many subscriptions were considered; the same at `compose.go:224` naming the
  selector and the worker. Validate at write time that a subscription's filter keys are keys the
  envelope schema can carry, and that a subscription naming a worker names one that exists —
  **rejecting at write time, where the user is looking**, not at fire time. Follow the cron
  validator's diagnostics style (doc 22 calls it the best in the product: a write-time 400 naming
  the field with a worked example).
  **Do not** make a zero-match event an error — fanning out to nothing is legal. Make it *visible*.
  **Tests:** a zero-match publish emits the line and still marks delivered; a subscription with an
  unknown filter key is refused at write time with a message naming the key and the legal set; a
  briefing selector that matches nothing logs, and the job still runs.
  *Validation:* GO + GOPG, with the revert-and-fail evidence per rule 5.

- [x] **F3 — RD22 + RD23 + RD21(docs half): the documentation stops lying.** One commit, no code.
  (a) **RD22 — Postgres credentials are undocumented and the volume initialises once.**
      `docker-compose.yml:69` builds `DATABASE_URL` from `POSTGRES_USER`/`POSTGRES_PASSWORD`/
      `POSTGRES_DB` (defaulting to the literal `agentorange`) and `.env.example` has **zero**
      `POSTGRES` hits. The trap: `pg-data` initialises on first `up`, so setting a password
      afterwards re-renders `DATABASE_URL` but not the database, and agentd dies with raw gorm text
      naming neither the `postgres` service nor the stale volume. **Fix:** document all three in
      `.env.example` with a comment saying a change needs `docker compose down -v`; wrap agentd's
      connect error with what to check (that half is a small code change in the boot path — keep it
      to the error-wrapping line, matching the boot-validation style doc 22 praises).
  (b) **RD23 — four stale "this doesn't exist" claims in `docs/18-workers-memory-events.md`**, all
      false: `snapshot_ttl_days` is called inert though `cmd/agentd/main.go:269,288,296` wires the
      reaper; "no `GET /agent/images` route" though `httpapi.go:305` registers it and the image
      picker uses it; "no other HTTP route takes a rationale" though six do; "no server-side worker
      filter" though `go/httpapi/history.go:112-114` implements it. Plus a cross-link to a docs/15
      section whose title now says the opposite; the same reaper claim in
      `docs/01-architecture.md:280-281`; `.env.example:73-74` documenting the pre-GC port lifecycle
      and contradicting itself twenty lines later; and `installations/README.md:75-76` claiming
      `.claude/` is gitignored when `git check-ignore` says it is not. **Verify each against the
      code before deleting the claim** — `history.go` is ⚠ CONFLICT-WATCH (read it, do not edit
      it).
  (c) **RD21, docs half only** — say in `docs/18` that operations doctrine v1 exists, where it
      lives (`docs/product/doctrine/`), that **nothing seeds it today**, and that an operator who
      wants it must paste it into the project prompt. The opt-in-checkbox half is **KAI-GATED**
      (see G2) — do not build it.
  *Validation:* every command, path, route and relative link you touch resolves; GO (for the
  error-wrapping line only); no `.env` edit of any kind.

- [x] **F4 — RD18 + RD20: tell the user which model answered, and why the job failed.** ⚠
  **CONFLICT-WATCH** *(touches `go/cmd/agentd/googleauth.go`, held by the embeddable session — the
  orchestrator confirms it is committed and merged before this starts; if not, split and land the
  RD20 half alone).*
  (a) **RD18 — mock mode is invisible and it is the default.** Both credential lines ship blank in
      `.env.example:9-10`, so a user following the README exactly gets the mock model. agentd logs
      it (`go/cmd/agentd/modelproxy.go:49`); the browser is told nothing —
      `/auth/config` returns only `{modes, google_client_id}` (`googleauth.go:489-...`,
      `authConfigHandler`) and `examples/web/src/App.tsx:125` hardcodes an Opus label regardless of
      the actual proxy. A scheduled job in mock mode writes plausible canned output into Desk,
      Events and Jobs with **no marker anywhere**. That is a credibility failure, not a UX one.
      **Fix:** add a credential mode (`mock` | `api-key` | `subscription`) to the `/auth/config`
      payload, computed where the proxy decides it, and render a persistent badge in the shell that
      is impossible to miss in mock mode. One field, one component.
  (b) **RD20 — failure reasons never reach the user.** `go/cmd/agentd/dispatch.go:437-438` logs the
      reason and `UpdateDeliveryStatus` writes status only; the code admits it at `:195-197` and
      the UI carries the limitation honestly (`web/src/desk.ts:277-279`). So the newcomer's answer
      to "why did my worker fail?" is `docker compose logs agentd`. **Fix:** a `reason` column on
      `event_deliveries` (new migration), populated from `d.fail(...)` where the string already
      exists, surfaced on the delivery row in Desk/Jobs. Equally invisible and included here:
      `create_error` is populated on provisioning failure and served at
      `go/httpapi/lifecycle.go:125` with **zero consumers in `web/src`** — read it wherever
      `status === "error"` renders (browser-side only; do not edit `lifecycle.go`). RD15's
      `failure_reason` ask is the same column — do not add a second one; I1 owns RD15's other half.
  **Tests:** `/auth/config` reports each mode; a failed delivery carries its reason through the
  store and out of the API; the browser renders `create_error` rather than a bare "error".
  *Validation:* GO + GOPG + WEB + SHELL, and an E2E assertion that a failed delivery shows a reason.

---

## Wave 2 — the browser tells the truth (all web-only, all parallel, disjoint files)

*Doc 22's browser sweep. Every one of these is the UI **asserting a comforting falsehood** after a
failed fetch — the silent-success class, in the surface a user actually looks at.*

- [x] **B1 — RD26: a dropped SSE stream reports the turn as finished.** `checkSessionStatus` returns
  `null` on *any* failure — network error, timeout, non-2xx — from an empty catch
  (`web/src/useAgentSession.ts:460-471`), so "the probe failed" and "there is no active query" are
  the same value. A `null` status falls through to a branch that only `console.log`s (`:534-547`),
  and the "Connection lost" error is gated behind `reconnectDepth >= MAX_RECONNECT_ATTEMPTS`
  (`:604-607`), never reached at depth 0. The UI then clears the spinner and calls
  `stopStuckDetection()` unconditionally (`:672-676`) — switching off the one remaining signal
  exactly when it would have fired. **A truncated answer reads as a complete one** while the agent
  is still running in its container producing output nobody will see.
  **Fix:** make `checkSessionStatus` distinguish *unreachable* from *idle* (three-valued, or throw);
  set the connection-lost error on any stream that ends without `query_complete` and cannot be
  positively confirmed complete; do not stop stuck-detection on an unconfirmed end.
  **Tests:** vitest with a fetch that rejects, one that 500s, and one that answers "no active
  query" — the first two must surface an error and keep the stuck detector armed; the third must
  settle quietly. Non-vacuity: all three must fail against today's code.
  *Validation:* WEB.

- [x] **B2 — RD27 + RD28: a failed load must not render a reassuring empty state.** Two instances
  of one bug; the correct pattern is already in-repo at
  `web/src/components/TopologyOnboarding.tsx:186` (gates its empty state on `error === null`).
  (a) **RD27** — `web/src/useAttentionRequests.ts:78-88` sets an empty list on failure but leaves
      `available` true (only 404/501 count as "unwired"), and `attention.error` is **absent from
      the error chain** in `useDesk.ts:250` and `useAsksCount.ts:77`. Because `available` stayed
      true, the designed fallback — parked deliveries without their messages (`useDesk.ts:217-219`)
      — is skipped in exactly the case it was written for. An operator with three workers parked at
      `awaiting_human` sees a **zero badge and "Nothing is waiting on you"**, closes the tab, and
      the approvals sit until they time out. **Fix:** add `attention.error` to both chains and
      drive the fallback off "did this load succeed", not off `available`.
  (b) **RD28** — `web/src/useWorkers.ts:52-58` leaves the initial `[]` on failure and
      `DeskPage.tsx:113` computes `firstRun = !loading && workerCount === 0`, replacing the entire
      Desk (`:137`). Same at `WorkersPage.tsx:123` and `WorkerList.tsx:59` (which has no error path
      at all). **An operator with a dozen workers and live jobs is invited to "start from a
      topology"** — and users read the confident product sentence, not the raw-server-text banner
      beside it. **Fix:** gate all three first-run/empty states on `error === null`.
  **Tests:** for each surface, a failing fetch renders the error state and **not** the empty/
  first-run state, and the attention badge does not read 0 on a failed load.
  *Validation:* WEB. Report whether `useConfigLog`'s "not wired vs failed" distinction (which is
  already correct) is worth extracting as a shared helper — do not extract it unless it is clean.

- [x] **B3 — RD29: a wire-shape guard, in the `token_usage.go` discipline.** No drift today —
  `ProjectSettings` (11 fields), `Worker` (13), `Subscription` (9), `Schedule` (10) all match their
  browser mirrors — but the mechanism is live and silent: `coerceProjectSettings`/`coerceWorker`
  build a fresh object from an **explicit field list**, so an unknown key is dropped on read; the
  body helper spreads that already-lossy object; and `PutProjectSettings`/`PutWorker` are
  whole-object writes assigning every column. **So the next time any human saves any setting in the
  console, a newly-added engine field is written back as its zero value** — and no test on either
  side enumerates keys, so both suites stay green throughout.
  **Fix (one captured artefact, two readers):** commit `web/src/wire-shapes.json` holding sorted key
  lists per struct, **generated from the Go structs** (captured from the writer — never hand-authored,
  per doc 22 §2's standing rule); a Go test that marshals each zero-valued struct and fails if its
  key set differs from the file; a vitest that fails if `Object.keys(coerceX(fullFixture))` differs.
  Adding a field on either side then fails the other side's test. **The Go half alone catches the
  direction the risk actually runs** — if you can only land one, land that.
  Say in the file's header how to regenerate it. (Schedules are already safe — `updateSchedule`
  patches named fields over a freshly-read row; note that as the shape the other two routes should
  eventually follow, and do not change them here.)
  *Validation:* GO + WEB; non-vacuity by adding a field to a Go struct locally and watching **both**
  tests fail, then removing it.

- [x] **B4 — RD25: finish the NUL sweep and guard it in CI.** R5 (doc 23) normalised `useEvents.ts`
  and `desk.ts`; **three files still carry literal NULs** — verified by byte count on
  2026-08-06, since grep cannot be trusted for this: `components/WorkerEditor.tsx` (2),
  `orgchart.ts` (3), `useStagedFeed.ts` (1). Those files are invisible to `grep` (GNU grep calls
  them binary and *reports no matches*, which reads as "cleared") and the ones with a NUL inside
  the first ~8000 bytes additionally render as `Binary files differ` with a stat line reading **0
  insertions, 0 deletions** — a PR touching the worker editor shows a reviewer nothing.
  **Fix:** replace each literal byte with an escape in the source (`'\u0000new'`), which is
  **behaviour-identical** — the runtime string is unchanged, so WorkerEditor's `'\x00new'`/
  `'\x00none'` sentinels keep working and every composite key keeps its collision guarantee. Do
  **not** substitute a different separator character in `orgchart.ts`/`useStagedFeed.ts` unless the
  key is ephemeral and you say so (R5 chose U+001F for exactly that reason — follow whichever the
  call site justifies, and state which you chose and why).
  Then add a CI guard: a step in `.github/workflows/ci.yml` failing the build if any tracked source
  file contains a raw NUL (`git ls-files -z | xargs -0 grep -lP '\x00'` or a small script — beware
  bash collapsing `$'\x00'` to the empty string, which is how the orchestrator's own first attempt
  reported *every* file as affected).
  *Caution: this is precisely the edit whose diff is invisible. **Verify by byte count, before and
  after**, not by eye — and put the byte counts in your report.*
  *Validation:* WEB + SHELL; `grep -n` now works on all three files; `git diff --stat` shows only
  the files you touched; the CI guard fails when you temporarily reintroduce a NUL.

---

## Wave 3 — nothing a user made disappears (readiness bar #2)

*Doc 22's durability blockers. Go-side; sequenced by file, not parallel — D2/D4 and D3/I1 collide.*

- [x] **D1 — RD5: deleting a session destroys the conversation, irreversibly, from an unguarded
  one-click button.** ⚠ **CONFLICT-WATCH** *(three lines in `go/httpapi/lifecycle.go`, held by the
  embeddable session — land the rest first if it is still uncommitted)*.
  `agent_query_events.session_id REFERENCES agent_sessions(id) ON DELETE CASCADE`
  (`go/agentdb/migrations.go:139`; same for `agent_messages` at `:77` and `agent_artifacts` at
  `:50`), and `DELETE /agent/session/{id}` is a hard row delete (`go/httpapi/lifecycle.go:183` →
  `go/agentdb/sessions.go:187`), fired from an **icon button with no confirmation**
  (`web/src/components/AgentSessionList.tsx:53-56`). No soft delete, no tombstone, no export. This
  is why the live calibration found `agent_query_events` empty for every session — the rig deleted
  sessions, and the code did what it does for any user tidying a list. *Second defect in the same
  three lines:* the handler **discards the delete error** (`_ =`) and returns `204 No Content`
  regardless — a failed delete reports success.
  **Fix:** `deleted_at` on `agent_sessions` (new migration); listings filter on it; the cascade
  stays for a separate operator purge (**the purge itself is KAI-GATED — G3; do not build it**);
  a confirmation step in the UI naming what is lost; and stop discarding the delete error.
  **Tests:** a deleted session vanishes from listings but its query events survive; a store error
  on delete produces a non-2xx; the UI does not delete without confirmation.
  *Validation:* GO + GOPG + WEB, plus E2E if `product-ui` covers deletion.

- [x] **D2 — RD6 + RD24: a crash mid-turn loses everything the model said.** The user's **prompt
  survives** (`seedUserMessage` writes it under `context.WithoutCancel`, `go/runner.go:2338-2353`,
  called at `:892`). The model's output becomes durable exactly once, at `query_complete`
  (`runner.go:943` → `go/events/pipeline.go:200-227`), and until then it lives in the `collected
  []Envelope` local inside `pipeline.Run`, whose **only caller in the repo** is that one line.
  `NewPipelineWithCadence`, written for exactly this case, has zero non-test callers. Worse, **the
  data survives agentd's death in a place agentd has no code to read**: the sandbox buffers up to
  2000 events in RAM and replays them to a newly attached stream, so a reconnecting **browser
  renders the turn** while nothing writes it to Postgres — `Runner.Stream` serves both `/stream`
  and `/reconnect` and is a bare `io.Copy` (`runner.go:975`) that never constructs a `QueryContext`
  and never touches the pipeline. The window then shuts by itself (the sandbox drops the buffer
  immediately after `query_complete`) and silently discards its oldest events past 2000. **A wiring
  gap, not a limit.**
  **RD24 rides along:** rehydration from the database runs **only** on the snapshot-restore path;
  the orphan-recover path deliberately skips it (`runner.go:1396-1402`). So after an agentd restart
  that re-adopts a live container, the harness holds a turn Postgres never recorded — the model
  answers with reference to something the user cannot see and no operator can retrieve, and it is
  silently erased at the next archive/restore when `loadConversation` overwrites the array.
  **Fix — two changes, and (a) alone is not enough:** (a) pass a cadence to the default pipeline
  (`runner.go:167`), fixing the crash case; (b) route `Runner.Stream` through the pipeline so a
  reconnect drains the sandbox buffer into Postgres (`runner.go:975`). Doing only (a) leaves the
  reconnect path silently lossy and leaves RD24 open.
  **Tests:** a turn killed before `query_complete` leaves partial assistant output in
  `agent_query_events`; a reconnect after the original stream died persists what it renders; no
  duplicate rows when both paths see the same events (say how you guarantee that).
  *Validation:* GO + GOPG. Then the **stack confirmation, now a check rather than an unknown**
  (orchestrator, mock mode): start a long turn, `docker kill` the **agentd** container, restart,
  hit `/reconnect`, and observe that events render **and** `agent_query_events` now gains a row.
  Record the before/after row counts.

- [x] **D3 — RD7 + RD10: a job wedges forever, or runs twice.** Both are ordering/atomicity defects
  in `go/cmd/agentd/dispatch.go`; one item because they touch the same paths.
  (a) **RD7** — `settle` releases the session lease *before* stamping the delivery's terminal status
      (`dispatch.go:632-637`: `releaseLease` then `onEnded`). A crash in that window — or a failed
      status write, whose error is only logged — leaves `status='running'` with
      `lease_expires_at=0`, and `ListExpiredLeaseSessions` filters on `lease_expires_at > 0`
      (`go/agentdb/leases.go:90`), so the reaper can **never** see it. Nothing else sweeps
      deliveries by age. The row eats a `max_instances` and a `max_concurrent_jobs` slot for the
      life of the project. `dispatch.go:351` calls the lease reaper "the backstop" — by that line
      the lease is already released, so it cannot be. **Fix:** stamp terminal status first, release
      the lease second; add an age-based sweep over `running` deliveries; correct the comment.
  (b) **RD10** — `UpdateDeliveryStatus` is a read-then-`Save` with no compare-and-set on
      `status='pending'` (`go/agentdb/events.go:725-757`); the only guard is against an in-memory
      snapshot (`dispatch.go:209`). `DrainPending` runs from **two goroutines in one process** —
      the router loop every 3s (`router.go:364`) and the scheduler loop (`scheduler.go:191`) — so
      one delivery can dispatch twice and **a user's job runs twice** (for the first real use case,
      a marketing worker, that means the same outbound action twice). The orphan session then emits
      a spurious `worker.failed{lost}` 15 minutes later. **Fix:** conditional update on the
      pending→running transition; act on `RowsAffected`; the loser drops the delivery silently.
      While there: `ReleaseSessionLease` ignores `RowsAffected` and treats a missing row as success
      (`leases.go:66-79`), which breaks `router.go:513-516`'s at-most-once justification and lets
      two reapers both emit `worker.failed{lost}` — fix it and the comment together.
  **Tests:** a status-write failure after lease release leaves nothing invisible to the sweep; two
  concurrent `DrainPending` calls over the same pending row produce exactly one running delivery
  (live-Postgres, real concurrency, not a fake).
  *Validation:* GO + GOPG, with revert-and-fail evidence for the concurrency test especially — a
  race test that passes against the unfixed code is proving nothing.

- [x] **D4 — RD8 + RD12: the archive loop leaks a port forever, and lies about artifacts.** *After
  D2 merges — same file.*
  (a) **RD8** — `Recover` re-adopts any container labelled with a session id
      (`go/execenv/docker/dind.go:484`), but with no session row `r.Snapshot` fails and the archive
      loop logs "snapshot failed, keeping the container" and `continue`s — **every minute, forever**
      (`go/runner.go:1104`). One port from the pool of 100 lost per occurrence, with no way back
      short of a restart. **Fix:** if the session row is absent, destroy rather than skip. (Keep the
      existing keep-the-container behaviour for a genuine snapshot failure with a live row — that
      one is correct.)
  (b) **RD12** — `teardownInstance` is shared by `Destroy` and the archive loop and unconditionally
      calls `Artifacts.MarkLost` (`go/runner.go:694`), stamping un-uploaded `live` artifacts as
      `lost` (`go/agentdb/artifacts.go:141-143`). For the archive path that is **false** — the
      snapshot is taken first, so the files return on restore — and nothing ever regresses the
      status. It contradicts `gc.go`'s own header claim that "nothing a user can see is lost".
      **Fix:** a flag on `teardownInstance` so the archive path skips `MarkLost`; fix the header
      comment if it needs it.
  **Tests:** a re-adopted container with no session row is destroyed and its port returns to the
  pool; an idle-archive cycle leaves artifact status untouched while `Destroy` still marks lost.
  *Validation:* GO + GOPG (some cases need Docker — say clearly which you could not run).

---

## Wave 4 — degrades a working deployment over days

- [x] **S1 — RD9: a snapshot in daily use is reaped out from under the user.** `snapshotExpired`
  tests only `ci.ExpiresAt` (**`go/snapshot_reaper.go:195`** — note: doc 22 cites
  `go/cmd/agentd/snapshot_reaper.go`, which does not exist; the file is at the module root).
  `last_resumed_at` is stamped on every launch (`go/cmd/agentd/imageresolver.go:119-125`) but does
  not extend expiry, despite the reaper's own header (`snapshot_reaper.go:21`) implying it bears on
  it. A worker pinned to a named image gets a hard `ErrCustomImageUnmaterialisable` 30 days after
  burn regardless of daily use. **Fix:** extend expiry on resume (preferred — the stamp already
  exists and is written on the launch path), and/or warn before reaping anything recently resumed;
  fix the header comment either way. State which you chose.
  **Tests:** an image resumed within the window survives a reaper pass that would otherwise delete
  it; an untouched one still dies (the reaper must not become vacuous).
  *Validation:* GO + GOPG.

- [x] **S2 — RD11: missed schedule occurrences vanish without a trace.**
  `go/cmd/agentd/scheduler.go:156-161` evaluates only the current minute
  (`s.now().In(s.loc).Truncate(time.Minute)`, `:160`) — no watermark, no catch-up. An hour of
  downtime means 60 occurrences with **no firing row, no event, no delivery and nothing recording
  that they were missed**: indistinguishable from "never scheduled". Worse in a narrow window: a
  kill between `CreateProjectEvent` (`:227`) and `EnsureDelivery` (`:248`) leaves a `schedule.fired`
  event in the user's feed for a job that never ran, with the occurrence permanently consumed.
  **Fix:** a per-schedule watermark (last evaluated minute), and on restart emit `schedule.missed`
  for unevaluated occurrences rather than replaying them — **do not silently run 60 jobs at boot**;
  the user must be told, and a bounded catch-up (if any) must be an explicit, documented choice.
  Say in the item report which you built and why. Adding a `schedule.missed` event type touches the
  event vocabulary — check `configLog.test.ts`'s pinned count and the web reducer.
  **Tests:** downtime spanning N occurrences produces N missed records and zero surprise jobs; the
  narrow window between event creation and delivery is closed or its remnant is visible.
  *Validation:* GO + GOPG.

- [x] **S3 — RD3: `memory_create` reports `"embedded": true` while storing a row with no
  embedding, permanently.** `go/agentdb/memories.go:137` drops the vector from the INSERT when the
  `content_embedding` column is absent; `go/cmd/agentd/mcp_memory.go:288` reports
  `Embedded: vec != nil` — true whenever the **embedder** returned a vector, regardless of what was
  **stored**. The comment two lines above states the correct principle ("CreateMemory returns the
  row as the database holds it… that is what is echoed") and the next line violates it. The column
  can genuinely be absent: migration `022_memories` (`migrations.go:293-304`) swallows a failed
  `CREATE EXTENSION vector` with `RAISE NOTICE`, which is exactly what happens on managed Postgres
  where the app role lacks the privilege — **the GCP deployment direction**. Memories are
  append-only with no update path by design, so those rows can never be embedded later and search
  silently degrades to keyword-only **forever**. Second route: `memoryHasVectorColumn`
  (`memories.go:373-386`) latches `sync.Once` on `err == nil && n > 0`, so one transient query error
  pins it false for the process lifetime and blames an absent column.
  **Fix:** distinguish "query failed" from "column absent" so the flag cannot latch on error; derive
  `embedded` from what the store actually wrote; fail the create when a vector was supplied and
  cannot be stored (matching the write path's existing fail-hard-on-embedder-error decision); and
  make the absent-extension case **loud at boot**, not at first search.
  **Tests:** live-Postgres with the vector column absent — the create fails or reports
  `embedded:false`, and never `true`; a transient error on the probe does not latch.
  *Validation:* GO + GOPG including the column-absent case.

---

## Wave 5 — integrity, cost and record-keeping (real, not urgent)

- [x] **I1 — RD15 + RD13: the record of what happened must be trustworthy.** *After D3 merges
  (shares `dispatch.go`) and F4 (shares the delivery `reason` column — do not add a second).*
  (a) **RD15** — `event_deliveries.session_id` is a plain VARCHAR with no foreign key
      (`go/agentdb/migrations.go:352`), so job history outlives the session it points at: after a
      delete the user sees a green `ok` delivery with a **dead transcript link**. **Fix:** render a
      "transcript deleted" state rather than a broken link (with D1's soft delete this becomes
      answerable rather than guessed). The `failure_reason` half is F4's column.
  (b) **RD13** — the config log's append-only guarantee is **enforced only in tests**.
      `InstallConfigEventGuard` is opt-in and every caller is a test
      (`go/agentdb/config_events.go:836`); `cmd/agentd/main.go:259-266` states agentd never arms it;
      `config_events` is not in the guarded-table set; migration 026 adds no trigger or `REVOKE`;
      and `Store.DB()` is exported. **Fix:** a DB-level `BEFORE UPDATE OR DELETE` trigger on
      `config_events` permitting only `emitted_at` to change. *This matters more than its position
      suggests — the config log is what the doctrine work treats as the record of truth.*
      Separately, `FoldTo` has no production callers and diverges from reality in three ways
      (project prompt folds as a second entity from the settings row it lives in and the two
      disagree after a mixed sequence; skills fold by name while `agent_skills` holds a row per
      revision; five wired paths mutate guarded tables with no config event at all).
      **Default decision, taken here so nobody relitigates it: mark `FoldTo` experimental** in its
      doc comment, listing the three divergences, and do **not** wire it to a read path. If you
      believe it should be wired instead, stop and report — that is a Kai call.
  **Tests:** an `UPDATE`/`DELETE` against `config_events` through raw SQL is refused by the database
  (live-Postgres — this cannot be proven with the in-process guard); a delivery whose session is
  gone renders the deleted state.
  *Validation:* GO + GOPG + WEB.

- [x] **I2 — RD16: transcript ordering is a coin toss within a second.** `ListQueryEvents` orders by
  `created_at ASC` (`go/agentdb/messages.go:188`) where `created_at` is `time.Now().Unix()` —
  **seconds** (`:167`) — and the id is a random uuid. Two queries in the same second replay in
  arbitrary order, so a user's transcript can render out of order and a replay disagrees with what
  they saw live. This is the identical hazard migration 028 added `revision` to fix for skills; the
  transcript never got the same treatment.
  **Fix:** milliseconds, or a per-session monotonic ordinal (preferred — it is total and cannot tie).
  Migration required; existing rows keep their second-resolution values, so the backfill/ordering
  story must be stated explicitly (what happens to pre-migration rows, and why that is acceptable).
  **Tests:** two queries written inside one second replay in write order, live-Postgres; mixed
  pre/post-migration rows still replay sensibly.
  *Validation:* GO + GOPG.

- [x] **I3 — RD30: a session token must not be a project credential.** *Filed by the parallel
  embeddable-singleton thread on 2026-08-06 and adopted here — it predates embeddability and
  outlives it.* `AGENTKIT_JWT_SECRET` and the "session secret" are the **same value in every real
  deployment** (`go/cmd/agentd/main.go:129-131`), despite the comment there claiming otherwise. The
  token handed to a session container — readable by the harness, and therefore reachable by a
  prompt-injected model — verifies as a project-scoped JWT on every route the middleware protects.
  §6.2.4's injection boundary governs the model's *reasoning*; this is its **blast radius**, and
  nothing detects the use because the token is genuinely valid.
  **Fix:** separate signing keys per credential class, so a session token cannot mint or verify a
  project credential. **This changes deployment configuration** — a new secret to set and roll.
  State the migration path, keep a compatible default, and do **not** ship a breaking boot
  requirement; if the only correct fix is breaking, stop and report rather than deciding it.
  Coordinate with the parallel thread before merging: they own `auth.go`/`main.go`'s auth wiring
  and are adding a third credential kind (embed tokens, their T10) signed by the same secret.
  **Tests:** a session token is **rejected** by the project routes (workers, settings, schedules,
  events); a project credential still works; the dev-open path is unchanged.
  *Validation:* GO + GOPG, with revert-and-fail evidence.

---

## KAI-GATED — not started, decision required

*Each needs a call only Kai can make (spend, credentials, or a destructive/irreversible choice).
Nothing below is in any wave; no executor touches them.*

- **G1 — RD14: reclaiming orphaned image and artifact bytes (destructive, billable).** Every archive
  cycle orphans a **full** container-image archive: `blobPathFor` keys on the committed image ref
  (`go/imageregistry/blobarchive/blobarchive.go:228`) and `ContainerCommit` returns a fresh digest
  each time (`go/execenv/docker/dind.go:354-358`), so each archive writes a new blob and
  `SetSnapshotHandle` overwrites the pointer to the previous one; `blobs.Delete`'s only production
  caller is the catalogue reaper, which never sees session snapshots. With `SupportsDiff=false`
  these are full archives — **unbounded storage growth on the GCS bill**, nothing user-visible. The
  mirror asymmetry: session delete destroys the *index* to a user's artifacts while the *bytes* stay
  on the bill forever (`artifacts.ArtifactStore` has no Delete method at all).
  **The decision:** deleting bytes is irreversible and runs against readiness bar #2 ("nothing a
  user made disappears without them being told"). Kai chooses the retention rule (delete the
  previous blob on successful `SetSnapshotHandle`? delete on session delete? a TTL? measure first
  and decide later?) and whether it runs against the live `webkit-servers-agent-orange` bucket.
  *Recommended first step if Kai wants one: a read-only report of orphaned blob count and total
  bytes, which is safe and answers whether this is urgent.*
- **G2 — RD21: does applying a topology seed operations doctrine v1 into the project prompt?**
  Today doctrine reaches no user: the only caller performing the doctrine mutation is
  `e2e/experiments/calibration/doctrine.ts`; no topology sets a doctrine `SettingsPatch`; zero
  occurrences in `web/src` or `examples/web/src`. So a new user gets none of it — not the
  injection-boundary rule, not the frozen-worker rule, not "no effect is a valid result". Defensible
  while every entry is `candidate`, but it means the doctrine work has **zero effect on the first
  production seeding**. **The decision is product policy** — what every new project silently
  inherits, and whether `candidate` doctrine is fit to ship as a default. The docs half (say it
  exists, say nothing seeds it, say where it lives) is **not** gated and lands in F3.
- **G3 — RD5's second half: the operator purge and the retention rule for soft-deleted sessions.**
  D1 adds `deleted_at` and filters listings, which is reversible and safe. What is **not** decided:
  whether a real purge exists, who can run it, and how long a deleted session's transcript is kept
  before the cascade is allowed to fire. That is a data-retention policy (and, on GCP, arguably a
  legal one). **D1 must not build a purge.**
- **G4 — a live-model readiness pass.** Everything in this plan validates in mock mode. Doc 22's
  base rate says contact with reality finds things our suite does not (three defects in forty
  minutes on 2026-07-28). A billable run against the real Anthropic API, after these waves land, is
  the only thing that tests that claim — **spend, therefore Kai's call**, and explicitly out of
  scope until asked.

---

## Orchestrator checklist (not executor work)

- Base sha for every wave's briefs is stated at dispatch time and re-stated after each merge.
- **Migration numbers**: D1, F4, S2 and I2 each add one. Renumber on conflict at merge; never let
  two land on the same number.
- After each wave: re-run GO + GOPG + WEB + SHELL on the merged tip, then the E2E canary
  (`product-ui.stack.spec.ts`) once Wave 0 has made it green.
- Re-check `git status` for the embeddable session's files before dispatching any ⚠ CONFLICT-WATCH
  item; hold the item rather than fight an uncommitted tree.
- Tick boxes **here**, in the same commit that files the item's discoveries below. Doc 22's items
  are ticked in *that* file only when the fix has merged and re-validated — the two files must not
  drift.
- After the final wave: re-read doc 22 §4's five readiness bars and record, bar by bar, which are
  met and which are not. That roll-up is the deliverable, not the tick-boxes.

## Discovered Issues Log

*(orchestrator-owned; executors report surprises in their final reports)*

- **(planning) Doc 22's RD9 cites a path that does not exist.** The reaper is `go/snapshot_reaper.go`
  (module root), not `go/cmd/agentd/snapshot_reaper.go`; `snapshotExpired` is at `:195`, and the
  rest of the finding is accurate. Corrected in S1. *Cite-and-verify, always: a stale path in an
  audit is how an executor concludes the defect was already fixed.*
- **(planning) Doc 23's diagnosis of the red settings test is wrong**, and would have sent an
  executor hunting a fetch race that is not there. The cause is `canSave`'s required rationale
  (K2/E3) against a test written before K2 existed — the test, not the product. See P1. *A DIL
  hypothesis written at 240s-timeout distance is a lead, not a finding.*
- **(planning) RD25 is two-thirds done and doc 22 does not know it.** R5 (doc 23) normalised
  `useEvents.ts` and `desk.ts` on 2026-08-06; a byte-count sweep on the same day found NULs
  remaining only in `WorkerEditor.tsx` (2), `orgchart.ts` (3) and `useStagedFeed.ts` (1). B4 carries
  the remainder plus the CI guard, which is the part that stops it recurring.
- **(planning) RD17's server route already exists** — `POST /agent/events` is registered
  (`go/httpapi/httpapi.go:307`). Doc 22 says so implicitly but reads as a full-stack gap; it is
  browser-side only, which makes the blocker item much smaller than its position suggests.
- **(planning) The shared test Postgres `ao-test-pg` is not running and no stopped container of that
  name exists** (`docker ps -a`, 2026-08-06) — only an unrelated `platinum-development-postgres` on
  54329. Every GOPG validation in this plan therefore has nothing to talk to until it is started.
  Flagged to Kai rather than acted on, per the do-not-touch rule. **Superseded within the hour:**
  Kai started it mid-batch, four executors independently reported the contradiction, and every Go
  item in batch 1 ran GOPG green. *An executor told "X is unavailable" who checks anyway and says
  so is doing the job right — all four did.* Note the side effect: **migration 037 is now applied to
  that shared database**, so a later item expecting a virgin schema must not assume one.

### Batch 1 — Waves 0, 1 and 2 (2026-08-06, eight items, all merged and re-validated)

*Merged into `readiness` conflict-free in the order P → B4 → B1 → B2 → F1 → F2 → F3 → F4, then
re-validated on the merged tip: GO + GOPG green, WEB 1251 tests (from 1223), SHELL green, and the
stack suite green — 11 specs including F1's new emit case, 12 more across config/schedule/session-MCP,
and all 13 topology specs under `--mock-script`.*

- **(P1/P2) THE FILE HAD FOUR RED TESTS, NOT TWO — and the smoke test was red too.**
  `test.describe.configure({mode:'serial'})` aborts the rest of the block on the first failure, so
  the worker test and the permalink test have been reported `did not run` for weeks and were read as
  passing. Doc 23's DIL, doc 24's Wave 0 premise and my own briefing all said "exactly two". Worse:
  `e2e/stack.spec.ts` — the top-level journey `README-stack.md` and CI treat as *the* smoke test —
  was **also red**, for the same cause, and nobody had noticed. **A serial describe's pass count is
  not a coverage number**; anywhere one is used as evidence of health, the same misreading is
  waiting. All five product-UI tests and the smoke test are green now.
- **(P2) The diagnosis was hypothesis (a) with a real product defect inside it.** The composer is
  not disabled — it is **not mounted**. Clicking "New session" creates a session and changes nothing
  on screen, and a pasted permalink resumes the session *behind* the Desk (the landing view since
  K1). `App.tsx`'s own comment on `showSession` states the rule it was breaking: "resuming a session
  behind a hidden tab would look like nothing happened". Fixed in 5 lines, keyed on the routed
  session id so it fires on a change and never drags a human out of Workers/Settings. Recording this
  as a test defect would have left the smoke test red — which is exactly how it stayed red for weeks.
- **(P1) The K2 rationale omission is systemic, not one test.** `ProjectSettingsPage` and
  `WorkerEditor` both gate Save on a non-empty "Why?", and `WorkerEditor` **re-seeds the rationale to
  empty whenever the editor changes identity** (`seededFor` ref, `WorkerEditor.tsx:99-112`), so a
  three-save test needs three separate reasons. A test that fills it once at the top still hangs.
- **(all) The stale-worktree trap fired on EVERY executor that reported its base — six of eight.**
  Not the expected pre-product-layer commit either: `dc49595 wip` over `946b237 wip` over
  `feb5d25 Merge comprehensive-tests`, a **divergent** lineage of which `58d5fff` is not an ancestor.
  Verified afterwards that those commits remain reachable from ~8 old session branches, so nothing
  was destroyed by the resets. Rule 2 is not a formality; it is the single highest-yield line in
  this plan's briefs.
- **(B4) The plan's own suggested CI guard command was a false negative.** Doc 24 offered
  `git ls-files -z | xargs -0 grep -lP '\x00'`; against a file that genuinely contains a NUL it
  prints nothing and exits 1, because grep classifies the file as binary *before* applying the
  pattern. `grep -alP` works. **The item designed to stop confident negatives shipped one in its own
  validation command** — third instance of this exact shape in the plan's history (bash collapsing
  `$'\x00'`, grep's binary suppression, now this).
- **(B4) The trap fired on the executor's tooling three times mid-item**, including materialising a
  real NUL into the new checker script and into a commit message while *writing the check that
  forbids them*. Caught only by byte-counting, never by reading. Also: this commit's own diff is
  still partly invisible — git compares *both* blobs, so two of the three files still render as
  `Binary files differ` for exactly one commit.
- **(F1) Doc 22's RD17 evidence contained a false detail.** "The replay panel prints the curl command
  for the user to run by hand (`:91`)" is wrong — line 91 was the TextField's helperText, and
  `grep -ran curl web/src examples/web/src` returns **nothing at all**. The gap was more complete
  than the item claimed: there was no escape hatch of any kind. *The finding survived; the
  supporting detail did not.*
- **(F1) The emit sends only `{type, text}`** — core stamps the envelope and `ingestEventBody`
  discards any envelope in the body, so sending the drafted envelope would have implied it was
  honoured. The dialog says so, and shows the live dry-run verdict ("N of M subscriptions match") as
  the blast radius before you accept.
- **(F2) The worker-existence refusal is a new precondition on EVERY store-level
  Create/UpdateSubscription** — it broke 9 existing fixture sites across 4 test files. All were
  fixture-only fixes, but any sibling item that creates a subscription in a test now inherits it.
  `ApplyTopology` was the real risk and is safe: it creates workers before subscriptions inside one
  transaction (verified by running the 13 topology specs, all green).
- **(F2) The same silent thinning exists twelve lines below the site the item names** — a memory
  that exists with *blank content* contributes no briefing section, just as silently as one that is
  missing. Covered; neither RD19 nor item F2 knew about it.
- **(F3) Two of doc 22's "stale" claims were TRUE and were kept.** The worker-editor image field
  really is unvalidated free text (`httpapi/workers.go:115` assigns `body.Image` with no check), and
  no HTTP path really can produce a `worker_prompt_write` event. **An audit's list of false claims is
  itself a claim.** Meanwhile RD23 *understated* one: the rationale-carrying route count is nine
  writes plus three deletes, not six. And `docs/18` never mentioned doctrine **at all** — RD21 is
  not "the docs undersell it", it is that the operator's guide never names it.
- **(F3) `.env.example` contradicted itself** rather than merely being stale: "nothing reaps them on
  a timer" at `:73-74`, then the 30-minute reclaim loop described at `:90-103`, in one file.
- **(B1) Doc 22/24's line citations for RD26 are all off by ~26 lines** against `58d5fff` — the
  findings were accurate, the offsets had drifted. Second citation drift found in two batches
  (RD9's wrong path was the first). **Cite-and-verify; never edit at a cited line without reading it.**
- **(B1, OPEN — the user-visible half is missing) The stuck-detection fix changes nothing an operator
  sees.** Both stuck banners in `AgentChat.tsx` (`:603`, `:615`) are gated on `isStreaming`, and
  every path producing an unconfirmed end sets `isStreaming = false`. So "keep the detector armed"
  changes the hook's exported `stuckStatus` and no rendered pixel. B1's connection-lost error *is*
  user-visible, so the item's core claim holds — but **someone should own the banner gating**; filed
  as **B5** below.
- **(B2) Two more instances of RD28's exact shape that the browser sweep missed** — `OrgChartPage`
  ("This project has no workers yet") and `MemoryBrowserPage` ("Nothing has been remembered in this
  project yet"), both rendering confident emptiness over a failed fetch. Fixed in the same item.
  *A sweep that finds four instances of a pattern has probably not found all of them.*
- **(B2) `looksUnwired` matches on the server's MESSAGE BODY, not the status code** — so a genuine
  500 whose text happens to contain "not found" or "not configured" is misclassified as an unmounted
  route, and the UI reports a *degradation* where there is a *failure*. It bit the executor inside
  its own test. Not fixed (out of item scope); filed as **B6** below.
- **(F4) The browser invents a second field it does not read.** `EventDelivery.worker` exists on the
  Go row (since migration 024) but not in the TS interface — the UI derives it by joining through
  the subscription, which is why a delivery whose subscription was deleted reports "the subscription
  that started them is gone". Same shape as the `failure_reason` gap F4 just closed. Candidate for I1.
- **(F4) Two tests pin prose.** `desk.test.ts:533` and `DeskPage.test.tsx:201` regex-match the
  "No reason is recorded on a delivery row" copy, which F4's own change falsified; the leading phrase
  was kept deliberately. Prose assertions are load-bearing in this package.
- **(orchestrator) F4 sprawled to 26 files across four other items' territory** (`router.go`,
  `events.go`, `events.ts`, `useAgentSession.ts`, `docs/18`) and still merged conflict-free with F1,
  F2, F3 and B1 — git auto-merged every overlap. **Conflict-free is not correctness**: the merged tip
  was re-validated end to end precisely because nothing conflicted. Future waves should keep the
  sprawl-prone item last, as this one did by luck rather than design.
- **(orchestrator) The topology specs skip silently without `STACK_MOCK_SCRIPT`** — 13 tests reported
  as `skipped` in a run that otherwise looks complete. Re-run under
  `--mock-script e2e/mock-scripts/topologies.json`: all 13 pass. **A skip is not a pass, and a
  suite's summary line will not tell you which you got** — the same misreading as the serial-describe
  finding above, in a different disguise.

### Batch 2 — Waves 3, 4 and 5 (2026-08-06, six items, all merged and re-validated)

*D2, D3, S1, S3, I2, I3. Merged conflict-free (again — `messages.go` was rewritten by both D2 and
I2, `types.go` by both S1 and I2, `main.go` by both S3 and I3), then re-validated on the merged tip:
GO green, GOPG green (31 packages), WEB 1251, SHELL green, and 22 stack specs green including
session-MCP — which independently exercises I3's session-secret change, since a container's token is
how core MCP authenticates.*

- **(D2, OPEN — THE RECONNECT FIX CANNOT FIRE TODAY; filed as D5)** The engine half is done and
  proven, but **three independent breaks stand between it and a real user**, and no single one of
  them is visible from the others. (i) `execenv.InstanceStatus.ActiveQueryID` is **never set by any
  adapter** — the only reader is `runner.go:856`. (ii) **The runner and the sandbox disagree about
  what a query id is**: agentd persists under `q-<sessionID>-<n>` while the sandbox generates its
  own uuid per turn, and the sandbox's is the one a client would reconnect with. (iii) *Found by the
  orchestrator while checking D2's claim:* the **browser never sends the id it already has** —
  `useAgentSession.ts:574` logs `status.activeQuery.queryId` and the very next line calls
  `endpoints.reconnect(sessionId)` with no query string, while the server reads `queryId` from
  exactly there (`httpapi/stream.go:62,94`). **RD6 and RD24 are ticked because the engine change is
  correct, merged and revert-proven — but the path is inert end to end until D5 lands. Anyone
  reading those ticks as "a reconnect now persists the turn" would be wrong.** This is the plan's
  own thesis biting the plan: a fix that reports success and does nothing.
- **(D2) An irreducible loss window remains, and it is documented rather than papered over.** Events
  the dying process read off the socket but had not yet flushed are in neither the sandbox buffer
  nor the row; the new cadence bounds it to ~2s of output. Closing it entirely would need the
  sandbox to keep buffering while a stream is attached — a sandbox change, out of scope and
  deliberately not smuggled in.
- **(D2) The dedup worry in the item was the wrong worry.** The sandbox buffers only while NO stream
  is attached, so a reconnect sees a *suffix*, never a repeat — but writing that suffix as the whole
  row would have **erased the pre-crash half including the human's prompt**. Hence `events.Splice`
  (append, absorbing the largest exact overlap, idempotent) plus a single-writer claim per session.
  Both were revert-proven: without the claim the model's words are recorded twice; without Splice
  the reconnect erases the prompt.
- **(D3) RD7's STATED FIX WOULD HAVE CHANGED NOTHING.** Doc 22 and doc 24 both say "stamp terminal
  status first, release the lease second". The executor implemented exactly that and the ordering
  test *still failed* — because the failure mode RD7 itself names is **a failed status write**, and
  reordering two writes does not help when one of them is the thing that fails. The real fix needed
  the sweep. *An item can state the defect correctly and prescribe a remedy that does not touch it.*
- **(D3) RD10's blast radius is worse than filed.** Doc 22 describes the loser producing a spurious
  `worker.failed{lost}`. In the unfixed run, **8 racing drains over one pending delivery created 8
  jobs** — 8 sessions, 8 containers, 8 host ports from a pool of 100. Reproduced on demand, then
  fixed to exactly one.
- **(D3) The shared `ao-test-pg` is near its connection ceiling.** Twelve concurrent connections from
  one test produced `FATAL: sorry, too many clients already`. Cause: `openLivePG`/`openLiveStore`
  open a fresh pool per test and never close it, so connections accumulate across a package run.
  Capped in the new test; **the leak itself is untouched and will bite the next concurrency test.**
- **(S1) RD9's premise is half wrong, and the executor was right to refuse it.** The non-extending
  expiry was **not an oversight — it was a deliberate, argued decision stated in three places**
  (`customimages.go:642`: "a resume that quietly bought another 30 days would make the operator's
  storage bill a function of use"), with a **live test asserting the old policy**. Taking RD9's
  literal "extend expiry on resume" would have meant deleting an assertion someone wrote on purpose.
  The landed fix defers at *reap* time instead, keeping the stamped promise honest. Also: doc 22's
  "misleading header" citation is wrong — `:21` implies nothing about expiry.
- **(S1) `last_resumed_at` had ZERO readers** before this change: written on every launch for
  months, consumed by nothing. A field whose entire documented purpose was "so an operator can see
  it" that no operator surface reads. Worth a sweep for its siblings.
- **(I2) RD16 is worse than "arbitrary within a second", and reliably so.** With no tie-break
  Postgres returns heap order, and **an UPDATE relocates a row to the end of the heap** — so every
  re-persist of a turn (which the pipeline does routinely) dragged that turn to the *end of the
  transcript*, regardless of timestamps. Transcripts were not occasionally ambiguous; they were
  routinely reordered.
- **(I2) `agentdb` is not Postgres-only in test** — `newSessionTestStore` builds tables with gorm
  AutoMigrate against **sqlite**, so any raw SQL added to a store method must parse on both dialects.
  This binds every remaining Go item that touches `agentdb` (D1, S2, I1).
- **(I3) The fix turned out non-breaking, so the escalation clause never fired.** The session key is
  derived from the API secret: no new configuration, cannot fail at boot, and an operator still gets
  a real knob (`AGENTKIT_SESSION_JWT_SECRET`). **But it opened a new hole that had to be closed in
  the same commit** — a new secret env var is by default forwardable into session containers via
  `AGENTKIT_MCP_ENV`, and forwarding the *session signing key* would let one session mint tokens for
  every other session, strictly worse than RD30. Added to `mcpenv.go`'s reserved list. *Any future
  secret-shaped env var inherits this trap.*
- **(I3) `/dev/token` was minting an API credential with the session issuer** — invisible while the
  two secrets were the same string, and it would have broken the e2e stack overlay the moment the
  keys diverged. A second, quieter instance of the same conflation, mentioned in no document.
- **(I3) An unforwarded variable is a silent no-op.** Adding `AGENTKIT_SESSION_JWT_SECRET` to
  `.env.example` without the matching `docker-compose.yml` line would have shipped a documented knob
  that does nothing in the stack — this plan's exact thesis, in the fix for this plan's own item.
  Both landed together.
- **(S3) The absent-column case can be tested against the shared database without harming it** —
  `search_path=<throwaway schema>` in the connection URL makes the `vector` type unresolvable, so
  migration 022's DO block takes its own EXCEPTION path and the column is genuinely never created.
  The orchestrator's warning (that ao-test-pg's healthy default would make this test vacuous) was
  right, and this is the answer to it.
- **(S3) `storePromptRevision` had the same defect and is now covered for free.** It embeds every
  prompt-revision memory — *the record the self-improvement loop is supposed to find years later* —
  and on a pgvector-less Postgres it stored an unfindable row while reporting `stored: true`.
- **(S3) The browser guesses whether the semantic leg is on.** `web/src/memories.ts:316-333`
  (`semanticLegLooksOff`) inspects result snippets for query words because, its own comment says,
  "the route never says whether it embedded the query". Same defect class as RD3, one layer up.
  Now fixable — the store knows the answer; the route still does not say it.
- **(all six) The stale-worktree trap fired on EVERY executor in this batch** — six of six, on the
  same divergent `dc49595 wip` lineage. Fourteen of fourteen executors across both batches where the
  base was reported. This is not decaying and rule 2 is the single highest-yield line in the briefs.
- **(S3, I2) Two more false negatives from executors' own commands** — a `grep -v` substring pattern
  that silently swallowed a second file, and a `grep -E "^ok +github"` that matched nothing because
  `go test` separates `ok` from the package name with **tabs**. Fourth and fifth instances of this
  shape in the plan's history. *Every "clean" grep in this workstream should be assumed guilty until
  a second method agrees.*
- **(orchestrator) `gofmt` dirt is accumulating across merges.** F4's `FailureReason` insertion left
  `agentdb/events.go` unformatted at `e799bb0` (D3 fixed it in passing); two files under
  `go/httpapi/` are still unformatted and were left alone under rule 3. Worth one sweep.

### Batch 3 — D4, I1, D5, B5, B6 (2026-08-06, five items, all merged and re-validated)

*Re-validated on the merged tip: GO green, GOPG green (and see the interference finding below),
WEB 1251 → 1276, SHELL green, 23 stack specs green plus the port-pool pair under `--port-pool 3`
(run deliberately because D4 is the port-leak item and that spec skips silently otherwise).*

- **(orchestrator) THE FIRST REAL MERGE CONFLICTS OF THE WHOLE RUN — three, after 19 conflict-free
  merges.** `agentkit.go` (D4 and D5 both documented a new optional `RunnerStore` capability in one
  doc block — union), `migrations.go` (D5 and I1 both claimed **039**; D5 keeps it since it was
  already applied to the shared database, I1's trigger became **040**), and `configApi.{ts,test.ts}`
  (below). The migration collision is the one the plan predicted in its own Standing traps and
  reserved numbers for — and it still happened, because two items in one batch each took "the next
  free number" independently. **Reserving a range is not enough; the orchestrator must assign
  numbers per item at dispatch.**
- **(orchestrator, MY OWN ERROR) I broke `migrations.go` resolving that conflict, and committed
  it.** A regex splice across a Go composite literal ate the 039 entry's closing backtick and the
  next entry's brace; `gofmt` caught it one command later and it was repaired in the same session.
  Recorded because it is the same class as everything else in this plan: *an automated edit that
  reported success and produced a broken file.* A merge conflict in a structured literal gets
  resolved by reading, not by pattern.
- **(I1/B6) Two executors independently invented the same class under different names** —
  `ApiError`/`errorStatus` (I1) and `ConfigApiError`/`configApiStatus` (B6). I1's own comment said
  it was "the material a later fix would use"; B6 *was* that fix, arriving in the same batch.
  Resolved to B6's, with I1's single consumer rewired; B6's tests already assert everything I1's
  did. **Two items that reference each other's defects will converge on the same code — dispatch
  them in one item or in sequence, not in parallel.**
- **(D5) The item's own prescribed fix was impossible, and the executor said so instead of faking
  it.** Doc 24 said "set `ActiveQueryID` in the adapters". No adapter *can* know it — a container
  cannot see inside a turn and the sandbox exposes no route reporting an active query. The runner
  knows, so `Status` now answers from the runner's own state and from the session row when the
  process that knew is dead. **Second time in two batches that an item named a defect correctly and
  prescribed a remedy that does not touch it** (RD7 was the first). That is now a standing warning
  in the briefs and it keeps paying.
- **(D5) The id question is settled: the runner's `q-<session>-<n>` is canonical** (it is the
  `agent_query_events` key, so it is the only id a client can be handed without splitting one turn
  across two rows), and the sandbox's per-turn uuid stays a transport detail (its replay buffer is
  keyed by it, so it is the only id that can *attach*). The two are joined from the sandbox's
  `connected` frame — the only moment the sandbox ever states its id — and stored on the session row
  as runtime state (migration 039, no config event). **No sandbox change was needed**, which was the
  constraint the brief set.
- **(D5, STILL OPEN — the stack confirmation RD6 asked for has NOT been run)** and it cannot be
  staged in mock mode as things stand: a mock turn completes in about a second, and the compose
  stack's Go mock proxy has **no delay knob** (`mock-server/`'s `delayMs` belongs to the standalone
  Vite mock, not to this path), so "kill agentd mid-turn" has no window to land in. D5 wrote a
  precise, literal recipe — exact SQL, exact URL with the query string, expected values at each step
  and four named failure signatures — and it is in the D5 report; it is executable the moment a slow
  turn is possible. **Two ways to close it: add a delay knob to the Go mock proxy (small, and it
  would serve every future crash test), or run it once against the real API (spend — Kai's call,
  see G4).** Until then RD6/RD24/D5 rest on unit and live-PG evidence only, and that is stated
  rather than glossed.
- **(orchestrator) THE SHARED `ao-test-pg` CANNOT SUPPORT CONCURRENT EXECUTORS, and it produced a
  false failure report.** D5 reported four httpapi live-PG tests failing "with counts that GROW run
  to run", and proved they were not its own by reverting everything. On the merged tip, serially,
  **all four pass — twice, verified with `-v` so it is runs and not a summary line.** Five agents
  were running the full live-PG suite against one Postgres simultaneously, and those tests count
  rows in shared tables. **Only the orchestrator's serial GOPG run is authoritative**; an
  executor's parallel GOPG result is evidence about the rig, not about the code. This also explains
  the connection-ceiling failure batch 2 recorded.
- **(B5) The honest answer was a small change, not a large one.** The stuck banners now render off
  `stuckStatus` rather than `isStreaming`, so B1's armed detector reaches a pixel — while the
  composer stays usable, which is the constraint that made the original gating look reasonable.
- **(D4) Both halves needed the *other* arm tested to be worth anything**: the port returns to the
  pool AND a genuine snapshot failure still keeps its container; the archive path leaves artifact
  status alone AND `Destroy` still marks lost. A fix to either that broke its twin would have
  traded a leak for data loss.

## Follow-up items filed by batch 1 (not yet scheduled)

- [x] **B5 — The stuck banners cannot fire.** `AgentChat.tsx:603,615` gate both stuck-detection
  banners on `isStreaming`, but every unconfirmed-end path sets `isStreaming = false` (correctly —
  holding it true would disable the composer forever, which is P2's defect in another costume). So
  B1's armed detector reaches no pixel. Decide what an operator should see when a turn's end cannot
  be confirmed, and render it off `stuckStatus` rather than off `isStreaming`.
- [x] **D5 — Nothing can actually reach the reconnect path, so D2's fix cannot fire.** *Filed by
  batch 2; the most important open item in this plan, because a tick in doc 22 currently rests on
  it.* Three breaks, all needed: (i) no `execenv` adapter ever sets
  `InstanceStatus.ActiveQueryID` (only reader: `go/runner.go:856`); (ii) the runner persists under
  `q-<sessionID>-<n>` while the sandbox streams under its own per-turn uuid — **two id spaces**, and
  a reconnect keyed by the sandbox's id writes a different row unless they are reconciled; (iii) the
  browser has the right id and drops it — `web/src/useAgentSession.ts:574` logs
  `status.activeQuery.queryId`, the next line calls `endpoints.reconnect(sessionId)` with no query
  string, and `go/httpapi/stream.go:62,94` reads `queryId` from exactly there.
  **Fix:** settle the id question first (whose id is canonical, and how the other side learns it) —
  that decision drives the rest. Then set `ActiveQueryID` in the adapters and send the id from the
  browser. **Validation must be the stack confirmation RD6 asked for and nobody has yet run**: start
  a long turn, `docker kill` the agentd container, restart, reconnect, and observe both that the
  events render *and* that `agent_query_events` gains a row. Record the before/after row counts.
  Until this lands, do not describe RD6/RD24 as closed to anyone outside this plan — and it blocks
  the parallel session's T12 as surely as D2 did.
- [x] **B6 — `looksUnwired` classifies by message text, not status code.** A 500 whose body contains
  "not found"/"not configured" is reported to the user as "this deployment does not serve it". Route
  the distinction off the HTTP status (404/501 = unwired) and keep the text match only as a
  fallback. Small; the helper is now shared by three call sites (`configApi.ts`), so one fix covers
  all of them.
