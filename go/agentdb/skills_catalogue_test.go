package agentdb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// I3 — the §14 skills catalogue at the store level.
//
// The invariants worth a test, in order of what they would cost to get wrong:
// a project cannot see another project's skills; `skill_create` on an existing
// name APPENDS rather than overwrites and reads resolve newest-wins; the
// pre-§14 host-built rows in the same table are invisible; and every write
// appends exactly one `skill_create` config event with its provenance.
// ---------------------------------------------------------------------------

// newSkillCatalogueTestStore returns a sqlite Store carrying the §14 catalogue
// and the config log it dual-writes into.
func newSkillCatalogueTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "skills_test.sqlite")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&Skill{}, &ConfigEvent{}); err != nil {
		t.Fatalf("automigrate Skill + ConfigEvent: %v", err)
	}
	// Migration 028's PARTIAL unique index, which AutoMigrate cannot express.
	// It is not a detail the tests may skip: it is what makes concurrent
	// revision allocation correct, and it is partial so the pre-§14 rows (which
	// all carry revision 0) keep their legacy latest-wins behaviour.
	if err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_skills_revision
		ON agent_skills(customer, name, revision) WHERE markdown <> ''`).Error; err != nil {
		t.Fatalf("create partial unique index: %v", err)
	}
	return &Store{gdb: db}
}

// teach records a revision the way I3's skill_create will. createdAt is
// explicit because created_at on this table is SECONDS: a test that let the
// clock decide would be asserting nothing about ordering, since everything it
// writes lands in the same second. 0 = let the store stamp it.
func teachAt(t *testing.T, s *Store, project, name, markdown, install string, labels LabelSet, createdAt int64) *Skill {
	t.Helper()
	sk, err := s.CreateSkill(context.Background(), &Skill{
		Name:      name,
		Customer:  project,
		Labels:    labels,
		Markdown:  markdown,
		InstallSh: install,
		CreatedAt: createdAt,
	}, ConfigWrite{Worker: "curator", Session: "s-" + name})
	if err != nil {
		t.Fatalf("teach %s/%s: %v", project, name, err)
	}
	return sk
}

func teach(t *testing.T, s *Store, project, name, markdown, install string, labels LabelSet) *Skill {
	t.Helper()
	return teachAt(t, s, project, name, markdown, install, labels, 0)
}

// ── Validation (§9: malformed input fails loudly, never half-writes) ────────

func TestSkillsCreateValidation(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		in   *Skill
		want error
	}{
		{"no project", &Skill{Name: "render-video", Markdown: "# doc"}, ErrSkillInvalid},
		{"no name", &Skill{Customer: "acme", Markdown: "# doc"}, ErrSkillInvalid},
		{"no markdown", &Skill{Customer: "acme", Name: "render-video"}, ErrSkillInvalid},
		{"blank markdown", &Skill{Customer: "acme", Name: "render-video", Markdown: "   "}, ErrSkillInvalid},
		{"upper-case name", &Skill{Customer: "acme", Name: "RenderVideo", Markdown: "# doc"}, ErrSkillInvalid},
		{"dotted name", &Skill{Customer: "acme", Name: "render.video", Markdown: "# doc"}, ErrSkillInvalid},
		{"traversal in name", &Skill{Customer: "acme", Name: "../etc", Markdown: "# doc"}, ErrSkillInvalid},
		{"slash in name", &Skill{Customer: "acme", Name: "a/b", Markdown: "# doc"}, ErrSkillInvalid},
		{"trailing dash", &Skill{Customer: "acme", Name: "render-", Markdown: "# doc"}, ErrSkillInvalid},
		{"free-text label", &Skill{Customer: "acme", Name: "render-video", Markdown: "# doc",
			Labels: LabelSet{"about": "how to render a video"}}, ErrSkillInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSkillCatalogueTestStore(t)
			if _, err := s.CreateSkill(ctx, tc.in, ConfigWrite{}); !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			// Nothing may have landed — not the row, and not the log line.
			var rows, evs int64
			s.gdb.Model(&Skill{}).Count(&rows)
			s.gdb.Model(&ConfigEvent{}).Count(&evs)
			if rows != 0 || evs != 0 {
				t.Fatalf("a rejected create wrote %d row(s) and %d event(s) — validation must precede both", rows, evs)
			}
		})
	}
}

func TestSkillsNameIsAKebabCaseDirectoryName(t *testing.T) {
	ok := []string{"render-social-video", "a", "a1", "video2gif", "x-y-z"}
	for _, n := range ok {
		if err := ValidateSkillName(n); err != nil {
			t.Fatalf("%q should be legal: %v", n, err)
		}
	}
	// Everything that could escape the harness's skills directory, plus the
	// charset the image names allow but a directory name should not.
	bad := []string{"", "..", "../x", "a/b", "a\\b", "A", "a_b", "a.b", "-a", "a-", "a b", "a:b"}
	for _, n := range bad {
		if err := ValidateSkillName(n); err == nil {
			t.Fatalf("%q must be rejected — it becomes a directory name", n)
		}
	}
}

// ── Append-only versioning + newest-wins resolution (§14.1) ────────────────

func TestSkillsCreateAppendsARevision(t *testing.T) {
	s := newSkillCatalogueTestStore(t)
	ctx := context.Background()

	first := teach(t, s, "acme", "render-video", "# v1", "apt-get install -y ffmpeg", LabelSet{"kind": "media"})
	second := teach(t, s, "acme", "render-video", "# v2", "apt-get install -y ffmpeg imagemagick", LabelSet{"kind": "media", "tier": "pro"})

	if first.ID == second.ID {
		t.Fatalf("a second create must APPEND a row, not overwrite one (§14.1)")
	}
	var count int64
	s.gdb.Model(&Skill{}).Where("name = ?", "render-video").Count(&count)
	if count != 2 {
		t.Fatalf("want 2 stored revisions, got %d", count)
	}

	// Resolution is newest-wins, in full.
	got, err := s.GetProjectSkill(ctx, "acme", "render-video")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != second.ID || got.Markdown != "# v2" {
		t.Fatalf("get must return the newest revision, got %q (%s)", got.Markdown, got.ID)
	}
	if got.InstallSh != "apt-get install -y ffmpeg imagemagick" {
		t.Fatalf("get returns the record in full including install_sh, got %q", got.InstallSh)
	}

	// …and the superseded revision is still there, unaltered.
	old, err := s.getSkillRevision(ctx, "acme", first.ID)
	if err != nil {
		t.Fatalf("the superseded revision must survive: %v", err)
	}
	if old.Markdown != "# v1" {
		t.Fatalf("superseded revision was mutated: %q", old.Markdown)
	}
}

// The reason the revision ordinal exists (migration 028): created_at is
// SECONDS and the id is a random uuid, so without it two teachings inside one
// second would order by coin toss and skill_get could hand back the superseded
// document. Every revision here shares a timestamp on purpose.
func TestSkillsNewestWinsWithinOneSecond(t *testing.T) {
	s := newSkillCatalogueTestStore(t)
	ctx := context.Background()

	var last *Skill
	for i := 1; i <= 6; i++ {
		last = teachAt(t, s, "acme", "render-video", "# v"+string(rune('0'+i)), "", nil, 1000)
		if last.Revision != i {
			t.Fatalf("revisions must be monotonic and gap-free: want %d, got %d", i, last.Revision)
		}
	}
	got, err := s.GetProjectSkill(ctx, "acme", "render-video")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != last.ID || got.Revision != 6 {
		t.Fatalf("newest-wins must be the newest REVISION, got revision %d (%s)", got.Revision, got.ID)
	}
	list, err := s.ListProjectSkills(ctx, SkillCatalogQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Revision != 6 || list[0].Revisions != 6 {
		t.Fatalf("list must carry the current revision and the count: %+v", list)
	}
	// A caller may not fabricate an identity.
	if _, err := s.CreateSkill(ctx, &Skill{
		Name: "render-video", Customer: "acme", Markdown: "# forged", Revision: 99,
	}, ConfigWrite{}); !errors.Is(err, ErrSkillInvalid) {
		t.Fatalf("a supplied revision must be refused, got %v", err)
	}
}

func TestSkillsGetUnknownNameIsNotFound(t *testing.T) {
	s := newSkillCatalogueTestStore(t)
	if _, err := s.GetProjectSkill(context.Background(), "acme", "never-taught"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("want ErrSkillNotFound, got %v", err)
	}
}

// ── Listing: one entry per name, newest first, no markdown (§14.2) ─────────

func TestSkillsListReturnsCurrentRevisionPerName(t *testing.T) {
	s := newSkillCatalogueTestStore(t)
	ctx := context.Background()

	teachAt(t, s, "acme", "render-video", "# v1", "", LabelSet{"kind": "media"}, 1000)
	teachAt(t, s, "acme", "render-video", "# v2", "echo hi", LabelSet{"kind": "media"}, 1010)
	teachAt(t, s, "acme", "write-brief", "# brief", "", LabelSet{"kind": "writing"}, 1020)

	got, err := s.ListProjectSkills(ctx, SkillCatalogQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want one entry per NAME (2), got %d", len(got))
	}
	// Newest first: write-brief was taught last.
	if got[0].Name != "write-brief" || got[1].Name != "render-video" {
		t.Fatalf("want newest-first by name, got %s then %s", got[0].Name, got[1].Name)
	}
	if got[1].Revisions != 2 {
		t.Fatalf("render-video was retaught once: want 2 revisions, got %d", got[1].Revisions)
	}
	if !got[1].HasInstallScript {
		t.Fatalf("the CURRENT render-video revision has an install script")
	}
	if got[0].HasInstallScript {
		t.Fatalf("write-brief brings no software")
	}
	if got[0].CreatedByWorker != "curator" || got[0].CreatedBySession != "s-write-brief" {
		t.Fatalf("provenance missing from the listing: %+v", got[0])
	}
}

// The selector must be applied to the SURVIVING revision, not to any revision:
// a label dropped by a newer teaching is gone, and a row that only an old
// revision matched must not surface as if it were current.
func TestSkillsListSelectorAppliesToTheCurrentRevision(t *testing.T) {
	s := newSkillCatalogueTestStore(t)
	ctx := context.Background()

	teachAt(t, s, "acme", "render-video", "# v1", "", LabelSet{"kind": "media", "status": "experimental"}, 1000)
	teachAt(t, s, "acme", "render-video", "# v2", "", LabelSet{"kind": "media"}, 1010) // status dropped
	teachAt(t, s, "acme", "write-brief", "# brief", "", LabelSet{"kind": "writing"}, 1020)

	cases := []struct {
		selector string
		want     []string
	}{
		{"kind=media", []string{"render-video"}},
		{"kind=writing", []string{"write-brief"}},
		{"kind in (media, writing)", []string{"write-brief", "render-video"}},
		{"status=experimental", nil}, // the newest render-video no longer carries it
		{"!status", []string{"write-brief", "render-video"}},
		{"exists kind", []string{"write-brief", "render-video"}},
		{"kind!=media", []string{"write-brief"}},
	}
	for _, tc := range cases {
		t.Run(tc.selector, func(t *testing.T) {
			got, err := s.ListProjectSkills(ctx, SkillCatalogQuery{Project: "acme", LabelSelector: tc.selector})
			if err != nil {
				t.Fatalf("list %q: %v", tc.selector, err)
			}
			names := make([]string, 0, len(got))
			for _, r := range got {
				names = append(names, r.Name)
			}
			if len(names) != len(tc.want) {
				t.Fatalf("selector %q: want %v, got %v", tc.selector, tc.want, names)
			}
			for i := range names {
				if names[i] != tc.want[i] {
					t.Fatalf("selector %q: want %v, got %v", tc.selector, tc.want, names)
				}
			}
		})
	}

	if _, err := s.ListProjectSkills(ctx, SkillCatalogQuery{Project: "acme", LabelSelector: "kind = ("}); !errors.Is(err, ErrSkillInvalid) {
		t.Fatalf("a malformed selector must fail loudly, got %v", err)
	}
}

func TestSkillsListLimit(t *testing.T) {
	s := newSkillCatalogueTestStore(t)
	for _, n := range []string{"a-one", "b-two", "c-three"} {
		teach(t, s, "acme", n, "# doc", "", nil)
	}
	got, err := s.ListProjectSkills(context.Background(), SkillCatalogQuery{Project: "acme", Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit ignored: got %d", len(got))
	}
}

// ── Project isolation (P5) ─────────────────────────────────────────────────

func TestSkillsAreProjectScoped(t *testing.T) {
	s := newSkillCatalogueTestStore(t)
	ctx := context.Background()

	mine := teach(t, s, "acme", "render-video", "# acme", "", LabelSet{"kind": "media"})
	teach(t, s, "globex", "render-video", "# globex", "", LabelSet{"kind": "media"})

	got, err := s.GetProjectSkill(ctx, "acme", "render-video")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != mine.ID || got.Markdown != "# acme" {
		t.Fatalf("a project must resolve its OWN skill, got %q", got.Markdown)
	}
	list, err := s.ListProjectSkills(ctx, SkillCatalogQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("a project must see only its own skills, got %d", len(list))
	}
	// A revision id from another project is not readable even when named exactly.
	var other Skill
	s.gdb.Where("customer = ?", "globex").First(&other)
	if _, err := s.getSkillRevision(ctx, "acme", other.ID); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("cross-project read must look like not-found, got %v", err)
	}
	if _, err := s.ListProjectSkills(ctx, SkillCatalogQuery{}); !errors.Is(err, ErrSkillInvalid) {
		t.Fatalf("a projectless list must be refused (P5), got %v", err)
	}
}

// ── The legacy population stays invisible (the markdown discriminator) ─────

func TestSkillsLegacyRowsAreInvisible(t *testing.T) {
	s := newSkillCatalogueTestStore(t)
	ctx := context.Background()

	// A pre-§14 host-built catalogue row: no markdown, latest-wins semantics.
	if _, err := s.UpsertSkill(ctx, &Skill{
		Name: "render-video", Visibility: "organizational", Customer: "acme",
		OwnerEmail: "u@acme.com", ContentHash: "hash1",
	}, ConfigWrite{}); err != nil {
		t.Fatalf("legacy upsert: %v", err)
	}

	if _, err := s.GetProjectSkill(ctx, "acme", "render-video"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("a legacy row must never resolve as a §14 skill, got %v", err)
	}
	list, err := s.ListProjectSkills(ctx, SkillCatalogQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a legacy row must never be listed as a §14 skill, got %+v", list)
	}

	// And a §14 revision of the same name coexists without either clobbering
	// the other: the legacy upsert path skips catalogue rows entirely.
	mine := teach(t, s, "acme", "render-video", "# real", "", nil)
	if _, err := s.UpsertSkill(ctx, &Skill{
		Name: "render-video", Visibility: "organizational", Customer: "acme",
		OwnerEmail: "u@acme.com", ContentHash: "hash2",
	}, ConfigWrite{}); err != nil {
		t.Fatalf("second legacy upsert: %v", err)
	}
	got, err := s.GetProjectSkill(ctx, "acme", "render-video")
	if err != nil {
		t.Fatalf("get after legacy upsert: %v", err)
	}
	if got.ID != mine.ID || got.Markdown != "# real" {
		t.Fatalf("a legacy upsert overwrote an append-only §14 revision: %+v", got)
	}
}

// ── The config log (§15.4): one event per write, with provenance ───────────

func TestSkillsCreateIsConfigEvented(t *testing.T) {
	s := newSkillCatalogueTestStore(t)
	ctx := context.Background()

	sk, err := s.CreateSkill(ctx, &Skill{
		Name: "render-video", Customer: "acme", Markdown: "# doc", InstallSh: "echo hi",
		Labels: LabelSet{"kind": "media"},
	}, ConfigWrite{Worker: "curator", Session: "s-42", Rationale: "hoisting what I worked out"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	evs, err := s.ListConfigEvents(ctx, ConfigEventQuery{Project: "acme"})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(evs) != 1 {
		t.Fatalf("want exactly one config event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Action != ActionSkillCreate {
		t.Fatalf("action: want %q, got %q", ActionSkillCreate, ev.Action)
	}
	if ev.ActorWorker != "curator" || ev.ActorSession != "s-42" || ev.Rationale != "hoisting what I worked out" {
		t.Fatalf("actor/rationale not threaded through: %+v", ev)
	}
	// The payload is the full new state (§15.2), markdown included.
	if ev.Payload["name"] != "render-video" || ev.Payload["markdown"] != "# doc" || ev.Payload["id"] != sk.ID {
		t.Fatalf("payload must be the full new row: %v", ev.Payload)
	}

	// Provenance defaults to the acting session when the caller sets none, so a
	// tool cannot record one actor in the log and another in the catalogue.
	if sk.CreatedByWorker != "curator" || sk.CreatedBySession != "s-42" {
		t.Fatalf("provenance not taken from ConfigWrite: %+v", sk)
	}
}

// ── Live Postgres: the partial index, real jsonb labels, real defaults ─────

func TestSkillsLivePostgresRoundTrip(t *testing.T) {
	if os.Getenv("AGENTKIT_TEST_POSTGRES_URL") == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	s := openLivePG(t)
	ctx := context.Background()
	project := "proj-" + uuid.New().String()
	t.Cleanup(func() {
		s.DB().Exec("DELETE FROM agent_skills WHERE customer = ?", project)
		s.DB().Exec("DELETE FROM config_events WHERE project = ?", project)
	})

	teach(t, s, project, "render-video", "# v1", "apt-get install -y ffmpeg", LabelSet{"kind": "media"})
	newest := teach(t, s, project, "render-video", "# v2", "", LabelSet{"kind": "media", "tier": "pro"})
	teach(t, s, project, "write-brief", "# brief", "", LabelSet{"kind": "writing"})

	got, err := s.GetProjectSkill(ctx, project, "render-video")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != newest.ID {
		t.Fatalf("newest-wins failed on Postgres: got %s want %s", got.ID, newest.ID)
	}
	if got.Labels["tier"] != "pro" {
		t.Fatalf("jsonb labels did not round-trip: %v", got.Labels)
	}
	if got.InstallSh != "" {
		t.Fatalf("empty install_sh must round-trip as empty, got %q", got.InstallSh)
	}

	list, err := s.ListProjectSkills(ctx, SkillCatalogQuery{Project: project, LabelSelector: "tier=pro"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Name != "render-video" || list[0].Revisions != 2 {
		t.Fatalf("selector/list wrong on Postgres: %+v", list)
	}
	if list[0].HasInstallScript {
		t.Fatalf("the CURRENT revision has no install script; the boolean must come from it")
	}
}
