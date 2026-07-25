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
```

Modes are `mock` (deterministic, offline — the CI signal), `api-key`, and `subscription`. Docker is
required; the stack runs one session container per session inside DinD.

> **`clean` restarts agentd, on purpose.** Removing session containers out from under a running
> agentd leaves its placement state naming dead instances, and it then refuses to provision *any*
> new session until restarted. `cmd_clean` now does the restart itself. Prefer deleting sessions
> through the API anyway — `client.cleanup()` in an `afterEach` — and keep `clean` for containers a
> previous run abandoned.

To iterate on a single spec against an already-running stack:

```sh
cd e2e && STACK_BASE_URL=http://localhost:8080 npx playwright test \
  --config playwright.stack.config.ts features/config-and-workers.stack.spec.ts
```

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
- **Delete the sessions you create.** A session holds a *running container* inside DinD until it is
  deleted, and nothing reaps them on a timer. Call `client.cleanup()` from `afterEach` (or a
  `finally`) in any spec that creates sessions. A suite that skips this fills the daemon — at 100
  live containers `image_create` starts failing with "no running instance", which looks exactly like
  a product bug and is not one.

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
| **The acceptance loop §8.7/§8.8** | `features/acceptance-loop.spec.ts` | the org seeds and every hire is logged; an inbound email enters with a core-stamped envelope; **the router starts an answerer job** and the composed prompt it ran with carries the worker's prompt; **the answerer finishing fans out to reviewer and archivist** at depth 1, and the reviewer does *not* react to its own finish; **a memory written by one job appears verbatim in the next job's composed prompt** under the heading its briefing selector asked for (§7.4 — the loop's substrate); **the reviewer rewrites the answerer's prompt through `worker_prompt_write`**, logged with its rationale and the acting worker+session, leaving a `kind=prompt-revision` memory holding the superseded text — §8.7's definition of done for the whole spec; a rewrite with a blank rationale is refused and changes nothing (§15.5); and the §8.8 bootstrap is one manager plus two schedules |
| Images §13 / skills §14 | `features/images-and-skills.stack.spec.ts` | curate-then-burn end to end: `skill_create` → `skill_install` lands the document and runs its script; **a failing script is a loud failure** carrying exit status, stderr and "do not proceed"; `image_create` allocates versions 1 then 2, gap-free, listed newest-first with worker/session/`session_url` provenance; an older version survives a newer burn unchanged; the catalogue exposes **no update or delete verb**; `image_create`/`skill_create` are logged with the acting session while `skill_install` logs nothing (§14.2); both lists cap at 200 with `truncated`; and a token with no `sid` is refused by both |
| Harness itself | `features/harness.stack.spec.ts` | the fixtures do what they claim, including the polling failure message and the permalink format |

### No known failures

Both defects this suite found have been fixed and are now guarded — see below. When you add a red
test, list it here with its evidence so nobody mistakes it for flakiness.


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
| Schedules §8.6 | Track H | a due schedule fires a job; a missed window is skipped, not caught up |
| `request_human_attention` (§9) | H2 | delivery goes `awaiting_human`, the attention channel receives `{message, session_url}` |
| Images & skills §13–§14 | I2/I3/I4 | `image_create` → a worker pinned to `name:version` launches from it; `skill_install` |
| **G1: the §8.7 acceptance loop** | J3, plus a mock script | **live** in `features/acceptance-loop.spec.ts` — see Covered. Three pending: `config.changed` (J3 has no emitter yet), and the two §8.8 tests that need a *job* to call a tool, which wants `AGENTKIT_MOCK_MODEL_SCRIPT` wired into the run script. Do not delete one to make the file green |
| G3: live smoke | G1 | the same loop in `api-key` mode, manually observed |

## Notes for whoever extends this

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
