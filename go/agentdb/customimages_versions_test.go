package agentdb

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/google/uuid"
)

// newImageCatalogueTestStore returns a sqlite Store carrying the §13 catalogue
// and the config log it dual-writes into, plus the PARTIAL unique index that
// migration 025 creates on Postgres. The index is not a detail the tests can
// skip: it is what makes concurrent version allocation correct, and it is
// partial so pre-§13 rows (version 0) keep their legacy latest-wins behaviour.
func newImageCatalogueTestStore(t *testing.T) *Store {
	t.Helper()
	s := newCustomImageTestStore(t) // sqlite + AutoMigrate(CustomImage, ConfigEvent)
	if err := s.gdb.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_custom_images_version
		ON agent_custom_images(customer, name, version) WHERE version > 0`).Error; err != nil {
		t.Fatalf("create partial unique index: %v", err)
	}
	return s
}

// burn appends a version the way I2's image_create will: a handle, some labels,
// and the burning worker/session as provenance.
func burn(t *testing.T, s *Store, project, name string, labels LabelSet) *CustomImage {
	t.Helper()
	ci, err := s.CreateCustomImage(context.Background(), &CustomImage{
		Name:           name,
		Customer:       project,
		Labels:         labels,
		RegistryHandle: fmt.Sprintf(`{"kind":"blob-archive","ref":"%s"}`, uuid.New().String()),
	}, ConfigWrite{Worker: "curator", Session: "s-" + name})
	if err != nil {
		t.Fatalf("burn %s/%s: %v", project, name, err)
	}
	return ci
}

// ── Identity: version allocation (§13.2) ────────────────────────────────────

func TestCustomImages_VersionAllocationIsMonotonicAndGapFree(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()

	// Versions climb by exactly one, per name…
	for want := 1; want <= 3; want++ {
		got := burn(t, s, "acme", "toolbox", LabelSet{"purpose": "marketing-toolbox"})
		if got.Version != want {
			t.Fatalf("burn %d: want version %d, got %d", want, want, got.Version)
		}
	}
	// …independently per name…
	if v := burn(t, s, "acme", "vanilla", nil).Version; v != 1 {
		t.Fatalf("a second name starts at 1, got %d", v)
	}
	if v := burn(t, s, "acme", "toolbox", nil).Version; v != 4 {
		t.Fatalf("interleaving another name must not disturb the sequence: got %d", v)
	}
	// …and independently per project (P5: names never cross projects).
	if v := burn(t, s, "globex", "toolbox", nil).Version; v != 1 {
		t.Fatalf("another project's toolbox starts at 1, got %d", v)
	}

	// Reaping bytes must not make a version number reusable: the tombstone
	// still counts toward the high-water mark (§13.7).
	if err := s.MarkCustomImageReaped(ctx, "acme", "toolbox", 4, 1700000000); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if v := burn(t, s, "acme", "toolbox", nil).Version; v != 5 {
		t.Fatalf("a reaped version's number is never handed out again: got %d", v)
	}

	// The unique index really is armed: a hand-rolled duplicate is refused, so
	// two racing burns cannot both claim a version.
	dup := &CustomImage{ID: uuid.New().String(), Name: "toolbox", Customer: "acme", Version: 5, Visibility: "organizational"}
	if err := s.gdb.WithContext(ctx).Create(dup).Error; err == nil {
		t.Fatalf("duplicate (project, name, version) must violate the unique index")
	} else if !isUniqueViolation(err) {
		t.Fatalf("expected a unique violation, got %v", err)
	}
}

func TestCustomImages_CreateAllocatesAndRecordsProvenance(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()

	got, err := s.CreateCustomImage(ctx, &CustomImage{
		Name:           "toolbox",
		Customer:       "acme",
		Labels:         LabelSet{"purpose": "marketing-toolbox", "adds": "ffmpeg"},
		RegistryHandle: `{"kind":"blob-archive","ref":"x"}`,
	}, ConfigWrite{Worker: "marketing-manager", Session: "s-42", Rationale: "curated toolbox"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got.Version != 1 {
		t.Fatalf("first version must be 1, got %d", got.Version)
	}
	// Provenance defaults from the ConfigWrite: the actor in the log and the
	// actor in the catalogue can never disagree.
	if got.CreatedByWorker != "marketing-manager" || got.CreatedBySession != "s-42" {
		t.Fatalf("provenance not recorded: %+v", got)
	}
	if got.CreatedAt == 0 {
		t.Fatalf("created_at must be stamped")
	}
	if got.Labels["adds"] != "ffmpeg" || got.Labels["purpose"] != "marketing-toolbox" {
		t.Fatalf("labels did not round-trip: %#v", got.Labels)
	}
	if got.ReapedAt != 0 {
		t.Fatalf("a fresh version is never tombstoned: %+v", got)
	}

	// The dual write happened (§15.4) and the payload is the full new state.
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list config events: %v", err)
	}
	if len(evs) != 1 || evs[0].Action != ActionImageCreate {
		t.Fatalf("expected exactly one image_create event, got %+v", evs)
	}
	if evs[0].Payload["version"] != float64(1) || evs[0].Payload["name"] != "toolbox" {
		t.Fatalf("config payload must carry the allocated identity: %v", evs[0].Payload)
	}
	if evs[0].ActorWorker != "marketing-manager" || evs[0].Rationale != "curated toolbox" {
		t.Fatalf("actor/rationale not threaded through: %+v", evs[0])
	}
}

func TestCustomImages_CreateValidates(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()

	tooManyLabels := LabelSet{}
	for i := 0; i <= MaxLabelsPerObject; i++ {
		tooManyLabels[fmt.Sprintf("k%d", i)] = "v"
	}

	cases := []struct {
		name string
		in   *CustomImage
	}{
		{"nil image", nil},
		{"no project", &CustomImage{Name: "toolbox"}},
		{"no name", &CustomImage{Customer: "acme"}},
		{"uppercase name", &CustomImage{Customer: "acme", Name: "Toolbox"}},
		{"colon in name", &CustomImage{Customer: "acme", Name: "toolbox:1"}},
		{"leading dash", &CustomImage{Customer: "acme", Name: "-toolbox"}},
		{"caller-supplied version", &CustomImage{Customer: "acme", Name: "toolbox", Version: 7}},
		{"bad label key", &CustomImage{Customer: "acme", Name: "toolbox", Labels: LabelSet{"bad key": "v"}}},
		{"too many labels", &CustomImage{Customer: "acme", Name: "toolbox", Labels: tooManyLabels}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.CreateCustomImage(ctx, tc.in, ConfigWrite{}); err == nil {
				t.Fatalf("expected a validation error")
			} else if !errors.Is(err, ErrCustomImageInvalid) {
				t.Fatalf("want ErrCustomImageInvalid, got %v", err)
			}
		})
	}

	// Nothing was written — validation happens before either row lands.
	rows, err := s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: "acme"})
	if err != nil || len(rows) != 0 {
		t.Fatalf("rejected creates must write nothing: %d rows, err=%v", len(rows), err)
	}
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil || len(evs) != 0 {
		t.Fatalf("rejected creates must log nothing: %d events, err=%v", len(evs), err)
	}
}

// ── The catalogue view (§13.4 image_list) ───────────────────────────────────

func TestCustomImages_ListIsNewestFirstAndProjectScoped(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()

	burn(t, s, "acme", "toolbox", LabelSet{"purpose": "video"})
	burn(t, s, "acme", "toolbox", LabelSet{"purpose": "video"})
	burn(t, s, "acme", "vanilla", LabelSet{"purpose": "base"})
	burn(t, s, "globex", "toolbox", nil)

	got, err := s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 acme versions, got %d", len(got))
	}
	for _, ci := range got {
		if ci.Customer != "acme" {
			t.Fatalf("cross-project leak: %+v", ci)
		}
	}
	// Newest first: created_at descends, and same-second ties break by version.
	for i := 1; i < len(got); i++ {
		prev, cur := got[i-1], got[i]
		if cur.CreatedAt > prev.CreatedAt {
			t.Fatalf("not newest-first: %+v before %+v", prev, cur)
		}
		if cur.CreatedAt == prev.CreatedAt && prev.Name == cur.Name && cur.Version > prev.Version {
			t.Fatalf("same-second tie must break by version DESC: %+v before %+v", prev, cur)
		}
	}

	// Name filter, limit.
	got, err = s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: "acme", Name: "toolbox"})
	if err != nil || len(got) != 2 {
		t.Fatalf("name filter: %d rows err=%v", len(got), err)
	}
	got, err = s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: "acme", Limit: 1})
	if err != nil || len(got) != 1 {
		t.Fatalf("limit: %d rows err=%v", len(got), err)
	}

	// Project is required — the namespace is never inferred (P5).
	if _, err := s.ListCustomImageVersions(ctx, ImageCatalogQuery{}); !errors.Is(err, ErrCustomImageInvalid) {
		t.Fatalf("listing without a project must fail loudly, got %v", err)
	}

	// No hits ⇒ empty, non-nil.
	empty, err := s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: "nobody"})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty listing: %#v err=%v", empty, err)
	}
}

func TestCustomImages_ListExcludesLegacyAndReapedRows(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()

	// A pre-§13 row written by the legacy latest-wins path: version 0, so it is
	// not part of the catalogue.
	if _, err := s.UpsertCustomImage(ctx, &CustomImage{
		Name: "legacy-stack", Customer: "acme", Visibility: "organizational", OwnerEmail: "a@acme.com",
	}, ConfigWrite{}); err != nil {
		t.Fatalf("legacy upsert: %v", err)
	}
	live := burn(t, s, "acme", "toolbox", nil)
	reaped := burn(t, s, "acme", "toolbox", nil)
	if err := s.MarkCustomImageReaped(ctx, "acme", "toolbox", reaped.Version, 1700000000); err != nil {
		t.Fatalf("reap: %v", err)
	}

	got, err := s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Version != live.Version {
		t.Fatalf("catalogue must show only live versioned rows, got %+v", got)
	}

	// The reaper's own view: tombstones included, so it never re-reaps.
	got, err = s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: "acme", IncludeReaped: true})
	if err != nil || len(got) != 2 {
		t.Fatalf("IncludeReaped: %d rows err=%v", len(got), err)
	}

	// CreatedBefore is the reaper's TTL cutoff.
	old, err := s.CreateCustomImage(ctx, &CustomImage{
		Name: "toolbox", Customer: "acme", CreatedAt: 1000, RegistryHandle: `{"ref":"old"}`,
	}, ConfigWrite{})
	if err != nil {
		t.Fatalf("create old: %v", err)
	}
	got, err = s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: "acme", CreatedBefore: 2000})
	if err != nil || len(got) != 1 || got[0].Version != old.Version {
		t.Fatalf("CreatedBefore cutoff: %+v err=%v", got, err)
	}
}

func TestCustomImages_LabelSelectorNeedsPostgres(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	_, err := s.ListCustomImageVersions(context.Background(), ImageCatalogQuery{
		Project: "acme", LabelSelector: "purpose=video",
	})
	if !errors.Is(err, ErrCustomImageInvalid) {
		t.Fatalf("a selector on sqlite must fail loudly rather than silently ignoring the filter, got %v", err)
	}
}

// ── The reaper reconciliation (§13.7) ───────────────────────────────────────

func TestCustomImages_ReapTombstonesRatherThanDeletes(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()
	v1 := burn(t, s, "acme", "toolbox", LabelSet{"purpose": "video"})

	if err := s.MarkCustomImageReaped(ctx, "acme", "toolbox", v1.Version, 1700000000); err != nil {
		t.Fatalf("reap: %v", err)
	}
	// The record survives, with its labels and provenance intact.
	got, err := s.GetCustomImageVersion(ctx, "acme", "toolbox", v1.Version)
	if err != nil {
		t.Fatalf("get after reap: %v", err)
	}
	if got.ReapedAt != 1700000000 || got.Labels["purpose"] != "video" || got.CreatedBySession != "s-toolbox" {
		t.Fatalf("tombstone must keep the record honest, got %+v", got)
	}

	// Re-reaping keeps the original stamp: the honest instant is when the bytes
	// actually went.
	if err := s.MarkCustomImageReaped(ctx, "acme", "toolbox", v1.Version, 1800000000); err != nil {
		t.Fatalf("re-reap: %v", err)
	}
	got, _ = s.GetCustomImageVersion(ctx, "acme", "toolbox", v1.Version)
	if got.ReapedAt != 1700000000 {
		t.Fatalf("re-reap must not move the timestamp, got %d", got.ReapedAt)
	}

	// Reaping something that never existed is an error, not a silent no-op.
	if err := s.MarkCustomImageReaped(ctx, "acme", "toolbox", 99, 0); !errors.Is(err, ErrCustomImageNotFound) {
		t.Fatalf("want ErrCustomImageNotFound, got %v", err)
	}
	if err := s.MarkCustomImageReaped(ctx, "globex", "toolbox", v1.Version, 0); !errors.Is(err, ErrCustomImageNotFound) {
		t.Fatalf("another project's version must not be reapable, got %v", err)
	}

	// Reaping writes no config event: it is storage GC, not curation.
	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(evs) != 1 || evs[0].Action != ActionImageCreate {
		t.Fatalf("only the burn is a configuration mutation, got %+v", evs)
	}
}

// TestCustomImages_SurfaceIsAppendOnly pins §13.2's append-only promise at the
// store surface: no update, no delete of a catalogue version. The legacy
// host-built catalogue keeps its own methods (already exempted in the config
// log); what must never appear is a way to rewrite or erase a version.
func TestCustomImages_SurfaceIsAppendOnly(t *testing.T) {
	storeType := reflect.TypeOf(&Store{})
	for _, forbidden := range []string{
		"UpdateCustomImage",
		"UpdateCustomImageVersion",
		"DeleteCustomImageVersion",
		"SetCustomImageLabels",
	} {
		if _, ok := storeType.MethodByName(forbidden); ok {
			t.Fatalf("%s exists: §13.2 versions are append-only — publish a new version instead", forbidden)
		}
	}
}

// ── Resolution (§13.3) ──────────────────────────────────────────────────────

func TestImageResolve_ParseRef(t *testing.T) {
	ok := []struct {
		in   string
		want ImageRef
	}{
		{"toolbox", ImageRef{Name: "toolbox"}},
		{"  toolbox  ", ImageRef{Name: "toolbox"}},
		{"marketing-tools", ImageRef{Name: "marketing-tools"}},
		{"tool.box_2", ImageRef{Name: "tool.box_2"}},
		{"toolbox:1", ImageRef{Name: "toolbox", Version: 1}},
		{"toolbox:42", ImageRef{Name: "toolbox", Version: 42}},
	}
	for _, tc := range ok {
		got, err := ParseImageRef(tc.in)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parse %q: want %+v, got %+v", tc.in, tc.want, got)
		}
		if got.String() != tc.want.String() {
			t.Fatalf("String() is not a fixed point for %q: %q", tc.in, got.String())
		}
		if got.Pinned() != (tc.want.Version > 0) {
			t.Fatalf("Pinned() wrong for %q", tc.in)
		}
	}

	for _, bad := range []string{"", "   ", "Toolbox", "toolbox:", "toolbox:0", "toolbox:-1", "toolbox:latest", "toolbox:1:2", "-toolbox", "tool box"} {
		if _, err := ParseImageRef(bad); err == nil {
			t.Fatalf("ref %q must not parse", bad)
		} else if !errors.Is(err, ErrCustomImageInvalid) {
			t.Fatalf("ref %q: want ErrCustomImageInvalid, got %v", bad, err)
		}
	}
}

func TestImageResolve_BareNameResolvesLatestAndPinResolvesExactly(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()

	v1 := burn(t, s, "acme", "toolbox", LabelSet{"adds": "ffmpeg"})
	v2 := burn(t, s, "acme", "toolbox", LabelSet{"adds": "imagemagick"})

	// Floating: the pointer follows curation without touching a worker row.
	got, err := s.ResolveCustomImage(ctx, "acme", "toolbox")
	if err != nil {
		t.Fatalf("resolve bare: %v", err)
	}
	if got.Version != v2.Version || got.ID != v2.ID {
		t.Fatalf("bare name must resolve to the latest version, got %d", got.Version)
	}

	// Pinned: stability beats freshness, and a newer burn does not move it.
	got, err = s.ResolveCustomImage(ctx, "acme", fmt.Sprintf("toolbox:%d", v1.Version))
	if err != nil {
		t.Fatalf("resolve pin: %v", err)
	}
	if got.Version != v1.Version || got.ID != v1.ID {
		t.Fatalf("pin must resolve exactly, got %d", got.Version)
	}

	v3 := burn(t, s, "acme", "toolbox", nil)
	got, _ = s.ResolveCustomImage(ctx, "acme", "toolbox")
	if got.Version != v3.Version {
		t.Fatalf("a new burn moves the floating pointer, got %d", got.Version)
	}
	got, _ = s.ResolveCustomImage(ctx, "acme", "toolbox:1")
	if got.Version != 1 {
		t.Fatalf("a new burn must not move a pin, got %d", got.Version)
	}
}

func TestImageResolve_UnknownRefErrorsAndNeverFallsBack(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()

	burn(t, s, "acme", "toolbox", nil)
	burn(t, s, "globex", "other-projects-image", nil)
	// A pre-§13 legacy row is not a catalogue version and must not resolve.
	if _, err := s.UpsertCustomImage(ctx, &CustomImage{
		Name: "legacy-stack", Customer: "acme", Visibility: "organizational", OwnerEmail: "a@acme.com",
	}, ConfigWrite{}); err != nil {
		t.Fatalf("legacy upsert: %v", err)
	}

	for _, tc := range []struct {
		name, project, ref string
	}{
		{"unknown name", "acme", "nope"},
		{"unknown pinned version", "acme", "toolbox:9"},
		{"name from another project", "acme", "other-projects-image"},
		{"pin from another project", "globex", "toolbox:1"},
		{"legacy version-0 row", "acme", "legacy-stack"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ResolveCustomImage(ctx, tc.project, tc.ref)
			if err == nil {
				t.Fatalf("resolution must fail loudly, got %+v — §13.3 forbids falling back to the project default", got)
			}
			if !errors.Is(err, ErrCustomImageNotFound) {
				t.Fatalf("want ErrCustomImageNotFound, got %v", err)
			}
			if got != nil {
				t.Fatalf("a failed resolution returns no image, got %+v", got)
			}
		})
	}

	// A malformed reference is a loud error too, not a name lookup.
	if _, err := s.ResolveCustomImage(ctx, "acme", "toolbox:latest"); !errors.Is(err, ErrCustomImageInvalid) {
		t.Fatalf("malformed ref: %v", err)
	}
	// Project is required.
	if _, err := s.ResolveCustomImage(ctx, "", "toolbox"); !errors.Is(err, ErrCustomImageInvalid) {
		t.Fatalf("resolve without a project must fail, got %v", err)
	}
}

func TestImageResolve_ReapedOrUnmaterialisableVersionsFailLoudly(t *testing.T) {
	s := newImageCatalogueTestStore(t)
	ctx := context.Background()

	burn(t, s, "acme", "toolbox", nil)       // v1, live
	v2 := burn(t, s, "acme", "toolbox", nil) // v2, about to be reaped
	if err := s.MarkCustomImageReaped(ctx, "acme", "toolbox", v2.Version, 1700000000); err != nil {
		t.Fatalf("reap: %v", err)
	}

	// The pin says exactly this version; the answer is "its bytes are gone",
	// never a different version's bytes.
	if _, err := s.ResolveCustomImage(ctx, "acme", "toolbox:2"); !errors.Is(err, ErrCustomImageReaped) {
		t.Fatalf("want ErrCustomImageReaped, got %v", err)
	}
	// The floating pointer resolves the NEWEST version and then insists on it:
	// quietly sliding back to v1 would be the silent substitution §13.3 forbids.
	got, err := s.ResolveCustomImage(ctx, "acme", "toolbox")
	if !errors.Is(err, ErrCustomImageReaped) {
		t.Fatalf("a reaped newest must not silently resolve to an older version, got %+v err=%v", got, err)
	}

	// A version with no registry handle cannot be materialised — also loud.
	if _, err := s.CreateCustomImage(ctx, &CustomImage{Name: "handleless", Customer: "acme"}, ConfigWrite{}); err != nil {
		t.Fatalf("create handleless: %v", err)
	}
	if _, err := s.ResolveCustomImage(ctx, "acme", "handleless"); !errors.Is(err, ErrCustomImageUnmaterialisable) {
		t.Fatalf("want ErrCustomImageUnmaterialisable, got %v", err)
	}
}

// ── Live Postgres: the parts sqlite cannot honestly prove ───────────────────

// TestCustomImages_LivePG_LabelSelectorAndConstraints exercises migration 025's
// real DDL: the jsonb labels column and its selector translation (D1's parser,
// reused verbatim), and the PARTIAL unique index that both guards catalogue
// identity and leaves legacy version-0 rows alone.
func TestCustomImages_LivePG_LabelSelectorAndConstraints(t *testing.T) {
	s := openLivePG(t)
	ctx := context.Background()
	acme := "proj-" + uuid.New().String()
	globex := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		s.DB().Exec("DELETE FROM agent_custom_images WHERE customer IN (?, ?)", acme, globex)
		s.DB().Exec("DELETE FROM config_events WHERE project IN (?, ?)", acme, globex)
	})

	burn(t, s, acme, "toolbox", LabelSet{"purpose": "video", "adds": "ffmpeg"})
	burn(t, s, acme, "toolbox", LabelSet{"purpose": "video", "adds": "imagemagick"})
	burn(t, s, acme, "vanilla", LabelSet{"purpose": "base"})
	burn(t, s, globex, "toolbox", LabelSet{"purpose": "video"})

	for _, tc := range []struct {
		name     string
		selector string
		want     int
	}{
		{"equality", "purpose=video", 2},
		{"conjunction", "purpose=video,adds=ffmpeg", 1},
		{"inequality", "purpose!=video", 1},
		{"set membership", "purpose in (video, base)", 3},
		{"exists", "adds", 2},
		{"not exists", "!adds", 1},
		{"no match", "purpose=nonesuch", 0},
		{"empty selector lists everything", "", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: acme, LabelSelector: tc.selector})
			if err != nil {
				t.Fatalf("list %q: %v", tc.selector, err)
			}
			if len(got) != tc.want {
				t.Fatalf("selector %q: want %d rows, got %d (%+v)", tc.selector, tc.want, len(got), got)
			}
			for _, ci := range got {
				if ci.Customer != acme {
					t.Fatalf("selector %q leaked across projects: %+v", tc.selector, ci)
				}
			}
		})
	}

	// A malformed selector fails loudly rather than matching everything.
	if _, err := s.ListCustomImageVersions(ctx, ImageCatalogQuery{Project: acme, LabelSelector: "purpose in (video"}); err == nil {
		t.Fatalf("malformed selector must error")
	}

	// Labels really are jsonb: the server can index into them.
	var purpose string
	if err := s.DB().Raw(
		"SELECT labels->>'purpose' FROM agent_custom_images WHERE customer = ? AND name = ? AND version = 1", acme, "toolbox",
	).Scan(&purpose).Error; err != nil {
		t.Fatalf("jsonb query: %v", err)
	}
	if purpose != "video" {
		t.Fatalf("jsonb ->> extraction: got %q", purpose)
	}

	// The unique index is partial: catalogue identity is enforced…
	dup := &CustomImage{ID: uuid.New().String(), Name: "toolbox", Customer: acme, Version: 1, Visibility: "organizational"}
	if err := s.DB().WithContext(ctx).Create(dup).Error; err == nil {
		t.Fatalf("duplicate catalogue version must violate the unique index")
	}
	// …while pre-§13 rows (version 0) may still repeat a name across scopes.
	for _, owner := range []string{"a@acme.com", "b@acme.com"} {
		legacy := &CustomImage{
			ID: uuid.New().String(), Name: "legacy-stack", Customer: acme,
			Visibility: "private", OwnerEmail: owner,
		}
		if err := s.DB().WithContext(ctx).Create(legacy).Error; err != nil {
			t.Fatalf("legacy version-0 rows must be unaffected by the partial index: %v", err)
		}
	}

	// Resolution and project isolation over the live schema.
	got, err := s.ResolveCustomImage(ctx, acme, "toolbox")
	if err != nil || got.Version != 2 {
		t.Fatalf("live resolve bare: %+v err=%v", got, err)
	}
	if _, err := s.ResolveCustomImage(ctx, globex, "vanilla"); !errors.Is(err, ErrCustomImageNotFound) {
		t.Fatalf("cross-project resolve must be not-found, got %v", err)
	}
}
