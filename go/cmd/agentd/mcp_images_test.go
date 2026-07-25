package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// ---------------------------------------------------------------------------
// I2 — the image MCP tools (§13.4).
//
// The invariants worth a test, in order of what they would cost to get wrong:
// a session can only ever snapshot ITSELF and can only see its own project's
// catalogue; provenance and the permalink are on every result; a rejected
// argument costs no snapshot; and a snapshot that could not be recorded is a
// loud failure rather than a cheerful one.
// ---------------------------------------------------------------------------

// invokeTool runs one registered tool directly and decodes its result the way
// the model sees it — through JSON, tags and all.
func invokeTool(t *testing.T, tools []*mcpTool, name string, caller mcpCaller, args any) (map[string]any, error) {
	t.Helper()
	var tool *mcpTool
	for _, tt := range tools {
		if tt.Name == name {
			tool = tt
		}
	}
	if tool == nil {
		t.Fatalf("no tool %q", name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := tool.Handler(context.Background(), caller, raw)
	if err != nil {
		return nil, err
	}
	blob, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(blob, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return out, nil
}

// ── Fakes ───────────────────────────────────────────────────────────────────

// fakeImageCatalog records what the tools asked for — the project argument is
// the whole tenancy story, so the test needs to see it.
type fakeImageCatalog struct {
	created  []*agentdb.CustomImage
	writes   []agentdb.ConfigWrite
	queries  []agentdb.ImageCatalogQuery
	versions map[string]int // project|name → high-water mark
	rows     []*agentdb.CustomImage
	err      error
}

func newFakeImageCatalog() *fakeImageCatalog {
	return &fakeImageCatalog{versions: map[string]int{}}
}

func (f *fakeImageCatalog) CreateCustomImage(_ context.Context, ci *agentdb.CustomImage, cw agentdb.ConfigWrite) (*agentdb.CustomImage, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := ci.Customer + "|" + ci.Name
	f.versions[key]++
	stored := *ci
	stored.ID = fmt.Sprintf("img-%d", len(f.created)+1)
	stored.Version = f.versions[key]
	stored.CreatedAt = 1700000000
	f.created = append(f.created, &stored)
	f.writes = append(f.writes, cw)
	return &stored, nil
}

func (f *fakeImageCatalog) ListCustomImageVersions(_ context.Context, q agentdb.ImageCatalogQuery) ([]*agentdb.CustomImage, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

// fakeSnapshotter records which session was snapshotted — the only thing that
// matters, since a session must never be able to snapshot another.
type fakeSnapshotter struct {
	refs []agentkit.SessionRef
	err  error
}

func (f *fakeSnapshotter) Snapshot(_ context.Context, ref agentkit.SessionRef) (imageregistry.Handle, error) {
	f.refs = append(f.refs, ref)
	if f.err != nil {
		return imageregistry.Handle{}, f.err
	}
	return imageregistry.Handle{Kind: "blob-archive", Ref: "blob/" + ref.SessionID}, nil
}

func testImageTools(store imageCatalog, snaps sessionSnapshotter) *imageTools {
	return newImageTools(store, snaps, testPermalinker())
}

// ── Surface ─────────────────────────────────────────────────────────────────

// TestImageToolsSurface pins the whole surface: create and list, and NOTHING
// that updates or deletes a version (§13.2, append-only).
func TestImageToolsSurface(t *testing.T) {
	tools := testImageTools(newFakeImageCatalog(), &fakeSnapshotter{}).tools()

	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
		if tool.Description == "" {
			t.Fatalf("tool %q has no description — descriptions are prompt, not documentation", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Fatalf("tool %q has no input schema", tool.Name)
		}
	}
	want := []string{"image_create", "image_list"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tool surface = %v, want exactly %v", names, want)
	}
	for _, name := range names {
		for _, forbidden := range []string{"update", "delete", "remove", "reap"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("tool %q: image versions are append-only (§13.2)", name)
			}
		}
	}

	// No tool takes a project parameter — the scope is the token's, in code.
	for _, tool := range tools {
		props, _ := tool.InputSchema["properties"].(map[string]any)
		for _, forbidden := range []string{"project", "customer", "session", "session_id"} {
			if _, bad := props[forbidden]; bad {
				t.Fatalf("tool %q must not take a %q parameter (P5: the scope comes from the token)", tool.Name, forbidden)
			}
		}
	}

	// §13.5: burning does not repoint anything. The description must say so, or
	// a model will assume image_create is adoption.
	var createDesc string
	for _, tool := range tools {
		if tool.Name == "image_create" {
			createDesc = strings.ToLower(tool.Description)
		}
	}
	for _, phrase := range []string{"append-only", "worker_update", "not point any worker"} {
		if !strings.Contains(createDesc, strings.ToLower(phrase)) {
			t.Fatalf("image_create description must explain %q", phrase)
		}
	}
}

// ── image_create ────────────────────────────────────────────────────────────

// TestImageToolsCreate: the calling session is snapshotted, its worker/session
// become the provenance and the ConfigWrite actor, and the STORED row is echoed.
func TestImageToolsCreate(t *testing.T) {
	store := newFakeImageCatalog()
	snaps := &fakeSnapshotter{}
	tools := testImageTools(store, snaps).tools()

	res, err := invokeTool(t, tools, "image_create", testCaller(), map[string]any{
		"name":   "marketing-tools",
		"labels": map[string]string{"purpose": "marketing-toolbox", "adds": "ffmpeg"},
	})
	if err != nil {
		t.Fatalf("image_create: %v", err)
	}

	// It snapshotted the CALLING session — the only one it can name.
	if len(snaps.refs) != 1 || snaps.refs[0].SessionID != "sess-1" {
		t.Fatalf("want a snapshot of the calling session, got %+v", snaps.refs)
	}
	if len(store.created) != 1 {
		t.Fatalf("created %d rows, want 1", len(store.created))
	}
	got := store.created[0]
	if got.Customer != "acme" {
		t.Fatalf("project = %q, want the caller's project", got.Customer)
	}
	if got.CreatedByWorker != "email-answerer" || got.CreatedBySession != "sess-1" {
		t.Fatalf("provenance = %q/%q, want the caller's worker and session (§13.2)", got.CreatedByWorker, got.CreatedBySession)
	}
	// The config event's actor is the same caller — one actor, not two (§15.2).
	if store.writes[0].Worker != "email-answerer" || store.writes[0].Session != "sess-1" {
		t.Fatalf("ConfigWrite actor = %+v, want the caller", store.writes[0])
	}
	// The handle is the snapshot's, JSON-encoded.
	var h imageregistry.Handle
	if err := json.Unmarshal([]byte(got.RegistryHandle), &h); err != nil {
		t.Fatalf("registry handle must be JSON: %v (%q)", err, got.RegistryHandle)
	}
	if h.Ref != "blob/sess-1" {
		t.Fatalf("catalogue row must point at the snapshot that was just taken, got %+v", h)
	}

	// §9 read-back: the version the STORE allocated is what comes back.
	if res["name"] != "marketing-tools" || res["version"] != float64(1) {
		t.Fatalf("result must echo the stored {name, version}: %v", res)
	}
	if res["session_url"] != "https://orange.example.com/p/acme/s/sess-1" {
		t.Fatalf("session_url = %v", res["session_url"])
	}
	labels, _ := res["labels"].(map[string]any)
	if labels["purpose"] != "marketing-toolbox" {
		t.Fatalf("labels not echoed: %v", res["labels"])
	}
	// The registry handle is an engine detail and must not leak to the model.
	if _, leaked := res["registry_handle"]; leaked {
		t.Fatalf("the registry handle must not be in the tool result: %v", res)
	}

	// A second burn of the same name is a NEW version, not an overwrite.
	res2, err := invokeTool(t, tools, "image_create", testCaller(), map[string]any{"name": "marketing-tools"})
	if err != nil {
		t.Fatalf("second image_create: %v", err)
	}
	if res2["version"] != float64(2) {
		t.Fatalf("burning an existing name must allocate the next version, got %v", res2["version"])
	}
}

// TestImageToolsCreateValidatesBeforeSnapshotting: committing a container costs
// real time and real bytes, so a bad argument must cost neither (§9).
func TestImageToolsCreateValidatesBeforeSnapshotting(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing name", map[string]any{}, "required"},
		{"blank name", map[string]any{"name": "   "}, "required"},
		{"upper case", map[string]any{"name": "Toolbox"}, "invalid"},
		{"pinned version", map[string]any{"name": "toolbox:2"}, "must not carry a version"},
		{"free-text label", map[string]any{"name": "toolbox",
			"labels": map[string]string{"why": "because we needed video tools"}}, "labels"},
		{"unknown argument", map[string]any{"name": "toolbox", "project": "globex"}, "invalid arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeImageCatalog()
			snaps := &fakeSnapshotter{}
			_, err := invokeTool(t, testImageTools(store, snaps).tools(), "image_create", testCaller(), tc.args)
			if err == nil {
				t.Fatalf("want a refusal")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
			if len(snaps.refs) != 0 {
				t.Fatalf("a rejected argument must not cost a snapshot")
			}
			if len(store.created) != 0 {
				t.Fatalf("a rejected argument must not write a row")
			}
		})
	}
}

// A snapshot that could not be taken records nothing, and says so plainly — the
// model must not carry on believing the environment was kept.
func TestImageToolsCreateSnapshotFailureRecordsNothing(t *testing.T) {
	store := newFakeImageCatalog()
	snaps := &fakeSnapshotter{err: errors.New("shared-tenancy worker does not support snapshots")}

	_, err := invokeTool(t, testImageTools(store, snaps).tools(), "image_create", testCaller(),
		map[string]any{"name": "toolbox"})
	if err == nil {
		t.Fatalf("want a failure")
	}
	if !strings.Contains(err.Error(), "NOTHING was recorded") {
		t.Fatalf("the failure must say nothing was recorded, got %q", err)
	}
	if len(store.created) != 0 {
		t.Fatalf("a failed snapshot must not leave a catalogue row")
	}
}

// The reverse: a snapshot that was taken but could not be recorded is still a
// failure, and one whose message does not pretend the image is usable.
func TestImageToolsCreateRecordFailureIsLoud(t *testing.T) {
	store := newFakeImageCatalog()
	store.err = errors.New("labels: too many labels")
	snaps := &fakeSnapshotter{}

	_, err := invokeTool(t, testImageTools(store, snaps).tools(), "image_create", testCaller(),
		map[string]any{"name": "toolbox"})
	if err == nil {
		t.Fatalf("want a failure")
	}
	if !strings.Contains(err.Error(), "NOT usable") {
		t.Fatalf("the failure must say the image is not usable, got %q", err)
	}
	if len(snaps.refs) != 1 {
		t.Fatalf("the snapshot did happen: %+v", snaps.refs)
	}
}

// A token with no session has nothing to snapshot, and no argument with which
// to name a substitute (§13.4).
func TestImageToolsCreateRequiresASession(t *testing.T) {
	store := newFakeImageCatalog()
	snaps := &fakeSnapshotter{}
	_, err := invokeTool(t, testImageTools(store, snaps).tools(), "image_create",
		mcpCaller{Project: "acme"}, map[string]any{"name": "toolbox"})
	if err == nil || !strings.Contains(err.Error(), "inside a session") {
		t.Fatalf("want a refusal naming the missing session, got %v", err)
	}
	if len(snaps.refs) != 0 {
		t.Fatalf("nothing may be snapshotted without a session")
	}
}

// ── image_list ──────────────────────────────────────────────────────────────

func TestImageToolsList(t *testing.T) {
	store := newFakeImageCatalog()
	store.rows = []*agentdb.CustomImage{
		{Name: "toolbox", Version: 2, Labels: agentdb.LabelSet{"purpose": "marketing"},
			CreatedByWorker: "curator", CreatedBySession: "sess-9", CreatedAt: 1700000200},
		{Name: "toolbox", Version: 1, Labels: agentdb.LabelSet{"purpose": "marketing"},
			CreatedByWorker: "curator", CreatedBySession: "sess-8", CreatedAt: 1700000100},
	}
	tools := testImageTools(store, &fakeSnapshotter{}).tools()

	res, err := invokeTool(t, tools, "image_list", testCaller(), map[string]any{
		"label_selector": "purpose=marketing",
	})
	if err != nil {
		t.Fatalf("image_list: %v", err)
	}

	// The project is a parameter of the STORE call, never of the tool.
	q := store.queries[0]
	if q.Project != "acme" {
		t.Fatalf("query project = %q, want the caller's", q.Project)
	}
	if q.LabelSelector != "purpose=marketing" {
		t.Fatalf("selector not passed through: %q", q.LabelSelector)
	}
	if res["count"] != float64(2) {
		t.Fatalf("count = %v", res["count"])
	}
	images, _ := res["images"].([]any)
	first, _ := images[0].(map[string]any)
	// Every field §13.4 names, plus the permalink.
	for _, key := range []string{"name", "version", "labels", "created_by_worker", "created_by_session", "created_at", "session_url"} {
		if _, ok := first[key]; !ok {
			t.Fatalf("listing entry is missing %q: %v", key, first)
		}
	}
	if first["version"] != float64(2) {
		t.Fatalf("newest first: want version 2 leading, got %v", first["version"])
	}
	if first["session_url"] != "https://orange.example.com/p/acme/s/sess-9" {
		t.Fatalf("session_url = %v", first["session_url"])
	}
}

// A huge catalogue is capped, and the cap is STATED — a silently short list
// would read as "that is all there is".
func TestImageToolsListTruncationIsVisible(t *testing.T) {
	store := newFakeImageCatalog()
	for i := 0; i < imageListCap+5; i++ {
		store.rows = append(store.rows, &agentdb.CustomImage{Name: "toolbox", Version: i + 1})
	}
	res, err := invokeTool(t, testImageTools(store, &fakeSnapshotter{}).tools(), "image_list", testCaller(), map[string]any{})
	if err != nil {
		t.Fatalf("image_list: %v", err)
	}
	if res["count"] != float64(imageListCap) {
		t.Fatalf("count = %v, want the cap %d", res["count"], imageListCap)
	}
	if res["truncated"] != true || res["note"] == nil {
		t.Fatalf("truncation must be visible in the result: %v", res)
	}
	// It asked for one more than the cap, so "there is more" is a fact.
	if store.queries[0].Limit != imageListCap+1 {
		t.Fatalf("limit = %d, want cap+1 so truncation is detectable", store.queries[0].Limit)
	}
}

func TestImageToolsListRejectsUnknownArguments(t *testing.T) {
	store := newFakeImageCatalog()
	_, err := invokeTool(t, testImageTools(store, &fakeSnapshotter{}).tools(), "image_list", testCaller(),
		map[string]any{"selector": "purpose=marketing"})
	if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("a misspelled argument must fail loudly rather than list the whole catalogue, got %v", err)
	}
	if len(store.queries) != 0 {
		t.Fatalf("nothing may be queried on a malformed call")
	}
}
