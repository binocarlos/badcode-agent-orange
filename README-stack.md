# Run agentkit as a standalone stack

One command brings up the whole thing — API, a chat UI, and the container runtime.

## 3 commands

    cp .env.example .env
    # (optional) put a real key in .env: ANTHROPIC_API_KEY=sk-ant-...
    docker compose up --build

Open http://localhost:8080, create a session, and chat.

- **With `ANTHROPIC_API_KEY` set** → you talk to a real agent.
- **Without it** → a deterministic mock model replies, so the UI still works offline.

## What's running

| Service | Role |
|---|---|
| `web` | nginx serving the bundled chat UI; same-origin reverse proxy to agentd |
| `agentd` | the API + orchestrator + `/agent-proxy`; shares DinD's network namespace |
| `dind` | Docker-in-Docker; hosts one container per session |
| `init-sandbox` | one-shot: builds + loads the sandbox image into DinD |

## Customize the agent image

Set `BASE_IMAGE` in `.env` to your own image (built on `agentkit-sandbox`). See
`docs/15-standalone-stack.md` for the app-image contract and per-app plugins.

## This is NOT how you embed agentkit as a library

If you want to integrate agentkit *into your own Go server*, you do NOT run this
stack — see `docs/15-standalone-stack.md` → "Library vs standalone".
