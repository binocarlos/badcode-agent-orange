# Installations

An **installation** is a versioned, independently-buildable sandbox image that a session
can select at launch. Each installation bundles:

- A specific set of skills and `CLAUDE.md` project memory (baked into the image).
- Platinum-specific tool plugins that the frontend renders.
- A data-science / rendering environment (Python, Node, `pt` CLI, etc.).

The installation concept follows the **app-image extension contract** defined in
`agent-library/docs/10-extension-points.md`. The agentkit core image
(`agent-library/sandbox/Dockerfile`) supplies the harness (Claude Code CLI, control
server, `node:20-slim` base). An installation's `Dockerfile` layers Platinum concerns on
top via `FROM ${BASE_IMAGE}`.

**Dynamic/hoisted skills** (skills pulled from the catalog and installed into a running
session at runtime) layer on top of an installation's baked `.claude/skills/` at runtime.
See `docs/superpowers/specs/2026-06-18-runtime-skill-install-design.md` for that design.

**Derived images guide:** `agent-library/docs/16-derived-images.md` — covers the
`parent` pointer, overlay mechanism, ancestor-first builds, and prod digest re-pinning in
detail.

---

## Folder layout

```
installations/
  <name>/
    installation.json   # {name, description, parent?, image?}
    Dockerfile          # FROM ${BASE_IMAGE} + layers
    plugins/            # tool plugins -> /app/product-plugins  (root installations only)
    overlay/            # files copied to /workspace/ (thin variants)
      CLAUDE.md         # project memory -> /workspace/CLAUDE.md
      .claude/
        skills/         # baked skills -> /workspace/.claude/skills/
      lib/              # Python/Node helpers -> /workspace/lib/
        SKILLS.md
        screenshot.py
        viz/
      index.html, ...   # webapp template -> /workspace/
```

Three installations ship today, in a `FROM`-chained tree rooted at `platinum-base`:
- `platinum-base` — shared foundation **root** (no parent). Owns the Python/data-science
  stack, the `pt` CLI, the plugins, the shared `webviz/` component library, and the full
  baked skill set (the always-on analytical/tabulation/reporting skills). Internal; not
  launched directly.
- `core-v1` — Vite webapp flavor. Thin variant (`parent: platinum-base`); adds the Vite
  toolchain, the Vite-flavored `data-visualization`/`dashboard-template` skills, and a
  `src/`-based app template via an `overlay/`. The default.
- `core-v2` — no-build webapp flavor. Thin variant (`parent: platinum-base`); adds the
  no-build-flavored `data-visualization`/`dashboard-template` skills and the static
  HTML/CDN-import-map app template via an `overlay/`.

Both flavors inherit the shared component library and baked skills from `platinum-base`;
they differ only in their build model (Vite vs no-build), their flavor of the two webapp
skills, and their `CLAUDE.md`. The shared component library lives once at
`platinum-base/workspace-lib/webviz/` and each flavor's `Dockerfile` copies it into that
flavor's served root (`src/lib/` for v1, `webapp/lib/` for v2).

---

## `installation.json` fields

| Field | Required | Description |
|---|---|---|
| `name` | yes | Unique identifier (matches the folder name). |
| `description` | yes | Human-readable summary. |
| `parent` | no | Name of the parent installation. Omit for roots. |
| `image` | no | Written by `./stack rebuild sandbox` in prod; do not edit by hand. |
| `image.digest` | — | ACR digest after the last prod push. |
| `image.baseDigest` | — | Parent image digest at the time this image was built. |
| `image.builtCommit` | — | Git short SHA at build time. |

---

## Authoring a new installation

### Option A — thin variant (recommended)

A thin variant inherits a parent installation and ships only the files it wants to
change (the **overlay**).

```bash
./stack new-installation <name> --from core-v1
```

This creates:
- `installations/<name>/installation.json` — with `"parent": "core-v1"`.
- `installations/<name>/Dockerfile` — `FROM ${BASE_IMAGE}` + `COPY overlay/ /workspace/`.
- `installations/<name>/overlay/.claude/skills/.keep` — empty skeleton.

Then add your overrides under `overlay/` — the layout mirrors `/workspace`:

| `overlay/` path | `/workspace` destination | What it overrides |
|---|---|---|
| `overlay/.claude/skills/<skill>/` | `/workspace/.claude/skills/<skill>/` | One skill |
| `overlay/CLAUDE.md` | `/workspace/CLAUDE.md` | Project memory |
| `overlay/lib/SKILLS.md` | `/workspace/lib/SKILLS.md` | Skills index |
| `overlay/lib/<helper>` | `/workspace/lib/<helper>` | Python/Node helper |
| `overlay/index.html` (etc.) | `/workspace/` | Webapp template files |

Overrides are **whole-file replacement** — Docker `COPY` replaces the matching path from
the parent layer. Files absent from `overlay/` are inherited unchanged.

> **`.claude/` is gitignored.** Force-add overlay skills:
> ```bash
> git add -f installations/<name>/overlay/.claude
> ```

#### Removing inherited files or packages

The overlay can only **add or replace** paths — it cannot delete what the parent ships.
To drop an inherited file or package, add an explicit `RUN` after the `COPY` in the
variant's `Dockerfile`. Both `core-v1` and `core-v2` branch directly off `platinum-base`
(which ships no Vite), so neither needs to remove anything today — they only **add**:
each runs a `cp` to materialize the shared `webviz/` lib into its served root, and `core-v1`
additionally installs Vite. If a future variant needed to drop an inherited file, it would
look like:

```dockerfile
COPY installations/<name>/overlay/ /workspace/
RUN rm -rf /workspace/<inherited-path-to-drop> \
    && (npm uninstall -g <pkg> || true)
```

`RUN rm`/`npm uninstall` are allowed; only `CMD`/`ENTRYPOINT`/`EXPOSE`/`HEALTHCHECK`
are forbidden (the runtime contract owned by the core image).

### Option B — full root installation

For a completely independent stack (different language runtime, different plugins), skip
`--from` and write a Dockerfile from scratch. The `BASE_IMAGE` build arg must resolve to
`agentkit-sandbox:dev` (the core harness).

Do **not** set `CMD`, `ENTRYPOINT`, `EXPOSE`, or `HEALTHCHECK` — those are owned by the
agentkit core image. `./stack rebuild sandbox` will reject your Dockerfile with a
runtime-contract lint error if you do.

---

## Build commands

```bash
# Build one installation (with its ancestor chain by default)
./stack rebuild sandbox <name>

# Build all installations in topological order (ancestors first)
./stack rebuild sandbox all

# Preview build order without building
./stack rebuild sandbox <name> --plan
./stack rebuild sandbox all --plan

# Build only the target, skip ancestors (unsafe unless parent image is current)
./stack rebuild sandbox <name> --no-deps
```

`./stack rebuild sandbox` always:
1. Runs `assert_runtime_contract` — rejects Dockerfiles that set `CMD`/`ENTRYPOINT`/`EXPOSE`/`HEALTHCHECK`.
2. Computes the ancestor chain via the `imagetree` CLI (`agent-library/go/cmd/imagetree`).
3. Builds the agentkit core image (`agentkit-sandbox:dev`) first.
4. Builds each installation in dependency order.

---

## Registry behaviour

- **Local dev** (`AGENT_V2_REGISTRY_URL=registry:5000`): pushed to the local registry; DinD
  force-pulls the `:dev` tag on every new session, so you always get the latest image.
  No goapi restart needed after a rebuild.
- **Prod** (`AGENT_V2_REGISTRY_URL=platinumimages.azurecr.io`): pushed to ACR; the digest is
  captured and written into `installations/<name>/installation.json` via
  `scripts/build-installations-manifest.py`. It also regenerates
  `goapi/pkg/installations/manifest.json` (embedded into the goapi binary).

---

## Deploy sequence (explicit)

`rebuild sandbox all` is a **pre-deploy** step. It is **not** nested inside `deploy`
because the deploy command checks for a clean working tree, and the digest writes would
violate that check if they happened inline.

```bash
# 1. Build every installation in dependency order, push to ACR, write digests.
./stack rebuild sandbox all

# 2. Commit the updated installation.json + manifest files.
git add installations/*/installation.json goapi/pkg/installations/manifest.json
git commit -m "chore: record sandbox digests after prod build"

# 3. Deploy.
./stack deploy <env>
```

---

## Select the installation

When creating a session via the API or frontend, pass `"installation": "<name>"` in the
request body. The frontend dropdown is populated by `GET /agent/:customer/installations`,
which reads the embedded manifest.

Set `AGENT_V2_DEFAULT_INSTALLATION=<name>` in `.env` to change the default for your
environment (default: `core-v1`).

---

## Shared seams

Each installation's `Dockerfile` references things outside its folder:

| Seam | What it is |
|---|---|
| `agent-library/sandbox/Dockerfile` | Builds the agentkit core image (`agentkit-sandbox:dev`). Built first by `./stack rebuild sandbox`. Changes here affect all installations. |
| `goapi/cmd/pt/` | The `pt` CLI binary compiled into the image. Changes to the pt CLI require rebuilding the installation image. |

---

## Local registry vs. ACR (deployed digest)

| Environment | `AGENT_V2_REGISTRY_URL` | Image reference used at launch |
|---|---|---|
| Local dev | `registry:5000` | `registry:5000/platinum-sandbox-<name>:dev` (force-pulled per session) |
| Staging / production | `platinumimages.azurecr.io` | `<registry>/platinum-sandbox-<name>@sha256:<digest>` (from `installation.json`) |

In local dev, goapi resolves installations by the `:dev` tag and instructs DinD to
always pull, picking up the latest push automatically. In prod, goapi resolves the image
from the committed digest in `installation.json` (via the embedded `manifest.json`). If
no digest is committed, the installation is listed as unavailable in the frontend
dropdown.

---

## Launch selection and default

The active installation for a session is resolved by the `ImageResolver` closure in
`goapi/pkg/server/agent_v2.go`:

1. If the session-create request includes `"installation": "<name>"`, that name is
   resolved to an image reference (or an error is returned if it is not built).
2. If no installation is specified, the value of `AGENT_V2_DEFAULT_INSTALLATION`
   (default: `core-v1`) is resolved. If the default is also unavailable, goapi falls
   back to the `AGENT_V2_BASE_IMAGE` policy image.

---

## No hot-reload — rebuild + new session

Session containers run entirely from their installation image. There are **no host bind
mounts** into session containers (in dev or prod), so there is no hot-reload. Any edit to
an installation — skills, `overlay/`, `CLAUDE.md`, `plugins`, or the harness source —
only takes effect after you rebuild the image and start a **new** session:

```bash
./stack rebuild sandbox <name>   # rebuild & push the :dev image
# then start a NEW agent session
```

This is intentional. Because session containers have no read-only mounts, their filesystem
is **writable**: skills can be installed into a live session at runtime (the `install_skill`
tool / Skill library writes to `/workspace/.claude/skills/`) and captured when the session
is burned into a new image (Save / publish-as-image → `docker commit`). The intended loop
is: install/iterate skills in a running session → burn a new installation image → launch
fresh sessions from it.
