# Spec — Session & project configuration

**Part of the product spec.** Entry point and binding principles: [`17-product-spec.md`](17-product-spec.md).
How MCP servers reach a session (gap G1) and per-project defaults (gap G2). Section numbers (§) are kept from the original single-file spec, so cross-references
like §7.6 or §8.8 anywhere in the repo still resolve — the entry point has the full map.

---

## 4. Session MCP plumbing (closes G1)

The engine must let the *host* specify MCP servers per session. This is pure mechanism.

### 4.1 Go surface

Add to `CreateSessionRequest` (and to the message-turn options if we want per-turn overrides —
we do not; per-session only):

```go
// MCPServers configures Model Context Protocol servers available to the
// in-image harness for the lifetime of the session. Merged with (never
// replacing) the sandbox's built-in tool registry.
MCPServers map[string]MCPServerConfig

type MCPServerConfig struct {
    // Exactly one transport:
    Command string            // stdio: executable inside the container
    Args    []string
    Env     map[string]string // env for the process; values may be ${VAR} references (§4.4)
    URL     string            // http/sse: reachable FROM INSIDE the container
    Headers map[string]string // values may be ${VAR} references (§4.4)
}
```

`Env` and `Headers` values support exactly one substitution form: a whole-value `${VAR_NAME}`
reference, resolved at MCP-process spawn time from the environment of the session container.
No partial interpolation, no defaults, no nesting. A reference to an unset variable fails the
MCP server loudly at spawn (a misconfigured credential must never silently become the literal
string `${GMAIL_TOKEN}`).

### 4.2 Wire protocol

The Runner already POSTs to the sandbox control server to boot a session. Extend that create
payload with `mcp_servers` (same shape, snake_case JSON). The sandbox stores it on the session
record.

### 4.3 Sandbox harness

In `sandbox/src/harness/claude-agent-sdk.ts`, merge session-supplied servers into the `query()`
options: `mcpServers: {...resolved.mcpServers, ...session.mcpServers}` — session config wins on
name collision. `allowedTools` gains the corresponding `mcp__<name>__*` entries. Stdio servers
run inside the container, so the *image* must contain their binaries — that is what custom
images (atom 3) are for; the spec makes this pairing explicit rather than accidental.

### 4.4 Credentials: environment references, never database storage

**Decision:** Agent Orange is delivered as an on-prem/self-operated open-source stack — a
trusted environment. Credentials are therefore configured by the **operator as environment
variables**, and MCP configuration stored in the database only ever *names* the variable
(`"Env": {"GMAIL_API_KEY": "${GMAIL_API_KEY}"}`, `"Headers": {"Authorization": "${NOTION_AUTH}"}`).
Secure credential storage in the database is a world of pain we deliberately do not open:
no secret values in `project_settings.mcp_config`, `workers.mcp_config`, or any session row —
which also means persisting and displaying MCP config is safe by construction (nothing to
redact except the resolved process environment, which never leaves the container).

**Propagation path** (where the variable travels so it exists where the MCP code runs):

1. Operator defines the variable once, in the stack environment (`.env` → compose → **agentd**;
   agentd is the owner, not dind — dind hosts containers but agentd already injects per-session
   env and is the single place config enters the system).
2. agentd forwards operator-designated variables into every session container via the existing
   `SessionEnv` injection seam (the same mechanism that injects model-provider config today).
   Designation is an explicit allowlist: `AGENTKIT_MCP_ENV=GMAIL_API_KEY,NOTION_AUTH,...`
   (never "forward everything" — agentd's own environment contains things sessions must not see,
   e.g. the JWT secret and `ANTHROPIC_API_KEY`).
3. Inside the container, the sandbox resolves `${VAR}` references from its own environment when
   spawning stdio MCP processes / building HTTP headers (§4.1).

Per-project credential separation, secret managers, and rotation are all explicitly out of
scope: one operator, one trusted environment, one set of variables.

### 4.5 Snapshot interaction

MCP config is *session config*, not filesystem state, so `rehydrateConversation`-style resume
must re-supply it: persist `MCPServers` on the session row (`agentdb`), and have resume paths
pass it back to the sandbox on re-provision. A snapshot restored as a *new* session under a
different worker gets that worker's current config, not the frozen one.

---

## 5. Project settings (closes G2)

One new table, `project_settings`, keyed on project (the customer string), created lazily on
first write (projects themselves remain "a name that exists once something carries it"):

| column | type | meaning |
| --- | --- | --- |
| `project` | text PK | the namespace |
| `base_image` | text | default launch image for all sessions in the project ('' → global `Policy.BaseImage`) |
| `system_prompt` | text | the project-level system prompt, prepended to every worker's prompt |
| `mcp_config` | jsonb | `map[string]MCPServerConfig` — granted to **all** workers, no exceptions, no filtering (explicit decision: no per-worker visibility rules for project tools) |
| `attention_channel` | jsonb | where `request_human_attention` notifications go (§9). v1: `{"kind":"webhook","url":"..."}` — a generic POST of `{message, session_url}` covers Slack/Discord/ntfy/email bridges. Unset ⇒ the tool still succeeds but only logs (the session still pauses awaiting the human). |
| `max_concurrent_jobs` | int | router/scheduler concurrency cap for the project (default 4 — §8.4) |
| `daily_tokens_soft` | bigint | soft daily token budget; crossing it sends one attention-channel notification (0 = off; checked by the router/scheduler — §8.4) |
| `daily_tokens_hard` | bigint | hard daily token budget; crossing it stops non-interactive job creation until midnight (0 = off; checked by the router/scheduler — §8.4) |
| `briefing_max_bytes` | int | byte cap on the injected rolling summary at composition time (default 2048 — §7.4) |
| `snapshot_ttl_days` | int | days before the snapshot reaper deletes a snapshot image (default 30; 0 = never) |
| `updated_at` | bigint | |

- **agentd wiring:** implement a real `SessionContextProvider` that reads `project_settings`
  for the session's project and applies base image + system prompt + MCP config as the
  *defaults* which the request may extend (request-supplied values are additive for MCP,
  concatenative for prompt, and overriding for image — same precedence the Runner already has).
- **HTTP:** `GET/PUT /agent/project-settings` (project inferred from the JWT). PUT is
  whole-object; no patch semantics (P4 thinking: plain values, wholesale writes).
- **UI:** a project settings page — base image field, big system-prompt textarea, MCP config
  JSON editor. Nothing clever.
- **Tools:** the project system prompt is also writable from *inside* sessions via the
  management MCP tool (§9), because a consultant must be able to improve it.

**Budget semantics** (two-tier, per project, per day): crossing `daily_tokens_soft` sends exactly
one attention-channel notification per day — a heads-up, nothing stops. Crossing
`daily_tokens_hard` makes the router and scheduler create **no non-interactive jobs** until
midnight (stack-local time), when both counters reset. Both tiers exempt interactive chat — a
blown budget must never lock a human out of talking to their workers.

**Snapshot TTL semantics:** every snapshot carries metadata `{source session, created_at,
expiry, last_resumed_at}`; a reaper deletes snapshot images whose expiry has passed.
`snapshot_ttl_days` sets the expiry at snapshot time (default 30); `0` disables reaping — the
snapshot is kept forever.
