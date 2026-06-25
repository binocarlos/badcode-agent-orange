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

## The contract

```
installations/<name>/
  installation.json   # { "name", "parent"?, "description" }   (omit "parent" for a root)
  Dockerfile          # ARG BASE_IMAGE=<parent-or-sandbox> ; FROM ${BASE_IMAGE} ; your layers
  overlay/            # (optional) files copied into /workspace/ — template, CLAUDE.md, skills
```

**Do not** set `CMD` / `ENTRYPOINT` / `EXPOSE` / `HEALTHCHECK` — owned by the sandbox base. An
installation only *adds* environment, tools, and workspace content.

## Building (manual, for now)

A first-class `ao installations build` command is coming (see `../MIGRATION.md`, Phase 3). Until
then, from the repo root:

```sh
docker build -t agentkit-sandbox:dev sandbox                                   # the harness/base
docker build -f installations/core/Dockerfile    --build-arg BASE_IMAGE=agentkit-sandbox:dev -t agent-orange-core:dev    installations/core
docker build -f installations/example/Dockerfile --build-arg BASE_IMAGE=agent-orange-core:dev -t agent-orange-example:dev installations/example
```

## Launching a session from one

The standalone stack points at a base image via `BASE_IMAGE` in `.env`:

```sh
echo "BASE_IMAGE=agent-orange-example:dev" >> .env
docker compose up --build      # then open http://localhost:8080
```

See `../README-stack.md` and `../docs/16-derived-images.md` for the full app-image contract.
