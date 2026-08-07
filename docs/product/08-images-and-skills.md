# Spec — Images and skills

**Part of the product spec.** Entry point and binding principles: [`17-product-spec.md`](17-product-spec.md).
Environments as first-class data: named, versioned, labeled images that agents create from inside a
session, and skills — portable capability = knowledge + its install. These are **new sections
§13–§14**, minted 2026-07-25 by the design-walkthrough amendments; no pre-existing § number
changed, so cross-references like §7.6 or §8.8 anywhere in the repo still resolve — the entry point
has the full map.

---

## 13. Images

### 13.1 Images are records, not side effects

The engine has always been able to snapshot a running session's filesystem and launch a new session
from the result (layer 0, §2). What was missing was a *grammar*: something to call the result, a way
to say why it exists, and a rule for which one a job gets. §13 supplies exactly that and nothing
more — an image becomes a **named, versioned, labeled, append-only record**, the same shape as a
memory (§7.1) and, per **P8**, the same shape as everything else in the system.

The consequence that matters: environment continuity is now **deliberate**. Nothing about a
session's filesystem survives the session unless an agent explicitly calls `image_create`, and when
it does, it says in labels why. There is no ambient accumulation anywhere (§13.8).

### 13.2 Data model

An image record carries:

| field | type | meaning |
| --- | --- | --- |
| `project` | text | the hard namespace (P5) — image names never cross projects |
| `name` | text | the stable identity a prompt or a worker points at, e.g. `marketing-tools` |
| `version` | int | monotonic integer, allocated by core per `(project, name)`, starting at 1 |
| `labels` | flat `map[string]string` | why this version exists — same grammar and same limits as memory labels: keys and values ≤63 chars, at most 32 labels (§7.1) |
| `created_by_worker` | text | provenance — which worker burned it |
| `created_by_session` | text | provenance — which session, permalinkable like a memory hit (§7.3) |
| `created_at` | bigint | |

Identity is **`name:version`**. Versions are **append-only**: a version is never overwritten and no
tool deletes one. Publishing an improved environment means burning a *new* version under the same
name, exactly as improving a rolling summary means appending a newer memory.

### 13.3 Resolution: floating names, pinnable versions

A reference resolves at **launch time**:

- a **bare `name`** resolves to the **latest** version of that name in the project — a floating
  pointer, so curation can publish improvements without touching a single worker row;
- **`name:version`** pins exactly — for when stability matters more than freshness (a worker whose
  behaviour was validated against a specific environment, a rollback, a bisect).

Both forms are one text field's worth of expressiveness, which is the whole point: the common case
("give me the current toolbox") costs no ceremony, and the careful case is available without a new
mechanism. Resolution failure (unknown name, pinned version that no longer materialises) fails the
job loudly rather than silently falling back to the project default — a worker that was pointed at
an environment and quietly got a different one is exactly the drift §13 exists to prevent.

### 13.4 Tools (core MCP, granted to every session — §9)

- `image_create(name, labels)` → `{name, version}` — snapshots the **current environment of the
  calling session** as a new version under `name`, allocating the next integer. Callable from inside
  any session: the agent that installed the tools is the agent that decides the result was worth
  keeping. Labels are the commit message (`{"purpose":"marketing-toolbox","adds":"ffmpeg+imagemagick"}`).
- `image_list(label_selector?)` →
  `[{name, version, labels, created_by_worker, created_by_session, created_at}]` — **newest first**;
  the selector is optional and uses the K8s selector semantics of §7.2 verbatim (one parser, one
  translator, reused). A bare call lists the project's whole catalogue newest-first; a selector
  answers "which images did the manager burn for the video pipeline?".

Both are ordinary mutation/read tools of the §9 surface, with §9's read-back validation. Because
`image_create` is a mutation, it appends a `config_events` record in-transaction and emits a
routable `config.changed` event (§15, [`09-config-log.md`](09-config-log.md)) — publishing an
environment is part of the organisation's history, and a chronicler can narrate it.

### 13.5 The worker pointer, and why adoption is a separate act

Workers gain an `image` column (§6.1, [`02-workers.md`](02-workers.md)): empty, `name`, or
`name:version`. Job composition step 1 (§6.2) becomes:

> **Image = `worker.image` (resolved per §13.3) > `project_settings.base_image` > global
> `Policy.BaseImage`.**

**Both** `worker.image` and `project_settings.base_image` are resolved per §13.3 — the same string
must not mean two different things in two columns — with one deliberate asymmetry: a `base_image`
naming no catalogue image is a **literal registry reference** and is used verbatim, whereas one that
names a catalogue image which cannot be produced **fails the launch**, naming the setting and the
value. (Amended 2026-07-26. The original text annotated only the worker pointer as resolved, and the
implementation followed it literally: writing a curated image name into `base_image` — exactly what
this section tells an operator to do — was accepted, read back correctly, and then stopped every
session in the project from launching.)

Adoption is a **visible act**, never a side effect. Burning a new version of `marketing-tools`
changes what floating pointers resolve to; it does not, by itself, repoint any worker that was
pinned, and it never *creates* a pointer. Moving a worker onto an image is
`worker_update(name, {image: ...}, rationale?)` (§9) — a config-evented mutation like any other, so
"when did the email worker start running on the toolbox image, and who decided that?" is a query,
not an archaeology project.

### 13.6 Engine mapping (cite, don't respec)

Everything here is a thin naming layer over machinery that already exists:

- **Burning** is the existing `Snapshot()` / `imageregistry.Persist()` path (`go/runner.go`,
  `go/imageregistry/`) — layer 0, done and tested.
- **Storage** is the existing `agentdb` `customimages` catalogue, extended with labels, the
  `(name, version)` identity, and the worker/session provenance columns. The migration is a
  work-plan item (Track I, migration **025**), not a design question for this section.
- **Launching** is the existing priority chain `Image > CustomImageID > Policy.BaseImage`
  (`runner.go:resolveLaunchImage`), which **gains the worker pointer at the front**; §5's agentd
  wiring ([`01-session-config.md`](01-session-config.md)) states the same chain from the settings side.

### 13.7 Interaction with `snapshot_ttl_days`

Append-only is a property of the **tool surface**, not a storage guarantee: no tool deletes a
version, and the `snapshot_ttl_days` reaper (§5) continues to govern storage GC exactly as before.
The two are not in conflict — one is about what agents may do, the other about what bytes the
operator pays to keep — but a project that curates long-lived named images should set
`snapshot_ttl_days` with that in mind (`0` = never reap). How the reaper and the named-image
catalogue are reconciled in code (exempting referenced versions, tombstoning reaped ones so the
record stays honest) is a Track I implementation detail, deliberately not respecced here.

### 13.8 The two curation workflows

One primitive, two sanctioned uses — and both are *prompt* patterns
([`07-reference-prompts.md`](07-reference-prompts.md)), not features:

1. **Live installation.** A worker needs a tool mid-job, installs it, uses it, finishes. Nothing
   persists. This is the default and it is fine — re-installing is cheap, and the next job starts
   from a known state rather than an accreted one.
2. **Curate, then burn.** Start from a vanilla image, `skill_install` a chosen set (§14), verify
   they work, then `image_create(name, labels)` and point workers at `name`. This is how a project
   ends up with a fast, reproducible environment whose contents someone deliberately chose — and
   whose labels say who chose them and why.

### 13.9 Why deliberate snapshots beat ambient workshops

The design considered and rejected on the way here was a **durable workshop**: one long-lived
container per worker, kept warm by a TTL scheduler, snapshotted on eviction, with the filesystem
simply accumulating whatever the worker left there. It is seductive — continuity for free — and it
is wrong for two reasons. Concurrent jobs for the same worker would contend for one filesystem, so
`max_instances > 1` (§6.1) would mean two jobs corrupting each other's working tree. And the state
that survives would be whatever happened to be lying around, with no record of why: unauditable
drift, the exact opposite of a system whose whole premise is that every persistent thing carries
labels and provenance. Deliberate snapshots cost one tool call and buy an environment history you
can query, diff, pin, and explain. Recorded as a non-goal in §10
([`17-product-spec.md`](17-product-spec.md)).

An *invisible* warm-container reuse optimisation inside the engine — semantically indistinguishable
from a cold launch — remains permissible future engine work. Warm reuse as a **semantic** feature
does not.

---

## 14. Skills

### 14.1 Portable capability = knowledge + its install

A skill is the answer to "how do I teach every worker in this project to do X?" where X needs both
*knowing how* and *having the software*. It is a project-scoped, labeled, append-only record:

| field | type | meaning |
| --- | --- | --- |
| `project` | text | the hard namespace (P5) |
| `name` | text | stable identity, kebab-case, e.g. `render-social-video` |
| `labels` | flat `map[string]string` | same grammar and limits as memory labels (§7.1) — what it's for, who it's for, which workers should install it |
| `markdown` | text | a Claude-Code-style skill document: what the capability is, when to reach for it, how to use it |
| `install_sh` | text | optional shell script that installs the skill's software dependencies |
| `created_by_worker` / `created_by_session` | text | provenance, permalinkable like a memory hit (§7.3) |
| `created_at` | bigint | |

The pairing is the idea. A markdown document alone is advice a worker cannot act on; a Dockerfile
alone is software nobody knows to use. A skill carries both, so it survives being moved between
environments — which is what makes it *portable*.

Skills are versioned by **append** (P8): `skill_create` on an existing name records a new revision
and name resolution is always newest-wins. Nothing is overwritten; the superseded revisions stay as
an honest record of how the capability was taught over time.

### 14.2 Tools (core MCP, granted to every session — §9)

- `skill_create(name, labels, markdown, install_sh?)` — records (or appends a revision to) a
  project skill. A mutation: config-evented like every other (§15).
- `skill_list(label_selector?)` — the project's skills, newest first, filtered by the §7.2 selector
  grammar; returns identity + labels + provenance, not the full markdown.
- `skill_get(name)` — the current (newest) record for that name in full: markdown and `install_sh`.
  Same search-returns-snippets / get-returns-everything split as `memory_search`/`memory_get` (§7.3).
- `skill_install(name)` — **the load-bearing one.** Inside the calling session, it writes the
  skill's markdown into the harness's skills directory (so the model picks it up the way it picks up
  any Claude-Code skill) and runs `install_sh` in the container. The tool result reports both
  outcomes — file written, script exit status and output — so a failed install is a visible failure
  the worker can react to, never a silent no-op.

`skill_install` is the only tool of the four that changes the *session*, not the project; it is
therefore not a config mutation and appends no `config_events` record.

### 14.3 Hoisting: how a lesson becomes a capability

"Hoisting" — a worker promoting something it worked out into shared project property — needs no
mechanism. It is `skill_create` with good labels. A worker that spent an afternoon getting video
rendering to work writes down what it learned as markdown, pastes the install steps it actually ran
into `install_sh`, labels it, and the next worker that needs it calls `skill_install(name)` and
skips the afternoon. The same move at the *memory* level is a `kind=lesson` memory (§7); the
difference is that a skill also brings its software, and is meant to be installed rather than read.

### 14.4 Two workflows, one primitive

Exactly the two of §13.8, seen from the skill side:

1. **Install live.** A worker's prompt says "before doing video work, `skill_install` the
   `render-social-video` skill" — the job installs what it needs, when it needs it, and the
   environment goes away with the session.
2. **Vanilla + skills → burn.** Open a session on a vanilla image, `skill_install` a chosen set,
   check it works, `image_create("marketing-tools", {...})`, then `worker_update` the workers that
   should use it (§13.5). The image is now a *materialised* skill set: same capabilities, no install
   cost per job.

The second is how a project graduates from "every job pays the install tax" to "the environment is
already right", without either being a special mode.

### 14.5 Selection is prompt policy (P1)

There is **no `skills` column on workers**, and core never auto-installs anything. Which skills a
worker installs, and when, is its **prompt's** business — one sentence of English in the worker
prompt, editable by a consultant without a migration. This is the same line P1 draws everywhere: the
store, the selectors, and the install mechanics are mechanism; "this worker should know how to render
video" is opinion, and opinion lives in prompts. A project that wants a skill everywhere puts that
instruction in the *project* prompt (§5); a project that wants it fast puts it in an image (§13).

### 14.6 Engine mapping (cite, don't respec)

`agentdb` already carries a `skills` table (`go/agentdb/skills.go`, `agent_skills`) with catalogue
semantics, content hashing, and a blob prefix for skill bytes. §14 reuses it, extended with the
project-scoped labels, the markdown/`install_sh` pair, and the worker/session provenance columns;
the migration is a work-plan item (Track I, migration **025**), together with the in-session
`skill_install` mechanics (harness skills directory + script execution) which land in the sandbox
harness ([`../07-in-image-agent.md`](../07-in-image-agent.md)) rather than in core.
