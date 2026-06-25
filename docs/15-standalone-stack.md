# 15 — The standalone stack (`agentd`)

`agentd` is a pre-built host: the agentkit library + reference adapters + `httpapi`
+ a folded-in model proxy, assembled into one runnable binary and a docker-compose
stack. Run it when you want agent sessions over HTTP without writing a host or
managing Docker.

## Library vs standalone — pick ONE

| | Library integration | Standalone stack |
|---|---|---|
| Who is the host? | your own Go server imports `agentkit` | `agentd` |
| Do you run `agentd`? | **No** | **Yes** |
| Adapters | you write them | shipped reference adapters |
| Docker/DinD | you manage it | the stack manages it |
| Model proxy | you mount `modelproxy.Handler` or write your own | `agentd` mounts it at `/agent-proxy` |

`agentd` is the *alternative* to integrating as a library — not a component of it.
Platinum (goapi) is the proof you can build a host without `agentd`.

## How apps integrate (the deployment topology)

    app frontend  ──vends──▶  @agentkit/chat-ui (NPM) + app render plugins
        │  HTTP/SSE (same origin)
        ▼
    app API server   — issues a JWT agentd trusts; picks an image; PROXIES the SSE stream
        │  HTTP/SSE   (agentd is PRIVATE — never browser-exposed)
        ▼
    agentd stack     — owns all Docker/infra; shared by all apps

- **Auth = JWT delegation.** Apps mint an HS256 JWT (claims `email`, `customer`,
  `job`) signed with `AGENTKIT_JWT_SECRET`; `agentd` only verifies. Leave the secret
  blank for dev-open mode (local demo only).
- **Streaming.** The app's API server opens the agentd SSE stream and relays it to
  its own frontend. Keep `agentd` private.

## Model routing

`agentd` injects `ANTHROPIC_BASE_URL=<self>/agent-proxy` and a dummy key into every
session container (the same `Policy.SessionEnv` seam Platinum uses). The real key
lives only in `agentd`'s env and is injected upstream by `/agent-proxy` — it never
enters a container. With no key, `/agent-proxy` serves canned mock responses.

Point at a non-Anthropic upstream with `ANTHROPIC_UPSTREAM_URL`.

## Customize the agent image (base image + plugins)

The Runner launches `BASE_IMAGE` per session. Build your own on top of
`agentkit-sandbox` and add tools via the app-image contract
(`/app/product-plugins`, `/workspace/lib`, `/workspace/.claude`,
`/workspace/CLAUDE.md`) — see `docs/10-extension-points.md`. Per-app **UI** plugins
register against `@agentkit/chat-ui`'s render-plugin seam in the app's frontend.

> Agent *profiles* (named base-image + prompt + tool bundles referenced at session
> start) are a separate upcoming feature (Spec 2). Today, select the image via
> `BASE_IMAGE` (stack-wide) or the `Image`/`CustomImageID` fields on the create-session
> request.
