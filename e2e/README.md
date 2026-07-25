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

From `helpers/ui.ts` (browser):

| Fixture | What it gives you |
| --- | --- |
| `loginUI(page)` | logged in, at the project picker, with stale localStorage cleared |
| `openFreshProject(page, prefix?)` | logged in and inside a brand-new project's workspace; returns the id |
| `gotoView(page, 'chat' \| 'workers' \| 'settings')` | the workspace view switch |
| `selectedProject(page)` | which project the switcher shows |
| `sendAndSettle(page, prompt)` | sends a prompt and waits for the reply to **stop changing** |

`sendAndSettle` is not optional politeness: a turn is only persisted once it ends, so any test
that reloads or navigates after talking must settle first or it is racing the model. (Reloading
mid-turn is itself broken — see the known failure below.)

From `helpers/configlog.ts`: `configEvents(project)`, `configActions(project)`,
`waitForConfigEvents(project, n)`. These read Postgres directly **because the config log has no HTTP
route yet** — see the queue below. When that route lands, rewrite these three functions and every
spec keeps working.

Two conventions worth keeping:

- **Each test gets its own project.** `newProjectClient` mints one; projects are just names, so
  they need no cleanup and repeated runs against one long-lived stack never collide.
- **Never assert a mapped project is empty.** `apples-oranges` and `pears-plums` are shared with the
  browser spec and with previous runs.

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
| Harness itself | `features/harness.stack.spec.ts` | the fixtures do what they claim, including the polling failure message and the permalink format |

### Known failure — deliberately red

`features/product-ui.stack.spec.ts` › *an interrupted turn is still persisted*.

**Reload the page while the agent is answering and the entire turn is lost, including the human's
own message.** `GET /agent/session/{id}/messages` returns `{"count":0,...}` for that session, and
the session row is left stuck at `status: "running"` indefinitely. Re-opening it — by permalink or
from the sidebar — shows a blank transcript that never recovers (verified over 60s). A turn allowed
to finish replays perfectly, so this is specifically about interruption.

It is left failing rather than adjusted, because the product is wrong: P8 says a transcript is an
immutable record of what happened, and this is the one way a human can silently lose their own
words. The assertion is against the API, not the DOM — the UI cannot replay what was never written.

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
| **G1: the §8.7 acceptance loop** | everything above | seed answerer + reviewer + archivist + subscriptions; post `email.received`; assert answerer job → reviewer job → prompt rewritten (with rationale, logged) → memory written → rolling summary present in the **next** job's composed prompt |
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
