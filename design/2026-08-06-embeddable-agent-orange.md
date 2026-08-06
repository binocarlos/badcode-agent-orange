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

### T1: Project map object form   [Status: pending | Model: sonnet]
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
- [ ] done
- Notes:

### T2: API key resolution from env   [Status: pending | Model: sonnet]
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

### T3: Scoped claims in devclaims   [Status: pending | Model: sonnet]
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

### T4: Auth middleware accepts API keys and enforces embed scope   [Status: pending | Model: opus]
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

### T5: BLOCKING — enforce tenancy on session-by-ID routes   [Status: pending | Model: opus]
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

### T6: Session names — schema and store   [Status: pending | Model: sonnet]
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

### T7: Session name on the create route + by-name lookup   [Status: pending | Model: sonnet]
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

### T8: Artifact download route   [Status: pending | Model: sonnet]
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

### T9: Session-mode schedules   [Status: pending | Model: opus]
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

### T10: Embed-token endpoint   [Status: pending | Model: sonnet]
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

### T11: verify-google endpoint   [Status: pending | Model: sonnet]
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

### T12: The embed page   [Status: pending | Model: sonnet]
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

### T13: frame-ancestors header + remove tokens from iframe URLs   [Status: pending | Model: sonnet]
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

### T14: Documentation + hazard log   [Status: pending | Model: sonnet]
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

### T15: Give HTTP-created sessions the core MCP server   [Status: pending | Model: sonnet]
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
- **Note:** Load-bearing for the Wolf model — the long-lived `hypothesis-a` session can only
  read memory if it has the core tools. Before ticking, verify against a running stack that
  `memory_current` is actually callable from a chat session; the unit tests can only prove the
  config was merged, not that the tool answers.

### T16: `session_list` core MCP tool   [Status: pending | Model: sonnet]
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

### T18: Full-content memory read over HTTP   [Status: pending | Model: sonnet]
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
