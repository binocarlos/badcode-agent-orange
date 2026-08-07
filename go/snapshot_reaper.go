package agentkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// The snapshot TTL reaper — spec §5 (docs/product/01-session-config.md) and
// §13.7 (docs/product/08-images-and-skills.md).
//
// # What it does
//
// Every snapshot carries {source session, created_at, expiry, last_resumed_at}.
// `project_settings.snapshot_ttl_days` sets the expiry at snapshot time — 30 by
// default, 0 = never. This reaper deletes the bytes of snapshots whose expiry
// has passed and leaves the catalogue record behind as a tombstone.
//
// # Use defers the reap (RD9)
//
// `last_resumed_at` is stamped on every launch (cmd/agentd/imageresolver.go).
// Until 2026-08-06 it was purely informational, so an image a worker launched
// from every single day was reaped the instant its burn-time promise ran out,
// and the next job failed with ErrCustomImageReaped — the pinned-image case has
// no fallback. That is the storage bill being collected from the user's running
// system instead of from their unused bytes.
//
// So the reap is now DEFERRED while a version is in use: an image resumed
// within the project's current `snapshot_ttl_days` window is kept for this pass
// and counted in ReapReport.Deferred, loudly. Effectively the expiry is
// max(expires_at, last_resumed_at + ttl), computed here rather than written
// back, which matters three ways: the stamped `expires_at` keeps meaning "the
// promise made at burn time" and stays honest testimony; the launch path (which
// is best-effort and must never fail a launch) gains no second write; and the
// deferral evaporates by itself — stop using an image and it dies on the first
// pass after the window, so storage is still bounded by policy, not by traffic.
//
// The reaper does NOT become vacuous: an untouched expired version is reaped
// exactly as before, and a version whose last resume is older than the window
// is reaped too.
//
// # Tombstones, not exemption (§13.7)
//
// §13 makes images append-only at the tool surface: no agent may delete a
// version. §5 makes storage GC the operator's business. The two are reconciled
// by TOMBSTONING — exempting referenced versions would make the reaper a no-op,
// because every catalogue row is referenced by construction. So the bytes go
// and the record stays: history, provenance and the version high-water mark all
// survive, a reaped number is never reissued, and resolving a reaped version
// fails with ErrCustomImageReaped instead of pointing at nothing.
//
// # Order of operations — bytes first, then the tombstone
//
// A crash between the two leaves ONE record that resolves but whose bytes are
// gone, which the next pass fixes. The reverse order orphans the bytes forever:
// once the record is tombstoned, nothing remembers which blob to delete. So:
// Registry.Remove first, MarkCustomImageReaped second, and never the tombstone
// when the removal failed.
//
// # The reaper must NOT run with the config-event write guard installed
//
// MarkCustomImageReaped writes `agent_custom_images` — a table registered in
// agentdb.ConfigMutations and therefore under agentdb.InstallConfigEventGuard —
// without a config event, deliberately: reaping is storage policy, not a
// configuration decision an agent made, and §15.3's closed vocabulary has no
// verb for it. The guard is opt-in (tests install it), so this is a constraint
// on how a host wires its store, and it is pinned by TestSnapshotReaper here.

// SnapshotCatalog is the narrow store surface the reaper needs. *agentdb.Store
// satisfies it; a host with its own catalogue can implement it.
type SnapshotCatalog interface {
	// ListCatalogueProjects returns every project holding live catalogue images.
	ListCatalogueProjects(ctx context.Context) ([]string, error)
	// GetProjectSettings supplies snapshot_ttl_days (§5).
	GetProjectSettings(ctx context.Context, project string) (*agentdb.ProjectSettings, error)
	// ListCustomImageVersions is the driver query.
	ListCustomImageVersions(ctx context.Context, q agentdb.ImageCatalogQuery) ([]*agentdb.CustomImage, error)
	// MarkCustomImageReaped tombstones a version whose bytes are gone.
	MarkCustomImageReaped(ctx context.Context, project, name string, version int, reapedAt int64) error
}

// SnapshotReaper deletes expired snapshot images. It is a plain value with no
// background state: call ReapAll from a loop (the Runner does, when
// Policy.SnapshotReapInterval is set) or from an operator command.
type SnapshotReaper struct {
	// Catalog is the image catalogue. Required.
	Catalog SnapshotCatalog
	// Registry owns the bytes. Required — without it the reaper would tombstone
	// records whose blobs are still being paid for, which is worse than doing
	// nothing.
	Registry imageregistry.ImageRegistry
	// Now is the clock seam, for tests. nil = time.Now.
	Now func() time.Time
	// Logf is where a deferred reap is announced. nil = log.Printf. A deferral
	// is the one outcome of a pass that an operator may want to act on (burn a
	// fresh version, or shorten the TTL), so it is never silent.
	Logf func(format string, args ...any)
}

func (sr *SnapshotReaper) logf(format string, args ...any) {
	if sr.Logf != nil {
		sr.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// ReapReport is the receipt of one pass.
type ReapReport struct {
	// Projects swept (those whose TTL is non-zero).
	Projects int
	// Scanned is the number of catalogue versions the driver query returned.
	Scanned int
	// Reaped is the number whose bytes were deleted and record tombstoned.
	Reaped int
	// Kept is the number that came back from the driver query but whose stamped
	// expiry had not passed — the TTL was longer when they were burned.
	Kept int
	// Deferred is the number whose stamped expiry HAS passed but which a session
	// launched from inside the project's current TTL window (RD9). Their bytes
	// stay for this pass. Distinct from Kept on purpose: Kept is "not due yet",
	// Deferred is "due, and spared because it is still in daily use" — the
	// second is the number an operator watching a storage bill wants to see.
	Deferred int
	// Errors are per-image failures. One bad image never stops the pass: the
	// next one may be reapable, and a failed image is retried next pass.
	Errors []error
}

func (r ReapReport) add(o ReapReport) ReapReport {
	r.Projects += o.Projects
	r.Scanned += o.Scanned
	r.Reaped += o.Reaped
	r.Kept += o.Kept
	r.Deferred += o.Deferred
	r.Errors = append(r.Errors, o.Errors...)
	return r
}

// Err folds the per-image failures into one error, or nil.
func (r ReapReport) Err() error { return errors.Join(r.Errors...) }

func (sr *SnapshotReaper) now() int64 {
	if sr.Now != nil {
		return sr.Now().Unix()
	}
	return time.Now().Unix()
}

// ReapAll sweeps every project that owns live catalogue images.
func (sr *SnapshotReaper) ReapAll(ctx context.Context) (ReapReport, error) {
	if sr == nil || sr.Catalog == nil {
		return ReapReport{}, fmt.Errorf("snapshot reaper: a catalogue is required")
	}
	projects, err := sr.Catalog.ListCatalogueProjects(ctx)
	if err != nil {
		return ReapReport{}, fmt.Errorf("snapshot reaper: list projects: %w", err)
	}
	var total ReapReport
	for _, p := range projects {
		rep, err := sr.ReapProject(ctx, p)
		if err != nil {
			// An infrastructure failure in one project (settings unreadable, query
			// broken) must not silence the others.
			total.Errors = append(total.Errors, fmt.Errorf("project %s: %w", p, err))
			continue
		}
		total = total.add(rep)
	}
	return total, nil
}

// ReapProject sweeps one project.
//
// The TTL is read fresh each pass and decides how far back to LOOK; each row's
// stamped `expires_at` decides whether it may actually go. They coincide while
// the setting is unchanged; when an operator lengthens the TTL the stamp keeps
// already-burned images alive for the promise they were given, and when the
// operator sets 0 nothing in the project is reaped at all.
func (sr *SnapshotReaper) ReapProject(ctx context.Context, project string) (ReapReport, error) {
	var rep ReapReport
	if sr == nil || sr.Catalog == nil {
		return rep, fmt.Errorf("snapshot reaper: a catalogue is required")
	}
	if sr.Registry == nil {
		return rep, fmt.Errorf("snapshot reaper: a registry is required — tombstoning without deleting the bytes would orphan them forever")
	}
	if strings.TrimSpace(project) == "" {
		return rep, fmt.Errorf("snapshot reaper: project is required (P5)")
	}

	ps, err := sr.Catalog.GetProjectSettings(ctx, project)
	if err != nil {
		return rep, fmt.Errorf("read snapshot_ttl_days: %w", err)
	}
	if ps.SnapshotTTLDays <= 0 {
		return rep, nil // §5: 0 = never reap. Nothing is even listed.
	}
	rep.Projects = 1

	now := sr.now()
	window := int64(ps.SnapshotTTLDays) * agentdb.SecondsPerDay
	cutoff := now - window
	stale, err := sr.Catalog.ListCustomImageVersions(ctx, agentdb.ImageCatalogQuery{
		Project:       project,
		CreatedBefore: cutoff,
		// IncludeReaped stays false: never re-reap what is already tombstoned.
	})
	if err != nil {
		return rep, fmt.Errorf("list stale versions: %w", err)
	}
	rep.Scanned = len(stale)

	for _, ci := range stale {
		if !snapshotExpired(ci, now) {
			rep.Kept++
			continue
		}
		if snapshotInUse(ci, now, window) {
			// RD9: due, but a session launched from it inside the window. Say so
			// — silence here is how an image in daily use disappears.
			rep.Deferred++
			sr.logf("agentkit: snapshot reaper: %s/%s:%d expired at %d but was resumed %d day(s) ago — "+
				"deferring the reap until it has been unused for %d day(s) (project_settings.snapshot_ttl_days); "+
				"burn a fresh version or shorten the TTL if you want the bytes back sooner",
				project, ci.Name, ci.Version, ci.ExpiresAt,
				(now-ci.LastResumedAt)/agentdb.SecondsPerDay, ps.SnapshotTTLDays)
			continue
		}
		if err := sr.reapOne(ctx, project, ci, now); err != nil {
			rep.Errors = append(rep.Errors, err)
			continue
		}
		rep.Reaped++
	}
	return rep, nil
}

// snapshotExpired applies the stamped promise. `expires_at == 0` means never —
// either the project's TTL was 0 when the image was burned, or the row predates
// the TTL metadata entirely and so was never promised an expiry.
func snapshotExpired(ci *agentdb.CustomImage, now int64) bool {
	return ci.ExpiresAt > 0 && ci.ExpiresAt <= now
}

// snapshotInUse answers "did a session launch from this version recently enough
// that reaping it would break a running system?" (RD9).
//
// `window` is the project's CURRENT snapshot_ttl_days in seconds — read fresh
// each pass, like the driver-query cutoff, so shortening the TTL shortens the
// grace too and an operator who wants the bytes back has a lever. A zero window
// cannot occur here (ReapProject returns early on TTL 0) but is handled anyway:
// no window, no deferral. `last_resumed_at == 0` means never resumed — nothing
// is in use, so nothing is deferred, which is exactly the row the reaper exists
// for.
func snapshotInUse(ci *agentdb.CustomImage, now, window int64) bool {
	return window > 0 && ci.LastResumedAt > 0 && ci.LastResumedAt+window > now
}

// reapOne deletes the bytes and THEN tombstones the record. Never the reverse.
func (sr *SnapshotReaper) reapOne(ctx context.Context, project string, ci *agentdb.CustomImage, now int64) error {
	if h, ok, err := snapshotHandle(ci); err != nil {
		return fmt.Errorf("image %s:%d: %w", ci.Name, ci.Version, err)
	} else if ok {
		if err := sr.Registry.Remove(ctx, h); err != nil {
			// Bytes may still exist, so the record must stay resolvable: a
			// tombstone here would strand them. Retried next pass.
			return fmt.Errorf("image %s:%d: delete bytes: %w", ci.Name, ci.Version, err)
		}
	}
	// No handle means there are no bytes to delete — the version was never
	// materialisable. Tombstoning it is still right: it is expired, and the
	// record says so honestly.
	if err := sr.Catalog.MarkCustomImageReaped(ctx, project, ci.Name, ci.Version, now); err != nil {
		return fmt.Errorf("image %s:%d: tombstone (bytes are already gone — the next pass repairs this): %w",
			ci.Name, ci.Version, err)
	}
	return nil
}

// snapshotHandle decodes the durable pointer stored on the catalogue row.
func snapshotHandle(ci *agentdb.CustomImage) (imageregistry.Handle, bool, error) {
	raw := strings.TrimSpace(ci.RegistryHandle)
	if raw == "" {
		return imageregistry.Handle{}, false, nil
	}
	var h imageregistry.Handle
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return imageregistry.Handle{}, false, fmt.Errorf("registry handle is not decodable: %w", err)
	}
	if h.Ref == "" && h.Kind == "" {
		return imageregistry.Handle{}, false, nil
	}
	return h, true, nil
}

// snapshotReapLoop is the Runner's wiring point: the same shape as archiveLoop,
// started by Start when Policy.SnapshotReapInterval is set and Deps.Snapshots is
// wired. The archive loop CREATES snapshots on idle; this one retires them.
func (r *runnerImpl) snapshotReapLoop() {
	interval := r.deps.Policy.SnapshotReapInterval
	t := time.NewTicker(interval)
	defer t.Stop()
	reaper := &SnapshotReaper{Catalog: r.deps.Snapshots, Registry: r.deps.Registry}
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			rep, err := reaper.ReapAll(context.Background())
			if err != nil {
				log.Printf("agentkit: snapshot reaper: %v", err)
				continue
			}
			if err := rep.Err(); err != nil {
				log.Printf("agentkit: snapshot reaper: %d/%d reaped, errors: %v", rep.Reaped, rep.Scanned, err)
			}
			if rep.Reaped > 0 || rep.Deferred > 0 {
				log.Printf("agentkit: snapshot reaper: reaped %d expired image(s) across %d project(s); "+
					"%d deferred as still in use", rep.Reaped, rep.Projects, rep.Deferred)
			}
		}
	}
}
