# Conversation handoff — how Agent Orange got here, and what's next

This summarizes the thread that created this repo so you can continue in a fresh Claude Code
session with cwd = this repo. Authoritative detail lives in `../CLAUDE.md` and `../MIGRATION.md`;
this is the short story.

## How we got here (the arc)

1. Started designing **Agent Orange** as an autonomous-org agent framework / art-object. Wrote a
   master spec + a TDD plan + built a **TypeScript Phase 0 skeleton** (a single goal-worker with
   shifts/calls/ledger/refer-up). That TS skeleton is **abandoned** — it was the wrong shape for the
   real use cases (configurable interactive sessions, base images, MCP/skills, namespacing).
2. Pivoted: adopted the in-house Go runtime **agentkit** (from `bayesprice/Platinum/agent-library`)
   **wholesale** as Agent Orange. agentkit already does sessions-with-{system prompt, base image,
   MCP, skills}, container management, multi-tenancy, persistence, interactive streaming.
3. Imported it here, re-moduled to `github.com/binocarlos/badcode-agent-orange` (**`go build ./...`
   green**), set up engine-owned example installations, and wrote `CLAUDE.md`.

## Current state

- **Engine** = agentkit (Go), at repo root. `cd go && go build ./...` passes (Go 1.24).
- **Installations** = examples here: `installations/core` (minimal root) → `installations/example`
  (per-project template). Real per-project images live in each project's own repo.
- **`migration-reference/`** = Platinum host-side image pipeline + original Platinum installations —
  **reference only**, do not build/import.
- **Not done yet:** `sandbox/` (TS in-image agent) and `web/` (React UI) haven't been `npm`-built in
  this fork; registry auth is basic-only; no cloud (GCP/Azure) backends in the module.
- Provenance/IP: bayesprice-owned; fine for **private** use; a public release needs licensing first.

## Locked decisions

- Module path: `github.com/binocarlos/badcode-agent-orange`.
- Installations: engine-owned **examples** (`core` → `example`); per-project images in their own repos.
- Installation Dockerfiles never set `CMD`/`ENTRYPOINT`/`EXPOSE`/`HEALTHCHECK` (owned by the sandbox base).
- Keep the **liftability invariant**: `go/` imports nothing from a host app (CI-enforced).

## What's next: Google Cloud integration (priority) — see `MIGRATION.md` Phase 4

Two independent pieces. **Artifacts are already in the repo** (`go/artifacts/` `ArtifactStore` +
`go/agentdb/artifacts.go` + `filesblob` reference); only a filesystem blob backend ships.

- **4a — artifacts + snapshots → Google Cloud Storage:** implement **one** GCS-backed
  `extension.BlobStore`/`BlobStoreFactory` (`go/extension/gcsblob/`, mirroring `extension/filesblob`).
  It serves **both** artifacts and snapshots (both write through `extension.BlobStore`).
- **4b — images → Artifact Registry:** `go/imageregistry/ociregistry` already does `docker push/pull`;
  add the OAuth2-access-token auth seam (`go/imageregistry/auth/gcp.go`). Today auth is hardcoded basic.

## Inputs needed to start Phase 4

GCP **project id**, **region**, **Artifact Registry repo name**, **GCS bucket name**, and **auth
method** (service-account JSON key vs workload identity / ADC).

## Note on continuity

This thread ran from the `badcode` repo, which holds the cross-project decision history in its
per-project memory. A fresh session here has its own memory — but `CLAUDE.md` + `MIGRATION.md` +
this file are written to make the repo self-orienting.
