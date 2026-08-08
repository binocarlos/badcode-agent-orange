# Run Agent Orange as a standalone stack

One command brings up the whole thing — API, a chat UI, and the container runtime.

## For day-to-day development: `./stack`

If you have a **real** `.env` (this project's actual GCP + Anthropic settings), the
three commands below do not work — see "two traps" further down. `./stack` exists
so you do not have to remember the workaround:

    ./stack build          # build the images
    ./stack start          # up + tmux log panes, REAL model (billable)
    ./stack start mock     # same, deterministic offline mock model (free)
    ./stack status         # which model, which services, how many session ports
    ./stack psql           # a psql shell
    ./stack testdb up      # throwaway Postgres for the live-PG Go suite
    ./stack test-go        # go test with that database wired up correctly

It bypasses `docker-compose.override.yml` and forces the local `fs`/`blobarchive`
backends, which is exactly what the manual invocation below does by hand.
`./stack help` lists everything.

## 3 commands

    cp .env.example .env
    # (optional) put a real key in .env: ANTHROPIC_API_KEY=sk-ant-...
    docker compose up --build

Open http://localhost:8080, create a session, and chat.

For the product layer on top of that — projects, workers, memory, subscriptions,
schedules, images, skills, the config log — see
`docs/18-workers-memory-events.md`.

Model credentials (precedence: API key > subscription token > mock):

- **`ANTHROPIC_API_KEY` set** → a real agent, API-billed via agentd's model proxy.
- **`CLAUDE_CODE_OAUTH_TOKEN` set** (from `claude setup-token`) → a real agent
  billed to your Claude Code **subscription**; sessions call api.anthropic.com
  directly. See the caveat in `.env.example`.
- **Neither** → a deterministic mock model replies, so the UI still works offline.

## If you have a real `.env`: two traps that bite before you see anything

The three commands above assume `.env` came from `.env.example`. A **working**
`.env` — one carrying this project's real GCP and Anthropic settings — does not
boot the stack locally, and would bill you if it did. Both were found on a live
run (doc 21); neither is a product defect, and neither is fixed by editing
`.env`, which is a real credential file.

**(a) It exits at boot on GCS credentials.** `.env` sets
`AGENTKIT_BLOB_BACKEND=gcs` with `GOOGLE_APPLICATION_CREDENTIALS=/gcp/key.json`.
That variable is *also* the host side of the key bind-mount, so compose mounts
`/gcp/key.json` onto `/gcp/key.json`; with no such file on the host Docker
creates it as a **directory**, and agentd dies with:

    gcsblob: new client: dialing: read /gcp/key.json: is a directory

**(b) A plain `docker compose up` runs a REAL, billable agent**, because `.env`
holds a real `ANTHROPIC_API_KEY`. Mock mode needs **both** credentials blanked —
the subscription token as well as the API key.

### The known-good local/mock invocation

Every override is load-bearing:

    WEB_PORT=8081 \
    ANTHROPIC_API_KEY= CLAUDE_CODE_OAUTH_TOKEN= \
    AGENTKIT_BLOB_BACKEND=fs AGENTKIT_REGISTRY_BACKEND=blobarchive \
    GOOGLE_APPLICATION_CREDENTIALS= \
    docker compose up -d --build

Then open **http://localhost:8081**. `WEB_PORT=8081` is there so this stack can
run alongside the e2e stack, which takes 8080 (`./e2e/run-stack-e2e.sh`); drop it
if nothing else holds that port. Check before you start — the compose stack is a
serial resource.

**Read the boot log before you seed anything.** agentd says which model it chose,
and this is the line that means you are not being billed:

    docker compose logs agentd | grep 'model proxy'
    [agentd] ANTHROPIC_API_KEY unset → MOCK model proxy (set it for a real agent)

## The product layer needs Postgres

The compose stack sets `DATABASE_URL`, so this is only a trap if you run `agentd`
yourself. **Everything above session chat is wired only when `DATABASE_URL` is
set**: without it the event router and scheduler never run, the core MCP server
(memory, worker, image, skill and management tools) is not mounted at all, and
project settings and worker prompts silently do not apply. agentd says so at
boot and then behaves like a healthy stack that does nothing.

Memory specifically **requires** Postgres — the store returns
`ErrMemoryRequiresPostgres` on sqlite rather than answering searches with
plausible but incomplete results. Inside Postgres, pgvector is optional: without
it search drops the semantic leg and ranks on keyword + recency, with the result
shape unchanged. Details in `docs/15-standalone-stack.md`.

The semantic leg also needs an embedding provider, and `AGENTKIT_EMBEDDING_BACKEND`
(forwarded by compose) offers only `none` — the default — and `mock`. So out of
the box, memory search in this stack is keyword + recency; agentd logs which it
is at boot. A typo in the variable is a boot error, not a silent fallback. A real
hosted embedder is host code — you construct one and pass it in.

## Login + projects (optional)

By default the stack is dev-open (no login, one demo tenant). To turn on login,
set in `.env`: `AGENTKIT_JWT_SECRET` (a real secret — agentd refuses to boot in a
login mode without it), a project map
(`AGENTKIT_PROJECT_MAP={"you@gmail.com":["apples-oranges"]}`), and either
`GOOGLE_CLIENT_ID` (Google Sign-In) or `AGENTKIT_TEST_LOGIN=email:password`
(fixed test account, granted every project). Pick a project after login, and the
sidebar lists that project's sessions with a filter by user.

A *project* is the one hard namespace: it scopes sessions, and it is also what
holds the product layer — workers, memories, events, schedules, images, skills,
the config log. In the token it is the `customer` claim.

## End-to-end test

The e2e runs against a stack you keep up between runs. It covers the chat flow
(login → create project → new session → streamed reply → replay → project
namespacing) and the product layer — session MCP config, workers and settings,
images and skills, image curation, the schedule and port-pool failure paths, and
`acceptance-loop.spec.ts`, in which one worker rewrites another worker's system
prompt with no human and no deploy, offline (the spec's definition of done —
§8.7). Specs live in `e2e/features/` and run via `playwright.stack.config.ts`;
`e2e/tests/` is the older Vite + mock-server rig. The fast loop is:

    ./e2e/run-stack-e2e.sh up            # build + start (mock mode), ~minutes once
    ./e2e/run-stack-e2e.sh test          # seconds per iteration — repeat at will
    ./e2e/run-stack-e2e.sh down          # capture logs + stop (volumes kept)

Tests clean up after themselves (run-scoped project names, sessions deleted in
teardown), so repeated `test` runs against one stack don't collide. The
clean-room one-shot is `./e2e/run-stack-e2e.sh run [mock|api-key|subscription|all]`
(up → test → purge-down); a bare mode name is shorthand for the same thing.

- `mock` (default): deterministic offline model — the signal you iterate against.
- `api-key` / `subscription`: the same flow against the real Anthropic model,
  billed to `ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN` (read from the
  shell env or `.env`) — sanity checks that both auth modes really work.
  Switching modes = another `up` (only agentd restarts).

Two per-run flags on `test`, mutually exclusive (each reload carries only its own
override, and both restore however the run ends):

- `--mock-script FILE` — let the mock model choose its own tool calls. §8.8's
  half of the acceptance loop (a manager worker creating a missing worker;
  `request_human_attention` parking a job) **only runs with it**, and skips
  silently without: `./e2e/run-stack-e2e.sh test --mock-script e2e/mock-scripts/g1-acceptance.json`.
- `--port-pool N` — boot agentd with exactly N session ports, so the exhaustion
  path is reachable in seconds. Refuses to start if any session is live.

Per-mode logs land in `e2e/stack-e2e-logs-<mode>.txt`, written by `down`.

**Note that `.github/workflows/ci.yml` does not run any of this.** CI covers
`go/`, `sandbox/` and `web/` only; the stack e2e is a thing a human runs.

**Sessions hold a running container — and one host port — until the session is
deleted or goes idle**: `agentd` snapshots and releases the container of a
session idle for `AGENTKIT_SESSION_IDLE_TIMEOUT` (default 30m) and the next
message restores it, so this frees the port without ending the conversation.
The port pool is the hard ceiling on
concurrent sessions per host: **100 by default** (`AGENTKIT_PORT_RANGE_START`
/`_END`, set both or neither). At zero free, every further session fails with
"host port pool is exhausted", naming the pool, its size and what holds it —
and the message arrives *inside the message stream*, because create answers 200
and provisions in the background. The suite
deletes its own sessions; `./e2e/run-stack-e2e.sh clean` removes leftovers when
something else did not. `clean` also **restarts agentd**, which is not optional:
pulling containers out from under a running agentd leaves its placement state
naming instances that no longer exist, and it then refuses to provision *any*
new session until restarted. The script does the restart for you.

## What's running

| Service | Role |
|---|---|
| `web` | nginx serving a **built** bundle of `examples/web` (the app shell that composes the `web/` component library); same-origin reverse proxy to agentd. UI edits are invisible to the browser until `docker compose up -d --build web` |
| `agentd` | the API + orchestrator + `/agent-proxy`; shares DinD's network namespace |
| `dind` | Docker-in-Docker; hosts one container per session |
| `init-sandbox` | one-shot: builds + loads the sandbox image into DinD |
| `postgres` | session/message store **and the whole product layer** — workers, memories, events, schedules, images, skills, the config log. `pgvector/pgvector:pg16`, so `CREATE EXTENSION vector` works without an image swap |

## Customize the agent image

Set `BASE_IMAGE` in `.env` to your own image (built on `agentkit-sandbox`). See
`docs/15-standalone-stack.md` for the app-image contract and per-app plugins.

## This is NOT how you embed Agent Orange as a library

If you want to integrate Agent Orange *into your own Go server*, you do NOT run this
stack — see `docs/15-standalone-stack.md` → "Library vs standalone".
