# Run agentkit as a standalone stack

One command brings up the whole thing — API, a chat UI, and the container runtime.

## 3 commands

    cp .env.example .env
    # (optional) put a real key in .env: ANTHROPIC_API_KEY=sk-ant-...
    docker compose up --build

Open http://localhost:8080, create a session, and chat.

Model credentials (precedence: API key > subscription token > mock):

- **`ANTHROPIC_API_KEY` set** → a real agent, API-billed via agentd's model proxy.
- **`CLAUDE_CODE_OAUTH_TOKEN` set** (from `claude setup-token`) → a real agent
  billed to your Claude Code **subscription**; sessions call api.anthropic.com
  directly. See the caveat in `.env.example`.
- **Neither** → a deterministic mock model replies, so the UI still works offline.

## Login + projects (optional)

By default the stack is dev-open (no login, one demo tenant). To turn on login,
set in `.env`: `AGENTKIT_JWT_SECRET` (a real secret), a project map
(`AGENTKIT_PROJECT_MAP={"you@gmail.com":["apples-oranges"]}`), and either
`GOOGLE_CLIENT_ID` (Google Sign-In) or `AGENTKIT_TEST_LOGIN=email:password`
(fixed test account, granted every project). A *project* is just a namespace
over sessions — pick one after login, and the sidebar lists that project's
sessions with a filter by user.

## End-to-end test

The browser e2e (login → create project → new session → streamed reply →
replay → project namespacing) runs against a stack you keep up between runs —
the fast loop is:

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

## What's running

| Service | Role |
|---|---|
| `web` | nginx serving the bundled chat UI; same-origin reverse proxy to agentd |
| `agentd` | the API + orchestrator + `/agent-proxy`; shares DinD's network namespace |
| `dind` | Docker-in-Docker; hosts one container per session |
| `init-sandbox` | one-shot: builds + loads the sandbox image into DinD |
| `postgres` | session/message store (pgvector image, ready for the memory system) |

## Customize the agent image

Set `BASE_IMAGE` in `.env` to your own image (built on `agentkit-sandbox`). See
`docs/15-standalone-stack.md` for the app-image contract and per-app plugins.

## This is NOT how you embed agentkit as a library

If you want to integrate agentkit *into your own Go server*, you do NOT run this
stack — see `docs/15-standalone-stack.md` → "Library vs standalone".
