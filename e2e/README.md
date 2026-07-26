# e2e — what is proved, and what is not yet

The user's standing goal for the product layer is *"prove that everything works by writing
end-to-end tests for each feature"*, and **G1** (`docs/product/06-work-plan.md`, Track G) is the bar
the whole spec is measured against: the §8.7 self-improvement loop closing offline.

This file is the map. **Before writing a new e2e, look here** — extend the spec that owns the
feature rather than starting a parallel one, and move a row out of the queue when you cover it.

## The two harnesses

| Harness | Config | Runs against | Status |
| --- | --- | --- | --- |
| **Stack e2e** (use this) | `playwright.stack.config.ts` | the real docker-compose stack on `:8080` | current |
| Legacy harness | `playwright.config.ts` (`tests/`, `global-setup.ts`) | Vite dev server + `mock-server/` + a `go run ./cmd/agentd` | pre-product-layer; not exercised by this work |

Everything new goes in the stack harness. It runs the same binaries, database and container runtime
a user gets from `docker compose up`, which is the only configuration where a claim like "the config
log recorded that" means anything.

### Running it

```sh
./e2e/run-stack-e2e.sh up mock      # build + start the stack (mock model, no credentials)
./e2e/run-stack-e2e.sh test         # run every spec against the running stack
./e2e/run-stack-e2e.sh down         # stop (add --purge to wipe volumes)
./e2e/run-stack-e2e.sh run mock     # clean-room: up → test → purge-down (the CI job)

# G1 from cold — everything, including the §8.8 tests that need the model to
# choose a tool call:
./e2e/run-stack-e2e.sh up mock && \
  ./e2e/run-stack-e2e.sh test --mock-script e2e/mock-scripts/g1-acceptance.json

# Without the script, the §8.8 pair skips and everything else runs:
./e2e/run-stack-e2e.sh test
```

`--mock-script` loads the script into agentd, runs, and restores the plain model however the run
ends — deliberately per-run, because the script is agentd-wide and read once at boot. See
[`mock-scripts/README.md`](mock-scripts/README.md).


## When a run fails, triage before you debug

**Most red runs on this suite are the stack, not the code.** Three people misdiagnosed the same
thing on 2026-07-26, twice confidently, and each time it cost hours. Read the failure text first:

| What you see | What it means | What to do |
| --- | --- | --- |
| `cannot start session …: execution environment is at capacity` | the **host port pool is full** — see below | `./e2e/run-stack-e2e.sh clean`, re-run |
| `session has no running instance and no snapshot` | since the port-pool fix, **what it says**: that one session is unrecoverable | investigate that session; it is no longer the capacity message in disguise |
| `502 Bad Gateway`, `socket hang up`, `ECONNREFUSED` | agentd restarted mid-run (someone rebuilt, or it crashed) | check `docker compose -p agent-orange-stack-e2e ps`, re-run |
| `no stack listening at …` | agentd is down, often after a `clean` that raced Docker | bring it back, then re-run |
| `timed out after Nms waiting for …` | ambiguous — read the "last value" in the message | if it shows `status: "failed"` deliveries, suspect the stack; otherwise investigate |
| an `expect(…)` assertion diff | **a real failure** | investigate; do not re-run and hope |

The rule of thumb: **connection-shaped errors mean re-run, assertion-shaped errors mean
investigate.** A test that fails on an `expect` has told you something true about the product.

### The port pool is 100, and the error now says so

agentd hands each session container a port from `PortRangeStart: 30001 … PortRangeEnd: 30100`
(`go/cmd/agentd/main.go`), so the 101st live session cannot start. Nothing reaps idle sessions, so
the pool fills by accumulation, not by concurrency — which is why a leak from a *previous* run is
the usual cause.

Since `6dc27ac` the caller is told that in full:

```
cannot start session "<id>" on this host: execution environment is at capacity: the host port pool
is exhausted — all 100 ports in 30001-30100 are leased to live sessions, and a session holds its
port until it is deleted, so every further session on this host will fail the same way until one is
released (a host capacity limit, not a lost or broken session)
```

The last clause is the one that matters: it tells you not to re-create the session, which is what
the old text invited. Before that fix the allocator said `port pool exhausted: no available ports in
the sandbox port pool` and the caller received only *"session has no running instance and no
snapshot — session must be re-created"* — a description of a lost session, and the sentence that
cost this project two misdiagnoses and the better part of a day.

**If you see the old message on a provisioning failure, check which binary you are on** — a stack
built before 2026-07-26 02:33 predates the fix, and this is not academic: the stack handed over
after that fix merged had been built nine minutes too early.

```sh
docker compose -p agent-orange-stack-e2e exec -T agentd \
  sh -c 'strings /usr/local/bin/agentd' | grep -c 'host port pool is exhausted'   # 1 = fix present
```

agentd also warns on the approach rather than only at the cliff: `[dind] WARNING: host port pool
nearly exhausted` on every provision once fewer than ten ports remain. Grep the agentd log for it
before starting a long run — it is the difference between losing a run and losing an afternoon.

### Runaway schedules — the original cause, now self-limiting

A `* * * * *` schedule keeps firing for as long as its row exists, whatever became of the test that
created it. Fifty-three of them, left by earlier runs of this suite, provisioned a session every
minute indefinitely and drained the whole pool — presenting as "the product will not provision", an
hour later, in somebody else's work.

Two things changed since. §8.6 now retires a schedule after **five consecutive firings that start no
job**, recording the decision and its reason in the config log — so a schedule that can never
provision stops hammering its neighbours within minutes instead of indefinitely
(`features/schedule-resilience.stack.spec.ts` proves this end to end). And the suite fails itself
when it leaks (see "The run refuses to leak").

Applied to the original incident, the rule would have stopped the bleeding without healing the
wound. The fifty-three schedules could provision at first — that is *how* they filled the pool — and
only began failing once it was full, at which point five minutes of failures would have retired all
of them. But the sessions they had already leaked go on holding their ports until somebody deletes
them, so the host stays full either way. The mechanism buys a stable host to debug on, not a fixed
one.

Which is why it is no substitute for cleaning up. A schedule that *can* provision is not failing, so
nothing retires it, and it will happily fill the pool at one session per minute. That is why
`client.cleanup()` deletes schedules and subscriptions *before* sessions, and why the §8.8 test
releases its schedule in a `finally`.

Still the first thing to check when provisioning fails:

```sh
docker compose -p agent-orange-stack-e2e exec -T postgres \
  psql -U agentorange -d agentorange -At \
  -c "select id, worker, provision_failures, enabled from schedules where enabled;"
```

### `clean` restarts agentd, on purpose

Removing session containers out from under a running agentd leaves its placement state naming dead
instances, and it then refuses to provision anything until restarted. `cmd_clean` does the restart
for you — and waits for the removals to finish first, because `docker rm -f` returns before the
daemon is done and agentd **fatals on boot** if it reclaims a container mid-removal
(`removal of container <id> is already in progress`). An earlier version of this fix skipped that
wait and reliably killed agentd, which then looked like "the stack is down" rather than "clean did
it".

Prefer deleting sessions through the API (`client.cleanup()`); reach for `clean` when a previous run
abandoned containers or schedules.

## Layout

```
e2e/
  stack.spec.ts              browser journey: login → project → session → streamed reply → replay
  features/                  product-layer features (HTTP, and the browser where the UI is the point)
  helpers/api.ts             login, project-scoped client, typed /agent/* calls, polling
  helpers/ui.ts              browser fixtures: login, fresh project, view switch, send-and-settle
  helpers/configlog.ts       reads config_events out of the stack's postgres
  helpers/stackdb.ts         raw psql — the only reach past the HTTP API; each use marks a missing route
  helpers/mcp.ts             drives agentd's core MCP server with a minted session token
  run-stack-e2e.sh           stack lifecycle
```

**Changing the UI?** The stack serves a *built* image of `examples/web`, so edits to
`examples/web/` or `web/` are invisible to the browser tests until you rebuild it:

```sh
docker compose -f docker-compose.yml -f docker-compose.stack-e2e.yml \
  -p agent-orange-stack-e2e up -d --build web
```

Feature specs are plain HTTP against the running stack — the whole `features/` directory runs in
about two seconds, so there is no reason to be sparing with them. Reserve the browser for things
that are actually about the browser.

### The fixtures

From `helpers/api.ts`:

| Fixture | What it gives you |
| --- | --- |
| `login(request)` | the test account's project tokens + wildcard login token |
| `newProjectClient(request, prefix?)` | a client bound to a **fresh, run-scoped, empty project** — start here |
| `mappedProjectClient(request, project)` | a client for a pre-mapped project (`apples-oranges`, `pears-plums`) |
| `client.getSettings()` / `putSettings()` | project settings (§5) |
| `client.listWorkers()` / `getWorker()` / `putWorker()` / `deleteWorker()` | workers (§6) |
| `client.toggleWorkerEnabled(name, on)` | read-modify-write `enabled`, so the log sees a real toggle |
| `client.postEvent()` / `listEvents()` | the event spine (§8) |
| `client.createSubscription()` / `listSubscriptions()` / `updateSubscription()` / `deleteSubscription()` | routing config (§8.3) |
| `client.listDeliveries()` / `waitForDeliveries()` / `waitForEvents()` | job history, with polling for anything asynchronous |
| `client.raw(method, path, body)` | the unchecked response, for asserting 401/404 |
| `client.listMessages(id)` / `getSession(id)` | the persisted transcript and the session row |
| `projectClient(request, project)` | a client for an *existing* project — e.g. one a browser test just created |
| `client.permalink(sessionId)` / `sessionPermalink()` | `<base>/p/<project>/s/<session>` (F3) |
| `client.createSession()` / `sendMessage()` / `archiveSession()` / `restoreSession()` | session lifecycle; `sendMessage` returns when the turn's SSE stream closes, so it doubles as "wait for the turn" |
| `client.queryEvents(id)` / `sessionInfoEvents(id)` / `errorEvents(id)` | what the **container** reported: tools, MCP servers and their connection status, and `AGENT_ERROR`s |

From `helpers/ui.ts` (browser):

| Fixture | What it gives you |
| --- | --- |
| `loginUI(page)` | logged in, at the project picker, with stale localStorage cleared |
| `openFreshProject(page, prefix?)` | logged in and inside a brand-new project's workspace; returns the id |
| `gotoView(page, 'chat' \| 'workers' \| 'settings')` | the workspace view switch |
| `selectedProject(page)` | which project the switcher shows |
| `sendAndSettle(page, prompt)` | sends a prompt and waits for the reply to **stop changing** |

`sendAndSettle` is not optional politeness: a turn is only persisted once it ends, so a test that
reloads or navigates immediately after talking is racing the model, not testing the feature. Writing
one such race is what turned up the interrupted-turn defect below — but only after the race was
removed and the failure persisted.


From `helpers/configlog.ts`: `configEvents(project)`, `configActions(project)`,
`waitForConfigEvents(project, n)`.

From `helpers/mcp.ts`: `sessionMCP(project, sessionId)`, `projectOnlyMCP(project)`, `mintSessionToken()`.
These drive agentd's core MCP server (`POST /mcp`) — the tools a *worker* calls. Two things make it
unlike an ordinary request, and both are facts about the deployment: the endpoint is not proxied by
nginx and agentd's port is not published, so calls go through `docker compose exec dind wget`; and it
authenticates by **session** token, so the helper mints one (HS256 over `customer` + `sid`, signed
with the overlay's known test secret). `projectOnlyMCP` mints the same token *without* `sid`, which
is how the "must be called from inside a session" refusals are tested. A tool that fails returns
`isError: true` with its report in `text` — that is not a transport error, and `call` does not throw;
`callOK` does.

From `helpers/configlog.ts`: `configEvents(client)`, `configActions(client)`,
`waitForConfigEvents(client, n)`, `waitForConfigAction(client, action)`. These read
`GET /agent/config-events` — they used to go through psql, and swapping the implementation left
every spec unchanged, which was the point of the seam.

From `helpers/stackdb.ts`: `psql()`, `seedSessionMCPServers()`, `storedSessionMCPServers()`. Now
down to **one** remaining use: a session's `mcp_servers` still has no write path, so seeding the row
is the only way to exercise what happens downstream of it.

Two conventions worth keeping:

- **Each test gets its own project.** `newProjectClient` mints one; projects are just names, so
  they need no cleanup and repeated runs against one long-lived stack never collide.
- **Never assert a mapped project is empty.** `apples-oranges` and `pears-plums` are shared with the
  browser spec and with previous runs.
- **Release sessions, including the ones you did not create.** A session holds a *running container*
  inside DinD until deleted, and nothing reaps them on a timer. `client.cleanup()` sweeps **every**
  session in the project, which matters because the router creates one per delivery and the browser
  creates them through the UI — tracking only your own `createSession` calls leaks exactly those.
  (It lists with `user_email=*`: the route defaults to the caller's own email, and job sessions run
  under a different one.) Call it from `afterEach`; browser specs use
  `cleanupOpenedProjects(request)`. **The ceiling is real**: at ~100 live containers every new
  session fails to provision with "has no running instance and no snapshot", which looks exactly
  like a product bug and is not one.

## Coverage

### Covered

| Spec area | Where | What is proved |
| --- | --- | --- |
| Login, projects, sessions, streaming, replay | `stack.spec.ts` | login → pick project → new session → prompt → **streamed** reply (incremental paints, not one) → reload replays → other project's list is empty |
| Project settings §5 | `features/config-and-workers.stack.spec.ts` | defaults on an unwritten project; PUT is whole-object (omitted fields reset); read-back agrees |
| Workers §6.1 | same | create/read/list/update/delete round-trip; `enabled` and `max_instances` defaults; PUT replaces rather than patches; 404 after delete |
| Subscriptions §8.3 | same | create/update/delete; `enabled` defaults true; `max_firings_per_hour` 0 = unlimited |
| Event ingestion §8.5 | same | `POST /agent/events` stamps `source=external, depth=0`; the event is readable back |
| Project isolation §12 | same | another project's rows are **404, not 403**; same-named workers in two projects are two rows; events and config history never cross |
| Auth | same | no token and a garbage token both 401 on read *and* write |
| Config log §15 | same | every mutation appended the action §15.3 names (`worker_create/update/disable/enable/delete`, `project_settings_put`, `subscription_create/update/delete`), payloads are full state, deletes carry the final state, HTTP edits log an **empty actor**, and `POST /agent/events` correctly logs **nothing** (§15.3 rule 3) |
| Project settings UI (B3) | `features/product-ui.stack.spec.ts` | edit the prompt and base image in the browser, save, and find them still there after a full reload; one save = one `project_settings_put` |
| Workers UI (C3) | same | create a worker in the browser, toggle it disabled, edit its prompt — and the config log reads `worker_create, worker_disable, worker_update`, proving the UI sends the **whole row** on a toggle |
| Session permalink (F3) | same | the open session is already permalinked (state→URL); pasting that link back resumes the transcript (URL→state); a link naming another project switches to it |
| Session MCP §4 (**A4**) | `features/session-mcp.stack.spec.ts` | a session-supplied MCP server connects (`session_info` reports `status: connected`), its tools reach the model as `mcp__<server>__*`, and all of it **survives snapshot→resume** — the A2 regression. Plus: an unresolvable `${VAR}` fails the turn with an `AGENT_ERROR` naming the variable, instead of connecting without the credential |
| **The acceptance loop §8.7/§8.8** | `features/acceptance-loop.spec.ts` | the org seeds and every hire is logged; an inbound email enters with a core-stamped envelope; **the router starts an answerer job** and the composed prompt it ran with carries the worker's prompt; **the answerer finishing fans out to reviewer and archivist** at depth 1, and the reviewer does *not* react to its own finish; **a memory written by one job appears verbatim in the next job's composed prompt** under the heading its briefing selector asked for (§7.4 — the loop's substrate); **the reviewer rewrites the answerer's prompt through `worker_prompt_write`**, logged with its rationale and the acting worker+session — §8.7's definition of done for the whole spec; a blank rationale is refused and changes nothing, and `worker_update` refuses to touch a prompt at all, naming the tool that can (§15.5, the boundary that keeps rewrites auditable); the rewrite emits a routable **`config.changed`** naming its config-event id, stamped `worker` with a non-zero depth so the loop floor still bites; and the §8.8 bootstrap is one manager plus two schedules |
| **G1 §8.8 autonomy** | same, with `--mock-script` | with a scripted model the **model chooses the tool call**: a due schedule fires, the manager job calls `worker_create`, and a worker its prompt described — which no human and no bootstrap code path created — exists, logged with the manager as actor. A content worker calls `request_human_attention` and gets back a permalink to its own conversation, succeeding with `channel: "none"` when none is configured |
| Images §13 / skills §14 | `features/images-and-skills.stack.spec.ts` | curate-then-burn end to end: `skill_create` → `skill_install` lands the document and runs its script; **a failing script is a loud failure** carrying exit status, stderr and "do not proceed"; `image_create` allocates versions 1 then 2, gap-free, listed newest-first with worker/session/`session_url` provenance; an older version survives a newer burn unchanged; the catalogue exposes **no update or delete verb**; `image_create`/`skill_create` are logged with the acting session while `skill_install` logs nothing (§14.2); both lists cap at 200 with `truncated`; and a token with no `sid` is refused by both |
| **The worker image pointer §13.3/§13.5 (I4)** | `features/image-curation.stack.spec.ts` | curate-then-burn all the way to a launch: a vanilla session `skill_install`s a probe whose script marks the filesystem, `image_create` burns it, `worker_update` adopts it — and the worker's **next job runs in a container carrying that marker**, read back through DinD, which is the only assertion a "resolved perfectly then launched the base image anyway" regression cannot fake. Plus: a floating `toolbox` follows a second burn while a pinned `toolbox:1` does not; a pointer at a name nobody burned **fails the delivery with no session created** (§13.3 — never a silent fallback); a worker with no pointer is undisturbed; and every launch stamps `last_resumed_at`, which nothing called before I4 |
| **Schedules that cannot provision §8.6** | `features/schedule-resilience.stack.spec.ts` | the mechanism that stops the incident this suite caused: a `* * * * *` schedule on a **disabled** worker is refused at the dispatch gate every minute, and after five consecutive firings that start no job the scheduler **switches the schedule off**, with the reason in the config log — one record for the decision, none for the five observations. The disable resets the streak (re-enabling gets a full budget), and nothing is provisioned along the way, so the test for the anti-storm mechanism cannot itself start a storm. Slowest test in the suite (~6 min): a firing is one wall-clock minute and there is no catch-up |
| Harness itself | `features/harness.stack.spec.ts` | the fixtures do what they claim, including the polling failure message and the permalink format |

### No known failures

Both defects this suite found have been fixed and are now guarded — see below. When you add a red
test, list it here with its evidence so nobody mistakes it for flakiness.


### Known gaps — deliberately red

`features/acceptance-loop.spec.ts` › *a worker's own prompt reaches the model, not just the session
row*.

**The composed prompt never reaches the model.** `dispatch.go` creates the session with `Worker` set
and the full composed prompt written to `composed_prompt`, but never sets `Persona`; `SendMessage`
then re-resolves the system prompt every turn through the provider, which — with an empty `Persona`
— contributes no worker layer; and nothing sends the stored `composed_prompt` on the query. So the
core preamble, the worker prompt and the memory briefings are composed deterministically, written to
the database, and discarded. The model sees the project prompt and nothing else.

This test is built so the row cannot fool it: the marker lives **only** in the worker's system
prompt, the triggering event text carries none, and the scripted model calls a tool only if it
actually saw the marker. The witness worker exists if and only if the prompt was delivered.

**Read this before trusting any other prompt assertion here.** Every other one reads
`composed_prompt` off the session row, which is what composition *stored* — including the §7.4
memory-briefing test. Those were all passing throughout the period the model was receiving none of
it. They are not wrong, but they are assertions about composition, not delivery, and their comments
now say so.


`features/acceptance-loop.spec.ts` › *asking for attention pauses the job*.

**Asking for human attention does not pause the job.** The tool records the request and returns
cleanly — the test above it proves that — but the delivery runs to `ok` with an `ended_at`, and the
`worker.finished` envelope carries `attention_requested: false`. §8.4 wants the delivery parked at
`awaiting_human` with no `ended_at`: a pause, not a finish, so the UI shows an open-ended duration
and a human can answer hours later. This is exactly the gap E2 flagged when it wrote
`attention_requested` as a parameter the Runner passes `false` — "H2 must add a session-level flag
and one line in `emitJobOutcome`". From the outside, a job that asked for sign-off is currently
indistinguishable from one that finished its work.

### Fixed, and now guarded

Two defects were found by writing the test first, leaving it red, and reporting it. Both are fixed;
the tests stay as regression guards.

- *a turn interrupted by a reload is still persisted* (`product-ui`) — red until `8faaa95`.
  Reloading mid-answer lost the whole turn, the human's own message included: `persist()` handed
  the store the caller's *already cancelled* context, so a real sink rejected the write instantly.
  Every unit test used a mock sink that ignores context, which is why only an e2e caught it. Leave
  it asserting the API rather than the DOM — a DOM assertion would pass on a UI that renders an
  optimistic echo of a message the server never stored.
- *a project's mcp_config reaches its sessions* (`session-mcp`) — red until `7170bed`. A project's
  tools resolved correctly and reached no container: agentd's `Resolve` never set the `MCPServers`
  field the Runner merges. Three tracks each built their half and nothing joined them.

### The queue — not covered, and why

Do **not** write these until the machinery exists; they would be tests of unbuilt code.

| Spec area | Blocked on | The e2e to write |
| --- | --- | --- |
| "Chat with this worker" (the workers page's Chat tab) | `createSessionBody` has no `worker` field | assert a session started from a worker's Chat tab carries that worker and its composed prompt. Today the tab opens a **plain** session, so the test would pass while proving nothing |
| Worker job history (the Jobs tab) | E3 (nothing writes deliveries yet) | assert a worker's Jobs tab lists its deliveries and that clicking one opens that session — the `onOpenSession` wiring is in place and unexercised |
| Router: event → delivery → job (§8.4) | Track E3 | post an event with a matching subscription; assert a delivery row appears, goes `pending → running → ok`, and a session is created for the right worker |
| Loop floor / depth, rate limits (§8.3–§8.4) | E3 | an event storm asserts depth caps and `rate_limited` deliveries |
| `max_instances` gating (§6.1) | E3 | deliveries beyond capacity stay `pending` and dispatch FIFO |
| Job composition in a real session (§6.2) | C4/E3 | the composed prompt of a launched session contains project prompt + worker prompt + briefing + event envelope |
| Memory §7 | D3 (tools) | a job writes a memory; a later job's briefing contains it; `name=` singleton replacement; RRF search ordering |
| Management tools §9 | D3/E4/H1 | a worker hires a worker / rewrites a prompt **through MCP tools**, not HTTP |
| Prompt rewrite provenance (§15.5) | E4/H1 | `worker_prompt_write` carries a **non-empty rationale** — the one field §15.5 makes mandatory |
| `config.changed` event (§15.4) | J3 | a config mutation emits the routable event **after commit**, once, idempotent on the config-event id |
| `config_history` / fold / restore (§15.6–§15.7) | J2/J4 | fold to a timestamp; restore a deleted worker from its `worker_delete` record |
| Schedules §8.6 — the happy path | Track H | a due schedule fires a job; a missed window is skipped, not caught up. (The *failure* path is covered: `features/schedule-resilience.stack.spec.ts`) |
| Port-pool exhaustion is reported as a host limit | nothing — it is **untestable from here**, on purpose | the capacity error needs 100 live sessions to reach, and `PortRangeStart/End` are hardcoded at `go/cmd/agentd/main.go:132` rather than configurable, so a test could only produce it by filling the host it runs on. Verified instead by reading the string out of the running binary (see the triage section) plus `go/runner_portpool_test.go` and `go/execenv/docker/ports_exhaustion_test.go`. That the message *reaches an HTTP caller* is evidenced by the red `base_image` test, which measures the sibling line of the same function (`runner.go:1007`) arriving verbatim. **Making the range configurable would make this testable** — worth doing if the pool is ever touched again |
| `request_human_attention` (§9) | H2 | delivery goes `awaiting_human`, the attention channel receives `{message, session_url}` |
| Images & skills §13–§14 | I2/I3/I4 | `image_create` → a worker pinned to `name:version` launches from it; `skill_install` |
| G3: live smoke | G1 | the same loop in `api-key` mode, manually observed |

## The run refuses to leak

`stack-setup.ts` and `stack-teardown.ts` wrap every run:

- **Before**: prints what the host is carrying (`N/100 session ports in use, M enabled schedules`),
  warns unmissably if e2e schedules are still firing, and **refuses to start** with fewer than 25
  free ports — because a run started on a full host fails every session it tries to provision, and
  a suite that reports fifty failures says nothing about the product. The engine now names that
  cause in the error (see the triage section); the guard stays because being told clearly at failure
  fifty is worse than not starting.
- **After**: compares against that baseline and **fails the run** if it leaked — sessions it did not
  release, or e2e schedules still enabled. A run that leaks does not get to report success; the leak
  *is* the failure, even when every assertion passed. Schedules are deleted as well as reported (so
  the next person is not poisoned); containers are only reported, since on a shared stack one may
  belong to somebody else's run.

The reason this lives outside the tests: `afterEach` cannot clean up after a test that died before
`afterEach`, and those are precisely the runs that leak. For the same reason, a test that creates a
`* * * * *` schedule releases it in a **`finally`** — see the §8.8 reconcile test. One abandoned
schedule that can still provision will drain the pool on its own, at a session a minute, and
nothing retires it: §8.6's five-failure rule catches only schedules that start *no* job.

## Writing an assertion that is worth having

One bug hid behind green tests for a day: the composed prompt was written to the session row and
never sent to the model, while every prompt assertion here read that row and passed. The habit that
prevents a repeat is a single question — **am I reading back a value the system just wrote?**

If yes, the test proves storage. Storage is worth asserting, but say so, and find the boundary the
feature actually crosses:

| The feature must reach… | Assert at | Not at |
| --- | --- | --- |
| the model | a scripted tool call that only fires if the text arrived (`mock-scripts/`) | `composed_prompt` on the session row |
| a container | the file, read back through DinD (`readFileInSessionContainer`) | the tool's own `{file_written, bytes_written}` |
| the router | a delivery row and the session it created | the subscription you just POSTed |
| another project | a 404 from the other project's token | your own project's read |

### The worked example: a test that looked sound and wasn't

`skill_install` writes a document into a session's container and runs an install script. The test
asserted this:

```ts
expect(installed).toMatchObject({ installed: true, file_written: `${SKILLS_DIR}/…/SKILL.md` })
expect(installed.bytes_written).toBeGreaterThan(0)
expect(installed.script).toMatchObject({ ran: true, exit_code: 0 })
```

Every one of those fields is written by the tool, **about itself**. A `skill_install` that wrote
nothing to disk and returned exactly that JSON would have passed. The test was green, it was
reviewed, it was cited twice as an example of doing this well — and it proved only that the tool is
internally consistent.

What it says now:

```ts
const onDisk = await readFileInSessionContainer(session, `${SKILLS_DIR}/…/SKILL.md`)
expect(onDisk).toContain('Use ffmpeg with the house preset.')
expect(await readFileInSessionContainer(session, '/tmp/render-social-video.log')).toContain('installing')
```

The container is a witness the tool cannot coach. The second line is the install script's own side
effect, so `ran: true` is corroborated by something the tool did not write.

It passed both before and after, so no bug was hiding — this time. The same shape of assertion, one
file away, hid the composed-prompt bug for a day: every prompt test read `composed_prompt` off the
session row while the model was receiving none of it.

Two more that catch a lot:

- **Could this pass if everything downstream of the write were disconnected?** `PUT /agent/project-settings`
  followed by `GET` passes whether or not a single session ever uses those settings. That exact gap
  was a real bug (project `mcp_config` resolved and dropped before reaching any container).
- **Does the name promise more than the assertions deliver?** A test called "installs into a session"
  that only checks the installer's return value retires the question without answering it, which is
  worse than not having it.

## Notes for whoever extends this

- **When you inherit this, read "When a run fails, triage before you debug" first.** The single most
  expensive habit on this suite is debugging the product because the stack was full.
- **A failing test that names a real gap is worth more than a passing one that dodges it.** If a
  feature cannot pass because the product is wrong, leave the test failing with a name that says
  what is broken, and log it in the work plan's Discovered Issues. Put it in its own `describe`
  so a serial block does not skip the tests after it.
- **The library components carry no `data-testid`.** `ProjectSettingsPage`, `WorkersPage` and
  `WorkerEditor` are driven by accessible role and label (`getByLabel('System prompt')`,
  `getByRole('button', { name: 'Save worker' })`) — which is the better habit anyway, but means a
  renamed label breaks a test. The app shell (`examples/web`) does have test ids: `login-*`,
  `project-picker`, `project-switcher`, `new-session`, `session-sidebar`, `session-row`, and the
  view switch `nav-chat` / `nav-workers` / `nav-settings`.
- `features/*.spec.ts` are serial per file (`fullyParallel: false`, one worker) because they share
  one stack. Keep them independent anyway — each mints its own project.
- The stack build takes minutes; the tests take seconds. Use `up` once and `test` in a loop.
