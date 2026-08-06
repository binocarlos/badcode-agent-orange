# installations — example base images

These are **example** installation images that ship with the engine to show the
layering. Real per-project installations live in their **own project repos** (an
`installations/` folder there, same contract), so each project curates the base
image its agent sessions launch from.

## The layering

Each image is `FROM` the one below it:

```
agentkit-sandbox:dev   ← the harness (engine), built from ../sandbox by the standalone stack
        ▲                node:20-slim + bash, git, ripgrep, ca-certificates,
        │                the in-image control server, /workspace, port 3010
   core                ← minimal, product-neutral root: shell + fs essentials, nothing else
        ▲
        │
   example             ← example per-project image. Copy into a project repo to start one.
```

## Derived images: a single-parent tree

An installation inherits **one parent** and ships only its delta. `installation.json`
carries a `"parent"` pointer; omit it for a root (which then `FROM`s the sandbox
harness directly):

```json
// core/installation.json — a root (no parent)
{ "name": "core", "description": "…" }

// example/installation.json — extends core
{ "name": "example", "parent": "core", "description": "…" }
```

Because each installation has **at most one parent**, the inheritance graph is
always a **forest of trees, never a DAG**. There is no merge/append logic — the
Docker layer cache handles composition, so a child that changes nothing but a
skill re-uses every parent layer for free. Building a child always builds its
ancestor chain first: if the parent is unchanged the cache makes it nearly free;
if the parent changed, the child correctly rebuilds against the new parent.

## The contract

```
installations/<name>/
  installation.json   # { "name", "parent"?, "description" }   (omit "parent" for a root)
  Dockerfile          # ARG BASE_IMAGE=<parent-or-sandbox> ; FROM ${BASE_IMAGE} ; your layers
  overlay/            # (optional) files copied into /workspace/ — template, CLAUDE.md, skills
```

**Do not** set `CMD` / `ENTRYPOINT` / `EXPOSE` / `HEALTHCHECK`. The sandbox base image owns
the **runtime contract** exclusively — the control server (port 3010), the healthcheck, the
start-up sequence the Go Runner and `docs/07-in-image-agent.md` depend on. A derived image that
redefined any of those would break session orchestration. Installations only *add* capabilities
(apt/pip/npm packages, binaries) and *knowledge* (skills, `CLAUDE.md`, workspace content) on top.

## The overlay model

The `overlay/` directory mirrors the layout of `/workspace` inside the container, and a
single `COPY overlay/ /workspace/` in the Dockerfile lays it down:

| `overlay/` path | `/workspace` destination | What it seeds |
|---|---|---|
| `overlay/CLAUDE.md` | `/workspace/CLAUDE.md` | Project memory |
| `overlay/.claude/skills/<name>/` | `/workspace/.claude/skills/<name>/` | One skill |
| `overlay/lib/<helper>` | `/workspace/lib/<helper>` | Helper script |
| `overlay/<template>` | `/workspace/<template>` | Workspace template |

Overrides are **whole-file replacement**: `COPY` replaces any matching path from the parent
layer, and files absent from `overlay/` are inherited unchanged. `COPY` can only add or replace
— to *drop* an inherited file or package, add an explicit `RUN rm …` / `npm uninstall …` after
the `COPY` (those are permitted; only the four runtime-contract directives above are forbidden).

> **Note on `.claude/`.** This repo does **not** gitignore it (checked with `git check-ignore`;
> `.gitignore` carries no such rule), so files under `installations/*/overlay/.claude/` commit
> normally — no `git add -f` needed. If you vendor this layout into a repo whose own `.gitignore`
> *does* exclude `.claude/`, that is where the force-add becomes necessary.

## Building (manual, for now)

A first-class `ao installations build` command is coming (see `../MIGRATION.md`, Phase 3). Until
then, from the repo root:

```sh
docker build -t agentkit-sandbox:dev sandbox                                   # the harness/base
docker build -f installations/core/Dockerfile    --build-arg BASE_IMAGE=agentkit-sandbox:dev -t agent-orange-core:dev    installations/core
docker build -f installations/example/Dockerfile --build-arg BASE_IMAGE=agent-orange-core:dev -t agent-orange-example:dev installations/example
```

Build the ancestor chain before the target (sandbox → core → example, above). The `imagetree`
CLI (next) computes that order for you.

## `imagetree` — build order from the `{name, parent}` graph

`go/cmd/imagetree` is a tiny CLI that reads a JSON array of `{name, parent}` nodes on stdin and
prints either the full topological build order or, with `-target`, the ancestor chain (root-first,
inclusive) for one node — one name per line. Run it from `go/`:

```sh
# What order should everything build in?
echo '[{"name":"core"},{"name":"example","parent":"core"}]' | go run ./cmd/imagetree
# core
# example

# What is the ancestor chain for one target?
echo '[{"name":"core"},{"name":"example","parent":"core"}]' | go run ./cmd/imagetree -target example
# core
# example
```

It validates the graph and errors on an **empty name**, a **duplicate name**, an **unknown
parent**, a **cycle**, or (in `-target` mode) an **unknown target**. Build order is
deterministic — ties are broken alphabetically.

## Dev tags vs prod digests

In local dev, images carry mutable `:dev` tags resolved at build time; the standalone stack
force-pulls them per session, so a rebuilt image is picked up by the next new session with no
pinning. For reproducible production launches, pin by **content digest** (`<registry>/<name>@sha256:…`)
instead of a mutable tag: the digest names an exact image, giving a traceable parent→child chain
across a deploy. The engine leans into this — a build's inputs fold into a content-hash tag
(`BuildSpec.SourceKey`), so `Resolve` cache-hits an unchanged build exactly rather than rebuilding.

## Launching a session from one

The standalone stack points at a base image via `BASE_IMAGE` in `.env`:

```sh
echo "BASE_IMAGE=agent-orange-example:dev" >> .env
docker compose up --build      # then open http://localhost:8080
```

See `../README-stack.md` and `../docs/15-standalone-stack.md` for the app-image contract and
per-app plugins.
