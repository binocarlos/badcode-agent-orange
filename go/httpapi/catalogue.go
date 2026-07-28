package httpapi

// The images/skills catalogue read routes (operator-console design B4; the
// catalogues themselves are spec 08-images-and-skills §13/§14).
//
//	GET /agent/images?label_selector=&limit=
//	  200 : {"images": [...], "count": n, "truncated"?: true, "note"?: "…"}
//	GET /agent/skills?label_selector=&limit=
//	  200 : {"skills": [...], "count": n, "truncated"?: true, "note"?: "…"}
//
// Read-only by design, exactly like the MCP tools they mirror: a version is
// never overwritten and a skill revision is never edited, so there is no write
// counterpart here and never will be. Burning an image is `image_create` from
// inside a session; teaching a skill is `skill_create`. A browser has no
// container to snapshot.
//
// Two contracts are copied from the tools deliberately, so the two surfaces
// cannot drift:
//
//   - the 200-newest cap, and the fact that it is STATED in the response
//     (`truncated` + `note`) — a silent truncation is worse than none;
//   - the response body, minus the registry handle: it is how the engine finds
//     the bytes, not something a caller of this route has any use for, and it
//     names storage locations.
//
// The project comes from the token's Customer claim, never from the query (P5),
// as on every sibling route.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// CatalogueStore is the slice of agentdb.Store these two routes need — the
// same two list methods the image/skill MCP tools call, and nothing that
// mutates. *agentdb.Store satisfies it; a host may supply its own.
type CatalogueStore interface {
	ListCustomImageVersions(ctx context.Context, q agentdb.ImageCatalogQuery) ([]*agentdb.CustomImage, error)
	ListProjectSkills(ctx context.Context, q agentdb.SkillCatalogQuery) ([]*agentdb.SkillSummary, error)
}

// The concrete store must always satisfy the seam.
var _ CatalogueStore = (*agentdb.Store)(nil)

// catalogueCap bounds a bare listing, for the reason imageListCap does in
// cmd/agentd: a catalogue curated for a year would otherwise arrive as one
// enormous page. `limit` may lower it; nothing raises it.
const catalogueCap = 200

// imageEntry is one catalogue version on the wire: §13.4's tuple plus the
// version's tombstone-free identity. The registry handle is deliberately
// absent (see the file comment).
type imageEntry struct {
	Name             string            `json:"name"`
	Version          int               `json:"version"`
	Labels           map[string]string `json:"labels"`
	CreatedByWorker  string            `json:"created_by_worker"`
	CreatedBySession string            `json:"created_by_session"`
	CreatedAt        int64             `json:"created_at"`
}

// catalogue returns the configured store, or writes 501 and returns nil
// (mirrors the workers/schedules/topologies contract).
func (h *Handlers) catalogue(w http.ResponseWriter) CatalogueStore {
	if h.cfg.Catalogue == nil {
		http.Error(w, "the image/skill catalogue is not configured on this host", http.StatusNotImplemented)
		return nil
	}
	return h.cfg.Catalogue
}

// catalogueLimit reads ?limit=, clamped to the cap. Junk and non-positive
// values mean "the cap", the same degrade-to-unbounded posture the config-log
// route takes.
func catalogueLimit(r *http.Request) int {
	n := queryInt(r, "limit", 0)
	if n <= 0 || n > catalogueCap {
		return catalogueCap
	}
	return n
}

// catalogueBody assembles the shared envelope: the rows, the count, and — only
// when the query actually hit the ceiling — the truncation note.
func catalogueBody(key string, rows any, count, limit int, truncated bool, kind string) map[string]any {
	body := map[string]any{key: rows, "count": count}
	if truncated {
		body["truncated"] = true
		body["note"] = fmt.Sprintf(
			"Only the %d newest %s are shown. Narrow the search with a label_selector to see older ones.", limit, kind)
	}
	return body
}

// ListImages serves GET /agent/images — the project's image catalogue, newest
// first. Reaped versions are not listed (the store's default): if a version you
// remember is missing, it is gone, not hidden.
func (h *Handlers) ListImages(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.catalogue(w)
	if store == nil {
		return
	}
	if id.Customer == "" {
		http.Error(w, "no project in token", http.StatusForbidden)
		return
	}
	limit := catalogueLimit(r)
	// One more than the limit, so "there is more" is a fact rather than a guess
	// from a full page — the same trick image_list plays.
	images, err := store.ListCustomImageVersions(r.Context(), agentdb.ImageCatalogQuery{
		Project:       id.Customer,
		LabelSelector: r.URL.Query().Get("label_selector"),
		Limit:         limit + 1,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	truncated := len(images) > limit
	if truncated {
		images = images[:limit]
	}
	out := make([]imageEntry, 0, len(images))
	for _, ci := range images {
		out = append(out, imageEntry{
			Name:             ci.Name,
			Version:          ci.Version,
			Labels:           map[string]string(ci.Labels),
			CreatedByWorker:  ci.CreatedByWorker,
			CreatedBySession: ci.CreatedBySession,
			CreatedAt:        ci.CreatedAt,
		})
	}
	writeJSON(w, catalogueBody("images", out, len(out), limit, truncated, "images"))
}

// ListSkills serves GET /agent/skills — one entry per skill name, carrying its
// newest revision (§14.1: a skill's identity is its name). The markdown is
// deliberately not included; that is skill_get's job.
func (h *Handlers) ListSkills(w http.ResponseWriter, r *http.Request) {
	id, ok := h.identify(w, r)
	if !ok {
		return
	}
	store := h.catalogue(w)
	if store == nil {
		return
	}
	if id.Customer == "" {
		http.Error(w, "no project in token", http.StatusForbidden)
		return
	}
	limit := catalogueLimit(r)
	skills, err := store.ListProjectSkills(r.Context(), agentdb.SkillCatalogQuery{
		Project:       id.Customer,
		LabelSelector: r.URL.Query().Get("label_selector"),
		Limit:         limit + 1,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	truncated := len(skills) > limit
	if truncated {
		skills = skills[:limit]
	}
	if skills == nil {
		skills = []*agentdb.SkillSummary{}
	}
	writeJSON(w, catalogueBody("skills", skills, len(skills), limit, truncated, "skills"))
}
