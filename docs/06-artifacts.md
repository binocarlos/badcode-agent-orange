# 06 — Artifacts (and why they are not snapshots)

The agent produces two very different kinds of persisted bytes, and conflating them is the most common
way these systems get muddled. The library keeps them on separate interfaces with separate lifecycles.

| | **Snapshot** | **Artifact** |
|---|---|---|
| What | The *whole filesystem* of a session, as an image | A *single user-facing file* the agent produced |
| Why | Resurrect/suspend the session; publish it as a reusable app | Let the user download/preview/pin a deliverable |
| Interface | `ExecutionEnvironment.Snapshot` + `ImageRegistry` ([02](02-execution-environment.md), [03](03-image-registry.md)) | `ArtifactStore` (this doc) |
| Granularity | One *archive* snapshot per session (latest), opaque — plus any number of named `name:version` catalogue entries an agent burns with `image_create` | Many per session, each with type/label/status |
| Lifecycle | live container → image → durable handle → restored container | live-in-workspace → extracted-to-blob → (or lost) |

A session can be snapshotted (so it resumes tomorrow) while *also* having ten artifacts (a report, a
chart JSON, a generated web app). These are orthogonal operations on orthogonal interfaces.

## The `ArtifactStore` contract

`go/artifacts/artifacts.go` holds **only** the interface and the portable types — there is no
implementation in that package. The shipped implementations are `extension/dbartifacts` (bytes in a
`BlobStore`, metadata in Postgres — what `cmd/agentd` wires when `DATABASE_URL` is set),
`extension/blobartifacts` (bytes in a `BlobStore`, metadata in an in-process map — the sqlite
fallback), and `artifacts.MockArtifactStore` in `mock.go`; `dir.go` holds
`WriteTarToBlobs`, the tar-stream → per-file-blob helper the directory path uses.

```go
package artifacts

import (
	"context"
	"io"
)

// ArtifactStore persists and retrieves agent artifacts — files produced in the session
// workspace and registered for download/preview.
type ArtifactStore interface {
	// Save upserts artifact metadata (dedup on session_id + file_path) and, when content
	// is non-nil, persists bytes and sets Status="extracted". Preserves the
	// live → extracted, never-regress rule and write-once Source.
	//
	// When art.IsDir is true, content MUST be a tar stream: the impl untars it and
	// writes one blob per regular file under the artifact's blob PREFIX (BlobPath),
	// and sets FileSize to the sum of entry sizes.
	Save(ctx context.Context, art *Artifact, content io.Reader) (*Artifact, error)

	// Load returns metadata plus an open reader for the bytes. reader is nil if the
	// artifact is metadata-only (status "lost"), if the bytes are gone, or if the
	// artifact is a directory (no single byte stream — list the BlobPath prefix).
	Load(ctx context.Context, artifactID string) (*Artifact, io.ReadCloser, error)

	// List returns all artifacts for a session.
	List(ctx context.Context, sessionID string) ([]*Artifact, error)

	// MarkLost flags all still-"live" artifacts for a session as lost — called when the
	// instance is destroyed before extraction.
	MarkLost(ctx context.Context, sessionID string) error

	// CaptureFolder slurps a named set of files (or a single file — the degenerate
	// case) from a tar stream and saves it as one artifact identified by
	// (sessionID, name).
	CaptureFolder(ctx context.Context, sessionID, name string, content io.Reader) (*Artifact, error)
}

// Artifact is the generic artifact shape, owned by the library so it depends on nothing in
// any host app.
type Artifact struct {
	ID           string
	SessionID    string
	FilePath     string // path within the workspace, the dedup key with SessionID
	ArtifactType string // "file" | "code" | "image" | "data" | "webapp" (extensible)
	Status       Status // live | extracted | lost | extraction_failed
	BlobPath     string // set once extracted
	Label        string
	Description  string
	MimeType     string
	FileSize     int64
	Source       string // "tool" | "auto" | "upload" (never overwritten once set)
	IsDir        bool   // when true, BlobPath is a PREFIX and bytes are one blob per file
	// Host-specific fields live in a generic bag so the library type stays portable.
	Meta map[string]string
}

type Status string

const (
	StatusLive            Status = "live"
	StatusExtracted       Status = "extracted"
	StatusLost            Status = "lost"
	StatusExtractionFailed Status = "extraction_failed"
)
```

## The status state machine (hard-won)

```
live ─┬─→ extracted          (bytes successfully uploaded to blob)
      ├─→ extraction_failed  (upload retries exhausted)
      └─→ lost               (instance destroyed before extraction; no blob)

extracted → [terminal] served from blob
lost      → [terminal] 410 Gone
```

Three non-obvious rules every implementation **must** keep (held by `extension/dbartifacts`,
`extension/blobartifacts` and `artifacts.MockArtifactStore`, so `artifacts_test.go`,
`blobartifacts_test.go` and `dbartifacts_test.go` catch regressions):

1. **Never regress `extracted` → `live`.** A `Save` that arrives with `live` after the artifact is
   already `extracted` keeps `extracted`.
2. **`MarkLost` promotes instead of losing when a blob exists.** If a "live" artifact already has a
   `BlobPath`, `MarkLost` makes it `extracted`, not `lost` — the bytes are safe even though the
   container is gone.
3. **`Source` is write-once.** Once set, it's never overwritten by a later `Save`.

## How bytes actually reach blob storage

**One path is wired by the library**, and it is the only one that fires without host code:

- **SSE-triggered.** The `artifact_registered` marker event in the live stream fires the Runner's
  `onArtifactRegistered` hook ([05](05-event-streaming.md)). It reads `filePath` off the event,
  resolves it under `/workspace`, pulls the bytes from the running instance, and `Save`s. The
  artifact metadata (`label`, `artifactType`, `description`) comes from the event's own fields;
  `Source` is `"auto"`. Any per-artifact failure is swallowed — it must never fail the turn.

Everything else is host-composed on top of `Save` / `CaptureFolder`: an upload route
(`httpapi` `Upload`, `Source: "upload"`), a metadata-only registration (`CreateArtifact`, saved with
nil content so the artifact stays `live`), or a host-driven eager pull. The *pulling* of bytes from
the workspace is an `ExecutionEnvironment`/sandbox-contract concern; the *storing* is `ArtifactStore`.

## Download

`Load` is deliberately dumb — it reports state, it does not repair it:

- bytes present in the `BlobStore` → metadata **and** an open reader.
- `lost`, no `BlobPath`, blob missing from the backend, or `IsDir` → metadata and a **nil reader**.
  There is no sentinel error for these; a caller must check `reader != nil`, not `err != nil`.
- a genuine backend failure → a wrapped error.

There is **no self-heal**: a `live` artifact whose blob happens to exist is not promoted to
`extracted` by `Load`. (`MarkLost` is what performs that promotion, on destroy.) And the shipped
`httpapi` handlers do **not** map these to 202/410 — that mapping is a host decision nobody has
made in this repo.

## Webapp artifacts

`ArtifactType == "webapp"` is handled **in core**, not by a host: `onArtifactRegistered` takes the
directory containing the registered entry file, tars it out of the workspace, and stores it as one
directory artifact whose `FilePath` is that directory (guarding against an entry at the workspace
root, so the whole workspace is never captured). That is what makes a bundled app's JS/CSS/font
assets survive, rather than only `index.html`.

What is *not* in the module: any code that **serves** a webapp behind a tokenised URL, and any
emission of `webapp_ready`. `web/`'s reducer handles `webapp_ready` if something sends one, but
nothing in this repo does. Serving remains a host recipe.

## Folder artifacts (generalized capture)

The v0 `ArtifactStore` captured a **single file** per artifact. That is now generalised to a
**named folder/file-set capture** (`CaptureFolder`, `Artifact.IsDir`): slurp a set of paths out of a
running session's workspace as a tar stream, name it, and store it as one artifact — one blob per
regular file under a shared `BlobPath` prefix, with per-file size and SHA-256 recorded by
`artifacts.WriteTarToBlobs`. Per-file behaviour is the degenerate case; the status state machine and
the dedup / never-regress rules are unchanged.

## Named images: what shipped, and what did not

An earlier plan for **user images** — "an App image plus a named set of artifacts copied into a
throwaway container, then snapshotted" — is **not what got built**. Two half-finished helpers for it
(`lookupImageCache`, `snapshotPersistCache` in `go/runner.go`) exist and are called by nothing; there
is no `Runner.BuildUserImage` method, despite `Deps.Blobs`' comment implying one.

What shipped instead is the product layer's **image catalogue**
([`product/08-images-and-skills.md`](product/08-images-and-skills.md), spec §13): a named, versioned,
append-only record of a **session snapshot**. `image_create` is a thin naming layer over
`Runner.Snapshot` — an agent snapshots *its own* container (the session is taken from its token,
never from an argument) and a catalogue row is written pointing at the resulting handle. No artifacts
are copied in. Curation is "get a container into the shape you want, then burn it", not "assemble one
from parts".

So the honest distinction is two-way, not three:

- **Artifact** = named files/folders in the `BlobStore` — the portable, container-independent unit
  (download, preview, hand to a human).
- **Snapshot image** = the *whole* filesystem of a session. Anonymous when the archive loop takes it
  (restore this session later); named `name:version` when an agent takes it via `image_create`
  (launch new sessions from it).

Both need per-session file attribution and are therefore unsupported under shared tenancy, except
plain artifact capture from a session that is the only one in its container.

## What's host-owned vs library-owned

| Library-owned (generic) | Host-owned (injected / product-specific) |
|-------------------------|-------------------------------------------|
| `ArtifactStore` interface + status state machine + dedup/never-regress rules | The `BlobStore` backend (GCS / fs / hybrid) — injected |
| `Artifact` portable type + `IsDir` + `Meta` bag | Publishing to a product's files area |
| The `artifact_registered` marker hook, incl. webapp directory capture | `webapp_ready` / tokenised webapp serving |
| `MarkLost` on destroy (via `ExecutionEnvironment.OnDestroy`) | Signed preview URLs; HTTP status mapping for missing bytes |
| In-memory mock with identical semantics | Brand/theme enrichment of artifact metadata (via an `ArtifactEnricher`) |

Where the code lives: the interface and types in `go/artifacts/artifacts.go`, the tar helper in
`dir.go`, the mock in `mock.go`, and the two implementations in `go/extension/dbartifacts/`
(Postgres index — what `cmd/agentd` wires with `DATABASE_URL` set) and
`go/extension/blobartifacts/` (in-process index — the sqlite fallback). The table-side methods are
`go/agentdb/artifacts_durable.go`. Snapshot/archive handling is **not** here — that is snapshot, see
[03](03-image-registry.md).

## Where metadata lives (durability)

Bytes always go to the `BlobStore`. The **metadata index** depends on what `agentd` is wired to:

| `DATABASE_URL` | Store | Index | Survives a restart |
|---|---|---|---|
| set (compose always sets it) | `extension/dbartifacts` | Postgres `agent_artifacts` | **yes** |
| unset (sqlite fallback) | `extension/blobartifacts` | in-process map | **no** |

The index is deliberately **not** in the blob store. An index in object storage cannot be queried,
and every concurrent write becomes a read-modify-write race on one JSON object — a database built on
a bucket. `agent_artifacts` has existed since migration 002, is indexed, and is scoped by
`customer`; migration 033 adds the `meta` jsonb column for the one field of the portable type with
no column of its own (`Meta["dirDigest"]`) plus a `(session_id, file_path)` index for the dedup key.

Blob layout is unchanged and shared by both stores: `_artifacts/bytes/<id>` for a file,
`_artifacts/dirs/<id>/…` for the one-blob-per-file layout of a directory artifact.

> **The sqlite fallback still loses metadata.** It is the pre-existing behaviour, kept rather than
> refusing to boot, and `agentd` logs it loudly at startup. Two things go wrong there, both pinned
> by `blobartifacts`' `TestIndexIsNotDurableAcrossRestart`: rows vanish while their bytes stay in the
> bucket, orphaned; and the in-process ID counter restarts at 1, so the first artifact written after
> a restart takes the blob key of the first one written before it and **overwrites those bytes**.
> `dbartifacts` mints UUIDs, so IDs never collide.

**Tenancy.** Each row carries the `customer` of its session. The `ArtifactStore` interface is
session-keyed and has no project parameter, so project scoping is enforced in two places: `httpapi`'s
artifact routes check session ownership before calling in (404, not 403 — existence is not leaked),
and the table-level reads `ListArtifactsForCustomer` / `GetArtifactForCustomer` (mirrored on
`dbartifacts` as `ListForCustomer` / `LoadForCustomer`) refuse cross-project reads. The negative
tests are `agentdb.TestArtifactProjectIsolation` (+ the live-Postgres twin),
`dbartifacts.TestProjectIsolation`, and `httpapi.TestArtifactRoutesAreProjectScoped`.

**Known orphan path, unchanged by this.** `agent_artifacts.session_id` has been
`REFERENCES agent_sessions(id) ON DELETE CASCADE` since migration 002, so deleting a session drops
its artifact rows while the bytes stay in the blob store. Pinned by
`TestLivePG_ArtifactRowsCascadeWithTheSession`. Nothing sweeps those blobs yet.
