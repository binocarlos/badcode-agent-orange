package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// ---------------------------------------------------------------------------
// I3 — the skill MCP tools (§14.2).
//
// The invariants worth a test, in order of what they would cost to get wrong:
// a failed install is a VISIBLE failure and never a silent no-op; skill_install
// changes the session and therefore writes no config event; a session sees only
// its own project's skills and can only install into ITSELF; skill_list carries
// no markdown; and skill_create appends rather than overwrites.
// ---------------------------------------------------------------------------

// ── Fakes ───────────────────────────────────────────────────────────────────

type fakeSkillStore struct {
	created   []*agentdb.Skill
	writes    []agentdb.ConfigWrite
	queries   []agentdb.SkillCatalogQuery
	gets      []string // "project|name"
	revisions map[string]int
	rows      []*agentdb.SkillSummary
	byName    map[string]*agentdb.Skill
	err       error
}

func newFakeSkillStore() *fakeSkillStore {
	return &fakeSkillStore{revisions: map[string]int{}, byName: map[string]*agentdb.Skill{}}
}

func (f *fakeSkillStore) CreateSkill(_ context.Context, sk *agentdb.Skill, cw agentdb.ConfigWrite) (*agentdb.Skill, error) {
	if f.err != nil {
		return nil, f.err
	}
	key := sk.Customer + "|" + sk.Name
	f.revisions[key]++
	stored := *sk
	stored.ID = fmt.Sprintf("sk-%d", len(f.created)+1)
	stored.Revision = f.revisions[key]
	stored.CreatedAt = 1700000000
	f.created = append(f.created, &stored)
	f.writes = append(f.writes, cw)
	f.byName[key] = &stored
	return &stored, nil
}

func (f *fakeSkillStore) ListProjectSkills(_ context.Context, q agentdb.SkillCatalogQuery) ([]*agentdb.SkillSummary, error) {
	f.queries = append(f.queries, q)
	if f.err != nil {
		return nil, f.err
	}
	return f.rows, nil
}

func (f *fakeSkillStore) GetProjectSkill(_ context.Context, project, name string) (*agentdb.Skill, error) {
	f.gets = append(f.gets, project+"|"+name)
	if f.err != nil {
		return nil, f.err
	}
	if sk, ok := f.byName[project+"|"+name]; ok {
		return sk, nil
	}
	return nil, agentdb.ErrSkillNotFound
}

// fakeSessionLocator records which session was located: a session must only
// ever be able to install into itself.
type fakeSessionLocator struct {
	refs   []agentkit.SessionRef
	status *agentkit.SessionStatus
	err    error
}

func (f *fakeSessionLocator) Status(_ context.Context, ref agentkit.SessionRef) (*agentkit.SessionStatus, error) {
	f.refs = append(f.refs, ref)
	if f.err != nil {
		return nil, f.err
	}
	return f.status, nil
}

// sandboxStub stands in for the in-image /skills/install route.
type sandboxStub struct {
	requests []sandboxSkillInstallRequest
	reply    sandboxSkillInstallResponse
	status   int
	raw      string // when set, returned verbatim instead of `reply`
}

func newSandboxStub(t *testing.T, stub *sandboxStub) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/skills/install" {
			http.NotFound(w, r)
			return
		}
		var req sandboxSkillInstallRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		stub.requests = append(stub.requests, req)
		if stub.status != 0 {
			w.WriteHeader(stub.status)
		}
		if stub.raw != "" {
			_, _ = w.Write([]byte(stub.raw))
			return
		}
		_ = json.NewEncoder(w).Encode(stub.reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testSkillTools(store skillStore, sessions sessionLocator) *skillTools {
	return newSkillTools(store, sessions, testPermalinker())
}

// installReady wires a stub sandbox into a locator whose session is running.
func installReady(t *testing.T, stub *sandboxStub) *fakeSessionLocator {
	t.Helper()
	srv := newSandboxStub(t, stub)
	return &fakeSessionLocator{status: &agentkit.SessionStatus{
		SessionID: "sess-1", RuntimeState: "running", SandboxAddress: srv.URL,
	}}
}

func okInstall() sandboxSkillInstallResponse {
	return sandboxSkillInstallResponse{
		OK: true, Name: "render-video", Path: "/workspace/.claude/skills/render-video/SKILL.md",
		BytesWritten: 42,
		Script:       sandboxSkillScriptResult{Ran: true, ExitCode: 0, Stdout: "installed"},
	}
}

// ── Surface ─────────────────────────────────────────────────────────────────

// TestSkillsMCPSurface pins the whole surface: create / list / get / install,
// and NOTHING that updates or deletes (§14.1, append-only).
func TestSkillsMCPSurface(t *testing.T) {
	tools := testSkillTools(newFakeSkillStore(), &fakeSessionLocator{}).tools()

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
	want := []string{"skill_create", "skill_list", "skill_get", "skill_install"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tool surface = %v, want exactly %v", names, want)
	}
	for _, name := range names {
		for _, forbidden := range []string{"update", "delete", "remove", "uninstall"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("tool %q: skills are append-only (§14.1)", name)
			}
		}
	}
	for _, tool := range tools {
		props, _ := tool.InputSchema["properties"].(map[string]any)
		for _, forbidden := range []string{"project", "customer", "session", "session_id"} {
			if _, bad := props[forbidden]; bad {
				t.Fatalf("tool %q must not take a %q parameter (P5: the scope comes from the token)", tool.Name, forbidden)
			}
		}
	}

	// The one thing a model will get wrong about skill_install if nobody says
	// it: a skill written now is loaded at the START of a turn, so it is usable
	// on the NEXT one.
	var installDesc string
	for _, tool := range tools {
		if tool.Name == "skill_install" {
			installDesc = strings.ToLower(tool.Description)
		}
	}
	for _, phrase := range []string{"next", "this session only"} {
		if !strings.Contains(installDesc, phrase) {
			t.Fatalf("skill_install description must mention %q: %s", phrase, installDesc)
		}
	}
}

// ── skill_create ────────────────────────────────────────────────────────────

func TestSkillsMCPCreate(t *testing.T) {
	store := newFakeSkillStore()
	tools := testSkillTools(store, &fakeSessionLocator{}).tools()

	res, err := invokeTool(t, tools, "skill_create", testCaller(), map[string]any{
		"name":       "render-social-video",
		"labels":     map[string]string{"kind": "media"},
		"markdown":   "# Render social video\n\nUse ffmpeg.",
		"install_sh": "apt-get install -y ffmpeg",
	})
	if err != nil {
		t.Fatalf("skill_create: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("created %d, want 1", len(store.created))
	}
	got := store.created[0]
	if got.Customer != "acme" {
		t.Fatalf("project = %q, want the caller's", got.Customer)
	}
	if got.CreatedByWorker != "email-answerer" || got.CreatedBySession != "sess-1" {
		t.Fatalf("provenance = %q/%q, want the caller's (§14.1)", got.CreatedByWorker, got.CreatedBySession)
	}
	if store.writes[0].Worker != "email-answerer" || store.writes[0].Session != "sess-1" {
		t.Fatalf("ConfigWrite actor = %+v, want the caller", store.writes[0])
	}
	if res["markdown"] != "# Render social video\n\nUse ffmpeg." || res["install_sh"] != "apt-get install -y ffmpeg" {
		t.Fatalf("skill_create must echo the stored record in full: %v", res)
	}
	if res["revision"] != float64(1) {
		t.Fatalf("revision = %v, want the store's allocation", res["revision"])
	}
	if res["session_url"] != "https://orange.example.com/p/acme/s/sess-1" {
		t.Fatalf("session_url = %v", res["session_url"])
	}

	// A second create under the same name APPENDS (§14.1).
	res2, err := invokeTool(t, tools, "skill_create", testCaller(), map[string]any{
		"name": "render-social-video", "markdown": "# v2",
	})
	if err != nil {
		t.Fatalf("second skill_create: %v", err)
	}
	if res2["revision"] != float64(2) {
		t.Fatalf("an existing name must record a NEW revision, got %v", res2["revision"])
	}
	if len(store.created) != 2 {
		t.Fatalf("the first revision must survive: %d rows", len(store.created))
	}
}

func TestSkillsMCPCreateValidation(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing name", map[string]any{"markdown": "# doc"}, "required"},
		{"upper-case name", map[string]any{"name": "RenderVideo", "markdown": "# doc"}, "kebab-case"},
		{"dotted name", map[string]any{"name": "render.video", "markdown": "# doc"}, "kebab-case"},
		{"traversal", map[string]any{"name": "../etc", "markdown": "# doc"}, "kebab-case"},
		{"missing markdown", map[string]any{"name": "render-video"}, "markdown is required"},
		{"blank markdown", map[string]any{"name": "render-video", "markdown": "  "}, "markdown is required"},
		{"free-text label", map[string]any{"name": "render-video", "markdown": "# doc",
			"labels": map[string]string{"about": "how to render a video"}}, "labels"},
		{"unknown argument", map[string]any{"name": "render-video", "markdown": "# doc", "project": "globex"}, "invalid arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeSkillStore()
			_, err := invokeTool(t, testSkillTools(store, &fakeSessionLocator{}).tools(), "skill_create", testCaller(), tc.args)
			if err == nil {
				t.Fatalf("want a refusal")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("error %q should mention %q", err, tc.want)
			}
			if len(store.created) != 0 {
				t.Fatalf("a rejected create must write nothing")
			}
		})
	}
}

// ── skill_list / skill_get ──────────────────────────────────────────────────

func TestSkillsMCPListCarriesNoMarkdown(t *testing.T) {
	store := newFakeSkillStore()
	store.rows = []*agentdb.SkillSummary{{
		Name: "render-video", Revision: 3, Revisions: 3, Labels: agentdb.LabelSet{"kind": "media"},
		HasInstallScript: true, CreatedByWorker: "curator", CreatedBySession: "sess-7", CreatedAt: 1700000100,
	}}
	res, err := invokeTool(t, testSkillTools(store, &fakeSessionLocator{}).tools(), "skill_list", testCaller(),
		map[string]any{"label_selector": "kind=media"})
	if err != nil {
		t.Fatalf("skill_list: %v", err)
	}
	if store.queries[0].Project != "acme" {
		t.Fatalf("query project = %q, want the caller's", store.queries[0].Project)
	}
	if store.queries[0].LabelSelector != "kind=media" {
		t.Fatalf("selector not passed through: %q", store.queries[0].LabelSelector)
	}
	skills, _ := res["skills"].([]any)
	entry, _ := skills[0].(map[string]any)
	// §14.2: identity + labels + provenance, NOT the markdown.
	for _, key := range []string{"name", "labels", "created_by_worker", "created_by_session", "session_url", "has_install_script"} {
		if _, ok := entry[key]; !ok {
			t.Fatalf("listing entry missing %q: %v", key, entry)
		}
	}
	for _, key := range []string{"markdown", "install_sh"} {
		if _, leaked := entry[key]; leaked {
			t.Fatalf("skill_list must not carry %q — that is what skill_get is for (§14.2)", key)
		}
	}
	if entry["session_url"] != "https://orange.example.com/p/acme/s/sess-7" {
		t.Fatalf("session_url = %v", entry["session_url"])
	}
}

func TestSkillsMCPGet(t *testing.T) {
	store := newFakeSkillStore()
	store.byName["acme|render-video"] = &agentdb.Skill{
		Name: "render-video", Revision: 2, Markdown: "# doc", InstallSh: "echo hi",
		Labels: agentdb.LabelSet{"kind": "media"}, CreatedByWorker: "curator", CreatedBySession: "sess-7",
	}
	tools := testSkillTools(store, &fakeSessionLocator{}).tools()

	res, err := invokeTool(t, tools, "skill_get", testCaller(), map[string]any{"name": "render-video"})
	if err != nil {
		t.Fatalf("skill_get: %v", err)
	}
	if res["markdown"] != "# doc" || res["install_sh"] != "echo hi" {
		t.Fatalf("skill_get returns the record in full: %v", res)
	}
	// The project is a parameter of the store call, never a filter applied after.
	if store.gets[0] != "acme|render-video" {
		t.Fatalf("lookup = %q, want the caller's project", store.gets[0])
	}

	// Another project's skill is simply not found — no existence leak.
	_, err = invokeTool(t, tools, "skill_get", mcpCaller{Project: "globex", SessionID: "s-x"},
		map[string]any{"name": "render-video"})
	if err == nil || !strings.Contains(err.Error(), "no skill named") {
		t.Fatalf("want a plain not-found across projects, got %v", err)
	}
}

// ── skill_install: the load-bearing one (§14.2) ─────────────────────────────

func TestSkillsMCPInstallHappyPath(t *testing.T) {
	store := newFakeSkillStore()
	store.byName["acme|render-video"] = &agentdb.Skill{
		Name: "render-video", Revision: 2, Markdown: "# doc", InstallSh: "apt-get install -y ffmpeg",
	}
	stub := &sandboxStub{reply: okInstall()}
	locator := installReady(t, stub)
	tools := testSkillTools(store, locator).tools()

	res, err := invokeTool(t, tools, "skill_install", testCaller(), map[string]any{"name": "render-video"})
	if err != nil {
		t.Fatalf("skill_install: %v", err)
	}

	// It installed into the CALLING session — the only one it can name.
	if len(locator.refs) != 1 || locator.refs[0].SessionID != "sess-1" {
		t.Fatalf("want the calling session located, got %+v", locator.refs)
	}
	// The sandbox got the document and the script the store holds — the image
	// never reads the catalogue itself.
	if len(stub.requests) != 1 {
		t.Fatalf("want one sandbox call, got %d", len(stub.requests))
	}
	sent := stub.requests[0]
	if sent.Name != "render-video" || sent.Markdown != "# doc" || sent.InstallSh != "apt-get install -y ffmpeg" {
		t.Fatalf("sandbox request = %+v", sent)
	}
	// Both outcomes are reported (§14.2).
	if res["installed"] != true {
		t.Fatalf("installed = %v", res["installed"])
	}
	if res["file_written"] != "/workspace/.claude/skills/render-video/SKILL.md" {
		t.Fatalf("file_written = %v", res["file_written"])
	}
	script, _ := res["script"].(map[string]any)
	if script["ran"] != true || script["exit_code"] != float64(0) || script["stdout"] != "installed" {
		t.Fatalf("script report = %v", script)
	}
	// §14.2: installing changes the SESSION, not the project — no config event
	// and no catalogue write of any kind.
	if len(store.created) != 0 || len(store.writes) != 0 {
		t.Fatalf("skill_install must write nothing to the project (§14.2)")
	}
}

func TestSkillsMCPInstallWithNoScript(t *testing.T) {
	store := newFakeSkillStore()
	store.byName["acme|write-brief"] = &agentdb.Skill{Name: "write-brief", Markdown: "# doc"}
	reply := okInstall()
	reply.Name = "write-brief"
	reply.Script = sandboxSkillScriptResult{Ran: false}
	locator := installReady(t, &sandboxStub{reply: reply})

	res, err := invokeTool(t, testSkillTools(store, locator).tools(), "skill_install", testCaller(),
		map[string]any{"name": "write-brief"})
	if err != nil {
		t.Fatalf("skill_install: %v", err)
	}
	script, _ := res["script"].(map[string]any)
	if script["ran"] != false || script["note"] == nil {
		t.Fatalf("a skill with no script must SAY so rather than imply one ran: %v", script)
	}
}

// The invariant §14.2 exists for: a failed install is a visible failure, with
// the script's output, never a cheerful success object.
func TestSkillsMCPInstallFailureIsVisible(t *testing.T) {
	cases := []struct {
		name  string
		reply sandboxSkillInstallResponse
		want  []string
	}{
		{
			name: "non-zero exit",
			reply: sandboxSkillInstallResponse{
				OK: false, Path: "/workspace/.claude/skills/render-video/SKILL.md", BytesWritten: 42,
				Script: sandboxSkillScriptResult{Ran: true, ExitCode: 3, Stdout: "fetching…", Stderr: "E: package not found"},
			},
			want: []string{"NOT successfully installed", "exit status 3", "E: package not found", "fetching…",
				"written to /workspace/.claude/skills/render-video/SKILL.md", "Do not proceed"},
		},
		{
			name: "timed out",
			reply: sandboxSkillInstallResponse{
				OK: false, Path: "/w/SKILL.md",
				Script: sandboxSkillScriptResult{Ran: true, ExitCode: -1, TimedOut: true, Error: "exceeded 840s"},
			},
			want: []string{"TIMED OUT", "exceeded 840s"},
		},
		{
			name: "document could not be written",
			reply: sandboxSkillInstallResponse{
				OK: false, Error: "could not write the skill document to /workspace/...: EACCES",
			},
			want: []string{"document: NOT written", "EACCES"},
		},
		{
			// Belt and braces: even a reply that claims ok:true is refused when
			// the script it reports says otherwise. The two must not disagree.
			name: "ok:true contradicted by the script",
			reply: sandboxSkillInstallResponse{
				OK: true, Path: "/w/SKILL.md",
				Script: sandboxSkillScriptResult{Ran: true, ExitCode: 1, Stderr: "boom"},
			},
			want: []string{"exit status 1", "boom"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeSkillStore()
			store.byName["acme|render-video"] = &agentdb.Skill{Name: "render-video", Markdown: "# doc", InstallSh: "x"}
			locator := installReady(t, &sandboxStub{reply: tc.reply})

			res, err := invokeTool(t, testSkillTools(store, locator).tools(), "skill_install", testCaller(),
				map[string]any{"name": "render-video"})
			if err == nil {
				t.Fatalf("a failed install must be an ERROR the model cannot miss, got a success: %v", res)
			}
			for _, phrase := range tc.want {
				if !strings.Contains(err.Error(), phrase) {
					t.Fatalf("failure report must contain %q; got:\n%s", phrase, err)
				}
			}
		})
	}
}

func TestSkillsMCPInstallUnreachableSandbox(t *testing.T) {
	store := newFakeSkillStore()
	store.byName["acme|render-video"] = &agentdb.Skill{Name: "render-video", Markdown: "# doc"}

	t.Run("session not running", func(t *testing.T) {
		locator := &fakeSessionLocator{status: &agentkit.SessionStatus{SessionID: "sess-1", RuntimeState: "destroyed"}}
		_, err := invokeTool(t, testSkillTools(store, locator).tools(), "skill_install", testCaller(),
			map[string]any{"name": "render-video"})
		if err == nil || !strings.Contains(err.Error(), "not running") {
			t.Fatalf("want a clear refusal, got %v", err)
		}
	})

	t.Run("status errors", func(t *testing.T) {
		locator := &fakeSessionLocator{err: errors.New("worker gone")}
		_, err := invokeTool(t, testSkillTools(store, locator).tools(), "skill_install", testCaller(),
			map[string]any{"name": "render-video"})
		if err == nil || !strings.Contains(err.Error(), "could not locate") {
			t.Fatalf("want a clear refusal, got %v", err)
		}
	})

	t.Run("sandbox replies with nonsense", func(t *testing.T) {
		locator := installReady(t, &sandboxStub{status: http.StatusBadGateway, raw: "<html>bad gateway</html>"})
		_, err := invokeTool(t, testSkillTools(store, locator).tools(), "skill_install", testCaller(),
			map[string]any{"name": "render-video"})
		if err == nil || !strings.Contains(err.Error(), "outcome is unknown") {
			t.Fatalf("an unparseable reply must be reported as UNKNOWN, not as success, got %v", err)
		}
	})
}

func TestSkillsMCPInstallUnknownSkill(t *testing.T) {
	store := newFakeSkillStore()
	locator := installReady(t, &sandboxStub{reply: okInstall()})
	_, err := invokeTool(t, testSkillTools(store, locator).tools(), "skill_install", testCaller(),
		map[string]any{"name": "never-taught"})
	if err == nil || !strings.Contains(err.Error(), "no skill named") {
		t.Fatalf("want a not-found naming skill_list, got %v", err)
	}
	if len(locator.refs) != 0 {
		t.Fatalf("an unknown skill must not reach the container at all")
	}
}

func TestSkillsMCPInstallRequiresASession(t *testing.T) {
	store := newFakeSkillStore()
	store.byName["acme|render-video"] = &agentdb.Skill{Name: "render-video", Markdown: "# doc"}
	locator := installReady(t, &sandboxStub{reply: okInstall()})
	_, err := invokeTool(t, testSkillTools(store, locator).tools(), "skill_install",
		mcpCaller{Project: "acme"}, map[string]any{"name": "render-video"})
	if err == nil || !strings.Contains(err.Error(), "inside a session") {
		t.Fatalf("want a refusal naming the missing session, got %v", err)
	}
}
