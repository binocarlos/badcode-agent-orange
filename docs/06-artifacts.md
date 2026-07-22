# 06 — Artifacts (and why they are not snapshots)

The agent produces two very different kinds of persisted bytes, and conflating them is the most common
way these systems get muddled. The library keeps them on separate interfaces with separate lifecycles.

| | **Snapshot** | **Artifact** |
|---|---|---|
| What | The *whole filesystem* of a session, as an image | A *single user-facing file* the agent produced |
| Why | Resurrect/suspend the session; publish it as a reusable app | Let the user download/preview/pin a deliverable |
| Interface | `ExecutionEnvironment.Snapshot` + `ImageRegistry` ([02](02-execution-environment.md), [03](03-image-registry.md)) | `ArtifactStore` (this doc) |
| Granularity | One per session (latest), opaque | Many per session, each with type/label/status |
| Lifecycle | live container → image → durable handle → restored container | live-in-workspace → extracted-to-blob → (or lost) |

A session can be snapshotted (so it resumes tomorrow) while *also* having ten artifacts (a report, a
chart JSON, a generated web app). These are orthogonal operations on orthogonal interfaces.

## The `ArtifactStore` contract

This interface carries the proven status semantics of the current artifact store into the library.

```go
package artifacts

import (
	"context"
	"io"
)

// ArtifactStore persists and retrieves agent artifacts — files produced in the session
// workspace and registered for download/preview. The real impl wraps a metadata store
// (rows) + a BlobStore (bytes); the mock keeps both in memory.
type ArtifactStore interface {
	// Save upserts artifact metadata (dedup on session_id + file_path) and, when content
	// is non-nil, uploads bytes and sets Status="extracted". Preserves the
	// live → extracted, never-regress rule.
	Save(ctx context.Context, art *Artifact, content io.Reader) (*Artifact, error)

	// Load returns metadata plus an open reader for the bytes. reader is nil if the
	// artifact is metadata-only (e.g. status "lost").
	Load(ctx context.Context, artifactID string) (*Artifact, io.ReadCloser, error)

	// List returns all artifacts for a session.
	List(ctx context.Context, sessionID string) ([]*Artifact, error)

	// MarkLost flags all still-"live" artifacts for a session as lost — called when the
	// instance is destroyed before extraction.
	MarkLost(ctx context.Context, sessionID string) error
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

Three non-obvious rules the real impl **must** keep (encoded in `go/artifacts/artifacts.go` and its
mock, so `artifacts_test.go` catches regressions):

1. **Never regress `extracted` → `live`.** A `Save` that arrives with `live` after the artifact is
   already `extracted` keeps `extracted`.
2. **`MarkLost` promotes instead of losing when a blob exists.** If a "live" artifact already has a
   `BlobPath`, `MarkLost` makes it `extracted`, not `lost` — the bytes are safe even though the
   container is gone.
3. **`Source` is write-once.** Once set, it's never overwritten by a later `Save`.

## The three extraction patterns

There are three ways an artifact's bytes reach blob storage. All three go through `ArtifactStore.Save`,
with the host deciding which pattern fires:

1. **Eager upload on create** — host registers `live`, then a background goroutine pulls the file from
   the workspace (`ExecutionEnvironment.Exec` / the in-image `/workspace/files/*` endpoint) and
   `Save`s with content → `extracted`.
2. **SSE-triggered** — the `artifact_registered` marker in the live stream triggers the host hook,
   which pulls + `Save`s directly as `extracted` and injects `artifacts_updated`.
3. **Direct extraction** — the in-image agent hands base64 content out-of-band; host `Save`s
   immediately.

The *pulling* of bytes from the workspace is an `ExecutionEnvironment`/sandbox-contract concern; the
*storing* is `ArtifactStore`. The library wires pattern (2) by default (via the event pipeline hook)
and exposes the others as host-callable helpers.

## Download / self-heal

`Load` mirrors `downloadAgentArtifact`'s logic:

- **`extracted`** → open the blob (via the host's `BlobStore`); the library returns the reader.
- **`live`** → self-heal: if the blob actually exists, promote to `extracted` and serve; if not,
  return "still preparing" (the host maps this to HTTP 202).
- **`lost`** → return a sentinel error (host maps to 410 Gone).

The host owns the HTTP status mapping; the `ArtifactStore` returns typed states/errors.

## Webapp artifacts (a worked example of host specificity)

A "webapp" artifact type extracts an entire `dist/` tree and serves it behind a tokenised
URL, emitting a `webapp_ready` event. This is **not** core — it's a host pattern built
on the generic pieces: a webapp is an artifact whose `ArtifactType="webapp"`, whose extraction copies a
*directory* (host loop over `List`/`Save`), and whose readiness event is a host-registered extension
event. The generic core ships single-file artifacts; multi-file/webapp is a documented host recipe.

## Folder artifacts (generalized capture) and their link to user images

The v0 `ArtifactStore` captured a **single file** per artifact. The redesign generalises this to a
**named folder/file-set capture**: at any point you can tell a running (isolated) session "slurp this
set of paths out of the workspace, name it, and store it in the BlobStore as one artifact." The bytes
are pulled via the in-image agent (`GET /workspace/files/*`) or `ExecutionEnvironment.Exec` + tar, then
`Save`d. Per-file behaviour is the degenerate case (a one-path set); the status state machine and
dedup/never-regress rules are unchanged.

This folder-capture is the **building block for user images** (a roadmap feature — see
[03](03-image-registry.md)). A user image is "an App image + a named set of artifacts copied in, then
snapshotted" — so the intended flow is: capture the useful files as a folder artifact → a user-image
build launches a throwaway container, copies those artifacts in, and snapshots. The artifact is the
*portable, container-independent* unit; the user image is the *re-launchable* unit derived from it.

Note the three-way distinction this completes:
- **Artifact** = named files/folders in the BlobStore (download/preview/seed a user image).
- **Session-snapshot image** = the *whole* filesystem of a live isolated session (archive/restore).
- **User image** = App image + curated artifacts, snapshotted (re-launchable capability).

All three are unsupported under shared tenancy (no per-session file attribution) except plain artifact
capture from a session that is itself the only one in its container.

## What's host-owned vs library-owned

| Library-owned (generic) | Host-owned (injected / product-specific) |
|-------------------------|-------------------------------------------|
| `ArtifactStore` interface + status state machine + dedup/never-regress rules | The `BlobStore` backend (GCS / fs / hybrid) — injected |
| `Artifact` portable type + `Meta` bag | Publishing to a product's files area |
| The three extraction patterns as wired hooks | `webapp_ready` / tokenised webapp serving |
| `MarkLost` on destroy (via `ExecutionEnvironment.OnDestroy`) | Signed preview URLs |
| In-memory mock with identical semantics | Brand/theme enrichment of artifact metadata (via an `ArtifactEnricher`) |

The library-owned pieces live in `go/artifacts/` (`artifacts.go` real impl + `mock.go`, with folder
capture in `dir.go`); snapshot/archive handlers are **not** here — those are snapshot, see
[03](03-image-registry.md).
