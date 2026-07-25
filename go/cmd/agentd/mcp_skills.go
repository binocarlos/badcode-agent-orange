package main

// mcp_skills.go — the skill MCP tools (spec 08-images-and-skills §14),
// registered onto the host MCP server in mcpserver.go.
//
// The whole surface is four tools:
//
//	skill_create(name, labels, markdown, install_sh?)  → the stored revision
//	skill_list(label_selector?)                        → identity+labels+provenance
//	skill_get(name)                                    → the current record in full
//	skill_install(name)                                → installs it into THIS session
//
// A skill is knowledge plus its install (§14.1): a markdown document nobody can
// act on is advice, an install script nobody knows to use is software. Carrying
// both is what makes a capability portable.
//
// # The one asymmetry worth understanding
//
// Three of these tools change the PROJECT, and are config-evented like every
// other configuration mutation (§15). `skill_install` changes the SESSION —
// this container's filesystem, for as long as this container lives — and
// therefore writes NO config event (§14.2). Nobody decided anything about the
// project by installing a skill for one job.
//
//	skill_create ──▶ agentdb (+ config event)          the project learns
//	skill_install ─▶ this session's container only     this job gets equipped
//
// # How skill_install reaches the container
//
// The tool runs on the host, but the work is in the image, so it goes back down
// the same path a human's workspace request takes: Runner.Status yields the
// calling session's sandbox address, and agentd POSTs the markdown and the
// script to the sandbox's /skills/install route, which writes the file into the
// harness's skills directory and runs the script. The route reports both
// outcomes and this tool reports them onward, in full: §14.2 requires a failed
// install to be a VISIBLE failure the worker can react to, never a silent
// no-op, so a non-zero exit status comes back as a tool error carrying the
// script's output rather than as a cheerful success object.
//
// Selection is prompt policy (§14.5, P1): core never auto-installs anything and
// workers have no `skills` column. Which skills a worker installs, and when, is
// one sentence of English in its prompt.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	agentkit "github.com/binocarlos/badcode-agent-orange"
	"github.com/binocarlos/badcode-agent-orange/agentdb"
)

// skillStore is the narrow slice of *agentdb.Store the tools need. Note what is
// NOT in it: no update and no delete, so §14.1's append-only invariant survives
// the seam.
type skillStore interface {
	CreateSkill(ctx context.Context, sk *agentdb.Skill, cw agentdb.ConfigWrite) (*agentdb.Skill, error)
	ListProjectSkills(ctx context.Context, q agentdb.SkillCatalogQuery) ([]*agentdb.SkillSummary, error)
	GetProjectSkill(ctx context.Context, project, name string) (*agentdb.Skill, error)
}

// sessionLocator is the engine seam skill_install needs: where the calling
// session's sandbox is listening. agentkit.Runner satisfies it.
type sessionLocator interface {
	Status(ctx context.Context, ref agentkit.SessionRef) (*agentkit.SessionStatus, error)
}

// skillListCap bounds a bare skill_list, for the reason imageListCap does.
const skillListCap = 200

// skillInstallTimeout bounds one in-session install. Generous, because an
// install script may compile something; bounded, because a script that hangs
// would otherwise hang the job with no explanation. The sandbox enforces its
// own, slightly shorter budget so the failure is reported by the side that can
// see the script's output.
const skillInstallTimeout = 15 * time.Minute

type skillTools struct {
	store      skillStore
	sessions   sessionLocator
	permalinks permalinker
	// http is the client used to reach a session's sandbox. Injected so tests
	// can point it at an httptest server rather than a real container.
	http *http.Client
}

func newSkillTools(store skillStore, sessions sessionLocator, permalinks permalinker) *skillTools {
	return &skillTools{
		store:      store,
		sessions:   sessions,
		permalinks: permalinks,
		http:       &http.Client{Timeout: skillInstallTimeout},
	}
}

// ---------------------------------------------------------------------------
// Result shapes
// ---------------------------------------------------------------------------

// skillRecord is a skill in full — what skill_create and skill_get return.
type skillRecord struct {
	Name             string            `json:"name"`
	Revision         int               `json:"revision"`
	Labels           map[string]string `json:"labels"`
	Markdown         string            `json:"markdown"`
	InstallSh        string            `json:"install_sh"`
	CreatedByWorker  string            `json:"created_by_worker"`
	CreatedBySession string            `json:"created_by_session"`
	SessionURL       string            `json:"session_url"`
	CreatedAt        int64             `json:"created_at"`
}

// skillEntry is one line of skill_list: identity, labels and provenance —
// deliberately NOT the markdown (§14.2, the same search-returns-snippets /
// get-returns-everything split as memory_search/memory_get).
type skillEntry struct {
	Name             string            `json:"name"`
	Revision         int               `json:"revision"`
	Revisions        int               `json:"revisions"`
	Labels           map[string]string `json:"labels"`
	HasInstallScript bool              `json:"has_install_script"`
	CreatedByWorker  string            `json:"created_by_worker"`
	CreatedBySession string            `json:"created_by_session"`
	SessionURL       string            `json:"session_url"`
	CreatedAt        int64             `json:"created_at"`
}

func (s *skillTools) record(project string, sk *agentdb.Skill) skillRecord {
	return skillRecord{
		Name:             sk.Name,
		Revision:         sk.Revision,
		Labels:           labelMap(sk.Labels),
		Markdown:         sk.Markdown,
		InstallSh:        sk.InstallSh,
		CreatedByWorker:  sk.CreatedByWorker,
		CreatedBySession: sk.CreatedBySession,
		SessionURL:       s.permalinks.SessionURL(project, sk.CreatedBySession),
		CreatedAt:        sk.CreatedAt,
	}
}

// ---------------------------------------------------------------------------
// Tool descriptions
// ---------------------------------------------------------------------------

const skillCreateDescription = `Teach this project a skill: a markdown document plus the shell script that installs whatever software it needs.

This is how a lesson becomes shared property. If you spent an afternoon working ` +
	`out how to do something, write down what you learned as the markdown, paste ` +
	`the install commands you ACTUALLY RAN into install_sh, label it, and the next ` +
	`worker calls skill_install and skips the afternoon.

Write the markdown the way a Claude Code skill is written: what the capability ` +
	`is, when to reach for it, how to use it. It is read by a model, not filed.

install_sh is optional — omit it for a skill that is pure knowledge. When you do ` +
	`supply one it must be non-interactive and safe to re-run (it will be run again ` +
	`in every session that installs the skill).

Skills are append-only. Calling this with an existing name records a NEW ` +
	`revision; readers always get the newest, and the superseded documents remain ` +
	`as an honest record of how the capability was taught over time. Nothing is ` +
	`overwritten and nothing can be deleted.

Labels say what it is for and who should install it. Identifiers only: ` +
	`alphanumeric with '-', '_' or '.', at most 63 characters, at most 32 labels.`

const skillListDescription = `List this project's skills, newest first — one entry per skill, carrying its current revision.

Each entry gives identity, labels, provenance and whether installing it runs ` +
	`anything (has_install_script). It does NOT include the markdown: use ` +
	`skill_get to read one, or skill_install to install it.

The optional label_selector uses the same Kubernetes-style grammar as ` +
	`memory_search, comma-ANDed: "kind=media", "kind in (media, writing)", ` +
	`"exists kind", "!deprecated". No OR and no nesting. It matches against the ` +
	`CURRENT revision's labels — a label a newer teaching dropped is gone.`

const skillGetDescription = `Read a skill in full: its markdown document and its install script, at the current (newest) revision.

Use this to read a skill without installing it — to check what it would do, or ` +
	`to write an improved revision of it. If you actually want the capability in ` +
	`this session, call skill_install instead: it writes the document where the ` +
	`model can use it AND runs the install script.`

const skillInstallDescription = `Install a project skill into THIS session: write its document where you can use it, and run its install script here.

This changes this session only. It teaches the project nothing and installs ` +
	`nothing anywhere else — the container goes away when the session does, and ` +
	`the next job starts clean unless someone burns an image (image_create).

Two things happen and BOTH are reported. The markdown is written into the skills ` +
	`directory, so it becomes available to you as an ordinary skill — on your NEXT ` +
	`turn, not this one, because skills are loaded when a turn starts. Then the ` +
	`install script runs, and its exit status and output come back to you.

If the script fails you are told, with its output. Do not proceed as though the ` +
	`capability is available: read the error, and either fix it (you have a shell) ` +
	`or say plainly that the skill could not be installed.`

// tools returns the four skill tools, in the order the model sees them.
func (s *skillTools) tools() []*mcpTool {
	return []*mcpTool{
		{
			Name:        "skill_create",
			Description: skillCreateDescription,
			InputSchema: objectSchema(map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Stable identity, kebab-case, e.g. \"render-social-video\". Lowercase alphanumerics and '-' only — it becomes a directory name when installed.",
				},
				"labels": map[string]any{
					"type":                 "object",
					"description":          "Flat string→string labels: what it is for, who should install it. Identifiers only: [A-Za-z0-9] with '-', '_', '.', ≤63 chars, ≤32 labels.",
					"additionalProperties": map[string]any{"type": "string"},
				},
				"markdown": map[string]any{
					"type":        "string",
					"description": "The skill document: what the capability is, when to reach for it, how to use it. Required.",
				},
				"install_sh": map[string]any{
					"type":        "string",
					"description": "Optional shell script installing the skill's software. Non-interactive and re-runnable.",
				},
			}, []string{"name", "markdown"}),
			Handler: s.create,
		},
		{
			Name:        "skill_list",
			Description: skillListDescription,
			InputSchema: objectSchema(map[string]any{
				"label_selector": map[string]any{
					"type":        "string",
					"description": "Kubernetes-style label selector, comma-ANDed. Optional; omit for every skill.",
				},
			}, nil),
			Handler: s.list,
		},
		{
			Name:        "skill_get",
			Description: skillGetDescription,
			InputSchema: objectSchema(map[string]any{
				"name": map[string]any{"type": "string", "description": "The skill name, as returned by skill_list."},
			}, []string{"name"}),
			Handler: s.get,
		},
		{
			Name:        "skill_install",
			Description: skillInstallDescription,
			InputSchema: objectSchema(map[string]any{
				"name": map[string]any{"type": "string", "description": "The skill name, as returned by skill_list."},
			}, []string{"name"}),
			Handler: s.install,
		},
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type skillCreateArgs struct {
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels"`
	Markdown  string            `json:"markdown"`
	InstallSh string            `json:"install_sh"`
}

func (s *skillTools) create(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args skillCreateArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(args.Name)
	if err := agentdb.ValidateSkillName(name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Markdown) == "" {
		return nil, errors.New("markdown is required and must not be blank: a skill with no document is software nobody knows to use (§14.1)")
	}
	// Validate here as well as in the store so the model gets the specific
	// complaint rather than a wrapped database error (§9).
	if err := agentdb.ValidateLabels(args.Labels); err != nil {
		return nil, fmt.Errorf("labels: %w", err)
	}

	// §9 read-back: CreateSkill returns the row as the database holds it — that,
	// and not the caller's struct, is what is echoed. It also writes the
	// `skill_create` config event in the same transaction (§15.4).
	stored, err := s.store.CreateSkill(ctx, &agentdb.Skill{
		Customer:         caller.Project, // in code, always — never an argument
		Name:             name,
		Labels:           agentdb.LabelSet(args.Labels),
		Markdown:         args.Markdown,
		InstallSh:        args.InstallSh,
		CreatedByWorker:  caller.Worker,
		CreatedBySession: caller.SessionID,
	}, agentdb.ConfigWrite{Worker: caller.Worker, Session: caller.SessionID})
	if err != nil {
		return nil, err
	}
	return s.record(caller.Project, stored), nil
}

type skillListArgs struct {
	LabelSelector string `json:"label_selector"`
}

func (s *skillTools) list(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	var args skillListArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	rows, err := s.store.ListProjectSkills(ctx, agentdb.SkillCatalogQuery{
		Project:       caller.Project, // in code, always — never an argument
		LabelSelector: args.LabelSelector,
		Limit:         skillListCap + 1,
	})
	if err != nil {
		return nil, err
	}
	truncated := len(rows) > skillListCap
	if truncated {
		rows = rows[:skillListCap]
	}
	out := make([]skillEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, skillEntry{
			Name:             r.Name,
			Revision:         r.Revision,
			Revisions:        r.Revisions,
			Labels:           labelMap(r.Labels),
			HasInstallScript: r.HasInstallScript,
			CreatedByWorker:  r.CreatedByWorker,
			CreatedBySession: r.CreatedBySession,
			SessionURL:       s.permalinks.SessionURL(caller.Project, r.CreatedBySession),
			CreatedAt:        r.CreatedAt,
		})
	}
	result := map[string]any{"skills": out, "count": len(out)}
	if truncated {
		result["truncated"] = true
		result["note"] = fmt.Sprintf(
			"Only the %d newest skills are shown. Narrow the search with a label_selector to see older ones.", skillListCap)
	}
	return result, nil
}

type skillNameArgs struct {
	Name string `json:"name"`
}

func (s *skillTools) get(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	sk, err := s.lookup(ctx, caller, raw)
	if err != nil {
		return nil, err
	}
	return s.record(caller.Project, sk), nil
}

// lookup decodes a {name} argument and resolves the current revision, with the
// not-found message both callers want.
func (s *skillTools) lookup(ctx context.Context, caller mcpCaller, raw json.RawMessage) (*agentdb.Skill, error) {
	var args skillNameArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(args.Name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if err := agentdb.ValidateSkillName(name); err != nil {
		return nil, err
	}
	// The project is a parameter of the store call, not a filter applied after:
	// another project's skill is simply not found, with no existence leak.
	sk, err := s.store.GetProjectSkill(ctx, caller.Project, name)
	if err != nil {
		if errors.Is(err, agentdb.ErrSkillNotFound) {
			return nil, fmt.Errorf("no skill named %q in this project — call skill_list to see what there is, or skill_create to teach it", name)
		}
		return nil, err
	}
	return sk, nil
}

// ---------------------------------------------------------------------------
// skill_install — the load-bearing one (§14.2)
// ---------------------------------------------------------------------------

// sandboxSkillInstallRequest is the wire shape agentd POSTs to the sandbox.
// snake_case to match the rest of the host↔sandbox protocol.
type sandboxSkillInstallRequest struct {
	Name      string `json:"name"`
	Markdown  string `json:"markdown"`
	InstallSh string `json:"install_sh,omitempty"`
}

// sandboxSkillScriptResult is what the sandbox reports about the script leg.
type sandboxSkillScriptResult struct {
	Ran      bool   `json:"ran"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timed_out"`
	Error    string `json:"error,omitempty"`
}

// sandboxSkillInstallResponse is the sandbox's reply.
type sandboxSkillInstallResponse struct {
	OK           bool                     `json:"ok"`
	Name         string                   `json:"name"`
	Path         string                   `json:"path"`
	BytesWritten int                      `json:"bytes_written"`
	Script       sandboxSkillScriptResult `json:"script"`
	Error        string                   `json:"error,omitempty"`
}

func (s *skillTools) install(ctx context.Context, caller mcpCaller, raw json.RawMessage) (any, error) {
	sk, err := s.lookup(ctx, caller, raw)
	if err != nil {
		return nil, err
	}
	if caller.SessionID == "" {
		return nil, errors.New("skill_install can only be called from inside a session: this token names no session")
	}

	addr, err := s.sandboxAddress(ctx, caller.SessionID)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(sandboxSkillInstallRequest{
		Name: sk.Name, Markdown: sk.Markdown, InstallSh: sk.InstallSh,
	})
	if err != nil {
		return nil, fmt.Errorf("encode install request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(addr, "/")+"/skills/install", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("skill %q was NOT installed: could not reach this session's sandbox: %w", sk.Name, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxMCPRequestBytes))
	if err != nil {
		return nil, fmt.Errorf("skill %q: reading the sandbox's reply failed, so the install outcome is unknown: %w", sk.Name, err)
	}
	var out sandboxSkillInstallResponse
	if uErr := json.Unmarshal(payload, &out); uErr != nil {
		return nil, fmt.Errorf("skill %q: the sandbox replied %s with something that is not an install result (%s), so the install outcome is unknown",
			sk.Name, resp.Status, truncateForReport(string(payload), 400))
	}
	if resp.StatusCode != http.StatusOK && out.Error == "" {
		out.Error = "sandbox returned " + resp.Status
	}

	// §14.2: a failed install must be a VISIBLE failure, never a silent no-op.
	// The whole report — what was written and what the script did — goes into
	// the error text, because the transport reduces an errored tool result to
	// its message and half a story is worse than none.
	if !out.OK || out.Error != "" || (out.Script.Ran && out.Script.ExitCode != 0) || out.Script.TimedOut {
		return nil, errors.New(installFailureReport(sk.Name, &out))
	}
	return map[string]any{
		"name":          sk.Name,
		"revision":      sk.Revision,
		"installed":     true,
		"file_written":  out.Path,
		"bytes_written": out.BytesWritten,
		"script":        installScriptReport(&out.Script),
		"note": "The document is in place; it becomes available as a skill on your NEXT turn, " +
			"because skills are loaded when a turn starts. Anything the script installed is usable now.",
	}, nil
}

// sandboxAddress resolves where the calling session's in-image server listens.
func (s *skillTools) sandboxAddress(ctx context.Context, sessionID string) (string, error) {
	if s.sessions == nil {
		return "", errors.New("skill_install is not available on this deployment: no session runtime is wired")
	}
	status, err := s.sessions.Status(ctx, agentkit.SessionRef{SessionID: sessionID})
	if err != nil {
		return "", fmt.Errorf("could not locate this session's container: %w", err)
	}
	if status == nil || strings.TrimSpace(status.SandboxAddress) == "" {
		return "", fmt.Errorf("this session's container is not running (state %q), so there is nowhere to install into",
			statusState(status))
	}
	return status.SandboxAddress, nil
}

func statusState(st *agentkit.SessionStatus) string {
	if st == nil {
		return "unknown"
	}
	return st.RuntimeState
}

// installScriptReport renders the script leg for a successful install.
func installScriptReport(sc *sandboxSkillScriptResult) map[string]any {
	if !sc.Ran {
		return map[string]any{"ran": false, "note": "this skill carries no install script"}
	}
	return map[string]any{
		"ran":       true,
		"exit_code": sc.ExitCode,
		"stdout":    sc.Stdout,
		"stderr":    sc.Stderr,
	}
}

// installFailureReport is the whole story in one message: what was written,
// what ran, how it ended, and what it said. Everything a worker needs to decide
// whether to retry, fix it by hand, or report that it could not be done.
func installFailureReport(name string, out *sandboxSkillInstallResponse) string {
	var b strings.Builder
	fmt.Fprintf(&b, "skill %q was NOT successfully installed.\n", name)
	if out.Path != "" {
		fmt.Fprintf(&b, "document: written to %s (%d bytes)\n", out.Path, out.BytesWritten)
	} else {
		b.WriteString("document: NOT written\n")
	}
	switch {
	case out.Script.TimedOut:
		fmt.Fprintf(&b, "install script: TIMED OUT after running too long (exit %d)\n", out.Script.ExitCode)
	case out.Script.Ran:
		fmt.Fprintf(&b, "install script: exit status %d\n", out.Script.ExitCode)
	default:
		b.WriteString("install script: did not run\n")
	}
	if out.Script.Error != "" {
		fmt.Fprintf(&b, "script error: %s\n", out.Script.Error)
	}
	if out.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", out.Error)
	}
	if strings.TrimSpace(out.Script.Stdout) != "" {
		fmt.Fprintf(&b, "--- stdout ---\n%s\n", truncateForReport(out.Script.Stdout, 4000))
	}
	if strings.TrimSpace(out.Script.Stderr) != "" {
		fmt.Fprintf(&b, "--- stderr ---\n%s\n", truncateForReport(out.Script.Stderr, 4000))
	}
	b.WriteString("Do not proceed as though this capability is available.")
	return b.String()
}

// truncateForReport keeps a failure message readable without hiding the end,
// which is where a script usually says what went wrong.
func truncateForReport(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	return s[:half] + "\n… (truncated) …\n" + s[len(s)-half:]
}
