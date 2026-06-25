# Derived Installation Images

> **Cross-links:** [07-in-image-agent.md](07-in-image-agent.md) (runtime contract) ·
> [10-extension-points.md](10-extension-points.md) (what derived images add).

## Overview

An installation image inherits a **parent** installation and ships only its delta
(an **overlay** of files). Derived images form a tree rooted at the agentkit core image.
This keeps each variant small, prevents duplication, and makes it easy to pull in fixes
from an upstream layer by rebuilding down the chain.

### Example: Platinum's core-v1 → core-v2

```
agentkit-sandbox:dev   (core harness — Claude Code CLI, control server, port 3010)
  └── platinum-sandbox-core-v1:dev   (Python data stack, pt CLI, base skills)
        └── platinum-sandbox-core-v2:dev   (webapp skills overlay + no-build app template)
```

`core-v2` adds only the skills, `CLAUDE.md`, and the HTML/CDN app template that
distinguish it from `core-v1`. It inherits `core-v1`'s Python environment, `pt` CLI,
plugins, and 22 base skills automatically — no duplication.

---

## The runtime-contract invariant

The **agentkit core image** owns the runtime contract exclusively:

| Owned by core | Never redefined by derived images |
|---|---|
| Control server (port 3010) | `CMD` |
| `HEALTHCHECK` | `ENTRYPOINT` |
| Process supervision | `EXPOSE` |

Derived images add **capabilities** (apt/pip/npm packages, binaries) and **knowledge**
(skills, `CLAUDE.md`, helpers, webapp templates). They never redefine the contract
because doing so would break session orchestration (the control server start-up sequence
expected by `07-in-image-agent.md`).

`./stack rebuild sandbox` enforces this with a lint step before every build:

```bash
assert_runtime_contract installations/<name>/Dockerfile
```

If a derived Dockerfile contains `CMD`, `ENTRYPOINT`, `EXPOSE`, or `HEALTHCHECK`, the
build fails immediately with a clear error.

---

## Tree structure (single parent — no multiple inheritance)

Each installation has **at most one parent** (`"parent"` in `installation.json`).  
An installation with no `parent` roots directly at the agentkit core image.

```json
// core-v1/installation.json  — root (no parent)
{ "name": "core-v1", "description": "..." }

// core-v2/installation.json  — thin variant
{ "name": "core-v2", "parent": "core-v1", "description": "..." }
```

This means the inheritance graph is always a **forest of trees**, never a DAG.  
No merge/append logic is needed; the Docker layer cache handles composition.

---

## The `imagetree` helper

`agent-library/go/cmd/imagetree` is a tiny CLI that understands the `{name, parent}`
graph and answers two questions:

```
# What order should we build everything in?
echo '[{"name":"core-v1"},{"name":"core-v2","parent":"core-v1"}]' | go run ./cmd/imagetree
# core-v1
# core-v2

# What is the ancestor chain for a specific target?
echo '[...]' | go run ./cmd/imagetree -target core-v2
# core-v1
# core-v2
```

The helper validates:
- **Cycles** — errors with `cycle detected`
- **Missing parents** — errors with `unknown parent`
- **Duplicate names** — errors with `duplicate node name`
- **Empty names** — errors with `empty name`
- **Unknown target** (chain mode only) — errors with `unknown target`

Build order is deterministic: ties broken by name (stable alphabetical sort).

---

## Ancestor-first builds

`./stack rebuild sandbox <name>` always builds the ancestor chain before the target:

```
./stack rebuild sandbox core-v2
# → builds: core-v1, then core-v2
```

`./stack rebuild sandbox all` builds every installation in topological order.

This is correct and cheap because of the Docker layer cache:

1. If the parent image is unchanged, Docker's cache means building it again costs
   almost nothing (every layer hits).
2. If the parent image *was* changed, you want the child to rebuild against the new
   parent — ancestor-first guarantees it.

Use `--plan` to preview the build order without building:

```bash
./stack rebuild sandbox core-v2 --plan
# Build plan (registry registry:5000):
#   core-v1  FROM  agentkit-sandbox:dev
#   core-v2  FROM  registry:5000/platinum-sandbox-core-v1:dev

./stack rebuild sandbox all --plan
```

Use `--no-deps` to build only the target without its ancestors (unsafe unless you know
the parent image is already current):

```bash
./stack rebuild sandbox core-v2 --no-deps
```

---

## Dev vs prod: tags and digest re-pinning

| Environment | Parent reference | Child reference |
|---|---|---|
| Local dev (`registry:5000`) | `registry:5000/platinum-sandbox-core-v1:dev` | `registry:5000/platinum-sandbox-core-v2:dev` |
| Prod (ACR) | `platinumimages.azurecr.io/platinum-sandbox-core-v1@sha256:<digest>` | `platinumimages.azurecr.io/platinum-sandbox-core-v2@sha256:<digest>` |

In dev, the `:dev` mutable tag is resolved at build time from the registry — DinD
force-pulls the `:dev` tag on each new session. No digest pinning.

In prod, each installation's built digest is written into `installation.json` after a
successful push (`record_installation_digest`). The manifest script
(`scripts/build-installations-manifest.py`) collects all digests into
`goapi/pkg/installations/manifest.json` (embedded into the goapi binary). The
`baseDigest` field records the parent's digest at the time the child was built, giving
full chain traceability:

```json
// core-v2/installation.json after a prod build
{
  "name": "core-v2",
  "parent": "core-v1",
  "image": {
    "registry": "platinumimages.azurecr.io",
    "digest": "sha256:abc...",
    "baseDigest": "sha256:def...",
    "builtCommit": "a1b2c3d",
    "builtAt": "2026-06-20T12:00:00Z"
  }
}
```

**Deploy sequence (explicit):**

```bash
# 1. Build every installation in dependency order, push to ACR, write digests.
./stack rebuild sandbox all

# 2. Commit the updated installation.json files (digest records).
git add installations/*/installation.json goapi/pkg/installations/manifest.json
git commit -m "chore: record sandbox digests after prod build"

# 3. Deploy the goapi binary (with the new embedded manifest).
./stack deploy <env>
```

`rebuild sandbox all` is a **pre-deploy** step. It is **not** nested inside `deploy`
because the deploy command checks for a clean working tree, which the digest writes
would violate if they happened inline.

---

## The overlay override model

A derived image's `Dockerfile` copies a single `overlay/` directory into the runtime
workspace:

```dockerfile
ARG BASE_IMAGE=registry:5000/platinum-sandbox-core-v1:dev
FROM ${BASE_IMAGE}
COPY installations/core-v2/overlay/ /workspace/
```

`overlay/` mirrors the layout of `/workspace` inside the container:

| `overlay/` path | `/workspace` destination | What it overrides |
|---|---|---|
| `overlay/.claude/skills/<name>/` | `/workspace/.claude/skills/<name>/` | One skill |
| `overlay/CLAUDE.md` | `/workspace/CLAUDE.md` | Project memory |
| `overlay/lib/SKILLS.md` | `/workspace/lib/SKILLS.md` | Skills index |
| `overlay/lib/<helper>` | `/workspace/lib/<helper>` | Python/Node helper |
| `overlay/index.html` (etc.) | `/workspace/index.html` | Webapp template |

Overrides are **whole-file replacement** — Docker's `COPY` replaces any matching path
from the parent layer. Files not present in `overlay/` are inherited unchanged from the
parent image.

**Limitation + escape hatch:** the overlay (`COPY`) can only add or replace files; it
cannot delete a file that exists in a parent layer. To intentionally drop an inherited
file or package, add an explicit `RUN` after the `COPY` in the derived `Dockerfile`. For
example, `core-v2` is no-build and removes core-v1's inherited Vite scaffolding:

```dockerfile
COPY installations/core-v2/overlay/ /workspace/
RUN rm -rf /workspace/index.html /workspace/package.json /workspace/vite.config.js \
           /workspace/src /workspace/node_modules \
    && (npm uninstall -g vite || true)
```

`RUN rm`/`npm uninstall` are permitted; only `CMD`/`ENTRYPOINT`/`EXPOSE`/`HEALTHCHECK`
are forbidden (the runtime contract owned by the core image). To remove a *skill*
specifically, you can also replace it with an empty/stub `SKILL.md` via the overlay.

### `.claude/` and `git add -f`

`.claude/` is gitignored in this repository. Files committed under
`installations/*/overlay/.claude/` must be force-added:

```bash
git add -f installations/<name>/overlay/.claude
```

---

## Scaffold a new derived installation

```bash
./stack new-installation <name> --from <parent>
# Creates:
#   installations/<name>/installation.json   (parent pointer)
#   installations/<name>/Dockerfile          (FROM ${BASE_IMAGE} + COPY overlay/)
#   installations/<name>/overlay/.claude/skills/.keep
```

Then add your overrides under `installations/<name>/overlay/` (mirroring `/workspace`),
rebuild, and start a new session:

```bash
./stack rebuild sandbox <name>   # ancestor-first by default
# start a NEW agent session to pick up the new image
```

---

## Summary of guarantees

| Property | Mechanism |
|---|---|
| Parent built before child | `imagetree` topological sort in `./stack rebuild sandbox` |
| Runtime contract not violated | `assert_runtime_contract` lint before every build |
| Prod chain traceability | `baseDigest` recorded in `installation.json` |
| Knowledge inheritance | Docker layer cache — parent layer present in child image |
| Override semantics | `COPY overlay/ /workspace/` — whole-file replacement |
| No multiple inheritance | Single `parent` pointer enforced by `imagetree` validation |
