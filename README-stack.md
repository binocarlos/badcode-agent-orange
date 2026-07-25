# Run Agent Orange as a standalone stack

One command brings up the whole thing — API, a chat UI, and the container runtime.

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

## Login + projects (optional)

By default the stack is dev-open (no login, one demo tenant). To turn on login,
set in `.env`: `AGENTKIT_JWT_SECRET` (a real secret), a project map
(`AGENTKIT_PROJECT_MAP={"you@gmail.com":["apples-oranges"]}`), and either
`GOOGLE_CLIENT_ID` (Google Sign-In) or `AGENTKIT_TEST_LOGIN=email:password`
(fixed test account, granted every project). A *project* is just a namespace
over sessions — pick one after login, and the sidebar lists that project's
sessions with a filter by user.

## End-to-end test

The e2e runs against a stack you keep up between runs. It covers the chat flow
(login → create project → new session → streamed reply → replay → project
namespacing) and the product layer — session MCP config, workers and settings,
images and skills, and `acceptance-loop.spec.ts`, in which one worker rewrites
another worker's system prompt with no human and no deploy, offline (the spec's
definition of done — §8.7). Specs live in `e2e/features/`
and run via `playwright.stack.config.ts`; `e2e/tests/` is the older Vite +
mock-server rig. The fast loop is:

    ./e2e/run-stack-e2e.sh up            # build + start (mock mode), ~minutes once
    ./e2e/run-stack-e2e.sh test          # seconds per iteration — repeat at will
    ./e2e/run-stack-e2e.sh down          # capture logs + stop (volumes kept)

Tests clean up after themselves (run-scoped project names, sessions deleted in
teardown), so repeated `test` runs against one stack don't collide. CI uses the
clean-room one-shot: `./e2e/run-stack-e2e.sh run [mock|api-key|subscription|all]`.

- `mock` (default): deterministic offline model — the CI signal.
- `api-key` / `subscription`: the same flow against the real Anthropic model,
  billed to `ANTHROPIC_API_KEY` / `CLAUDE_CODE_OAUTH_TOKEN` (read from the
  shell env or `.env`) — sanity checks that both auth modes really work.
  Switching modes = another `up` (only agentd restarts).

Per-mode logs land in `e2e/stack-e2e-logs-<mode>.txt`.

**Sessions hold a running container until the session is deleted** — nothing
reaps them on a timer, and a stack that has accumulated enough of them starts
failing to provision new ones (roughly a hundred was enough once; the symptom is
"session has no running instance and no snapshot", which reads like a product
bug and is not one). The suite
deletes its own sessions; `./e2e/run-stack-e2e.sh clean` removes leftovers when
something else did not. `clean` also **restarts agentd**, which is not optional:
pulling containers out from under a running agentd leaves its placement state
naming instances that no longer exist, and it then refuses to provision *any*
new session until restarted. The script does the restart for you.

## What's running

| Service | Role |
|---|---|
| `web` | nginx serving the bundled chat UI; same-origin reverse proxy to agentd |
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
