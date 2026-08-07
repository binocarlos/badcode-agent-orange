# Embeddable Agent Orange — Design & Implementation Plan

> **EXECUTION RULES (for agents):** Work ONE ticket at a time, in order unless
> dependencies say otherwise. Only the orchestrator changes ticket Status;
> workers may only append to Notes and the Discovered Issues Log. A ticket's
> checkbox is checked only after its Validation commands have been re-run by
> the orchestrator and pass. Do not expand scope; log surprises in the
> Discovered Issues Log instead.

Status: approved (design signed off 2026-08-06; not yet started)
Relates: `docs/product/17-product-spec.md` §8.5 (headless posters / project tokens),
`docs/06-artifacts.md`, `docs/14-host-adapters.md` (tenancy contract),
`docs/05-event-streaming.md`

## Context

Agent Orange today is a **single-app system**: one console (`examples/web`) served
same-origin with `agentd` behind nginx, authenticated by per-project JWTs held in
`localStorage`, with project membership coming from a static env map. Everything
works because there is exactly one UI and it lives on the same origin as the API.

This plan makes Agent Orange a **singleton service that other applications embed**.
The driving use case is **Agent Wolf**, a financial-hypothesis product: a user writes
a hypothesis ("the AI bubble bursts, government prints liquidity, Bitcoin rises"), and
Agent Orange researches it daily, maintaining state Wolf renders on its own hypothesis
page. Wolf owns the opinionated UI, the vocabulary ("hypotheses", not "sessions"), and
its own user allowlist. Agent Orange owns all the state: prompts, sessions, schedules,
memories, artifacts.

### The Wolf shape: two bots, state in memory

An earlier draft assumed **one hypothesis = one session, forever**. That was rejected:
a session resumed daily for a year accumulates transcript, and — more sharply — a
session's composed prompt is frozen at creation, so configuration updates never reach it.
The model is instead two named workers plus project memory as the system of record:

| Atom | Kind | Role |
| --- | --- | --- |
| `hypothesis-a` | Long-lived session, user-facing | The conversation surface. Reads current state from memory **at message time**; holds no authoritative state of its own |
| `reviewer-a` | Worker + daily schedule, fresh session per tick | Researches the world, writes the updated state as a memory, and where warranted rewrites `hypothesis-a`'s prompt |

Everything this needs already exists and must **not** be rebuilt:

- **Memory is a genuine shared bus.** Project-scoped, append-only, no per-worker
  permissions and no origin check: any worker writes, any other worker's briefing reads
  (`go/agentdb/memories.go:199-208`, `go/compose.go:191-247`). **No cross-container
  file-mutation tool is needed, and none should be added.**
- **History of a name is already queryable.** `memory_search` with selector
  `name=<x>` and no query returns every version newest-first
  (`go/agentdb/memories.go:264-273`); `memory_current` takes only the newest
  (`go/cmd/agentd/mcp_memory.go:231,373`).
- **One agent rewriting another's prompt is built and guarded.**
  `worker_prompt_write(name, system_prompt, rationale)` — rationale mandatory, superseded
  prompt auto-stored as a `kind=prompt-revision` memory, frozen workers refuse
  (`go/cmd/agentd/mcp_management.go:536,1114-1127,1254-1295`).
- **Session→memory archiving is a policy, not a feature to build.** `worker.finished`
  carries the full verbatim transcript (`go/runner.go:1657-1666,1833-1843`); the intended
  pattern is an archivist worker writing `kind=rolling-summary,worker=<subject>`, which is
  the *default* briefing selector (`go/compose.go:157-159`). No archivist is wired in this
  repo; wiring one is a prompt, not a migration.
- **Memories are on HTTP but only as 500-byte snippets.** `GET /agent/memories`
  (`go/httpapi/httpapi.go:245,301`) calls `SearchMemories`, whose result type has a `Snippet`
  field and **no `Content` field** (`go/agentdb/memories.go:87-95`, truncation at `:35,259-262`),
  and there is no `GET /agent/memories/{id}`. Full memory content is reachable only from inside
  a container, via the `memory_get` / `memory_current` MCP tools. **T18 adds the full-content
  read route** — without it Wolf cannot render a hypothesis summary of any real length.

Wolf must not vendor Orange's code. It integrates over three seams:

1. a **project API key** for its backend,
2. an **iframe** rendering a live session chat, and
3. **artifact bytes** fetched server-side and re-served.

### The two persistence strategies (important — do not collapse them)

Agent Orange deliberately offers **two** ways to carry state between agent runs, and
this plan adds infrastructure for the second without displacing the first:

| Strategy | Mechanism | Suits |
| --- | --- | --- |
| **Memory** | Labelled, append-only rows; `memory_current(name)` returns the newest match; injected per-job via `Worker.Briefing` selectors (`go/agentdb/workers.go:85`, `go/cmd/agentd/mcp_memory.go:204-235`) | Product-layer workers, where **every scheduled tick spawns a fresh session and container** by design (`go/cmd/agentd/dispatch.go:535-540`) |
| **Session snapshot** | The archive loop snapshots an idle session and releases its container; the next message restores the filesystem *and* rehydrates conversation history (`go/runner.go:1014-1116`, `go/runner.go:1341-1410`) | Long-lived workspaces where **files are the state** — Wolf's per-hypothesis CSV and `summary.md` |

Wolf chooses session snapshots. A future embedding app may choose memory. The
platform must keep supporting both; an application-layer builder picks.

### What does not exist today (verified by sweep)

- **No API keys.** `httpapi.ProjectTokenIssuer` is a defined seam (`go/httpapi/httpapi.go:45-48`)
  whose route `POST /agent/project-token` (`go/httpapi/events.go:363-386`) returns **501**
  because `agentd` never sets it (`go/cmd/agentd/main.go:306-318`).
- **No session names.** Sessions have UUIDs and a title only.
- **No session-targeting schedules.** `scheduler.fire` (`go/cmd/agentd/scheduler.go:203-301`)
  always creates a project event that dispatches a *worker job* into a *new* session.
- **No artifact download route at all.** `ArtifactStore.Load` is implemented in every
  backend and called from nothing in `httpapi` — the TODO is explicit at
  `go/httpapi/artifacts_handler.go:13-15`. Three React components already call
  `/agent/artifacts/{id}/download` across seven call sites
  (`web/src/components/ArtifactViewer.tsx:167,208`; `ArtifactPreviewDialog.tsx:103,156`;
  `InlineArtifactPreview.tsx:52,89,112`). **The console is broken here too**; this plan fixes it.
- **No embeddable view.** The session permalink `/p/<project>/s/<id>`
  (`web/src/permalink.ts:17-21`) boots the whole shell — login gate, project picker,
  sidebar (`examples/web/src/App.tsx:49-292`).
- **No CORS anywhere in Go**, and no framing headers anywhere (neither `X-Frame-Options`
  nor CSP). Framing is not blocked; it is simply unusable because tokens live in
  `localStorage` on the Orange origin with no handoff mechanism.
- **Tenancy is not enforced on session-by-ID routes.** `Stream` (`go/httpapi/stream.go:36-40`),
  `Reconnect` (`stream.go:65-69`), `SendMessage` (`stream.go:96+`), `Status`
  (`go/httpapi/lifecycle.go:40-44`), `Cancel` (`lifecycle.go:70-74`), `Messages`
  (`go/httpapi/history.go:22-25`) and `QueryEvents` (`history.go:68-71`) call `identify()`
  for authentication and then discard the identity. `httpapi` documents this as delegated
  to the host (`go/httpapi/httpapi.go:97-111`); `agentd` is the host and does not do it.
  **This is a precondition for issuing a second credential** and is a blocking ticket here.

### Intended outcome

After this plan: ops sets `WOLF_API_KEY` in `agentd`'s environment and names it in the
project map. Wolf's backend creates a session named `hypothesis-a`, attaches a daily
session-mode schedule to it, embeds `<iframe src=".../embed/session/hypothesis-a#token=…">`,
and fetches `summary.md` by name to render on its own page. No Orange code is vendored,
no database table is added for keys, and no key-management UI exists.

## Architecture

```
   ┌─────────────────────── browser ───────────────────────┐
   │  Agent Wolf UI (wolf.badcode.dev)                     │
   │   hypothesis page                                     │
   │    ├─ <iframe src=orange/embed/session/hyp-a#token=…> │
   │    └─ markdown/img from wolf's own /api/artifact/…    │
   └───────┬───────────────────────────────┬───────────────┘
           │ wolf's own session cookie     │ iframe loads from Orange origin
           ▼                               ▼
   ┌────────────────────┐          ┌──────────────────────────┐
   │  Wolf backend      │          │  Agent Orange (singleton)│
   │  - user allowlist  │─────────▶│  agentd + console SPA    │
   │  - hypothesis rows │  X-API-  │                          │
   │    (name ↔ session)│  Key:    │  project "wolf"          │
   │  - proxies bytes   │  $WOLF_  │   ├ sessions (named)     │
   │  - mints embed tok │  API_KEY │   ├ schedules (session)  │
   └────────────────────┘          │   └ artifacts (blob)     │
           │                       └──────────────────────────┘
           └── POST /auth/verify-google ──────────▲
               (Orange verifies the Google ID token and
                returns {email}; Wolf decides access)
```

### Three credentials, one middleware

| Credential | Lifetime | Where it lives | Grants |
| --- | --- | --- | --- |
| **Project API key** | Long-lived, rotated by ops | `agentd` env var, named by the project map. Server-side only — never in a browser | Full API access to one project |
| **Embed token** | Minutes (default 15m) | Browser, in memory, arrives via URL fragment | Read/stream/message on **exactly one session** |
| **Console JWT** | 12h | `localStorage` on the Orange origin (unchanged) | Full API access to the projects the user's email maps to |

`jwtAuthMiddleware` (`go/cmd/agentd/auth.go:35-64`) becomes `apiAuthMiddleware`: it tries
API key first (constant-time compare), then JWT. Both paths produce the same `principal`;
the JWT path additionally carries an optional session scope.

### Config surface: the project map grows, no table is added

There is deliberately **no `api_keys` table and no key-management UI**. Keys are ops
config, like the project map itself. The map (`go/cmd/agentd/googleauth.go:24-64`) gains
an object form; the current flat form stays valid:

```jsonc
// legacy (still accepted, unchanged semantics)
{ "kai@badcode.dev": ["wolf", "demo"] }

// new object form
{
  "users":    { "kai@badcode.dev": ["wolf", "demo"] },
  "projects": {
    "wolf": {
      "api_key_env": "WOLF_API_KEY",
      "allowed_origins": ["https://wolf.badcode.dev"]
    }
  }
}
```

A project with no `api_key_env`, or whose named env var is empty at boot, has **no key**;
`agentd` logs that once at startup and does not fail. A key value shorter than 24
characters is a boot error (weak keys are worse than none).

### Why there is no CORS in this design

Every cross-origin hop is arranged so the browser never makes a cross-origin request to
`agentd`:

- The **embed page is served by Orange**, so its API calls are same-origin.
- **Wolf's backend → Orange** is server-to-server; CORS does not apply.
- **Artifact bytes** are proxied by Wolf's backend to Wolf's own origin.

`allowed_origins` therefore drives **`Content-Security-Policy: frame-ancestors`** on the
embed page — the header that actually controls who may frame Orange — not
`Access-Control-Allow-Origin`. Rejected alternative: adding CORS middleware plus signed
download URLs so Wolf's browser could hit Orange directly. It is strictly more code, puts
credentials in the browser, and buys nothing at markdown/CSV/PNG scale.

### Rejected alternatives (recorded)

- **DB-backed API keys with a settings UI.** Rejected: only BadCode ops will ever add a
  key, and the project map is already the ops config surface.
- **Wolf runs its own cron** calling the message API. Rejected: scheduling with
  idempotent firing, restore-on-fire and busy-session handling is platform capability;
  every embedding app would otherwise rebuild it.
- **Wolf runs its own Google OAuth.** Rejected: duplicates the verification logic and
  client id. Orange exposes verification only (`POST /auth/verify-google` returns an
  email and nothing else) — it does **not** become an identity provider with users,
  sessions or refresh tokens.
- **`postMessage` token handshake** instead of the URL fragment. Deferred: the fragment
  needs zero JS in the embedding app. A `postMessage` channel remains the natural future
  seam for custom plugin rendering from the parent; nothing here forecloses it.
- **Product-layer worker + schedule for each hypothesis** (fresh session per tick, state
  in memory). Rejected *for Wolf* — the session view would show one day's run rather than
  the whole research history. Still the right choice for other apps; see the two-strategies
  table above.

### Every session is composed (the toolless-session gap)

**Principle: there is one kind of session.** Whether a session is started by a user in the
chat UI, by a schedule, or by another agent, it must launch with the project's tools, the
project's system prompt, and the project's base image. A session without tools should not
be creatable.

**The engine already does most of this, via a different mechanism than dispatch.** An earlier
draft of this plan claimed HTTP-created sessions got no project tools, no project prompt and no
project image. That was wrong, and the correction matters because it shrinks the work by an
order of magnitude. `agentd` wires a `SessionContextProvider`
(`go/cmd/agentd/main.go:223,280`, impl `go/cmd/agentd/sessioncontext.go:89-185`) that reads
`project_settings` and `workers`, and the Runner consumes it on **every** create:

| Concern | HTTP-created chat session today | Where |
| --- | --- | --- |
| Project ∪ worker MCP tools | ✅ merged in | `go/runner.go:366,484-495` |
| Project system prompt | ✅ resolved **per turn** from live config | `go/runner.go:1938-1958`, `sessioncontext.go:135,189-197` |
| Project base image | ✅ resolved through the catalogue | `go/runner.go:2526-2534,2547-2575` |
| **Core MCP server** (memory, worker, image, config tools) | ❌ **missing** | `coreMCPServers` (`go/cmd/agentd/mcpserver.go:543-552`) has exactly one call site: `main.go:354` → `dispatch.go:305` |

```
schedule / event ─▶ dispatch ─▶ ComposeJob ─▶ CreateSession   ✅ core tools too

user in chat UI  ─▶ SessionContextProvider ─▶ CreateSession   ✅ project prompt/tools/image
                                                              ❌ core tools only
```

So the gap is **one thing**: the core MCP server never reaches a session that wasn't dispatched.
That is why the long-lived `hypothesis-a` session cannot call `memory_current` — and it is the
whole of T15. Routing the create path through `ComposeJob` was considered and **rejected**:
`ComposeJob` requires a named worker and refuses to compose vanilla sessions by design
(`go/compose.go:270-271,345-350`), its core preamble hard-codes *"You are the worker %q"* plus
autonomous-agent instructions that are actively wrong for interactive chat
(`compose.go:524-553`), persisting a worker name would make every console chat emit
`worker.finished` onto the event spine carrying its transcript verbatim
(`runner.go:1635-1637,1827`), and its `composeImage` returns the project base image **unresolved**
(`compose.go:386-388`), re-breaking the I4 catalogue fix documented at `sessioncontext.go:139-142`.

**Correcting the "frozen session" claim.** An earlier draft said a resumed session runs its
original prompt forever. That is true only for **dispatched job sessions**, which persist a
`ComposedPrompt`. A chat session leaves that column empty, and `turnSystemPrompt` therefore
re-resolves the prompt from the provider **on every turn**, deliberately — *"a chat session's
prompt legitimately follows the live project/worker configuration"* (`go/runner.go:1914-1941`,
pinned by `TestPlainSessionStillUsesTheProvider`, `go/runner_systemprompt_test.go:254`). So a
long-lived hypothesis session **does** pick up project and worker prompt edits. What genuinely
does not update is its **MCP tool set** (fixed when the container is provisioned) and its
**briefing** (built only at composition, so chat sessions never get one). Reading current state
at message time via `memory_current` remains the right pattern — not because the prompt is
frozen, but because briefings don't apply to chat sessions at all.

### Session names and session-mode schedules

The table is **`agent_sessions`** (`go/agentdb/types.go:159`), not `sessions`:

```
agent_sessions: + name TEXT NULL   UNIQUE (customer, name)   kebab-case, immutable
schedules:      + target_session TEXT NULL  (mutually exclusive with worker)

scheduler Tick (10s poll, per-minute eval — unchanged)
   │
   ├─ worker schedule  → ClaimFiring → project_event → dispatch → NEW session   (unchanged)
   └─ session schedule → ClaimFiring → resolve name→id → Runner.SendMessage
                                          │
                                          ├ session archived? SendMessage's own ensureRunning
                                          │  restores container, workspace and history
                                          ├ turn already in flight? (Runner.Status →
                                          │  ActiveQueryID non-empty) skip, record, do NOT queue
                                          └ name not found / session deleted? disable the
                                             schedule (mirrors the worker-not-found rule)
```

### Embedding and artifacts

```
Wolf backend                     Orange
  POST /agent/embed-token  ──▶   validate X-API-Key → project
   {session:"hyp-a", ttl}   ◀──  {token, expires_at}   JWT{sid, customer,
                                                        scope:"session:<id>"}
Wolf HTML
  <iframe src="…/embed/session/hyp-a#token=eyJ…">
       fragment never leaves the browser — no server logs, no Referer
                    │
                    ▼
  Orange embed page: reads location.hash, clears it, holds the token in memory,
  renders <AgentChatProvider><AgentChat/></AgentChatProvider> — no sidebar,
  no login gate, no project picker. Served with frame-ancestors from the
  project's allowed_origins.

Artifacts (proxy model)
  Wolf backend ──X-API-Key──▶ GET /agent/sessions/by-name/{name}/artifacts
                              GET /agent/sessions/by-name/{name}/artifacts/file?path=summary.md
                              GET /agent/artifacts/{id}/download
                          ◀── bytes; Wolf re-serves to its own users
```

Artifacts dedup on `(session_id, file_path)` (`go/agentdb/artifacts_durable.go:64-119`),
so `summary.md` is a **stable logical handle** that upserts rather than accumulating —
"the current summary for `hypothesis-a`" is genuinely addressable once sessions have names.

## File Structure

### Create

| Path | Purpose |
| --- | --- |
| `go/cmd/agentd/apikey.go` | Project-key resolution from the parsed map + env; constant-time lookup by key value |
| `go/cmd/agentd/apikey_test.go` | Table tests for resolution, weak-key boot error, missing env var |
| `go/cmd/agentd/embedtoken.go` | `POST /agent/embed-token` handler: API key → session-scoped short-lived JWT |
| `go/cmd/agentd/embedtoken_test.go` | Minting, TTL clamping, unknown-session 404, scope claim shape |
| `go/cmd/agentd/mcp_sessions.go` | The `session_list` core MCP tool (provenance only, no transcripts) |
| `go/cmd/agentd/mcp_sessions_test.go` | Scoping, limit clamping, no-worker case |
| `go/httpapi/artifacts_download.go` | `GET /agent/artifacts/{id}/download` + by-name artifact routes |
| `go/httpapi/artifacts_download_test.go` | Byte serving, `lost`/`extraction_failed`/dir cases, tenancy 404s |
| `go/httpapi/sessions_byname.go` | Name → session resolution helper + `GET /agent/sessions/by-name/{name}` |
| `examples/web/src/EmbedSession.tsx` | The minimal embed page: hash-token read, chat only, no shell |
| `examples/web/embed.html` + `examples/web/src/embed-main.tsx` | Second Vite entry point for `/embed/*` (the HTML sits at project root, beside the existing `index.html`) |
| `e2e/features/embedding.stack.spec.ts` | Stack e2e: named session, session schedule fires twice, embed page loads |
| `docs/19-embedding.md` | Integration guide for embedding apps (the doc Wolf's author reads) |

**Migrations are Go values, not `.sql` files.** There is no `go/agentdb/migrations/` directory
anywhere in the repo. `go/agentdb/migrations.go:15-18` defines
`type migration struct { Name string; SQL string }`, and migrations are literals appended to the
`agentMigrations` slice; the last is `034_workers_frozen` (`migrations.go:715-720`). Append
`{Name: "035_session_names_and_session_schedules", SQL: ...}` with idempotent DDL
(`ADD COLUMN IF NOT EXISTS` / `CREATE UNIQUE INDEX IF NOT EXISTS`), matching the surrounding
entries. Do not create a directory.

### Modify

| Path | Change |
| --- | --- |
| `go/cmd/agentd/googleauth.go:24-80` | Project map gains the object form (`users` + `projects`), keeping flat-form parsing |
| `go/cmd/agentd/googleauth_test.go` | Cases for both forms, malformed object form, unknown `api_key_env` |
| `go/cmd/agentd/googleauth.go` (new handler) | `POST /auth/verify-google` — verify credential, return `{email, email_verified}` only |
| `go/cmd/agentd/auth.go:35-70` | API-key branch; `principal` gains `EmbedSession`; scope enforcement helper |
| `go/cmd/agentd/main.go:395-470` | Wire key resolution, embed-token route, verify-google route, CSP header on embed |
| `go/extension/devclaims/devclaims.go:37-48` | Optional `scope` claim, passed **per token** at issue time (see T3) |
| `go/httpapi/httpapi.go:18-21` | `Identity` gains `SessionScope`; enforcement beside `ownsSession` |
| `go/agentdb/types.go` (Session) | `Name string` field |
| `go/agentdb/sessions.go` | Name validation, uniqueness error → typed `ErrSessionNameTaken`, `GetSessionByName(customer, name)` |
| `go/httpapi/session.go:44-110` | Accept `name` on create; 409 on conflict; never accept a rename |
| `go/httpapi/stream.go`, `lifecycle.go`, `history.go` | Apply `ownsSession` to the seven unenforced routes |
| `go/httpapi/httpapi.go` | Register the new routes; extend `Config` if a name-lookup seam is needed |
| `go/agentdb/schedules.go:63-87` | `TargetSession` field; XOR validation against the worker target |
| `go/cmd/agentd/scheduler.go:62-81,203-301` | `scheduler` struct gains a Runner seam (it currently holds only `schedulerStore` + `jobDispatcher`); `fire` branches on target type |
| `go/httpapi/session.go` (T15) | Merge the core MCP server into HTTP-created sessions |
| `go/httpapi/memories.go` (T18) | Full-content memory read routes (by id, and newest-by-name) |
| `go/cmd/agentd/scheduler_test.go` | Session-branch cases: archived, busy, missing |
| `examples/web/vite.config.ts` | Multi-page build (`index.html` + `embed.html`) |
| `deploy/web.nginx.conf:9` | Serve `/embed/*` from `embed.html` instead of the SPA fallback |
| `web/src/components/ArtifactViewer.tsx:597-616`, `InlineArtifactPreview.tsx:190` | Remove the raw JWT from the webapp iframe URL path (see Out of Scope for the route itself) |
| `CLAUDE.md`, `docs/06-artifacts.md` | Record the download route now existing; link `docs/19-embedding.md` |

### Delete

None.

## Interfaces

### Project map (parsed shape)

```go
type projectConfig struct {
    APIKeyEnv      string   `json:"api_key_env"`
    AllowedOrigins []string `json:"allowed_origins"`
}

type projectMapFile struct {
    Users    map[string][]string      `json:"users"`
    Projects map[string]projectConfig `json:"projects"`
}
// Flat legacy form: map[string][]string, parsed into Users with empty Projects.

type projectKeys interface {
    // ProjectForKey returns the project a raw API key grants, using a
    // constant-time compare. ok=false for unknown or empty keys.
    ProjectForKey(raw string) (project string, ok bool)
    // AllowedOrigins returns the frame-ancestors list for a project.
    AllowedOrigins(project string) []string
}
```

### Auth

```
Header: X-API-Key: <raw>            → principal{Customer: <project>, Email: "api-key:<project>"}
Header: Authorization: Bearer <jwt> → principal{Customer, Email, EmbedSession?}
```

`principal.EmbedSession` is non-empty only for tokens carrying `scope: "session:<id>"`.
Any session-by-ID route called with such a principal must 404 unless the path session id
equals `EmbedSession`.

**Where scope is enforced (layering — decided, do not re-litigate).** The seven session-by-ID
routes and the download route are `httpapi` handlers, and the only identity crossing that seam
is `httpapi.Identity{UserEmail, Customer}` (`go/httpapi/httpapi.go:18-21`) supplied by the host's
`IdentityFunc`. `httpapi` is the library and `cmd/agentd` the host, so the import only goes one
way: a helper living in `cmd/agentd` cannot be called by an `httpapi` handler. Therefore
**`httpapi.Identity` gains a third field**, and enforcement happens inside `httpapi` next to the
existing `ownsSession` check:

```go
type Identity struct {
    UserEmail    string
    Customer     string
    SessionScope string // non-empty ⇒ this credential may touch only this session id
}
```

`agentd`'s `identityFromRequest` (`go/cmd/agentd/auth.go:67-70`) populates it from the principal.
Rejected alternative: having the `agentd` middleware inspect request paths before routing — it
would duplicate `httpapi`'s route table in the host and drift the moment a route is added.

### New HTTP routes

| Method + path | Auth | Returns |
| --- | --- | --- |
| `POST /agent/embed-token` | API key | `{token, expires_at}` — body `{session: "<name>", ttl_seconds?: int}` |
| `GET /agent/sessions/by-name/{name}` | API key or JWT | Session JSON (404 if absent **or** other project) |
| `GET /agent/sessions/by-name/{name}/artifacts` | API key or JWT | Artifact metadata list |
| `GET /agent/sessions/by-name/{name}/artifacts/file?path=…` | API key or JWT | Raw bytes + `Content-Type` from `MimeType` |
| `GET /agent/artifacts/{id}/download` | API key, JWT, or matching embed token | Raw bytes |
| `POST /auth/verify-google` | API key | `{email, email_verified}` — body `{credential: "<google id token>"}` |

**`POST /agent/session` also changes behaviour (T15):** it now runs `ComposeJob`, so every
session it creates carries core tools ∪ project `mcp_config` ∪ worker tools, the project system
prompt, and the project base image. The request shape is unchanged; a project with no settings
behaves exactly as before.

**New core MCP tool (T16):** `session_list(worker?, limit?)` → session metadata newest-first
(id, name, timestamps, status, permalink). No message content.

**New memory read routes (T18):** `GET /agent/memories/{id}` and
`GET /agent/memories/current?name=<n>` → full memory content, project-scoped, read-only.
The existing `GET /agent/memories` stays snippet-only.

`POST /agent/session` gains an optional `name` field: kebab-case (`^[a-z0-9]+(-[a-z0-9]+)*$`,
≤64 chars, matching `validProjectID` at `googleauth.go:35`), **409** if taken in that project,
**400** if malformed. No route ever renames a session.

### Embed page contract

```
GET /embed/session/{name}#token=<jwt>
  → HTML with `Content-Security-Policy: frame-ancestors <allowed_origins…>`
```

The page reads `location.hash`, immediately replaces the URL without it
(`history.replaceState`), keeps the token in memory only, and never writes it to
`localStorage`.

### Schedule row

```go
// Exactly one of Worker and TargetSession must be non-empty.
type Schedule struct {
    // … existing fields …
    TargetSession string // session NAME, not id
}
```

## Out of Scope

- **Any Agent Wolf code.** This plan changes only Agent Orange. Wolf's backend, UI,
  hypothesis model and user allowlist are a separate project.
- **A users table, sessions table, or refresh tokens in Orange.** `verify-google` returns
  an email; it does not create or track users.
- **API-key management UI, self-serve key creation, or key rotation tooling.** Ops edits
  the project map and env vars.
- **CORS middleware and signed download URLs.** Explicitly designed out (see Architecture).
- **The missing `/webapp/{session}/{token}/{path}` serving route.** It is referenced by
  `web/src/components/ArtifactViewer.tsx:597-605` and does not exist in this repo; webapp
  artifacts are already broken in the deployed stack. This plan only removes the raw JWT
  from that URL construction (T13) and logs the rest as a discovered issue.
- **Fixing the artifact iframes' `sandbox="allow-scripts allow-same-origin"`**
  (`ArtifactViewer.tsx:612`, `InlineArtifactPreview.tsx:313`) and the unauthenticated
  `/agent-proxy/` carrying the real Anthropic key (`go/cmd/agentd/main.go:439`,
  `go/cmd/agentd/modelproxy.go:39-58`). Both are pre-existing and become more dangerous
  under embedding; T14 records them so they are not silently inherited.
- **`postMessage` plugin rendering from the parent frame.** Future work; the fragment
  handoff does not block it.
- **Retro-naming existing sessions.** The column is nullable; existing rows stay null.
- **Any cross-container file-mutation tool** ("let worker A write a file into worker B's
  workspace"). Memory is already the shared bus between workers; adding a filesystem channel
  would duplicate it with worse provenance and no history. Explicitly rejected.
- **Wiring an archivist worker.** Summarising finished sessions into
  `kind=rolling-summary,worker=<subject>` memories is a prompt-and-subscription act in a live
  project, not engine work. The plumbing (`worker.finished` carrying the verbatim transcript,
  the default briefing selector) already exists.
- **Composing chat sessions through `ComposeJob`, or giving them a `Worker` / persisted
  `ComposedPrompt`.** Explicitly rejected in the Architecture section: `ComposeJob` refuses
  vanilla sessions by design, its preamble is written for autonomous workers, and a worker-named
  chat session would emit its transcript onto the event spine. The existing
  `SessionContextProvider` already delivers project prompt/tools/image to chat sessions, and its
  per-turn prompt resolution is better than a frozen one.
- **Injecting briefings into chat sessions.** Briefings are a composition-time concept; chat
  sessions read memory through tools instead.
- **Any transcript-reading MCP tool.** T16 exposes session *metadata* only.

## Tickets

### T1: Project map object form   [Status: done | Model: sonnet]
- **Scope:** Extend `parseProjectMap` to accept `{users, projects}` while still accepting
  the flat `{email: [projects]}` form. Validate `api_key_env` is a plausible env var name
  and `allowed_origins` entries are absolute `https://` (or `http://localhost…`) origins
  with no path. Unknown top-level keys are an error. **Detect the form by value shape, not key
  names** — an email key cannot be distinguished from `"users"`/`"projects"` structurally, so
  decide on whether the values are arrays (legacy) or objects (new form).
- **Files:** `go/cmd/agentd/googleauth.go` (modify), `go/cmd/agentd/googleauth_test.go` (modify).
- **Acceptance criteria:** Both forms parse; existing tests pass unmodified; malformed
  object form yields a descriptive error naming the offending project; a project listed in
  `projects` but referenced by no user is legal.
- **TDD:** yes
- **Validation:** `cd go && go test ./cmd/agentd/ -run 'ProjectMap' -count=1`
- **Depends on:** —
- [x] done
- Notes:
  - **`parseProjectMap` keeps its exact old signature** `([]byte) (projectMap, error)` — it is now
    a thin wrapper reading the users half of whichever form arrived, so `googleauth_test.go`'s
    existing cases pass unmodified. The new entry point is `parseProjectSettings` returning
    `*projectSettings{users, projects}`; `loadProjectSettings` / `loadProjectMap` mirror the pair.
  - **Form detection is by value shape**, as instructed: probe into
    `map[string]json.RawMessage`, look at the first non-whitespace byte of each value —
    `[` is legacy, `{` is the object form. A file mixing both is a descriptive error, not a guess.
    (An email key genuinely can be `users@…`, so key names prove nothing.)
  - Validation added: project ids must satisfy the existing `validProjectID` kebab-case regex
    (≤64 chars, matching `/auth/project-token`); `api_key_env` must be a plausible env var name
    (`^[A-Za-z_][A-Za-z0-9_]*$`); each `allowed_origins` entry must be `scheme://host[:port]` with
    **no path, query, fragment or userinfo**, scheme `https` except loopback, which may be `http`.
    Every message names the offending project. Trailing slashes are rejected rather than trimmed —
    an origin list feeding CSP `frame-ancestors` should not be silently rewritten.
  - `"*"` in `allowed_origins` fails the absolute-origin check, so a project cannot accidentally
    become framable by anyone.
  - Legal by design and asserted: a project in `projects` that no user is a member of. That is
    exactly the API-key-only integration shape. An object form with *neither* users nor projects
    is an error, mirroring the flat form's empty-map rule.
  - `main.go` still calls `loadProjectMap` — wiring the projects half is T2's.
  - Validation re-run: `go test ./cmd/agentd/ -run 'ProjectMap' -count=1` ✅ (also the whole
    package, plus `go build ./... && go vet ./...`).

### T2: API key resolution from env   [Status: done | Model: sonnet]
- **Scope:** New `apikey.go` implementing the `projectKeys` interface: at boot, read each
  project's `api_key_env` from `getenv`, build a value→project index, constant-time compare
  on lookup. Empty/missing env var → project has no key, logged once. Key shorter than 24
  chars → boot error. Two projects sharing a key value → boot error.
- **Files:** `go/cmd/agentd/apikey.go` (create), `go/cmd/agentd/apikey_test.go` (create),
  `go/cmd/agentd/main.go` (wire after the map loads).
- **Acceptance criteria:** `ProjectForKey` returns the right project; unknown/empty keys
  return `ok=false`; comparison uses `crypto/subtle`; boot errors fire as specified.
- **TDD:** yes
- **Validation:** `cd go && go test ./cmd/agentd/ -run 'APIKey' -count=1 && go build ./...`
- **Depends on:** T1
- [x] done
- Notes:
  - `go/cmd/agentd/apikey.go`: `projectKeyIndex` implements the `projectKeys` interface from the
    design verbatim, plus `hasKeys()` (T4 needs it to decide dev-open) and `projectConfigsOf`
    (nil-tolerant accessor so a map-less deployment is not a special case at each call site).
  - `ProjectForKey` scans **every** entry with no early exit and compares with
    `crypto/subtle.ConstantTimeCompare`, so neither the comparison count nor the duration depends
    on how much of a guessed key was correct.
  - **Key values are trimmed** before indexing. Not in the ticket, but a key mounted from a file
    or a compose `env_file` routinely arrives with a trailing newline, and chasing that through a
    constant-time compare is a bad afternoon. Trimming weakens nothing.
  - Boot errors as specified: below 24 chars (message quotes the actual length), and two projects
    resolving to the same value (message names both projects *and* both env vars). Exactly 24 is
    accepted — the rule is "at least". Projects are visited in sorted order so which
    misconfiguration is reported first is deterministic across restarts.
  - Empty/missing env var → keyless, one log line per project naming the variable, plus a summary
    line. Never fatal.
  - **`main.go` wiring required moving the project-map load out of the `if loginEnabled` block.**
    It is now loaded once via a new `loadProjectSettingsOptional` (returns `nil, nil` when neither
    env var is set) before the login section, because the `projects` half is needed by an
    embed-only deployment that has no console login at all. The previous behaviour is preserved
    exactly: with a login mode on and no map, agentd still fatals — just with a clearer message.
  - `apiKeys` is constructed at boot and currently parked behind `_ = apiKeys`; its consumers are
    T4 (middleware) and T10 (embed-token route), which will remove the blank assignment.
  - Validation re-run: `go test ./cmd/agentd/ -run 'APIKey' -count=1 && go build ./...` ✅ (also
    the whole `cmd/agentd` package and `go vet ./...`).

### T3: Scoped claims in devclaims   [Status: done | Model: sonnet]
- **Scope:** Add an optional `scope` claim carried **per token, not per issuer**. `Issue(ctx,
  scope ContextScope, sessionID)` (`go/extension/devclaims/devclaims.go:37-48`) already writes a
  `sid` claim, so an issuer-level scope would force a fresh `Issuer` per embed-token request and
  duplicate `sid`. Add an `IssueScoped(ctx, cs, sessionID, scope string)` (or an options variant)
  on the existing issuer instead. Parsing surfaces the claim; tokens without it behave exactly as
  today. Record in Notes whether `sid` alone would have sufficed — if the middleware can simply
  treat `sid` on an embed-issued token as the scope, prefer that and say so.
- **Files:** `go/extension/devclaims/devclaims.go` (modify), `devclaims_test.go` (modify).
- **Acceptance criteria:** Round-trip preserves scope; absent scope parses as empty string;
  no existing caller changes behaviour.
- **TDD:** yes
- **Validation:** `cd go && go test ./extension/devclaims/ -count=1`
- **Depends on:** —
- [x] done
- Notes:
  - **Asked question, answered: no, `sid` alone would NOT have sufficed — an explicit `scope`
    claim is right.** The reasoning, since the ticket asks for it on the record:
    `sid` already has a live, different meaning. It is read at `go/cmd/agentd/mcpserver.go:464` to
    identify *which session is calling* a core MCP tool, and `go/runner.go:1911` stamps it on the
    per-session token every container carries. The decisive fact is at `main.go:129-131`:
    `sessionSecret := envOr("AGENTKIT_JWT_SECRET", "dev-secret")` — so whenever
    `AGENTKIT_JWT_SECRET` is set, which is every real deployment, **the session-token secret and
    the API-token secret are the same value**. Making the middleware read `sid` as an
    authorization scope would therefore silently re-interpret every container's token. The
    direction of that change happens to be fail-closed, which is why it is not an outstanding
    vulnerability, but it would have been a semantic change to a credential family this ticket
    has no business touching. Two questions ("who minted this" / "what may it touch") get two
    claims.
  - Added: `IssueScoped(ctx, cs, sessionID, scope)` on the existing `*Issuer` — **per token, not
    per issuer**, as instructed. `Issue` is now a one-line delegation to it, so there is a single
    claim-building path. An empty scope writes **no claim at all**, so existing tokens are
    byte-identical to before; asserted by `TestIssueWithoutScopeCarriesNoScopeClaim`.
  - Also exported the scope *vocabulary*, so the `"session:<id>"` string format lives in one place
    rather than being re-spelled in `auth.go` (T4) and `embedtoken.go` (T10): `ScopeClaim`,
    `SessionScope(id)`, `ParseSessionScope(scope) (id, ok)`. `ParseSessionScope` rejects `""`,
    `"session:"` with nothing after it, and any other scope kind.
  - **T10 will need `NewWithTTL(secret, ttl).IssueScoped(...)`** for its per-request clamped TTL.
    TTL stayed an issuer property on purpose — unlike a scope, it genuinely describes an issuer,
    and constructing a two-field struct per request is free.
  - Validation re-run: `go test ./extension/devclaims/ -count=1` ✅; full `go test ./...` ✅.

### T4: Auth middleware accepts API keys and enforces embed scope   [Status: done | Model: opus]
- **Scope:** Rework `jwtAuthMiddleware` into a middleware that tries `X-API-Key` first, then
  `Authorization: Bearer`. Extend `principal` with `EmbedSession`. Add an exported helper that
  route handlers use to reject a scoped principal on a non-matching session id. Preserve the
  dev-open behaviour when `AGENTKIT_JWT_SECRET` is empty **and** no key is configured; if any
  key is configured, dev-open must be off (a configured key implies a real deployment).
  The helper the routes use is **not** in this package — add `SessionScope` to
  `httpapi.Identity` (`go/httpapi/httpapi.go:18-21`) and have `identityFromRequest`
  (`go/cmd/agentd/auth.go:67-70`) populate it; enforcement itself is T5's, inside `httpapi`.
- **Files:** `go/cmd/agentd/auth.go` (modify), `go/cmd/agentd/auth_test.go` (modify/create),
  `go/httpapi/httpapi.go` (add the field), `go/cmd/agentd/main.go` (modify).
- **Acceptance criteria:** Valid key authenticates with the right customer; invalid key 401s;
  JWT path unchanged for existing tokens; scoped token rejected on a foreign session id;
  dev-open disabled whenever a key exists.
- **TDD:** yes
- **Validation:** `cd go && go test ./cmd/agentd/ -run 'Auth' -count=1 && go vet ./...`
- **Depends on:** T2, T3
- [x] done
- Notes:
  - `jwtAuthMiddleware` → `apiAuthMiddleware(secret, keys, next)`. Order: `X-API-Key` first, then
    dev-open, then `Authorization: Bearer`. **A bad key never falls through** to the bearer path or
    to dev-open — a caller that sent a key meant to use it, and a silent downgrade on a typo is
    exactly how a project ends up authenticated as `demo`. Pinned by
    `TestAuthMiddleware_BadAPIKeyDoesNotFallThroughToJWT`.
  - Dev-open is now `len(secret) == 0 && !keys.hasKeys()`, computed once at wiring time. With a key
    configured and no JWT secret, unauthenticated requests 401 while the key itself still works.
  - `principal` gained `embedSession`, read from the `scope` claim through
    `devclaims.ParseSessionScope`. A scope of an unrecognised kind (`project:…`) yields **no**
    session scope rather than being treated as unrestricted-and-fine; there is a test for it.
    `identityFromRequest` maps it to `httpapi.Identity.SessionScope`, where T5 already enforces it.
  - **Deviation, small and deliberate:** `hasKeys() bool` was added to the `projectKeys` interface
    (the design's Interfaces section lists two methods). The middleware needs it, and the
    alternatives — passing the concrete `*projectKeyIndex`, or threading a separate `devOpen bool`
    from `main.go` — either orphan the interface or create two sources of truth for one decision.
  - **No helper was added in `cmd/agentd`**, per the ticket's own correction: the routes are
    `httpapi` handlers and the import only goes one way. The API-key principal's email is
    `api-key:<project>` (`apiKeyEmail`), synthetic on purpose so an audit trail never records a
    key's action as the empty-actor spelling a human edit uses.
  - Also fixed the now-stale reference to `jwtAuthMiddleware` in `googleauth.go`'s package comment.
  - Validation re-run: `go test ./cmd/agentd/ -run 'Auth' -count=1 && go vet ./...` ✅; full
    `go build ./... && go test ./...` ✅.

### T5: BLOCKING — enforce tenancy on session-by-ID routes   [Status: done | Model: opus]
- **Scope:** Apply the existing `ownsSession` check (`go/httpapi/lifecycle.go:195-217`,
  404-not-403 convention) to `Stream`, `Reconnect`, `SendMessage` (`go/httpapi/stream.go:36,65,96`),
  `Status`, `Cancel` (`lifecycle.go:40,70`), `Messages`, `QueryEvents` (`history.go:22,69`).
  Alongside it, enforce `Identity.SessionScope` (from T4): a scoped identity gets 404 on any
  session id other than its own. Update the tenancy-contract comment at
  `go/httpapi/httpapi.go:97-111` to state that the shipped host now enforces it.
  **Also handle the legacy branch:** `Messages` and `QueryEvents` fall through to
  `listQueryEventsLegacy` *before* calling `identify()` when `AgentDB` is nil
  (`go/httpapi/history.go:16-19,63-66`) — that path has neither auth nor tenancy today. Decide and
  implement one of: authenticate it identically, or refuse it when a project credential is in use.
  State which in Notes; do not leave the sqlite-fallback hole open silently.
- **Files:** `go/httpapi/stream.go`, `go/httpapi/lifecycle.go`, `go/httpapi/history.go`,
  `go/httpapi/httpapi.go` (modify); new negative tests modelled on
  `TestArtifactRoutesAreProjectScoped`.
- **Acceptance criteria:** A project-A identity gets 404 on every one of the seven routes for a
  project-B session; a session-scoped identity gets 404 on any other session; project-A's own
  sessions are unaffected; the empty-customer skip (`lifecycle.go:212`) is preserved so dev-open
  still works; the legacy branch behaves as decided above.
- **TDD:** yes
- **Validation:** `cd go && go test ./httpapi/ -count=1`
- **Depends on:** T4
- **Note:** No third-party API key may be issued before this lands.
- [x] done
- Notes:
  - **Taken before T4**, on instruction, because it is the blocking precondition. That meant
    pulling one line of T4's scope forward: `httpapi.Identity.SessionScope` is added here (it is
    in T5's own file list) since the enforcement is meaningless without the field. T4 now only has
    to *populate* it in `identityFromRequest`.
  - **Enforcement lives in one place:** `ownsSession` (`go/httpapi/lifecycle.go`) gained a
    `scopeAllows` pre-check, so every route already routed through it — Delete, Snapshot, Archive,
    Restore, Artifacts, CreateArtifact, Upload — inherits scope confinement for free. The scope
    check runs *before* the store lookup so a scoped credential cannot probe sibling session ids
    by response timing. `GetSession` keeps its own inline tenancy check (it already loads the row,
    and going through `ownsSession` would load it twice) and calls `scopeAllows` explicitly.
  - **Seven routes gained the check:** `Stream`, `Reconnect`, `SendMessage`, `Status`, `Cancel`,
    `Messages`, `QueryEvents`. On the three SSE routes it runs before any header is written —
    once the response commits to `200 text/event-stream` the only refusal left is an error frame,
    which a client renders as a broken session rather than a missing one.
  - **Legacy branch — decision: authenticate and authorize it identically.** `identify` +
    `ownsSession` were hoisted *above* the `AgentDB == nil` fork in both `Messages` and
    `QueryEvents`, so one gate covers both arms; `listQueryEventsLegacy` now takes the session id
    as a parameter and no longer identifies. Refusing the path outright was rejected: the sqlite
    fallback is the zero-config demo, and `ownsSession` already works there (it falls back to
    `cfg.Store`), so closing the hole cost nothing that refusing would have saved.
  - The empty-customer skip is preserved and pinned by
    `TestUnscopedIdentityStillReachesUnownedSessions` — dev-open mode still works.
  - Behaviour change worth knowing: `Status` on a session id with no row now answers **404**
    instead of consulting the Runner. That matches what `DeleteSession`/`Snapshot` already did.
  - Tenancy-contract comment at `go/httpapi/httpapi.go` rewritten: the library now enforces,
    rather than delegating to the host.
  - Tests: `go/httpapi/session_tenancy_test.go` (new) — cross-project 404 across all eight
    by-ID routes with a runner-reached tripwire, scope confinement both ways, scope on the
    already-guarded routes, the dev-open control, and the legacy path.
  - Validation re-run: `go test ./httpapi/ -count=1` ✅; full gate
    `go build ./... && go vet ./... && go test ./...` ✅.

### T6: Session names — schema and store   [Status: done | Model: sonnet]
- **Scope:** Migration adding `sessions.name TEXT NULL` with a unique index on
  `(customer, name)` (partial index excluding NULL). Add `Name` to the session type; validate
  kebab-case ≤64 chars on create; add `GetSessionByName(ctx, customer, name)`; map the unique
  violation to a typed `ErrSessionNameTaken`. **No update path** — the field is immutable.
- **Files:** `go/agentdb/migrations.go` (add `035_…`), `go/agentdb/types.go`,
  `go/agentdb/sessions.go`, `go/agentdb/sessions_test.go`.
- **Acceptance criteria:** Duplicate name in the same project errors as `ErrSessionNameTaken`;
  the same name in two projects is fine; multiple NULL names coexist; no store method can rename.
- **TDD:** yes
- **Validation:** `cd go && go test ./agentdb/ -count=1` (set `AGENTKIT_TEST_POSTGRES_URL` to a
  throwaway database to exercise the live-PG twins — a green run without it does not prove the index)
- **Depends on:** —
- [x] done
- Notes:
  - Migration **`035_session_names`** (not the `035_session_names_and_session_schedules` the File
    Structure section named — T9 took its own `036`). Column `agent_sessions.name TEXT NULL` plus a
    partial unique index on `(customer, name)`.
  - **Deviation, load-bearing: the index predicate is `WHERE name IS NOT NULL AND name <> ''`, not
    the ticket's "excluding NULL".** Both spellings of "unnamed" live in that one column — every
    pre-035 row holds NULL, and GORM writes `''` for a zero-value Go string on every unnamed create.
    A NULL-only predicate would have made a project's *second console chat* a unique violation.
    Recorded in the migration comment.
  - Immutability is a GORM field permission (`<-:create`), not a guard in `UpdateSession` —
    `UpdateSession` is a wholesale `Save` with no single choke point, so the permission means no
    UPDATE this store can emit carries the column at all. Enforced at the ORM layer, not the
    database: a caller holding `Store.DB()` could still rename by raw SQL. Nothing in-repo does.
  - **Three sentinels, not one:** `ErrSessionNameTaken` (409), `ErrSessionNameInvalid` (400),
    `ErrSessionNotFound` (404). T7 has to answer three different statuses and cannot from one.
  - No pre-check `SELECT` before insert — uniqueness is the index's job, since two racing creates
    would both pass a pre-check. `isSessionNameCollision` mirrors `isConfigSeqCollision` and
    deliberately excludes duplicate-PK failures, so re-using a session id is never mis-reported as
    a taken name.
  - `GetSessionByName` answers `ErrSessionNotFound` identically for absent, malformed **and other
    project**. The malformed case is load-bearing, not tidiness: an unvalidated `""` would make
    `WHERE customer=? AND name=?` match every unnamed row and hand out an arbitrary console chat.
  - Validation re-run by the orchestrator with the throwaway Postgres: `go test ./agentdb/` ✅, and
    `-run TestLivePG -v` shows **30 PASS / 0 SKIP with the variable set, 30 SKIP without** — the
    index is genuinely exercised. Full gate ✅.

### T7: Session name on the create route + by-name lookup   [Status: done | Model: sonnet]
- **Scope:** Accept optional `name` on `POST /agent/session` (`go/httpapi/session.go:44-110`),
  409 on `ErrSessionNameTaken`, 400 on malformed. Add `GET /agent/sessions/by-name/{name}` plus
  an internal resolver returning 404 for both "absent" and "other project".
- **Files:** `go/httpapi/session.go`, `go/httpapi/sessions_byname.go` (create),
  `go/httpapi/httpapi.go`, plus tests.
- **Acceptance criteria:** Create-with-name works; duplicate is 409; cross-project lookup is 404
  and indistinguishable from absent; unnamed creates behave exactly as today.
- **TDD:** yes
- **Validation:** `cd go && go test ./httpapi/ -count=1`
- **Depends on:** T6
- [x] done
- Notes:
  - **Answer to the question the ticket asked (what the by-name route does with a session-scoped
    identity): it works, but only for the name that resolves to its own session — every other name
    is a 404 identical to "absent".** T12's embed page must resolve `hypothesis-a` → id using
    nothing but the embed token before it can mount the chat, so refusing scoped identities outright
    would have made the route useless to its main caller.
  - **Named creates INSERT through a new `SessionNameStore` seam rather than upserting through
    `Config.Store`.** Forced by T6's schema: `Session.Name` is `<-:create`, so no UPDATE carries the
    column, and only an INSERT trips the unique index that is the sole authority on whether a name
    is taken. Unnamed creates keep their exact old upsert path, untouched. `Config.SessionNames`
    auto-fills from `AgentDB` exactly like Workers/Schedules/Memories, so `agentd` needed no wiring.
  - Every name rejection happens **before `MarkCreating`**, which installs a Runner create guard and
    a progress op and has no exported undo — so a refused name leaves no guard, no progress op, no
    row and no container. A fast-path duplicate check makes the ordinary 409 cost nothing; the
    insert's `ErrSessionNameTaken` remains the authority for the race.
  - **501** when no name store is wired (the sqlite fallback has no `name` column, so a named create
    there would hand back a name resolving to nothing) and **403** when the credential carries no
    project — an unscoped credential has nothing to resolve against, which is an unanswerable
    question rather than a hidden session. Consequence worth knowing: **named creates and by-name
    lookup are unavailable in dev-open mode**, so T17's e2e must run with a real project credential.
  - The by-name response deliberately **omits `composed_prompt`** — this is the one session route an
    embed token is meant to reach, and a composed prompt is the project's system prompt plus its
    memory briefings.
  - Validation re-run: `go test ./httpapi/ -count=1` ✅; full gate ✅.

### T8: Artifact download route   [Status: done | Model: sonnet]
- **Priority note:** this fixes a route that is missing outright and that three console
  components already call. An earlier draft demoted it on the grounds that Wolf would render
  state from `GET /agent/memories` instead — that was wrong (memories come back as 500-byte
  snippets; see T18), so this ticket stays on the critical path alongside T18. Which of the
  two Wolf actually uses for the summary is a product choice; both need to work.
- **Scope:** Implement `GET /agent/artifacts/{id}/download` over `ArtifactStore.Load`, honouring
  the documented nil-reader cases (`docs/06-artifacts.md`, "Download"): `lost` → 410,
  `extraction_failed` → 409, `IsDir` → 409 with a pointer to the list route, still-`live` with no
  blob → 202. Set `Content-Type` from `MimeType` and `Content-Disposition` from `FileName`.
  Auth: API key, JWT, or an embed token whose scope matches the artifact's session. Also add
  `GET /agent/sessions/by-name/{name}/artifacts` and `…/artifacts/file?path=…` (the latter via
  `GetArtifactByPath(ctx, sessionID, filePath)`, `go/agentdb/artifacts_durable.go:35`). That
  method takes a session **id** and has no customer parameter, so tenancy rides entirely on T7's
  name resolution — resolve first, never accept a session id from the query string. Normalize the
  `path` parameter: stored `FilePath` values may carry a leading slash, so `summary.md` and
  `/summary.md` must resolve to the same artifact.
- **Files:** `go/httpapi/artifacts_download.go` (create), `artifacts_download_test.go` (create),
  `go/httpapi/httpapi.go` (modify).
- **Acceptance criteria:** Bytes served for `extracted`; each nil-reader case maps to its status;
  cross-project artifact is 404; the status mapping is asserted per-case; leading-slash and bare
  path forms both resolve.
- **TDD:** yes
- **Validation:** `cd go && go test ./httpapi/ -count=1`
- **Depends on:** T5, T7
- [x] done
- Notes:
  - All three routes funnel through one `serveArtifactBytes` — the single place artifact bytes leave
    the package. Status mapping order is lost → extraction_failed → IsDir → live → default.
    `IsDir` is checked *before* the live case because retrying a directory will never produce a byte
    stream, so 202 "come back later" would be a lie.
  - **`Load` errors map to 404, not 500.** Both shipped backends report an unknown id as a wrapped
    error with no sentinel to distinguish it from a backend fault, 404 is the required answer for a
    foreign id anyway, and surfacing the store error would be an existence oracle.
  - The 404 body is `ownsSession`'s string **byte for byte**. An earlier version said "artifact not
    found" for a `Load` miss, and `TestDownloadArtifactHonoursTheSessionScope` caught that this let
    an embed token distinguish "exists but belongs to a sibling session" from "no such id". The test
    now asserts the two bodies are identical.
  - `Content-Disposition: attachment` + `X-Content-Type-Options: nosniff` + `application/octet-stream`
    when `MimeType` is empty. Security, not UX: an agent can write an artifact containing HTML, and
    rendering it inline would be scripting on the console's own origin, within reach of the JWT in
    `localStorage`. Every console call site fetches into a blob URL, so nothing is lost.
  - No `Content-Length`: `FileSize` is metadata written by a different call than the bytes, and a
    stale value would truncate the response.
  - Tenancy on the by-name routes rides entirely on T7's resolver — the id handed to
    `GetArtifactByPath` is only ever the one the name resolved to, never anything from the request,
    and a test pins that. Leading-slash normalization asks for both spellings (two exact lookups at
    most), covered by a four-case table.
  - **Deviation:** the ticket says `Content-Disposition` from `FileName`, but the portable
    `artifacts.Artifact` has no `FileName` field — only the `agentdb` row does, and `fromRow` drops
    it. The route uses `path.Base(art.FilePath)`, which is the same result for every artifact whose
    FileName was derived from its path.
  - Validation re-run: `go test ./httpapi/ -count=1` ✅; full gate ✅.

### T9: Session-mode schedules   [Status: done | Model: opus]
- **Scope:** Add `schedules.target_session` (migration in T6's file or a follow-on), XOR-validated
  against the worker target. Branch `scheduler.fire` (`go/cmd/agentd/scheduler.go:203-301`): for a
  session schedule, `ClaimFiring` as today, resolve the name (missing → disable the schedule,
  mirroring `ErrWorkerNotFound` at `scheduler.go:216`), then send the schedule's `Input` to that
  session.
- **The seam:** `Runner.SendMessage(ctx, ref SessionRef, msg SendMessageRequest, w Writer) error`
  (`go/agentkit.go:206`, impl `go/runner.go:857`). Its first act is `ensureRunning`
  (`runner.go:858,1120-1138`) → `restoreToWorker` (`runner.go:1341+`), which materializes the
  snapshot **and** rehydrates conversation history — so "restore an archived session" needs no
  extra code. The existing headless-call precedent is `dispatch.go:608` with the discarding
  `leaseWriter` (`dispatch.go:659`); reuse that writer pattern.
- **Threading it in:** the `scheduler` struct currently holds only `schedulerStore` +
  `jobDispatcher` (`scheduler.go:62-81`) and `main.go` constructs it without a Runner. Add a narrow
  interface (not the concrete `*Runner`) covering `SendMessage` and `Status`, and wire it in
  `main.go` — do not widen the struct to the whole Runner.
- **Busy detection:** `SendMessage` does not refuse a busy session on its own. Use
  `Runner.Status(...)` and skip when `ActiveQueryID` is non-empty; record the skip; never queue.
- **Files:** `go/agentdb/schedules.go`, `go/agentdb/migrations.go`, `go/cmd/agentd/scheduler.go`,
  `go/cmd/agentd/main.go` (wire the seam), `go/cmd/agentd/scheduler_test.go`,
  `go/httpapi/schedules.go` (accept the field).
- **Note — document what resuming does and does not refresh.** A **chat** session (empty
  `composed_prompt`) re-resolves its system prompt from the live provider on every turn
  (`go/runner.go:1914-1941`), so it *does* pick up project and worker prompt edits. A
  **dispatched job** session has a frozen `ComposedPrompt` (`go/agentdb/types.go:112-121`).
  Neither refreshes its **MCP tool set** (fixed when the container is provisioned) or gains a
  **briefing** (built only at composition, so chat sessions never have one). Say this plainly
  in `schedule_create`'s tool description and in `docs/19-embedding.md` — the distinction is
  easy to get backwards. The way updated state reaches a long-lived session is the memory
  tools at message time (T15).
- **Note on the worker column:** `Schedule.Worker` is `not null` (`go/agentdb/schedules.go:70`) and
  `NewSchedule(project, worker, cron, input)` assumes worker mode. Session schedules store
  `worker = ''` (legal under NOT NULL), so `validateSchedule` and `NewSchedule` must be explicitly
  relaxed to allow it — and must still reject a row with both or neither target set.
- **Acceptance criteria:** A session schedule fires exactly once per scheduled minute across two
  processes (claim idempotency); an archived session is restored and receives the message; a busy
  session's firing is skipped, not queued; a deleted session disables the schedule; worker
  schedules are byte-for-byte unaffected.
- **TDD:** yes
- **Validation:** `cd go && go test ./cmd/agentd/ -run 'Schedul' -count=1 && go test ./agentdb/ -count=1`
- **Depends on:** T6
- [x] done
- Notes:
  - Migration **`036`**, appended — T6's `035` was left untouched, since an already-applied
    migration never re-runs. `schedules.target_session` holds the session **name**, XOR-validated
    against `worker` in `validateSchedule`. `NewSessionSchedule(project, sessionName, cron, input)`
    was added rather than a fifth argument to `NewSchedule`, leaving ~30 worker-mode call sites
    untouched.
  - **The name is resolved BEFORE `ClaimFiring`, which inverts the ticket's prose.** The worker
    branch already resolves first for a stated reason (a schedule disabled for a missing target must
    be able to fire that same minute once the target returns and it is re-enabled), and resolving
    first also means a transient DB error retries next minute instead of spending the occurrence.
    Only `ErrSessionNotFound` disables; any other read error is logged and retried, mirroring RD1.
  - The Runner reaches the scheduler through a two-method `sessionMessenger` (SendMessage + Status)
    wired in `main.go` — the struct was **not** widened to the whole `*Runner`, as instructed.
  - Busy detection is `Runner.Status` → skip when `ActiveQueryID` is non-empty; never queued. A
    `Status` error is treated as "cannot tell" → skip, rather than guessing toward a second
    concurrent turn. `Status` on a destroyed container returns `RuntimeState=destroyed` with a nil
    error, which is what makes the check safe for archived sessions — the restore cost is paid
    entirely inside `SendMessage`.
  - The send is spawned off the tick goroutine (`context.WithoutCancel`) or a scheduled turn would
    stall every other schedule for as long as the model takes. `schedulerConfig` gained an
    injectable `Spawn` so tests stay synchronous — the same seam `dispatch.go` uses.
  - A failed `SendMessage` counts toward the existing five-consecutive-failure disable streak,
    because a `SendMessage` error means the turn could not be *delivered*; what the model then does
    comes back as events, not as an error.
  - **`TargetSession` carries a gorm `default:''` tag** — the one exception to this struct's stated
    no-defaults rule, because without it AutoMigrate builds a sqlite test schema *stricter* than
    production. Safe here only because the zero value and the default are the same string.
  - Beyond the file list: `schedule_create`'s MCP tool gained `target_session` (documenting resume
    semantics in a tool that could not create a session schedule would have been incoherent), and
    the HTTP PUT accepts it. `schedule_update`'s MCP tool deliberately does **not** — a mode switch
    through a partial-field update is the ambiguous case.
  - **Carry-forward for T14:** the ticket asks for the resume-semantics text in *both*
    `schedule_create`'s description and `docs/19-embedding.md`. Only the first exists; the doc does
    not yet.
  - Validation re-run: `go test ./cmd/agentd/ -run 'Schedul'` ✅, `go test ./agentdb/` ✅ (live PG).

### T10: Embed-token endpoint   [Status: done | Model: sonnet]
- **Scope:** `POST /agent/embed-token`, API-key auth only (never JWT — a browser must not mint
  its own embed tokens). Body `{session: "<name>", ttl_seconds?}`; TTL default 900, clamped to
  [60, 3600]. Resolve the name within the key's project (404 if absent/foreign), mint via
  `devclaims.NewScoped` with `scope: "session:<id>"`, return `{token, expires_at}`.
- **Files:** `go/cmd/agentd/embedtoken.go` (create), `embedtoken_test.go` (create),
  `go/cmd/agentd/main.go` (register).
- **Acceptance criteria:** Key auth required; TTL clamped; token carries the scope and the right
  customer; unknown/foreign session is 404; JWT auth on this route is 403.
- **TDD:** yes
- **Validation:** `cd go && go test ./cmd/agentd/ -run 'EmbedToken' -count=1`
- **Depends on:** T4, T7
- [x] done
- Notes:
  - **⚠️ The embed token's `sid` claim is deliberately left EMPTY, and this is a security decision,
    pinned by test.** Passing `sess.ID` as devclaims' `sessionID` argument is the obvious reading and
    would have been dangerous: agentd signs per-session *container* tokens with the same secret in
    every real deployment (the T3 discovery), and the core MCP server authenticates its caller by
    exactly that claim. An embed token carrying a `sid` would have been structurally usable as that
    session's container token against `/mcp` — i.e. the project's memory tools. The scope claim, not
    `sid`, is what confines it.
  - Auth follows T11 exactly: mounted on `apiMux` so the middleware runs first, with
    `authenticatedByAPIKey` inside the handler. Console JWT → 403, embed token → 403 (a token cannot
    mint another), no credential → 401, bad key → 401. A refused caller never reaches the store, so
    the route is not a session-name oracle.
  - Minted with `main.go`'s `jwtSecret` — the value `apiAuthMiddleware` verifies with.
    `TestEmbedTokenAuthenticatesAsItsSession` closes the loop: mint through the real middleware, then
    present the token back to it and assert the principal carries `customer=wolf` and the resolved
    `embedSession`.
  - TTL clamped, never rejected: absent/0 → 900, <60 → 60, >3600 → 3600. Explicit 0 is treated as
    absent, since JSON cannot distinguish them without a pointer and "I did not choose" is a
    different statement from "I chose 0".
  - `expires_at` is read back off the token just signed rather than recomputed — devclaims stamps
    `exp` from its own `time.Now()`, and a second `time.Now()` here lands a second later whenever the
    call straddles a boundary. A body promising an expiry the token does not have is the kind of
    off-by-one that surfaces as a rare unreproducible 401 inside somebody else's product.
  - Two unspecified deployments answer **501** rather than minting a token nothing will accept: no
    session-name store (names are Postgres-only) and no `AGENTKIT_JWT_SECRET`.
  - `devclaims.NewScoped` in the ticket's prose does not exist; used T3's
    `NewWithTTL(secret, ttl).IssueScoped(...)` per T3's Notes.
  - Verifier: PASS, 5/5 criteria evidenced.

### T11: verify-google endpoint   [Status: done | Model: sonnet]
- **Scope:** `POST /auth/verify-google`, API-key auth, body `{credential}`. Reuse the existing
  `googleVerifier` (`go/cmd/agentd/googleauth.go:113-161`) and return only
  `{email, email_verified}`. Mint nothing; grant nothing; do not consult the project map's user
  list. Registered only when `GOOGLE_CLIENT_ID` is set, alongside the existing `/auth/google`
  (`main.go:406-410`).
- **Files:** `go/cmd/agentd/googleauth.go` (modify), `googleauth_test.go` (modify),
  `go/cmd/agentd/main.go` (register).
- **Acceptance criteria:** Valid credential returns the email; invalid returns 401; the response
  contains no token and no project list; the route 404s when Google auth is not configured.
- **TDD:** yes
- **Validation:** `cd go && go test ./cmd/agentd/ -run 'Google' -count=1`
- **Depends on:** T2
- [x] done
- Notes:
  - **The wiring question the ticket left open, resolved:** the route is registered on **`apiMux`**,
    not on `root` beside `/auth/google`. The plan's own pointer (`main.go:406-410`) would have put it
    on the unauthenticated mux — `root.Handle("/", apiAuthMiddleware(...))` is registered last and
    only wraps `apiMux` — which would have shipped an **unauthenticated Google-token verification
    oracle**. The design's route table says "Auth: API key", and that wins over the prose pointer.
  - API-key-*only* is enforced in the handler (`authenticatedByAPIKey`), because the middleware
    accepts either credential and a browser JWT must not be able to use this route. It tests for the
    `X-API-Key` header's presence, which is sound only because `apiAuthMiddleware` tries the key
    first and 401s a bad one outright — noted so the coupling is visible.
  - `registerVerifyGoogle(mux, googleClientID)` guards its own registration and is called
    unconditionally, so the condition protecting production is the same one the test exercises
    rather than a duplicated `if`. 404 when `GOOGLE_CLIENT_ID` is unset.
  - Every verification failure — bad signature, wrong audience, unverified address — maps to one 401
    "invalid credential", matching `authGoogleHandler`. Distinguishing them would tell a caller
    holding a stolen token which part of it Google disliked.
  - Verifier: PASS, 10/10 criteria evidenced.

### T12: The embed page   [Status: done | Model: sonnet]
- **Scope:** New Vite entry (`embed.html` + `embed-main.tsx`) rendering `EmbedSession.tsx`: parse
  the session name from the path, read the token from `location.hash`, `history.replaceState` it
  away, hold it in memory, and mount `AgentChatProvider` + `AgentChat` with
  `getAuthToken` returning it. No sidebar, no login gate, no project picker. Resolve the name to
  an id via `GET /agent/sessions/by-name/{name}` before mounting. Show a plain "session
  unavailable" state on 401/404 rather than redirecting to login.
- **Files:** `examples/web/src/EmbedSession.tsx`, `examples/web/src/embed-main.tsx`,
  `examples/web/embed.html` (create — at project root, beside the existing `index.html`);
  `examples/web/vite.config.ts` (multi-page build);
  `deploy/web.nginx.conf` (route `/embed/*` to `embed.html`).
- **Acceptance criteria:** `yarn build` in `examples/web` emits both entries; the token never
  appears in `localStorage` or in the URL after first paint; the page renders a live session.
- **TDD:** no (wiring/UI — covered by the e2e in T15)
- **Validation:** `cd examples/web && yarn install --frozen-lockfile && yarn build`
  (use **yarn** here — `examples/web` tracks only `yarn.lock`)
- **Depends on:** T10
- [x] done
- Notes:
  - Second Vite entry at `examples/web/embed.html` (project root, beside `index.html`), rendering
    `EmbedSession.tsx`: reads `location.hash`, `history.replaceState`s it away, holds the token in a
    ref, and mounts `AgentChatProvider` + `AgentChat` with `getAuthToken` returning it. Never
    `localStorage`, never `sessionStorage`, never a query parameter.
  - Resolves the name → id through `GET /agent/sessions/by-name/{name}` before mounting, which works
    on the embed token alone per T7's Notes.
  - On 401/404 it renders a plain "session unavailable" card and does **not** redirect to login — the
    page lives inside someone else's iframe, where a login redirect is both useless and a phishing
    surface.
  - `yarn build` emits both entries; verified in `dist/` (`dist/index.html` + `dist/embed.html`,
    `assets/embed-*.js`) rather than asserted.
  - **The dependency on T10 is only real for the live check** — the page consumes a token it is
    handed and needs no mint route at build time.
  - Verifier: PASS, 10/10 criteria evidenced, including a throwaway Playwright harness against the
    built bundle proving the token leaves neither storage nor the URL.
  - Live-stack pass owed to T17 (the stack serves a **built** `examples/web` image, so
    `docker compose up -d --build web` is required first).

### T13: frame-ancestors header + remove tokens from iframe URLs   [Status: done | Model: sonnet]
- **Scope:** Serve the embed page with `Content-Security-Policy: frame-ancestors <origins>` derived
  from the project's `allowed_origins` (no origins configured → `'none'`). Since nginx serves the
  static page, the header must come from a small `agentd` route or an nginx map keyed by project —
  choose the simpler one and record which in Notes. Separately, stop embedding the raw JWT in the
  webapp iframe path at `web/src/components/ArtifactViewer.tsx:597-605` and
  `InlineArtifactPreview.tsx:190`.
- **Files:** `deploy/web.nginx.conf` and/or `go/cmd/agentd/main.go`;
  `web/src/components/ArtifactViewer.tsx`, `web/src/components/InlineArtifactPreview.tsx`.
- **Acceptance criteria:** The embed response carries the header with exactly the configured
  origins; no raw token appears in any URL constructed by `web/`.
- **TDD:** no (config/wiring)
- **Validation:** `cd web && npm ci && npm run typecheck && npm test`, plus a live header check
  against the running stack:
  `curl -sI http://localhost:8080/embed/session/hypothesis-a | grep -i content-security-policy`
  must show the configured origins (the npm tests do not exercise the nginx/`agentd` half).
- **Depends on:** T12
- [x] done — live check run by the orchestrator against the e2e stack, 2026-08-07:
  `curl -sI http://localhost:8080/embed/session/hypothesis-a` →
  `Content-Security-Policy: frame-ancestors http://localhost:5173 https://wolf.example.test`
  (exactly the two configured origins, in the boot-computed order); the console page `/` carries
  **no** CSP header; the embed URL serves the embed entry (`<title>Agent Orange — session</title>`,
  `src="/assets/embed-*.js"`) rather than the SPA; and agentd's boot log corroborates with
  `[agentd] embed framing: frame-ancestors http://localhost:5173 https://wolf.example.test`, proving
  the value came from the parsed project map rather than from nginx.
- Notes:
  - **The choice the ticket demanded: a small `agentd` route (`go/cmd/agentd/embedcsp.go`), not an
    nginx map.** The nginx-map option is not merely harder, it is *unimplementable*: the embed URL is
    `/embed/session/{name}` and the only credential is in the URL **fragment**, which a browser never
    sends — so nginx has nothing to key a per-project map on. nginx now does an `internal;`
    `auth_request` to agentd and copies the header onto the static page.
  - **The header carries the UNION of every configured project's origins, because the project is
    undeterminable server-side.** Three independent blocks: no project segment in the URL, the token
    is in the fragment, and resolving name → project would need the cross-tenant session-name lookup
    T6 deliberately does not provide. Recorded here because the ticket's Scope sentence ("the
    project's allowed_origins") cannot be honoured literally.
  - No origins configured → `frame-ancestors 'none'`, **emitted** rather than omitted, since an
    absent CSP means "frame me anywhere". Backed by an nginx `map` turning an empty upstream value
    into `'none'`, because **nginx silently skips `add_header` when its value is empty** — without
    the map a missing upstream header would fail *open*.
  - **T12's `try_files $uri /embed.html;` silently discarded the CSP header** and was changed to
    `try_files $uri /embed.html =404;`. A URI fallback is an internal redirect that re-matches
    `location /`, so the response is generated there and this block's `add_header` is dropped. The
    implementer reproduced exactly that against a real nginx before fixing it.
  - Header value is computed once at wiring time (deduped, sorted) — the project map is boot config
    with no reload path — and logged at boot as `[agentd] embed framing: …` so a misconfiguration is
    visible without a browser.
  - `GET /embed/csp` is on the unauthenticated `root` mux deliberately: its caller is nginx's
    internal subrequest, which carries no credential, and it discloses only the origin list that the
    header itself broadcasts to every framer anyway. It is `internal;` in nginx, so not reachable
    from outside.
  - web/: the raw JWT is gone from the webapp iframe path — the token branch was dropped entirely in
    favour of the existing no-token form, and the now-dead `authHeader` prop threading was removed.
    The `/webapp/{session}/{token}/{path}` **serving route was NOT built** (explicitly Out of Scope).
  - Verifier: PASS, 7/7 criteria evidenced, via a verbatim-config nginx rig with a stub upstream.

### T14: Documentation + hazard log   [Status: done | Model: sonnet]
> **Ordering:** despite its number, this ticket runs **after T15, T16 and T18** — it documents
> them. Take it second-to-last, before the e2e.
- **Scope:** Write `docs/19-embedding.md`: the three credentials, the project map object form,
  creating a named session, attaching a session schedule, minting an embed token, the iframe
  snippet, and the artifact proxy pattern — enough for Wolf's author to integrate without reading
  Go. Also document the **two-bot pattern** (long-lived conversation session + scheduled reviewer
  worker, with project memory as the system of record), that reading hypothesis state is
  `GET /agent/memories` rather than an artifact fetch, and — prominently — that a resumed session
  keeps its original composed prompt and tools. Update `CLAUDE.md`'s repo map/status and `docs/06-artifacts.md` (the download route now
  exists). Record in the doc's "known hazards" section: the `allow-scripts allow-same-origin`
  artifact iframes, the missing `/webapp/…` route, and the unauthenticated `/agent-proxy/`.
- **Files:** `docs/19-embedding.md` (create), `CLAUDE.md`, `docs/06-artifacts.md`,
  `docs/18-workers-memory-events.md` (note the composition fix and `session_list`) (modify).
- **Acceptance criteria:** Every route and env var in the doc matches the code; the hazards are
  named with `path:line`.
- **TDD:** no (docs)
- **Validation:** Manual read-through against the implemented routes.
- **Depends on:** T8, T10, T11, T12, T15, T16, T18
- [x] done
- Notes:
  - `docs/19-embedding.md` written; `CLAUDE.md`, `docs/06-artifacts.md` and
    `docs/18-workers-memory-events.md` updated. T9's carry-forward is discharged: the
    "what a resumed session does and does NOT refresh" table now exists in §4, matching
    `schedule_create`'s tool description.
  - **Fact-checked twice, and it earned it.** The checker opened ~70 distinct `path:line` citations.
    Round 1 found a `web/src/schedules.ts` off-by-one (320 vs 321, in two files) and a JSON sample
    printing three `omitempty` fields as empty strings. Round 2 found two more, both fixed by the
    orchestrator:
    - a hazard paragraph describing a failure mode **that does not exist** (see the retraction in the
      log — T8's implementer had mis-read `InlineArtifactPreview.tsx`, and I had copied it into the
      log unverified);
    - a `created_at` example printed in seconds for a **milliseconds** field, which would have made
      any "how stale is this state?" calculation wrong by 1000×.
  - Also corrected by the orchestrator after the second pass: `docs/19-embedding.md` §2 now warns that
    `docker-compose.yml` forwards only `AGENTKIT_PROJECT_MAP`/`_FILE`, so a per-project key variable
    in `.env` never reaches `agentd` on the standalone stack.
  - Hazards documented as **H1–H13**, each with `path:line`: embed-token project authority (the HIGH
    entry), the CSP path-prefix binding plus the two nginx traps, the artifact-iframe sandbox, the
    missing `/webapp/…` route, the missing `workspace/files` route, the unauthenticated
    `/agent-proxy/`, no core-tool backfill for pre-T15 sessions, the empty `GET /agent/sessions`
    without `?user_email=*`, Postgres-only/dev-open, the console's inability to author session
    schedules, a session named `session`, the shared JWT/session secret, and `current` being reserved
    in the memory id path space.
  - Two counts in this plan's own inventory were **low**, and the doc records the real numbers: the
    `workspace/files` route is referenced by **nine** web/ call sites (not five or seven), and the
    artifact-iframe sandbox lines are `ArtifactViewer.tsx:609` / `InlineArtifactPreview.tsx:315`
    (not 612/313).
  - The doc was written from the **code**, not from this plan — deliberately, since the plan's prose
    was wrong about `devclaims.NewScoped`, about where `verify-google` mounts, and about the CSP
    being per-project.

### T15: Give HTTP-created sessions the core MCP server   [Status: done | Model: sonnet]
- **Scope:** Deliberately narrow. Project prompt, project/worker MCP config and the project
  base image **already reach** sessions created through `POST /agent/session`, via the
  `SessionContextProvider` (see the Architecture table). The single gap is the core tool
  server: `coreMCPServers(selfURL)` (`go/cmd/agentd/mcpserver.go:543-552`) has one call site,
  `main.go:354` → `dispatch.go:305`. Add a `CoreMCP agentdb.MCPServers` field to
  `httpapi.Config`, merge it into the create request's MCP servers the way dispatch does
  (core last and non-overridable — mirror `go/compose.go:436-441`), and wire it in `main.go`
  from the same `coreMCPServers(selfURL)` value the dispatcher gets.
- **Do NOT** route the create path through `ComposeJob`, set `Session.Worker`, or persist
  `ComposedPrompt` on chat sessions. Each was considered and rejected for a specific reason —
  see the Architecture section. In particular, persisting a worker name would make every
  console chat emit `worker.finished` with its transcript onto the event spine.
- **Files:** `go/httpapi/httpapi.go` (new Config field), `go/httpapi/session.go` (merge),
  `go/cmd/agentd/main.go` (wire), plus tests.
- **Acceptance criteria:** A session created over HTTP in a Postgres-backed project launches
  with the core tools merged over project and worker tools, with core winning on name
  collision; project prompt, project tools and project base-image resolution are **unchanged**
  (assert this — it is the regression risk); the sqlite fallback, where the core MCP server is
  not mounted at all (`main.go:452,466-467`), is an explicit no-op rather than a 500;
  dispatched jobs are byte-for-byte unaffected.
- **TDD:** yes
- **Validation:** `cd go && go test ./httpapi/ ./cmd/agentd/ -count=1 && go test ./... -count=1`
- **Depends on:** —
- [x] done — stack check run by the orchestrator against the e2e stack, 2026-08-07, and it passed
  on all three legs the implementer specified:
  (1) a session created through `POST /agent/session` has
  `mcp_servers = {"core": {"url": "http://172.17.0.1:8099/mcp", "headers": {"Authorization": "${SESSION_TOKEN}"}}}`
  while `worker` and `composed_prompt` are both **empty** — the rejected alternatives stayed rejected;
  (2) **the tool answers.** With the mock model scripted to call
  `mcp__core__memory_create` then `mcp__core__memory_current`, the chat session made both calls and
  reached the following turn (only possible if a real tool result came back), and the row landed in
  Postgres with `project=apples-oranges`, `created_by_session=<that chat session>` and
  `created_by_worker` **empty** — exactly the path the implementer flagged as never previously
  exercised, since every prior caller of `newSessionTokenAuth` was a dispatched job;
  (3) the same memory read back over HTTP with the API key returned its full content.
  No billable model was used: agentd logged `ANTHROPIC_API_KEY unset → SCRIPTED mock model proxy`.
- Notes:
  - Deliberately tiny, as the corrected premise demands: `Config.CoreMCP` + a `mergeCoreMCPServers`
    helper writing core **last**, mirroring `go/compose.go:436-441`. Because the Runner folds the
    provider's project ∪ worker servers *under* the request (`runner.go:484-495`), putting core on
    the request is exactly what makes it non-overridable.
  - The merge always returns a **fresh map** when core is non-empty. `Config.CoreMCP` is built once
    at boot and shared by every create, so handing the map itself to the Runner would let one
    session's config mutation leak into every later session. Pinned by
    `TestCreateSessionDoesNotHandOutTheHostsCoreMCPMap`.
  - `main.go` sets `coreMCP` only when `agentDB != nil` — the same condition that mounts `/mcp`.
    Advertising an endpoint that answers 404 would be strictly worse than having no core tools. The
    sqlite fallback is an explicit no-op table case, never a 500.
  - The dispatcher now reads the **same `coreMCP` variable** instead of calling
    `coreMCPServers(selfURL)` a second time, so the chat path and the job path cannot drift onto
    different endpoints.
  - **The regression risk was the point, and it is covered twice.** `go/runner_coremcp_test.go` runs
    a real `runnerImpl` with a provider supplying a project prompt, a §13 catalogue pointer as the
    project base image, and project MCP servers including one that tries to shadow `agentkit-core` —
    asserting core wins the collision, the project's server survives, the turn's system prompt is
    still the provider's (per-turn resolution intact), the launch image is still the catalogue's
    resolution (the I4 fix intact), and the row gains **no worker and no composed_prompt**. The
    httpapi test asserts the entire captured `CreateSessionRequest` with `reflect.DeepEqual`, so a
    future attempt to stamp `Worker` shows up as a failing diff rather than passing silently.
  - **What the stack check must observe** (from the implementer, and what T17 will do): (1) a session
    created through `POST /agent/session` has an `agentkit-core` entry in its `agent_sessions.mcp_servers`
    column; (2) from inside that session the model actually **calls** `memory_current` and gets a real
    answer — the only thing that proves the three links unit tests cannot reach: the sandbox resolving
    `${SESSION_TOKEN}` for a *chat* session's container, `/mcp` accepting that token, and
    `newSessionTokenAuth` mapping its `sid` to a session row with **no worker** (every prior caller of
    that path was a dispatched job); (3) negative control: no `DATABASE_URL` → create still 200s with
    no mcp_servers.
  - Verifier: PASS, 6/6 criteria evidenced — and it independently flagged the same outstanding
    stack requirement.
- **Note:** Load-bearing for the Wolf model — the long-lived `hypothesis-a` session can only
  read memory if it has the core tools. Before ticking, verify against a running stack that
  `memory_current` is actually callable from a chat session; the unit tests can only prove the
  config was merged, not that the tool answers.

### T16: `session_list` core MCP tool   [Status: done | Model: sonnet]
- **Scope:** One new tool letting an agent enumerate its own worker's recent sessions —
  provenance only. Args: `worker?` (default: the caller's own worker), `limit?` (default 10,
  max 50). Returns metadata: session id, name, created/updated, status, and the
  `session_url` permalink built the way memory results already build theirs
  (`go/cmd/agentd/mcp_memory.go:80,105`). The query exists — `SessionQuery.Worker`
  (`go/agentdb/types.go:377-379`) and `Store.ListSessions` (`go/agentdb/sessions.go:194`);
  it is simply not exposed to agents. Follow the house rule: one `mcp_*.go` file plus one
  `srv.register(...)` line in `main.go` (`go/cmd/agentd/main.go:454-458`).
- **Deliberately excluded:** transcripts and message bodies. This is "when did I run and
  what came of it", not "re-read my own history" — memory is the answer to the latter, and
  a transcript-reading tool would burn context re-deriving what an archivist already
  summarised.
- **Files:** `go/cmd/agentd/mcp_sessions.go` (create), `mcp_sessions_test.go` (create),
  `go/cmd/agentd/main.go` (register).
- **Acceptance criteria:** Returns the caller's worker's sessions newest-first; another
  project's sessions are never returned; `limit` clamped; no message content in the payload;
  a caller whose session row has no worker gets an empty list rather than an error.
- **Caveat to state in the tool description:** `sess.Worker` is populated only by
  `persistComposition` (`go/runner.go:536-549`), i.e. only for dispatched jobs — a console
  session carries its worker in `Persona`, not the `worker` column (`go/httpapi/session.go:93`,
  `go/runner.go:466`). So this tool lists **job** sessions and will legitimately return an empty
  list when called from a chat session. Decide and record whether to fall back to `Persona`;
  if not, say so in the tool description so the model doesn't read empty as "I have no history".
- **Performance note:** `ListSessions` runs three unconditional `COUNT(*)` subqueries over
  `agent_artifacts` and `agent_messages` (`go/agentdb/sessions.go:196-203`). Fine at
  `limit ≤ 50`; do not raise the cap without revisiting the query.
- **TDD:** yes
- **Validation:** `cd go && go test ./cmd/agentd/ -run 'SessionList|MCP' -count=1`
- **Depends on:** —
- [x] done
- Notes:
  - **The decision the ticket demanded, recorded: NO fallback to `Session.Persona`.** `mcpCaller.Worker`
    is the one auth-established answer to "who am I", resolved in `sessionTokenAuth.authenticate` and
    shared by every core tool; `Persona` is caller-supplied. The tool description says so explicitly,
    so a model does not read an empty list as "I have no history".
  - A workerless caller gets an **empty list and the store is never called**. `SessionQuery.Worker == ""`
    means *every session in the project*, so falling through would have handed a chat session the whole
    console's history — a different question from the one asked.
  - Limit is clamped (≤0 → 10, >50 → 50), not rejected. The cap stays at 50 and the reason — three
    unconditional `COUNT(*)` subqueries per row — is now a comment on the constant so nobody raises it
    casually.
  - Four fields beyond the ticket's list (`artifact_count`, `message_count`, `attention_requested`,
    `create_error`). The verifier judged this within scope and I agree: the binding exclusion is
    *transcripts and message bodies*, none of these is content, and the first two are free because
    `ListSessions` computes them regardless.
  - A full page carries a `note` that older runs may exist, rather than fetching limit+1 rows to
    measure it — that extra row would pay for another set of COUNT(*) subqueries on every call.
  - Timestamps are documented as unix **seconds**, with a pointer that config_history's are
    milliseconds. That discrepancy was undocumented anywhere a model would read it.
  - Verifier: PASS, 10/10 criteria evidenced.

### T18: Full-content memory read over HTTP   [Status: done | Model: sonnet]
- **Scope:** `GET /agent/memories` returns `MemorySearchResult`, which has a `Snippet` and
  **no `Content`** (`go/agentdb/memories.go:87-95`), truncated at 500 bytes (`:35,259-262`) —
  and there is no by-id route. Full content is currently reachable only from inside a container
  via `memory_get`/`memory_current`. Add read routes so an embedding app can render memory
  state: `GET /agent/memories/{id}` (full content) and `GET /agent/memories/current?name=<n>`
  (the newest memory labelled `name=<n>`, mirroring `memory_current`'s semantics at
  `go/cmd/agentd/mcp_memory.go:373`). Both project-scoped from the identity, both read-only —
  memories stay append-only from the UI's perspective (`go/httpapi/httpapi.go:76-78`). Reuse
  the store methods the MCP tools already use; do not add a second query path.
- **Files:** `go/httpapi/memories.go`, `go/httpapi/httpapi.go` (routes + Config seam if
  `GetMemory`/`NewestMemory` aren't already on the interface), `go/cmd/agentd/main.go` if
  wiring changes, plus tests.
- **Acceptance criteria:** Full content returned untruncated for a memory larger than 500
  bytes; a cross-project id is 404 with no existence leak (mirror `mcp_memory.go:357-365`);
  `current` with an unknown name returns a 404 or an explicit not-found body rather than an
  error; no write or delete route is added.
- **TDD:** yes
- **Validation:** `cd go && go test ./httpapi/ -count=1`
- **Depends on:** —
- [x] done
- Notes:
  - Store seam: extended the existing `MemoryStore` interface with `GetMemory` and `NewestMemory`
    rather than adding a second Config field — one field means a host cannot point the search path
    and the read path at different stores.
  - **`current` with an unknown name answers 404**, not the MCP tool's `{found:false}` 200. The
    ticket allowed either; 404 is what every other "not there" answer in `httpapi` is, and a 200
    with a found flag makes a fetch-based client treat absence as success unless it remembers to
    branch. The MCP shape exists because a *model* reads it.
  - Non-`ErrMemoryNotFound` store failures map to **500**, not 404 — a database refusing connections
    is not a missing memory, and answering 404 sends an operator hunting for a row that is sitting
    right there.
  - Body is the record itself (not wrapped), mirroring `GET /agent/sessions/by-name/{name}`, and is
    the MCP `memoryRecord` key-for-key minus `session_url`.
  - The shared 501/403 preamble was hoisted into `memoryReadable` and `ListMemories` routed through
    it, so all three routes state the same rule once. `ListMemories`' check order is preserved and
    its existing tests passed unmodified.
  - Read-only, as required: no write or delete route was added. Untruncated content is asserted
    against a memory larger than 500 bytes, and `TestMemoryReadRoutes_LivePG` exercises it against
    real Postgres.
  - Verifier: PASS, 7/7 criteria evidenced.
- **Note:** This is what makes "Wolf renders the hypothesis from memory" true. Without it the
  product must fall back to artifacts (T8) for anything longer than a snippet.

### T17: End-to-end verification — ALWAYS LAST   [Status: pending | Model: opus]
> T18 was added after this ticket was numbered and appears above it in the file. This one is
> still the final ticket; work it only when every other box is ticked.
- **Scope:** Stack e2e proving the whole feature against the compose stack: create a session named
  `hypothesis-a` with an API key; attach a daily session-mode schedule; force two firings; assert
  the second run sees the file the first run wrote (proving restore, not a fresh container);
  register an artifact and fetch it by name with the API key; mint an embed token and load
  `/embed/session/hypothesis-a#token=…`, asserting the chat renders and no login screen appears.
  **Also prove the composition fix (T15) end to end:** with project `mcp_config` set, a session
  created over HTTP can call a core memory tool — write a memory from a scheduled reviewer run
  and read it back from the long-lived session, which is the Wolf loop in miniature. Read the
  same memory's **full content** over HTTP with the API key (T18), asserting it is not
  truncated at 500 bytes. Then run the full gates.
- **Files:** `e2e/features/embedding.stack.spec.ts` (create).
- **Acceptance criteria:** The spec passes against the stack; the artifact fetched by name has the
  second run's content; the embed page renders without login.
- **TDD:** no (this IS the test)
- **Validation:**
  `cd go && go build ./... && go vet ./... && go test ./...`;
  `cd web && npm ci && npm run typecheck && npm test`;
  `cd sandbox && npm ci && npm test && git checkout sandbox/yarn.lock`;
  `docker compose up -d --build web agentd` then
  `cd e2e && npx playwright test -c playwright.stack.config.ts features/embedding.stack.spec.ts`
  (the config lives at `e2e/playwright.stack.config.ts` and playwright is installed in `e2e/`,
  which has its own `package.json`/`yarn.lock` — see `e2e/run-stack-e2e.sh:255`).
  Remember the stack serves a **built** image of `examples/web` — rebuild `web` or UI changes are
  invisible to the browser test. Delete sessions afterwards (`./e2e/run-stack-e2e.sh clean`), since
  each holds a container and a host port.
- **Depends on:** T1–T16, T18

## Discovered Issues Log

(appended by executors during implementation)

- **T5 — the legacy branch did have authentication, just not authorization.** The ticket says
  the `listQueryEventsLegacy` fall-through "has neither auth nor tenancy today". Half right:
  `listQueryEventsLegacy` called `h.identify(w, r)` as its first act (`go/httpapi/history.go:157`
  before this change), so an unauthenticated request was already refused. What it genuinely lacked
  was the ownership check. The hole was real but narrower than described, and the fix is the same
  either way. Nothing was changed on the strength of the misdescription.
- **T5 — `SessionScope` was added to `httpapi.Identity` here, not in T4.** Sequencing consequence
  of taking T5 first; the field is listed in both tickets' file sets, and T4's remaining share is
  populating it in `identityFromRequest`. Flagged so T4's executor does not try to add it twice.
- **T3 — `AGENTKIT_JWT_SECRET` and the "session secret" are the same value in every real
  deployment.** `go/cmd/agentd/main.go:129-131` reads
  `jwtSecret := os.Getenv("AGENTKIT_JWT_SECRET")` and
  `sessionSecret := envOr("AGENTKIT_JWT_SECRET", "dev-secret")`. The comment above them says "Two
  secrets, deliberately", and they diverge only in the dev-open case where `AGENTKIT_JWT_SECRET`
  is unset. Consequence, which pre-dates this plan: a container's per-session token (signed with
  `sessionSecret`, `sid` set) is already a structurally valid credential for `jwtAuthMiddleware`,
  and is accepted there with **full project scope**. Not exploited by anything in-repo and not
  changed by this plan — but it is the reason T3 refused to overload `sid` as the embed scope, and
  it is worth its own ticket some day (giving the session-token family its own secret, or having
  the API middleware refuse tokens carrying a `sid`).
- **T6 — the "~90s with `AGENTKIT_TEST_POSTGRES_URL`, ~0.1s without" heuristic is WRONG on this
  machine, and CLAUDE.md should not be trusted on it.** `go test ./agentdb/ -count=1` takes 116–313s
  *with* the variable and ~269s *without*: the sqlite suites dominate either way, so wall time proves
  nothing about whether the live cases ran. The reliable check, used from now on, is
  `go test ./agentdb/ -run TestLivePG -count=1 -v` and counting `--- PASS` vs `--- SKIP`
  (currently 30/0 with the variable, 0/30 without).
- **T6 — the ticket's "partial index excluding NULL" is insufficient as literally written.** GORM
  writes `''`, not NULL, for an unnamed session, so a `WHERE name IS NOT NULL` index would have been
  armed against every unnamed row and rejected a project's second console chat. Corrected in the
  implementation; see T6's Notes. Anyone re-reading the plan's File Structure section should read
  the Notes instead.
- **T6 — `Store.UpdateSession` is a wholesale GORM `Save`, so every field on `Session` is writable
  by anyone who loads a row and saves it.** There is no per-field authority anywhere in the session
  store. Pre-existing; T6 sidestepped it for one column with a field permission. Also:
  GORM's `Save` fallback emits `INSERT … ON CONFLICT DO UPDATE`, not a plain INSERT — surprising for
  anyone reasoning about session-row lifecycles.
- **T6/T7 — session names exist only on Postgres.** The sqlite fallback store has its own hand-rolled
  schema with no `name` column and no `GetSessionByName`. T7 answers 501 there rather than degrading
  silently, but it means the whole embedding feature is Postgres-only — consistent with the rest of
  the product layer, and worth saying plainly in T14.
- **~~T8 — one console call site will STILL fail after this fix.~~ RETRACTED — this was false.**
  T8's implementer reported that `InlineArtifactPreview.tsx:52` (`getArtifactImageUrl`) puts
  `/agent/artifacts/{id}/download` straight into an `<img src>` with no Authorization header and
  would therefore 401. T14's fact-checker disputed it and the code agrees with the fact-checker: the
  URL is `fetch`ed **with** an `Authorization` header (`:256-259`) into an object URL, and the
  `<img>` renders that (`:265,:273`). The blob-URL indirection is precisely what makes authenticated
  images work. The original entry was written from reading line 52 in isolation; it was carried into
  this log unverified by the orchestrator, and `docs/19-embedding.md` now documents the mechanism so
  nobody "simplifies" it away. **Standing lesson: an implementer's incidental observation about code
  it did not change is the weakest evidence in this whole process — two agents disagreeing is worth
  more than either.**
- **T8 — an EIGHTH console call site to a non-existent route.** `ArtifactPreviewDialog.tsx:113` calls
  `GET /agent/artifacts/{id}/preview-url`, which the plan's inventory of "seven call sites" misses.
  It degrades gracefully (falls through to download), so it is cosmetic.
- **T8 — `docs/06-artifacts.md:139-142` is now factually false.** It says the shipped `httpapi`
  handlers do not map the nil-reader cases to 202/410 and that "nobody has made that decision in
  this repo". `artifacts_download.go` is exactly that decision. T14 already owns this file.
- **T8 — `extension/dbartifacts` has an unused `LoadForCustomer(ctx, customer, artifactID)`** that is
  not on the `artifacts.ArtifactStore` interface and is called from nothing. It would have given the
  download route a project-scoped load without a session round-trip. Not used, for consistency with
  the existing artifact handlers.
- **T9 — the web console cannot create or edit a session-mode schedule.** `web/src/schedules.ts:321`
  hard-requires a non-empty worker client-side, so a session schedule renders with a blank worker
  column and no way to set the target. Session schedules are API/MCP-only. Out of T9's file list;
  needs a follow-up ticket or a line in T14.
- **T9 — `schedule_update`'s MCP tool cannot change `target_session` while the HTTP PUT can.**
  Deliberate (a mode switch through a partial-field update is the ambiguous case), but the two
  management surfaces are now asymmetric, which is unusual for this codebase.
- **T9 — the `Sessions: runner` wiring in `main.go` has no test.** The verifier's sharpest finding:
  if that argument were dropped, every T9 test would still pass and session schedules would silently
  never fire in production. T17's e2e is the only thing that can catch it — it must actually observe
  a firing, not just a schedule row.
- **gofmt is not checked by CI** (`.github/workflows/ci.yml` runs build/test/vet only). Two files
  were left unformatted by T9 and fixed by the orchestrator; `go/httpapi/sessions_worker_filter_test.go`
  and `go/triagelab/content.go` are pre-existing offenders on this branch and were left alone.
- **Batch 2 — ⚠️ the throwaway Postgres died mid-run and the agents did not notice.** The container
  exited during the T15–T11 batch, and a live-PG test whose `AGENTKIT_TEST_POSTGRES_URL` is set but
  *unreachable* **FAILS rather than skipping** (correct behaviour). Some agents therefore reported a
  green full gate that was not green. The orchestrator's independent re-run caught it; after
  restarting the container (now with `--restart unless-stopped`) the gate is genuinely green:
  0 failures, 30 live agentdb cases, 10 live httpapi cases. **Lesson for later batches: an agent's
  "fullGatePassed" is a claim, not evidence — always re-run, and always grep for `^FAIL` rather
  than eyeballing a head-truncated log, because GORM's "record not found" noise fills the first
  screen.**
- **T15 — sessions created BEFORE this change never gain the core tools.** MCP config is fixed when
  the container is provisioned and re-supplied on restore from the persisted
  `agent_sessions.mcp_servers` column (§4.5), so an existing long-lived chat session restores with
  its old, empty set forever. There is no backfill and no route that rewrites a session's MCP
  config. **Consequence for the Wolf model: `hypothesis-a` must be created after this ships**, and
  T14 must say so. Any pre-existing session needing core tools has to be recreated.
- **T15 — the "is the core MCP server mounted?" condition is now spelled twice in `main.go`** (once
  for the `coreMCP` variable at ~:313, once for the mount at ~:487), because the `httpapi.Config` is
  built ~40 lines before the `if agentDB != nil` block. Two conditions that must stay in agreement.
  Same untested-wiring shape as T9's `Sessions: runner` — only the stack check catches a drift.
- **T15 — unlike `ComposeJob`, the httpapi path does not call `CoreMCP.Validate()`.** An invalid
  host-supplied value would surface as a background create failure inside `persistMCPServers` rather
  than at boot. Low risk (the value is a host constant), but the asymmetry is real.
- **T16 — `go/compose.go:544`'s core preamble names specific core tools in hand-written prose that is
  pinned byte-for-byte by test.** Newly registered core tools therefore never appear in the preamble
  a worker reads at job start — `session_list` is discoverable only through `tools/list`. Not acted
  on; worth knowing before adding more tools.
- **T16 — session timestamps are unix SECONDS while config-log timestamps are MILLISECONDS**, and
  nothing in the codebase said so anywhere a model would read. Now stated in `session_list`'s
  description; the rest of the surface still doesn't say it.
- **T18 — `GET /agent/memories/current` reserves the name `current` in the id path space.** Harmless
  today because ids are uuids, but `CreateMemory` accepts a caller-supplied `m.ID`, so a memory
  created with the literal id `current` would be unreachable by id. Nothing in-repo supplies one.
- **T18 — the two new memory routes inherit the T4 embed-scope hole verbatim.** They take no session
  id, so `ownsSession` never runs and a session-scoped embed token can read the whole project's
  memory. This is the third ticket to widen that hole; see the T4 entry below.
- **T11 — the design's Architecture diagram and its route table disagree.** The diagram draws
  `POST /auth/verify-google` as an arrow from the Wolf backend with no credential shown, and T11's
  prose says register it beside `/auth/google` (which is on the **unauthenticated** `root` mux),
  while the route table says "Auth: API key". Implemented per the route table, on `apiMux`. Had the
  prose been followed literally it would have shipped an unauthenticated Google-token verification
  oracle.
- **T11 — `apiMux` is no longer purely `httpapi`'s route table.** `registerVerifyGoogle` is the
  second thing `main.go` mounts on it directly (after `POST /agent/attention`), so a reader of
  `httpapi.Handlers.Mux()` no longer sees every authenticated route in one place.
- **T14 — ⚠️ compose does not forward per-project API key env vars.** `docker-compose.yml:102-103`
  passes only `AGENTKIT_PROJECT_MAP` / `_FILE` to `agentd`, so a `WOLF_API_KEY` set in `.env` never
  reaches the process and the project boots keyless (logged once). Documented in `docs/19-embedding.md`
  §2. **This also bites T17**: the e2e stack must add the variable to the `agentd` service's
  `environment:` block before any API-key flow can be exercised.
- **T14 — memory `created_at` is unix MILLISECONDS** (`go/agentdb/memories.go:127` writes
  `UnixMilli()`), while `session_list`'s session timestamps are SECONDS (the T16 entry above). Two
  units on one product surface. The doc now states the unit at each site; nothing enforces it.
- **T14 — `schedule_create`'s MCP tool description names a tool that does not exist.**
  `go/cmd/agentd/mcp_management.go:453` says "write what you learned with `memory_write`"; the real
  tool is `memory_create` (`mcp_memory.go:188`). A model following the description would call a
  nonexistent tool. Not acted on — outside T14's file list — but it is a one-word fix worth making.
- **T13 — ⚠️ the CSP is bound to the `/embed/` PATH PREFIX, not to the document.**
  `curl -D- http://localhost:8080/embed.html` returns 200 with **no** `Content-Security-Policy`
  header, because that URI falls through to `location /`. The framing protection therefore depends on
  a third party using the `/embed/session/{name}` form. Direct `/embed.html` renders nothing useful
  without a name in the path, so the exposure is small — but it belongs in T14's hazard list.
- **T13 — nginx silently skips `add_header` when the value is empty, which fails OPEN.** Handled here
  with a `map` that turns an empty upstream value into `'none'`. Worth knowing anywhere else this
  pattern is used.
- **T13 — `try_files $uri /file.html;` discards `add_header` from its own location block.** The URI
  fallback is an internal redirect that re-matches `location /`, generating the response there.
  `=404` as the final argument is what keeps the response in the original block. This silently broke
  T12's first nginx form and would break any future header added the same way.
- **T13 — on the default stack the embed page is not framable at all.** `.env.example` sets no
  `AGENTKIT_PROJECT_MAP`, so the header is `frame-ancestors 'none'`. T17 as specified is unaffected
  (it loads the embed page as a top-level navigation, which `frame-ancestors` does not restrict), but
  **any e2e that puts the embed page in a real `<iframe>` will be blocked** until the test stack's
  project map names allowed origins.
- **T13 — `auth_request` couples a static asset's availability to agentd.** If agentd is down the
  embed page 500s rather than rendering T12's "session unavailable" card. Judged acceptable (the page
  cannot resolve its session without agentd either) and commented in the nginx file.
- **T13 — who may frame the product is now boot config.** Changing `allowed_origins` requires an
  agentd restart, consistent with the rest of the project map having no reload path.
- **T13 — the `workspace/files` route does not exist either.** `ArtifactPreviewDialog.tsx:104,157`,
  `InlineArtifactPreview.tsx:54,90,113` and `ArtifactViewer.tsx:168,209` all build
  `/agent/session/{id}/workspace/files/{path}` URLs and `grep -rn 'workspace/files' go/` finds only a
  comment describing an *in-image* endpoint. So removing the token from the webapp iframe URL points
  it at a second non-existent route. Pre-existing, larger than the missing `/webapp/…` route the plan
  already lists, and untouched.
- **T12 — a session literally named `session` breaks the embed page's path parsing.**
  `segments.lastIndexOf("session")` at `EmbedSession.tsx:57` resolves `/embed/session/session` to the
  empty string. `session` is valid kebab-case, so it is a legal name. Cosmetic, not acted on.
- **T12 — `examples/web` has no test runner at all** (no `test` script, no vitest), which is why T12's
  TDD flag is "no". The two security-shaped criteria — token out of storage, token out of the URL —
  are currently guarded by nothing in CI. T17 is the only thing that will hold them.
- **T12 — the 2.6 kB embed bundle pulls in the shared 754 kB vendor chunk.** A parent page framing the
  embed downloads the same MUI/React bundle the console does. No `manualChunks` tuning was in scope.
- **T10 — the untested-wiring pattern now has four instances.** `registerEmbedToken(apiMux, jwtSecret,
  sessionNames)` joins T9's `Sessions: runner`, T15's twice-spelled `agentDB != nil` condition and
  T16's register line: pass the wrong secret or a nil store and every unit test still passes while the
  route is dead in production. Related: a `ServeMux` pattern conflict is a **runtime panic at boot**,
  not a compile error, and nothing in `cmd/agentd`'s tests constructs the real httpapi mux (it needs a
  Runner). T17 is the only net under all of these.
- **T10/T11/T13 — `main.go` now registers three routes directly on the muxes**, outside `httpapi`'s
  route table (`/auth/verify-google`, `/agent/embed-token`, `/embed/csp`), and `root` now carries an
  unauthenticated one that is neither `/health` nor `/auth/`. `httpapi.Handlers.Mux()` is no longer a
  complete picture of agentd's surface.
- **T10 — the whole embed flow is unreachable in dev-open mode by construction:** the route demands an
  API key, and a configured API key is exactly what turns dev-open off. With T7's note that named
  creates and by-name lookup also need a project credential, T14 must tell integrators not to try the
  flow against a zero-config demo.
- **T4 — ⚠️ HIGH: the embed scope confines session-by-ID routes only. An embed token can still
  reach every project-wide route.** The design's credential table says an embed token grants
  "Read/stream/message on **exactly one session**", but the mechanism it specifies —
  `Identity.SessionScope` checked inside `ownsSession` — is reachable only from routes that take a
  session id. An embed token still carries `customer: <project>`, so as specified it can call
  `PUT /agent/workers/{name}`, `PUT /agent/project-settings`, `POST /agent/events`,
  `GET /agent/memories`, the schedule CRUD, and so on. That credential is handed to a browser
  inside a third-party page via a URL fragment, so this is a real privilege escalation, not a
  theoretical one — it is bounded only by the token's 15-minute TTL.
  **Not acted on**, because closing it means a new project-wide rule ("a scoped identity may touch
  only session routes"), which is a design decision and the layering section says the enforcement
  point is decided. Options, cheapest first: (a) `httpapi` refuses any request from an identity
  with a non-empty `SessionScope` unless the handler opted in — one check in `Mux`, an allowlist of
  session routes; (b) `apiAuthMiddleware` strips `customer` from scoped principals and the session
  routes resolve tenancy from the session row instead. **This should be resolved before any embed
  token is minted for a real third party (i.e. before T10 ships to production), the same way T5
  gated the API key.**
- **T4 — an API key sees an empty session list by default.** `httpapi.ListSessions` filters on
  `id.UserEmail` unless `?user_email=*` is passed (`go/httpapi/history.go:102-107`). An API key's
  synthetic email is `api-key:<project>`, which matches no session row, so
  `GET /agent/sessions` with a key returns `[]` rather than the project's sessions. Not a bug —
  the documented lookup path is `GET /agent/sessions/by-name/{name}` (T7) — but it is a trap for
  an integrator and **T14 must document `?user_email=*`**.
- **T5 — eight routes, not seven.** `GetSession` was not in the ticket's list because it already
  had a tenancy check, but it needed the *scope* leg adding, so the tests cover eight by-ID routes.
