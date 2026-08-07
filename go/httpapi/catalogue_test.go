package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// fakeCatalogue records the queries it was handed and returns canned pages, so
// the handlers' parameter plumbing can be asserted without a database.
type fakeCatalogue struct {
	gotImages agentdb.ImageCatalogQuery
	gotSkills agentdb.SkillCatalogQuery
	images    []*agentdb.CustomImage
	skills    []*agentdb.SkillSummary
	err       error
	calls     int
}

func (f *fakeCatalogue) ListCustomImageVersions(_ context.Context, q agentdb.ImageCatalogQuery) ([]*agentdb.CustomImage, error) {
	f.calls++
	f.gotImages = q
	return f.images, f.err
}

func (f *fakeCatalogue) ListProjectSkills(_ context.Context, q agentdb.SkillCatalogQuery) ([]*agentdb.SkillSummary, error) {
	f.calls++
	f.gotSkills = q
	return f.skills, f.err
}

func newCatalogueHandlers(t *testing.T, store CatalogueStore, id IdentityFunc) *Handlers {
	t.Helper()
	return newHandlers(t, Config{
		Runner:    stubRunner{},
		Store:     stubStore{},
		Identity:  id,
		Catalogue: store,
	})
}

// The project comes from the token, the selector passes through verbatim, and
// the store is always asked for one more row than the page — that extra row is
// how "there is more" stays a fact rather than a guess.
func TestCatalogue_QueryPlumbing(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantImages agentdb.ImageCatalogQuery
		wantSkills agentdb.SkillCatalogQuery
	}{
		{
			name:       "no filters caps at 200 (+1 probe)",
			path:       "",
			wantImages: agentdb.ImageCatalogQuery{Project: "acme", Limit: 201},
			wantSkills: agentdb.SkillCatalogQuery{Project: "acme", Limit: 201},
		},
		{
			name:       "selector passes through verbatim",
			path:       "?label_selector=purpose%3Dmarketing%2C%21deprecated",
			wantImages: agentdb.ImageCatalogQuery{Project: "acme", LabelSelector: "purpose=marketing,!deprecated", Limit: 201},
			wantSkills: agentdb.SkillCatalogQuery{Project: "acme", LabelSelector: "purpose=marketing,!deprecated", Limit: 201},
		},
		{
			name:       "a smaller limit is honoured",
			path:       "?limit=5",
			wantImages: agentdb.ImageCatalogQuery{Project: "acme", Limit: 6},
			wantSkills: agentdb.SkillCatalogQuery{Project: "acme", Limit: 6},
		},
		{
			name:       "a bigger limit cannot raise the cap",
			path:       "?limit=5000",
			wantImages: agentdb.ImageCatalogQuery{Project: "acme", Limit: 201},
			wantSkills: agentdb.SkillCatalogQuery{Project: "acme", Limit: 201},
		},
		{
			name:       "junk and negative limits degrade to the cap",
			path:       "?limit=abc",
			wantImages: agentdb.ImageCatalogQuery{Project: "acme", Limit: 201},
			wantSkills: agentdb.SkillCatalogQuery{Project: "acme", Limit: 201},
		},
		{
			name:       "a project in the query is ignored, not honoured",
			path:       "?project=other",
			wantImages: agentdb.ImageCatalogQuery{Project: "acme", Limit: 201},
			wantSkills: agentdb.SkillCatalogQuery{Project: "acme", Limit: 201},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeCatalogue{}
			h := newCatalogueHandlers(t, store, identityFor("acme"))
			if rec := do(h, http.MethodGet, "/agent/images"+tc.path, ""); rec.Code != http.StatusOK {
				t.Fatalf("images status=%d body=%s", rec.Code, rec.Body)
			}
			if store.gotImages != tc.wantImages {
				t.Fatalf("image query:\n got %+v\nwant %+v", store.gotImages, tc.wantImages)
			}
			if rec := do(h, http.MethodGet, "/agent/skills"+tc.path, ""); rec.Code != http.StatusOK {
				t.Fatalf("skills status=%d body=%s", rec.Code, rec.Body)
			}
			if store.gotSkills != tc.wantSkills {
				t.Fatalf("skill query:\n got %+v\nwant %+v", store.gotSkills, tc.wantSkills)
			}
		})
	}
}

// The image response is the §13.4 tuple and nothing more — in particular NOT
// the registry handle, which names storage locations and is of no use to a
// caller of this route.
func TestListImages_ResponseShape(t *testing.T) {
	store := &fakeCatalogue{images: []*agentdb.CustomImage{
		{Name: "toolbox", Version: 2, Labels: agentdb.LabelSet{"purpose": "marketing"},
			CreatedByWorker: "burner", CreatedBySession: "sess-2", CreatedAt: 1789000002,
			RegistryHandle: `{"kind":"blob","ref":"secret-bucket/xyz"}`},
		{Name: "toolbox", Version: 1, CreatedAt: 1789000001},
	}}
	h := newCatalogueHandlers(t, store, identityFor("acme"))
	rec := do(h, http.MethodGet, "/agent/images", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		Images    []map[string]any `json:"images"`
		Count     int              `json:"count"`
		Truncated bool             `json:"truncated"`
	}
	decodeInto(t, rec, &body)
	if body.Count != 2 || len(body.Images) != 2 {
		t.Fatalf("want 2 entries, got count=%d len=%d", body.Count, len(body.Images))
	}
	// Newest first: the store orders, the handler must not resort.
	if body.Images[0]["version"].(float64) != 2 {
		t.Fatalf("entries must be newest-first: %+v", body.Images)
	}
	for _, k := range []string{"name", "version", "labels", "created_by_worker", "created_by_session", "created_at"} {
		if _, ok := body.Images[0][k]; !ok {
			t.Fatalf("entry is missing %q: %+v", k, body.Images[0])
		}
	}
	if _, leaked := body.Images[0]["registry_handle"]; leaked {
		t.Fatalf("the registry handle must never reach the wire: %+v", body.Images[0])
	}
	if body.Truncated {
		t.Fatal("a short page is not truncated")
	}

	// An empty catalogue is [] and not null, so a UI can tell "nothing burned
	// yet" from "the route is broken".
	empty := &fakeCatalogue{}
	h = newCatalogueHandlers(t, empty, identityFor("acme"))
	if got := do(h, http.MethodGet, "/agent/images", "").Body.String(); got != "{\"count\":0,\"images\":[]}\n" {
		t.Fatalf("empty catalogue must be []: %s", got)
	}
}

// One entry per skill name, with the markdown left behind (§14.2's
// list-is-not-get split), and an empty catalogue as [].
func TestListSkills_ResponseShape(t *testing.T) {
	store := &fakeCatalogue{skills: []*agentdb.SkillSummary{
		{Name: "brand-voice", Revision: 3, Revisions: 3, Labels: agentdb.LabelSet{"topic": "copy"},
			HasInstallScript: true, CreatedByWorker: "editor", CreatedBySession: "sess-9", CreatedAt: 1789000009},
	}}
	h := newCatalogueHandlers(t, store, identityFor("acme"))
	rec := do(h, http.MethodGet, "/agent/skills", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	var body struct {
		Skills []map[string]any `json:"skills"`
		Count  int              `json:"count"`
	}
	decodeInto(t, rec, &body)
	if body.Count != 1 {
		t.Fatalf("want 1 entry, got %d", body.Count)
	}
	for _, k := range []string{"name", "revision", "revisions", "labels", "has_install_script", "created_by_worker", "created_by_session", "created_at"} {
		if _, ok := body.Skills[0][k]; !ok {
			t.Fatalf("entry is missing %q: %+v", k, body.Skills[0])
		}
	}
	if _, leaked := body.Skills[0]["markdown"]; leaked {
		t.Fatalf("skill_list never carries the document: %+v", body.Skills[0])
	}

	empty := &fakeCatalogue{}
	h = newCatalogueHandlers(t, empty, identityFor("acme"))
	if got := do(h, http.MethodGet, "/agent/skills", "").Body.String(); got != "{\"count\":0,\"skills\":[]}\n" {
		t.Fatalf("empty catalogue must be []: %s", got)
	}
}

// Truncation is STATED, never silent: exactly `limit` rows come back, plus the
// flag and the sentence that tells a reader how to see the rest.
func TestCatalogue_TruncationIsStated(t *testing.T) {
	images := make([]*agentdb.CustomImage, 0, 4)
	skills := make([]*agentdb.SkillSummary, 0, 4)
	for i := 0; i < 4; i++ {
		images = append(images, &agentdb.CustomImage{Name: fmt.Sprintf("img-%d", i), Version: 1})
		skills = append(skills, &agentdb.SkillSummary{Name: fmt.Sprintf("skill-%d", i)})
	}
	store := &fakeCatalogue{images: images, skills: skills}
	h := newCatalogueHandlers(t, store, identityFor("acme"))

	for _, route := range []struct{ path, key string }{
		{"/agent/images?limit=3", "images"},
		{"/agent/skills?limit=3", "skills"},
	} {
		rec := do(h, http.MethodGet, route.path, "")
		var body map[string]any
		decodeInto(t, rec, &body)
		rows, _ := body[route.key].([]any)
		if len(rows) != 3 {
			t.Fatalf("%s: want 3 rows, got %d", route.path, len(rows))
		}
		if body["truncated"] != true {
			t.Fatalf("%s: truncation must be stated: %+v", route.path, body)
		}
		if note, _ := body["note"].(string); note == "" {
			t.Fatalf("%s: a truncated page must say how to see the rest", route.path)
		}
	}
}

// Auth posture: 401 without an identity, 403 for a token carrying no project,
// 501 when the host wired no store, a store error is a 400 carrying the
// parser's own message, and no method other than GET.
func TestCatalogue_AuthAndAvailability(t *testing.T) {
	paths := []string{"/agent/images", "/agent/skills"}

	t.Run("401 without identity", func(t *testing.T) {
		store := &fakeCatalogue{}
		h := newCatalogueHandlers(t, store, func(*http.Request) (Identity, error) {
			return Identity{}, http.ErrNoCookie
		})
		for _, p := range paths {
			if rec := do(h, http.MethodGet, p, ""); rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s status=%d", p, rec.Code)
			}
		}
		if store.calls != 0 {
			t.Fatal("an unauthenticated request must not reach the store")
		}
	})

	t.Run("403 with no project claim", func(t *testing.T) {
		store := &fakeCatalogue{}
		h := newCatalogueHandlers(t, store, identityFor(""))
		for _, p := range paths {
			if rec := do(h, http.MethodGet, p, ""); rec.Code != http.StatusForbidden {
				t.Fatalf("%s status=%d", p, rec.Code)
			}
		}
		if store.calls != 0 {
			t.Fatal("a projectless token must not reach the store")
		}
	})

	t.Run("501 with no store", func(t *testing.T) {
		h := newCatalogueHandlers(t, nil, identityFor("acme"))
		for _, p := range paths {
			if rec := do(h, http.MethodGet, p, ""); rec.Code != http.StatusNotImplemented {
				t.Fatalf("%s status=%d", p, rec.Code)
			}
		}
	})

	t.Run("400 carries the store's own message", func(t *testing.T) {
		store := &fakeCatalogue{err: fmt.Errorf("%w: label selector: unexpected ','", agentdb.ErrCustomImageInvalid)}
		h := newCatalogueHandlers(t, store, identityFor("acme"))
		rec := do(h, http.MethodGet, "/agent/images?label_selector=,", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d", rec.Code)
		}
		if got := rec.Body.String(); !strings.Contains(got, "unexpected ','") {
			t.Fatalf("the parser's own message must survive: %s", got)
		}
	})

	t.Run("write methods are not routed", func(t *testing.T) {
		// Both catalogues are append-only and written only from inside a
		// session (§13.4, §14.2): there is no POST here, by design.
		h := newCatalogueHandlers(t, &fakeCatalogue{}, identityFor("acme"))
		for _, p := range paths {
			for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
				if rec := do(h, m, p, `{}`); rec.Code == http.StatusOK {
					t.Fatalf("%s %s must not be served", m, p)
				}
			}
		}
	})
}

// PROJECT ISOLATION, against the real store: two projects burn their own
// images and teach their own skills, and neither route can see the other's.
func TestCatalogue_ProjectIsolation_LivePG(t *testing.T) {
	url := os.Getenv("AGENTKIT_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("AGENTKIT_TEST_POSTGRES_URL not set — skipping live Postgres test")
	}
	store, err := agentdb.Open(url)
	if err != nil {
		t.Fatalf("open live postgres: %v", err)
	}
	mine, theirs := "catread-mine", "catread-theirs"
	t.Cleanup(func() {
		for _, p := range []string{mine, theirs} {
			_ = store.DB().Exec("DELETE FROM agent_custom_images WHERE customer = ?", p).Error
			_ = store.DB().Exec("DELETE FROM agent_skills WHERE customer = ?", p).Error
			_ = store.PurgeConfigEvents(context.Background(), p)
		}
	})
	ctx := context.Background()
	for _, p := range []string{mine, theirs} {
		if _, err := store.CreateCustomImage(ctx, &agentdb.CustomImage{
			Customer: p, Name: "toolbox", RegistryHandle: `{"kind":"test"}`,
			Labels: agentdb.LabelSet{"owner": p},
		}, agentdb.ConfigWrite{}); err != nil {
			t.Fatalf("burn image for %s: %v", p, err)
		}
		if _, err := store.CreateSkill(ctx, &agentdb.Skill{
			Customer: p, Name: "brand-voice", Markdown: "# " + p,
			Labels: agentdb.LabelSet{"owner": p},
		}, agentdb.ConfigWrite{}); err != nil {
			t.Fatalf("teach skill for %s: %v", p, err)
		}
	}

	h := newCatalogueHandlers(t, store, identityFor(mine))

	var images struct {
		Images []imageEntry `json:"images"`
	}
	decodeInto(t, do(h, http.MethodGet, "/agent/images?project="+theirs, ""), &images)
	if len(images.Images) != 1 || images.Images[0].Labels["owner"] != mine {
		t.Fatalf("images must be this project's only: %+v", images.Images)
	}

	var skills struct {
		Skills []agentdb.SkillSummary `json:"skills"`
	}
	decodeInto(t, do(h, http.MethodGet, "/agent/skills?project="+theirs, ""), &skills)
	if len(skills.Skills) != 1 || skills.Skills[0].Labels["owner"] != mine {
		t.Fatalf("skills must be this project's only: %+v", skills.Skills)
	}
}
