package agentkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/binocarlos/badcode-agent-orange/agentdb"
	"github.com/binocarlos/badcode-agent-orange/artifacts"
	"github.com/binocarlos/badcode-agent-orange/events"
	"github.com/binocarlos/badcode-agent-orange/execenv"
	"github.com/binocarlos/badcode-agent-orange/extension"
	"github.com/binocarlos/badcode-agent-orange/fleet"
	"github.com/binocarlos/badcode-agent-orange/imageregistry"
)

// ErrHarnessUnavailable is returned by CreateSession when the sandbox rejects
// the harness choice with a 400 (UNKNOWN_HARNESS) or 424 (HARNESS_CREDENTIALS_MISSING)
// response. The host can use this to clean up the orphan session row.
type ErrHarnessUnavailable struct {
	// StatusCode is the HTTP status returned by the sandbox (400 or 424).
	StatusCode int
	// Body is the raw response body from the sandbox.
	Body string
}

func (e *ErrHarnessUnavailable) Error() string {
	return fmt.Sprintf("harness unavailable (status %d): %s", e.StatusCode, e.Body)
}

// codeInvalidMCPServers is the sandbox's error code for a create payload whose
// `mcp_servers` it refuses (docs/product/01-session-config.md §4.2).
const codeInvalidMCPServers = "INVALID_MCP_SERVERS"

// ErrInvalidMCPServers is returned by CreateSession when the sandbox rejects the
// session's MCP configuration with a 400. It is terminal, never retryable: the
// config is wrong and will be just as wrong next time. The host cleans up the
// orphan session row exactly as it does for ErrHarnessUnavailable.
type ErrInvalidMCPServers struct {
	// Body is the raw response body from the sandbox.
	Body string
}

func (e *ErrInvalidMCPServers) Error() string {
	return fmt.Sprintf("invalid mcp servers: %s", e.Body)
}

// sandboxErrorCode pulls the `code` field out of a sandbox error body, or ""
// when the body is not the expected shape (which keeps an unparseable 400 on
// the pre-existing ErrHarnessUnavailable path rather than swallowing it).
func sandboxErrorCode(body []byte) string {
	var parsed struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return parsed.Code
}

// runnerImpl is the default Runner. It contains the generic orchestration logic
// ported from the TypeScript orchestrator (sandbox-manager.ts, routes/sessions.ts,
// state-machine.ts) plus the SSE relay ported from goapi/pkg/server/agent.go.
//
// Lifecycle methods touch only interfaces and are fully implemented here.
// SendMessage/Stream/Stop speak the sandbox HTTP contract (docs/07) over the
// engine-reported Instance.Address using deps.HTTPClient — production-shaped; a
// host integration-tests them by pointing a mock engine at a fake agent server.
type runnerImpl struct {
	deps     Deps
	pipeline events.EventPipeline
	sink     *storeSink

	mu              sync.Mutex
	instances       map[string]*execenv.Instance // sessionID -> instance
	instanceWorkers map[string]string            // sessionID -> workerID
	lastActivity    map[string]time.Time
	seq             int

	// queryErrors holds the error text seen on the in-flight turn, keyed
	// sessionID+"\x00"+queryID, so worker.failed can report what went wrong.
	// Written by the error MarkerHook, drained at the end of every turn.
	queryErrors map[string]string

	// userImageHandles caches content-hash → Handle for previously built user images.
	// On a cache hit from registry.Resolve, the runner returns this handle directly
	// without calling Provision/Snapshot/Persist (the build path).
	userImageHandles map[string]imageregistry.Handle

	// progress holds live snapshot/restore progress per session, read by Status.
	progress *progressStore

	stop   chan struct{}
	closed bool
}

func newRunnerImpl(deps Deps) *runnerImpl {
	r := &runnerImpl{
		deps:             deps,
		instances:        map[string]*execenv.Instance{},
		instanceWorkers:  map[string]string{},
		lastActivity:     map[string]time.Time{},
		queryErrors:      map[string]string{},
		userImageHandles: map[string]imageregistry.Handle{},
		progress:         newProgressStore(),
		stop:             make(chan struct{}),
	}
	r.sink = &storeSink{store: deps.Store, pending: map[string]int{}}
	if deps.Events != nil {
		r.pipeline = deps.Events
	} else {
		// Default pipeline: persist via the host SessionStore, with an
		// artifact_registered marker hook that pulls bytes + saves them.
		r.pipeline = events.NewPipeline(r.sink,
			struct {
				Type events.Type
				Hook events.MarkerHook
			}{Type: events.ArtifactRegistered, Hook: r.onArtifactRegistered},
			struct {
				Type events.Type
				Hook events.MarkerHook
			}{Type: events.SkillHoisted, Hook: r.onSkillHoisted},
			struct {
				Type events.Type
				Hook events.MarkerHook
			}{Type: events.SkillInstalled, Hook: r.onSkillInstalled},
			// Capture the error text of a failing turn for worker.failed (§8.2).
			struct {
				Type events.Type
				Hook events.MarkerHook
			}{Type: events.Error, Hook: r.onQueryError},
		)
	}
	return r
}

// --- lifecycle ---------------------------------------------------------------

// MarkCreating pre-registers the "create" progress op (downloading phase) for a
// session synchronously. Hosts call this BEFORE backgrounding CreateSession so a
// status poll that lands before the goroutine schedules still observes an active
// op — otherwise the not-yet-provisioned session reports a "destroyed" runtime
// state with no progress and the frontend would treat it as settled and stop
// polling. Idempotent with CreateSession's own begin (CreateSession skips begin
// when an entry already exists, preserving StartedAt).
func (r *runnerImpl) MarkCreating(sessionID string) {
	r.progress.begin(sessionID, "create")
	r.progress.phase(sessionID, "downloading")
}

func (r *runnerImpl) CreateSession(ctx context.Context, req CreateSessionRequest) (handle *SessionHandle, err error) {
	// Track image-pull + provision progress under a "create" op so the frontend can
	// render a download bar while the launch image is pulled. MarkCreating may have
	// begun this already (async host path) — don't reset StartedAt if so.
	if _, ok := r.progress.get(req.SessionID); !ok {
		r.progress.begin(req.SessionID, "create")
	}
	defer func() {
		if err != nil {
			r.progress.finish(req.SessionID, err.Error())
		} else {
			r.progress.finish(req.SessionID, "")
		}
	}()

	// The host's per-session defaults (§5), resolved ONCE: both the MCP union
	// below and the launch image read it, and asking twice would double every
	// query the provider makes on the hot path of every create.
	sctx, err := r.resolveSessionContext(ctx, req)
	if err != nil {
		return nil, err
	}
	// Fold the host-resolved project ∪ worker MCP defaults under the
	// request-supplied servers, so the union a SessionContextProvider computes
	// actually reaches the container (§4.1, §5).
	req.MCPServers = mergeSessionMCPServers(sctx, req.MCPServers)
	// Record session-scoped MCP config on the session row before anything is
	// provisioned, so resume / re-provision can re-supply it (§4.5) and so a
	// malformed config fails the create loudly instead of reaching the harness.
	if err = r.persistMCPServers(ctx, req.SessionID, req.MCPServers); err != nil {
		return nil, err
	}
	// Record the composition of a worker job (§6.2): which worker, and the
	// exact prompt it launched with.
	if err = r.persistComposition(ctx, req); err != nil {
		return nil, err
	}

	img, imgFrom, err := r.resolveLaunchImage(ctx, req.Image, req.CustomImageID, req.UserEmail, req.Customer, sctx)
	if err != nil {
		return nil, fmt.Errorf("resolve launch image: %w", err)
	}
	// Pull (force-pull on dev :dev tags) while streaming byte progress to the store.
	r.progress.phase(req.SessionID, "downloading")
	pctx := imageregistry.WithProgressSink(ctx, r.progressSinkFor(req.SessionID))
	if err = r.deps.Registry.EnsurePresent(pctx, img); err != nil {
		// Annotated with the setting that chose the image: an image that cannot
		// be pulled is otherwise indistinguishable from any other launch
		// failure, and "no running instance" is what the operator is left with.
		return nil, fmt.Errorf("ensure image present: %w", imgFrom.annotate(img, err))
	}
	// Resolve which worker will host this session.
	worker, err := r.deps.Fleet.PlaceForSession(ctx, req.SessionID, fleet.PlacementHint{})
	if err != nil {
		return nil, fmt.Errorf("place session: %w", err)
	}
	scope := extension.ContextScope{Customer: req.Customer, Job: req.Job, Persona: req.Persona, UserEmail: req.UserEmail}
	token, err := r.issueToken(ctx, scope, req.SessionID)
	if err != nil {
		return nil, err
	}

	// Tenancy-aware provisioning: shared tenancy reuses an existing instance.
	r.progress.phase(req.SessionID, "starting")
	inst, err := r.provisionOnWorker(ctx, req.SessionID, img, worker,
		r.sessionEnv(req.SessionID, token, req.Model))
	if err != nil {
		// The host owns the session row and deletes the orphan on this error.
		// Annotated too: an image that pulls but will not run (wrong arch, no
		// harness in it) is still the configured image's fault.
		return nil, fmt.Errorf("provision: %w", imgFrom.annotate(img, err))
	}
	r.track(req.SessionID, worker.ID, inst)

	// POST /sessions to the in-image control server: boot the harness + credential check.
	// Skip when the instance address is not an HTTP URL (e.g. mock:// in unit tests that
	// test only the orchestration layer, not the sandbox HTTP contract).
	if inst.Address != "" && (len(inst.Address) >= 4 && inst.Address[:4] == "http") {
		if err := r.postCreateSession(ctx, inst.Address, req); err != nil {
			// If the sandbox refused the harness, surface a typed error so the host
			// can clean up the orphan session row.
			return nil, err
		}
	}

	return &SessionHandle{SessionID: req.SessionID, Address: inst.Address, State: string(inst.State)}, nil
}

// resolveSessionContext asks the host for this session's defaults (§5: system
// prompt, image chain, MCP servers). nil, nil when no provider is wired — the
// pre-provider path, unchanged.
//
// A provider error fails the create: a session that silently launched without
// the project's tools, or from a different environment than the one it was
// pointed at, is the failure this seam exists to prevent.
func (r *runnerImpl) resolveSessionContext(ctx context.Context, req CreateSessionRequest) (*extension.SessionContext, error) {
	if r.deps.SessionContext == nil {
		return nil, nil
	}
	scope := extension.ContextScope{
		Customer: req.Customer, Job: req.Job, Persona: req.Persona, UserEmail: req.UserEmail,
	}
	sc, err := r.deps.SessionContext.Resolve(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("resolve session context: %w", err)
	}
	return sc, nil
}

// mergeSessionMCPServers is the effective MCP configuration for a create: the
// host's project ∪ worker defaults (extension.SessionContext.MCPServers) with
// the request-supplied servers layered on top, so a request entry wins a name
// collision (§5 — the resolved context is "the defaults which the request may
// extend").
//
// Returns reqServers unchanged when the context is absent or contributes
// nothing, which keeps every pre-existing caller on exactly the old path.
func mergeSessionMCPServers(sc *extension.SessionContext, reqServers map[string]MCPServerConfig) map[string]MCPServerConfig {
	if sc == nil || len(sc.MCPServers) == 0 {
		return reqServers
	}
	merged := make(agentdb.MCPServers, len(sc.MCPServers)+len(reqServers))
	for name, cfg := range sc.MCPServers {
		merged[name] = cfg
	}
	for name, cfg := range reqServers {
		merged[name] = cfg
	}
	return merged
}

// persistMCPServers validates and writes the session's MCP config onto its
// (host-owned) session row. It is a no-op when the request carries no MCP
// config, which keeps every pre-existing caller — including tests that never
// seed a row — on exactly the old path. When config *is* supplied the row is
// required: the host's contract is to persist the row before CreateSession, and
// silently dropping MCP config would break resume.
func (r *runnerImpl) persistMCPServers(ctx context.Context, sessionID string, servers map[string]MCPServerConfig) error {
	if len(servers) == 0 {
		return nil
	}
	cfg := agentdb.MCPServers(servers)
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("mcp servers: %w", err)
	}
	sess, err := r.deps.Store.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("persist mcp servers: load session row: %w", err)
	}
	sess.MCPServers = cfg
	if _, err := r.deps.Store.UpdateSession(ctx, sess); err != nil {
		return fmt.Errorf("persist mcp servers: %w", err)
	}
	return nil
}

// persistComposition writes the provenance of a composed worker job onto the
// session row (docs/product/02-workers.md §6.2, §6.5): the worker whose job
// this is, and `composed_prompt` — the exact system prompt ComposeJob produced,
// recorded at composition time so every transcript is tied to the prompt that
// produced it.
//
// The prompt written is req.SystemPrompt, i.e. the very string handed to the
// harness, rather than a second copy the caller could let drift.
//
// A no-op for plain vanilla sessions (no worker), which keeps every
// pre-existing caller on exactly the old path. For a worker job the row is
// required — losing the provenance link silently is the failure this whole
// column exists to prevent.
func (r *runnerImpl) persistComposition(ctx context.Context, req CreateSessionRequest) error {
	if req.Worker == "" {
		return nil
	}
	sess, err := r.deps.Store.GetSession(ctx, req.SessionID)
	if err != nil {
		return fmt.Errorf("persist composition: load session row: %w", err)
	}
	sess.Worker = req.Worker
	sess.ComposedPrompt = req.SystemPrompt
	if _, err := r.deps.Store.UpdateSession(ctx, sess); err != nil {
		return fmt.Errorf("persist composition: %w", err)
	}
	return nil
}

// provisionOnWorker provisions a new instance on the given worker, branching on
// tenancy. For TenancyShared it returns the single shared instance for that worker
// (creating it if not yet present). For TenancyPerSession it provisions fresh.
func (r *runnerImpl) provisionOnWorker(ctx context.Context, sessionID string, img execenv.ImageRef, worker *fleet.Worker, env map[string]string) (*execenv.Instance, error) {
	if worker.Caps.Tenancy == execenv.TenancyShared {
		// Shared tenancy: look for an existing shared instance on this worker.
		// The shared instance is keyed by the worker ID (not sessionID).
		sharedKey := "__shared__" + worker.ID
		if existing := r.get(sharedKey); existing != nil {
			return existing, nil
		}
		// Provision the shared instance once.
		inst, err := worker.Env.Provision(ctx, execenv.ProvisionSpec{
			SessionID: sharedKey,
			Image:     img,
			Env:       env,
			Labels:    map[string]string{"agentkit.managed": "true", "agentkit.shared": worker.ID},
			Mounts:    r.deps.Policy.Mounts,
			AgentPort: r.deps.Policy.AgentPort,
		})
		if err != nil {
			return nil, err
		}
		r.track(sharedKey, worker.ID, inst)
		return inst, nil
	}
	// Per-session: provision a fresh instance.
	return worker.Env.Provision(ctx, execenv.ProvisionSpec{
		SessionID: sessionID,
		Image:     img,
		Env:       env,
		Labels:    map[string]string{"agentkit.managed": "true", "agentkit.session": sessionID},
		Mounts:    r.deps.Policy.Mounts,
		AgentPort: r.deps.Policy.AgentPort,
	})
}

// postCreateSession calls POST {addr}/sessions on the in-image control server.
// A 400 (UNKNOWN_HARNESS) or 424 (HARNESS_CREDENTIALS_MISSING) response is mapped
// to *ErrHarnessUnavailable; a 400 whose body carries INVALID_MCP_SERVERS is
// mapped to *ErrInvalidMCPServers instead, because "your MCP config is wrong" is
// a different thing for a host to report than "that harness does not exist".
//
// The create is the ONLY place session MCP config crosses into the container
// (§4.2): the payload carries `mcp_servers` in snake_case, whose inner shape is
// exactly the JSON tags of agentdb.MCPServerConfig (command/args/env for stdio,
// url/headers for http) — a plain marshal is the wire format, no adapter.
// Re-provision posts create again with the config read back off the session row,
// so an idempotent create refreshes it (§4.5).
func (r *runnerImpl) postCreateSession(ctx context.Context, addr string, req CreateSessionRequest) error {
	harnessName := string(req.Harness)
	if harnessName == "" {
		harnessName = string(HarnessClaudeAgentSDK)
	}
	payload := map[string]any{
		"sessionId": req.SessionID,
		"harness":   harnessName,
	}
	if req.Model != "" {
		payload["model"] = req.Model
	}
	if req.MaxTurns > 0 {
		payload["maxTurns"] = req.MaxTurns
	}
	if len(req.MCPServers) > 0 {
		payload["mcp_servers"] = req.MCPServers
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, addr+"/sessions", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create-session request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := r.deps.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("create-session: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 400 || resp.StatusCode == 424 {
		respBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == 400 && sandboxErrorCode(respBody) == codeInvalidMCPServers {
			return &ErrInvalidMCPServers{Body: string(respBody)}
		}
		return &ErrHarnessUnavailable{StatusCode: resp.StatusCode, Body: string(respBody)}
	}
	// Drain body to allow connection reuse.
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Resume brings a session back to running: if its container is live it is reused,
// otherwise it is restored from the session's snapshot (the archived → running
// transition). This is the only "wake" path — there is no warm suspended state.
func (r *runnerImpl) Resume(ctx context.Context, ref SessionRef) (*SessionHandle, error) {
	inst, err := r.ensureRunning(ctx, ref.SessionID)
	if err != nil {
		return nil, err
	}
	return &SessionHandle{SessionID: ref.SessionID, Address: inst.Address, State: string(inst.State)}, nil
}

func (r *runnerImpl) Destroy(ctx context.Context, ref SessionRef) error {
	inst := r.get(ref.SessionID)
	if inst != nil {
		env, err := r.workerEnvFor(ref.SessionID)
		if err == nil {
			// Check tenancy: for shared instances, DELETE the session route rather than
			// destroying the container.
			workerID := r.getWorkerID(ref.SessionID)
			if workerID != "" {
				if worker, err2 := r.deps.Fleet.WorkerForSession(context.Background(), ref.SessionID); err2 == nil &&
					worker.Caps.Tenancy == execenv.TenancyShared {
					// Destroy on a shared instance is a DELETE /sessions/:id, not a container teardown.
					r.forget(ref.SessionID)
					return r.deps.Artifacts.MarkLost(ctx, ref.SessionID)
				}
			}
			if err := env.Destroy(ctx, inst.ID, execenv.DestroyOptions{SkipSnapshot: true}); err != nil {
				return err
			}
		}
	}
	r.forget(ref.SessionID)
	// Artifacts not yet extracted are lost when the workspace is gone.
	return r.deps.Artifacts.MarkLost(ctx, ref.SessionID)
}

func (r *runnerImpl) progressSinkFor(sid string) imageregistry.ProgressSink {
	return sessionProgressSink{store: r.progress, sid: sid}
}

func (r *runnerImpl) Snapshot(ctx context.Context, ref SessionRef) (h imageregistry.Handle, err error) {
	// Shared-tenancy snapshot ban: a filesystem diff is not attributable to a single
	// session when many sessions share one container.  Fail fast with a clear error.
	// Keep this ABOVE progress.begin — a rejected shared-tenancy snapshot never
	// started, so it should not register progress.
	if worker, wErr := r.deps.Fleet.WorkerForSession(ctx, ref.SessionID); wErr == nil {
		if worker.Caps.Tenancy == execenv.TenancyShared || !worker.Caps.SupportsSnapshot {
			return imageregistry.Handle{}, fmt.Errorf(
				"snapshot: session %q runs on a shared-tenancy worker (%s) that does not support per-session snapshots",
				ref.SessionID, worker.ID,
			)
		}
	}

	r.progress.begin(ref.SessionID, "snapshot")
	defer func() {
		if err != nil {
			r.progress.finish(ref.SessionID, err.Error())
		} else {
			r.progress.finish(ref.SessionID, "")
		}
	}()

	inst, err := r.ensureRunning(ctx, ref.SessionID)
	if err != nil {
		return imageregistry.Handle{}, err
	}
	env, err := r.workerEnvFor(ref.SessionID)
	if err != nil {
		return imageregistry.Handle{}, err
	}
	caps := r.deps.Registry.Capabilities()

	r.progress.phase(ref.SessionID, "committing")
	ref2, err := env.Snapshot(ctx, inst.ID, execenv.SnapshotOptions{ForceFull: !caps.SupportsDiff})
	if err != nil {
		return imageregistry.Handle{}, fmt.Errorf("snapshot: %w", err)
	}

	// Diff-base fix: diff against the LAUNCH image (the image the session was
	// actually provisioned from — may be a user image, not Policy.BaseImage).
	// inst.Image is set by Provision and recorded on the execenv.Instance.
	// If not set (e.g. legacy path), fall back to Policy.BaseImage.
	diffBase := inst.Image
	if diffBase == "" {
		diffBase = execenv.ImageRef(r.deps.Policy.BaseImage)
	}

	r.progress.phase(ref.SessionID, "uploading")
	pctx := imageregistry.WithProgressSink(ctx, r.progressSinkFor(ref.SessionID))
	h, err = r.deps.Registry.Persist(pctx, ref2, imageregistry.PersistOptions{
		SessionID:  ref.SessionID,
		PreferDiff: caps.SupportsDiff,
		BaseImage:  diffBase,
	})
	if err != nil {
		return imageregistry.Handle{}, fmt.Errorf("persist snapshot: %w", err)
	}
	if err = r.deps.Store.SetSnapshotHandle(ctx, ref.SessionID, h); err != nil {
		return imageregistry.Handle{}, err
	}
	return h, nil
}

// safeWorkspaceJoin resolves a workspace-relative dest to an absolute path under
// /workspace, rejecting absolute paths and traversal (.. escaping the root).
func safeWorkspaceJoin(dest string) (string, error) {
	if dest == "" {
		return "", fmt.Errorf("empty dest")
	}
	if path.IsAbs(dest) {
		return "", fmt.Errorf("dest must be workspace-relative, got absolute %q", dest)
	}
	cleaned := path.Clean("/workspace/" + dest)
	if cleaned != "/workspace" && !strings.HasPrefix(cleaned, "/workspace/") {
		return "", fmt.Errorf("dest escapes workspace: %q", dest)
	}
	return cleaned, nil
}

// WriteWorkspaceFile writes content to /workspace/<relPath> in the running
// instance (mkdir -p parent, then cat >). Used to bake a focus into CLAUDE.md
// before snapshotting a session as an image.
func (r *runnerImpl) WriteWorkspaceFile(ctx context.Context, ref SessionRef, relPath string, content []byte) error {
	target, err := safeWorkspaceJoin(relPath)
	if err != nil {
		return fmt.Errorf("write-workspace-file: %w", err)
	}
	inst := r.get(ref.SessionID)
	if inst == nil {
		return fmt.Errorf("write-workspace-file: session %q has no running instance", ref.SessionID)
	}
	env, err := r.workerEnvFor(ref.SessionID)
	if err != nil {
		return fmt.Errorf("write-workspace-file: %w", err)
	}
	parent := path.Dir(target)
	if _, err := env.Exec(ctx, inst.ID, []string{"mkdir", "-p", parent}, execenv.ExecOptions{}); err != nil {
		return fmt.Errorf("write-workspace-file mkdir: %w", err)
	}
	if _, err := env.Exec(ctx, inst.ID, []string{"sh", "-c", `cat > "$1"`, "--", target}, execenv.ExecOptions{Stdin: bytes.NewReader(content)}); err != nil {
		return fmt.Errorf("write-workspace-file write: %w", err)
	}
	return nil
}

func (r *runnerImpl) Status(ctx context.Context, ref SessionRef) (*SessionStatus, error) {
	var prog *OpProgress
	if p, ok := r.progress.get(ref.SessionID); ok {
		cp := p
		prog = &cp
	}
	_, hasSnap, _ := r.deps.Store.GetSnapshotHandle(ctx, ref.SessionID)
	inst := r.get(ref.SessionID)
	if inst == nil {
		return &SessionStatus{SessionID: ref.SessionID, RuntimeState: string(execenv.StateDestroyed), HasSnapshot: hasSnap, Progress: prog}, nil
	}
	env, err := r.workerEnvFor(ref.SessionID)
	if err != nil {
		return &SessionStatus{SessionID: ref.SessionID, RuntimeState: string(execenv.StateDestroyed), HasSnapshot: hasSnap, Progress: prog}, nil
	}
	st, err := env.Status(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	// Expose the sandbox address so hosts can proxy workspace-file requests.
	addr := ""
	if st.State == execenv.StateRunning {
		addr = inst.Address
	}
	return &SessionStatus{SessionID: ref.SessionID, RuntimeState: string(st.State), ActiveQueryID: st.ActiveQueryID, SandboxAddress: addr, HasSnapshot: hasSnap, Progress: prog}, nil
}

func (r *runnerImpl) RunningSessions(ctx context.Context) (map[string]bool, error) {
	r.mu.Lock()
	ids := make([]string, 0, len(r.instances))
	for sid := range r.instances {
		ids = append(ids, sid)
	}
	r.mu.Unlock()

	out := make(map[string]bool, len(ids))
	for _, sid := range ids {
		st, err := r.Status(ctx, SessionRef{SessionID: sid})
		if err != nil {
			continue // transient inspect failure — treat as not-running for this poll
		}
		if st.RuntimeState == string(execenv.StateRunning) {
			out[sid] = true
		}
	}
	return out, nil
}

// --- messaging (sandbox HTTP contract) --------------------------------------

func (r *runnerImpl) SendMessage(ctx context.Context, ref SessionRef, msg SendMessageRequest, w Writer) error {
	inst, err := r.ensureRunning(ctx, ref.SessionID)
	if err != nil {
		return err
	}
	r.touch(ref.SessionID)

	// The per-turn request usually carries only the prompt — the chat UI does not
	// resend customer/job/persona on every message — so backfill any missing scope
	// fields (and the user email, never sent per-turn) from the authoritative
	// session record. This keeps the resolved system prompt's Session Context block
	// accurate for the dataset the session is actually bound to.
	scope := extension.ContextScope{Customer: msg.Customer, Job: msg.Job, Persona: msg.Persona}
	pinned := ""
	if r.deps.Store != nil {
		if sess, gerr := r.deps.Store.GetSession(ctx, ref.SessionID); gerr == nil {
			if scope.Customer == "" {
				scope.Customer = sess.Customer
			}
			if scope.Job == "" {
				scope.Job = sess.Job
			}
			if scope.Persona == "" {
				scope.Persona = sess.Persona
			}
			scope.UserEmail = sess.UserEmail
			pinned = sess.ComposedPrompt
		}
	}
	sys, err := r.turnSystemPrompt(ctx, pinned, scope)
	if err != nil {
		return err
	}
	queryID := r.nextQueryID(ref.SessionID)

	// Record the human's message the moment we accept it, BEFORE the sandbox is
	// asked to do anything. It also rides the pipeline as a LeadingEvent below,
	// but the pipeline only ever runs if the SSE response headers arrive: if the
	// caller goes away while POST /query-stream is still in flight (a browser
	// reload during the model's first think), Do() fails and we return without
	// the pipeline having existed. That used to lose the prompt entirely.
	// PersistQueryEventsFlat is an upsert on (session, query), so the pipeline's
	// end-of-turn write supersedes this seed rather than duplicating it.
	r.seedUserMessage(ctx, ref.SessionID, queryID, msg.Content)

	// attachments must be a JSON array: a nil slice marshals to null, which the
	// in-image agent's schema rejects ("expected array, received null").
	attachments := msg.Attachments
	if attachments == nil {
		attachments = []Attachment{}
	}
	payload := map[string]any{
		"prompt":       msg.Content,
		"systemPrompt": sys,
		"model":        msg.Model,
		"attachments":  attachments,
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, inst.Address+"/sessions/"+ref.SessionID+"/query-stream", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if ref.ScopedToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+ref.ScopedToken)
	}
	resp, err := r.deps.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("sessions/%s/query-stream: %w", ref.SessionID, err)
	}
	defer resp.Body.Close()

	// Persist the user's prompt as a user_message event so reloaded/restored
	// sessions replay the user turn (the live client renders it optimistically,
	// so it is NOT teed to the client writer — see QueryContext.LeadingEvents).
	q := events.QueryContext{SessionID: ref.SessionID, QueryID: queryID}
	if msg.Content != "" {
		q.LeadingEvents = []events.Envelope{{
			Type:      events.UserMessage,
			Data:      map[string]any{"content": msg.Content},
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		}}
	}

	res, err := r.pipeline.Run(ctx, q, resp.Body, w)
	r.touch(ref.SessionID)
	// The turn has settled and (crucially) been persisted: emit the §8.2
	// internal event for it, if this session is a worker job.
	r.emitJobOutcome(ctx, ref.SessionID, queryID, res, err)
	return err
}

func (r *runnerImpl) Stream(ctx context.Context, ref SessionRef, opts StreamOptions, w Writer) error {
	inst, err := r.ensureRunning(ctx, ref.SessionID)
	if err != nil {
		return err
	}
	// Both normal attach and reconnect use the session-scoped stream path.
	// GET /sessions/:sessionId/stream/:queryId replays the in-image buffer and
	// then streams live — no separate /reconnect endpoint exists in the contract
	// (doc 07 HTTP contract table).
	path := "/sessions/" + ref.SessionID + "/stream/" + opts.QueryID
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, inst.Address+path, nil)
	if err != nil {
		return err
	}
	if ref.ScopedToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+ref.ScopedToken)
	}
	resp, err := r.deps.HTTPClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(w, resp.Body)
	return err
}

func (r *runnerImpl) Stop(ctx context.Context, ref SessionRef) error {
	inst := r.get(ref.SessionID)
	if inst == nil {
		return nil
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, inst.Address+"/sessions/"+ref.SessionID+"/cancel", nil)
	if err != nil {
		return err
	}
	resp, err := r.deps.HTTPClient.Do(httpReq)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// --- control loops ----------------------------------------------------------

func (r *runnerImpl) Start(ctx context.Context) error {
	// Recover from ALL workers so a multi-worker fleet re-adopts all surviving instances.
	workers, err := r.deps.Fleet.Workers(ctx)
	if err != nil {
		return fmt.Errorf("list workers: %w", err)
	}
	for _, w := range workers {
		recovered, err := w.Env.Recover(ctx)
		if err != nil {
			return fmt.Errorf("recover (worker %s): %w", w.ID, err)
		}
		for _, inst := range recovered {
			r.track(inst.SessionID, w.ID, inst)
		}
	}
	if r.deps.Policy.ArchiveTimeout > 0 {
		go r.archiveLoop()
	}
	// The other half of the snapshot lifecycle: archiveLoop creates snapshots on
	// idle, snapshotReapLoop retires the ones whose TTL has expired (§5, B4).
	if r.deps.Policy.SnapshotReapInterval > 0 && r.deps.Snapshots != nil {
		go r.snapshotReapLoop()
	}
	return nil
}

func (r *runnerImpl) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		close(r.stop)
		r.closed = true
	}
	return nil
}

func (r *runnerImpl) archiveLoop() {
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			ctx := context.Background()
			for _, sid := range r.idleSessions(r.deps.Policy.ArchiveTimeout) {
				if r.sink.pendingCount(sid) > 0 { // flush guard
					continue
				}
				env, err := r.workerEnvFor(sid)
				if err != nil {
					continue
				}
				if !env.Capabilities().SupportsSnapshot {
					continue
				}
				if _, err := r.Snapshot(ctx, SessionRef{SessionID: sid}); err != nil {
					continue
				}
				_ = r.Destroy(ctx, SessionRef{SessionID: sid})
			}
		}
	}
}

// --- helpers ----------------------------------------------------------------

// ensureRunning makes the session's instance ready to accept work.
//
// Resolve-worker phase:
//  1. Try WorkerForSession (fast path: binding already exists).
//  2. If no binding, PlaceForSession (first message).
//
// Worker-loss: if the bound worker is no longer registered, check for a snapshot:
//   - snapshot present  → Rebind to a healthy worker, Materialize + Provision there.
//   - no snapshot       → return a clear unrecoverable error (session must be re-created).
//
// Instance-ready phase (on the resolved worker):
//   - running   → use it.
//   - destroyed → restore from snapshot handle via Materialize + Provision.
//   - none      → first message: already provisioned by CreateSession, which tracks
//     the instance; we should not reach here for a fresh per-session env.
//
// Tenancy-aware routing for TenancyShared: reuse the single shared instance on
// the worker; routing is done by the sandbox /sessions routes.
func (r *runnerImpl) ensureRunning(ctx context.Context, sessionID string) (*execenv.Instance, error) {
	// -- Step 0: Wait out an in-flight async create --
	// Hosts call MarkCreating synchronously and then background CreateSession (image
	// pulls can take minutes). A first message/stream can reach the runner before the
	// goroutine has tracked the instance. Without this wait we would observe no tracked
	// instance and wrongly fall into restore-from-snapshot — which fails for a brand-new
	// session that has no snapshot, surfacing a spurious "Restore failed" to the user.
	r.awaitCreateSettled(ctx, sessionID)

	// -- Step 1: Resolve worker --
	var worker *fleet.Worker
	var workerErr error

	// Try the durable binding first.
	worker, workerErr = r.deps.Fleet.WorkerForSession(ctx, sessionID)
	if workerErr != nil {
		// No binding: PlaceForSession.
		worker, workerErr = r.deps.Fleet.PlaceForSession(ctx, sessionID, fleet.PlacementHint{})
		if workerErr != nil {
			return nil, fmt.Errorf("place session: %w", workerErr)
		}
	} else {
		// We have a binding — but is the worker still alive?
		// WorkerForSession already checked the workers map; if the worker was gone it
		// returned an error. We only reach here if it succeeded, so worker is valid.
		_ = worker // assigned above
	}

	// -- Step 2: Tenancy-aware instance resolution --
	if worker.Caps.Tenancy == execenv.TenancyShared {
		return r.ensureSharedInstance(ctx, sessionID, worker)
	}
	return r.ensurePerSessionInstance(ctx, sessionID, worker)
}

// awaitCreateSettled blocks while an async CreateSession is still provisioning this
// session. It returns as soon as the create op reaches a terminal phase (done/failed),
// the instance becomes tracked, no create op is in flight, or ctx is cancelled. The
// poll is bounded by ctx (the caller's request context), so a client that disconnects
// or a cancelled request unblocks immediately.
func (r *runnerImpl) awaitCreateSettled(ctx context.Context, sessionID string) {
	for {
		if r.get(sessionID) != nil {
			return
		}
		p, ok := r.progress.get(sessionID)
		if !ok || p.Op != "create" || p.Phase == "done" || p.Phase == "failed" {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// ensureSharedInstance handles TenancyShared: one instance on the worker hosts
// all sessions. Snapshot is gated off by the worker's capabilities
// (SupportsSnapshot=false), so the archive loop skips shared workers.
func (r *runnerImpl) ensureSharedInstance(ctx context.Context, sessionID string, worker *fleet.Worker) (*execenv.Instance, error) {
	sharedKey := "__shared__" + worker.ID
	if inst := r.get(sharedKey); inst != nil {
		if inst.State == execenv.StateRunning {
			// For shared instances, point the session's address at the shared instance
			// while preserving the session-ID-scoped routing the sandbox provides.
			return inst, nil
		}
	}
	// Shared instance not running; provision it.
	img := execenv.ImageRef(r.deps.Policy.BaseImage)
	inst, err := worker.Env.Provision(ctx, execenv.ProvisionSpec{
		SessionID: sharedKey,
		Image:     img,
		Env:       map[string]string{},
		Labels:    map[string]string{"agentkit.managed": "true", "agentkit.shared": worker.ID},
		Mounts:    r.deps.Policy.Mounts,
		AgentPort: r.deps.Policy.AgentPort,
	})
	if err != nil {
		return nil, fmt.Errorf("provision shared instance: %w", err)
	}
	r.track(sharedKey, worker.ID, inst)
	return inst, nil
}

// ensurePerSessionInstance handles TenancyPerSession — the normal path.
func (r *runnerImpl) ensurePerSessionInstance(ctx context.Context, sessionID string, worker *fleet.Worker) (*execenv.Instance, error) {
	// Worker-loss detection: if instanceWorkers records a different worker than the
	// one the fleet just resolved, the originally-bound worker is gone.
	//   (a) snapshot exists → use this new worker: Materialize + Provision there.
	//   (b) no snapshot     → unrecoverable; surface a clear error.
	prevWorkerID := r.getWorkerID(sessionID)
	workerChanged := prevWorkerID != "" && prevWorkerID != worker.ID
	if workerChanged {
		// Clear the stale in-memory instance — it lived on the dead worker.
		r.forget(sessionID)
		return r.restoreToWorker(ctx, sessionID, worker)
	}

	if inst := r.get(sessionID); inst != nil {
		if inst.State == execenv.StateRunning {
			return inst, nil
		}
		// Any non-running tracked instance (destroyed/error): fall through to
		// restore-from-snapshot. There is no warm suspended state to resume.
	}

	// Destroyed or never-provisioned: restore from snapshot if one exists.
	return r.restoreToWorker(ctx, sessionID, worker)
}

// workerCapacity asks a worker's environment whether it could provision anything
// at all right now. nil means "yes, or it does not say" — an environment that
// does not implement execenv.CapacityReporter is never guessed about.
func workerCapacity(worker *fleet.Worker) error {
	if worker == nil || worker.Env == nil {
		return nil
	}
	cr, ok := worker.Env.(execenv.CapacityReporter)
	if !ok {
		return nil
	}
	return cr.Capacity()
}

// restoreToWorker attempts to restore a session from its snapshot handle onto the
// given worker. If no snapshot handle exists the session is unrecoverable.
func (r *runnerImpl) restoreToWorker(ctx context.Context, sessionID string, worker *fleet.Worker) (inst *execenv.Instance, err error) {
	r.progress.begin(sessionID, "restore")
	defer func() {
		if err != nil {
			r.progress.finish(sessionID, err.Error())
		} else {
			r.progress.finish(sessionID, "")
		}
	}()

	h, ok, err := r.deps.Store.GetSnapshotHandle(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		// "Re-create the session" is only sound advice if a session CAN be
		// created. When the host has no capacity left — every port in the DinD
		// pool leased to a live session — the create that put us here failed for
		// a reason that has nothing to do with this session, and so will every
		// retry. Reporting that as a lost session sent two engineers chasing a
		// Docker container limit and then an image-resolution bug, neither of
		// which was happening. So ask the environment before blaming the session.
		if cerr := workerCapacity(worker); cerr != nil {
			return nil, fmt.Errorf("cannot start session %q on this host: %w", sessionID, cerr)
		}
		return nil, fmt.Errorf("session %q has no running instance and no snapshot — session must be re-created", sessionID)
	}

	r.progress.phase(sessionID, "downloading")
	pctx := imageregistry.WithProgressSink(ctx, r.progressSinkFor(sessionID))
	img, err := r.deps.Registry.Materialize(pctx, h)
	if err != nil {
		return nil, fmt.Errorf("materialize snapshot: %w", err)
	}

	r.progress.phase(sessionID, "starting")
	inst, err = r.provisionForSession(ctx, sessionID, img, worker)
	if err != nil {
		return nil, err
	}
	// Rehydrate the in-image conversation history. `docker commit` captures the
	// container filesystem but NOT the harness's in-process conversationHistory,
	// so a freshly-restored container has no memory of the pre-archive turns.
	// Reconstruct the user/assistant message list from the persisted query events
	// and POST it to the restored sandbox's /load-conversation endpoint. This is
	// done ONLY on the snapshot-restore path: the orphan-recover path (Start ->
	// Env.Recover) re-adopts a still-RUNNING container that already holds its
	// in-memory history, so rehydrating there would duplicate the conversation.
	//
	// Best-effort: a failure here leaves the session usable (just without prior
	// context), so we log and continue rather than failing the restore.
	r.rehydrateConversation(ctx, sessionID, inst)
	return inst, nil
}

// rehydrateConversation re-establishes a restored session inside its fresh
// container: it re-creates the sandbox's session record (re-supplying the MCP
// configuration, which is session config rather than filesystem state and so is
// NOT carried by the snapshot — §4.5), then reconstructs the ordered
// user/assistant conversation from the persisted query events and loads it into
// the in-image harness. Best-effort: errors are logged, never fatal.
//
// The create runs even when there is nothing to load. A restored session with
// an empty transcript still needs its MCP servers back, and query-stream's lazy
// auto-create would otherwise mint a session record with no MCP config at all.
func (r *runnerImpl) rehydrateConversation(ctx context.Context, sessionID string, inst *execenv.Instance) {
	// The session row is the durable home of the config the container lost.
	sess, err := r.deps.Store.GetSession(ctx, sessionID)
	if err != nil {
		log.Printf("agentkit: rehydrate %s: get session: %v", sessionID, err)
		return
	}

	// /load-conversation 404s unless the session exists in the restored sandbox's
	// in-memory sessionManager (empty after a fresh container start). query-stream
	// auto-creates lazily, but load-conversation does not — so create it first.
	// Harness is left empty, which the sandbox resolves to the default
	// (claude-agent-sdk) — the same harness the original session was created with.
	if err := r.postCreateSession(ctx, inst.Address, CreateSessionRequest{
		SessionID:  sessionID,
		MCPServers: sess.MCPServers,
	}); err != nil {
		log.Printf("agentkit: rehydrate %s: create session: %v", sessionID, err)
		return
	}

	evs, err := r.deps.Store.ListQueryEventsFlat(ctx, sessionID)
	if err != nil {
		log.Printf("agentkit: rehydrate %s: list events: %v", sessionID, err)
		return
	}
	msgs := reconstructConversation(evs)
	if len(msgs) == 0 {
		return // config re-supplied; nothing to load
	}

	scope := extension.ContextScope{Customer: sess.Customer, Job: sess.Job, Persona: sess.Persona, UserEmail: sess.UserEmail}
	token, err := r.issueToken(ctx, scope, sessionID)
	if err != nil {
		log.Printf("agentkit: rehydrate %s: issue token: %v", sessionID, err)
		return
	}

	if err := r.postLoadConversation(ctx, inst.Address, sessionID, token, msgs); err != nil {
		log.Printf("agentkit: rehydrate %s: load-conversation: %v", sessionID, err)
		return
	}
	log.Printf("agentkit: rehydrate %s: loaded %d conversation messages", sessionID, len(msgs))
}

// postLoadConversation POSTs the reconstructed message list to the restored
// sandbox's POST /sessions/:id/load-conversation endpoint.
func (r *runnerImpl) postLoadConversation(ctx context.Context, addr, sessionID, token string, msgs []conversationMessage) error {
	body, _ := json.Marshal(map[string]any{"messages": msgs})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, addr+"/sessions/"+sessionID+"/load-conversation", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.deps.HTTPClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// conversationMessage is one entry in the rehydrated conversation. The JSON shape
// matches the sandbox /load-conversation contract: {role, content}.
type conversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// reconstructConversation rebuilds an ordered user/assistant message list from a
// session's persisted query events. It is the inverse of how the live stream is
// captured: user_message envelopes become user turns, and consecutive
// content_delta envelopes are accumulated into a single assistant turn that is
// flushed at each turn boundary (when the next user_message arrives, or at end).
// All other event types (thinking, tool, status, lifecycle) are irrelevant to the
// conversation and are skipped. Empty messages are dropped. Pure function.
func reconstructConversation(evs []events.Envelope) []conversationMessage {
	var msgs []conversationMessage
	var assistant strings.Builder

	flushAssistant := func() {
		text := strings.TrimSpace(assistant.String())
		if text != "" {
			msgs = append(msgs, conversationMessage{Role: "assistant", Content: text})
		}
		assistant.Reset()
	}

	for _, ev := range evs {
		switch ev.Type {
		case events.UserMessage:
			// A new user turn closes the previous assistant turn.
			flushAssistant()
			if content, ok := ev.Data["content"].(string); ok {
				if c := strings.TrimSpace(content); c != "" {
					msgs = append(msgs, conversationMessage{Role: "user", Content: c})
				}
			}
		case events.ContentDelta:
			assistant.WriteString(deltaText(ev.Data["delta"]))
		default:
			// thinking_delta, tool_*, table_rendered, message_start/end,
			// query_complete, session_info, etc. — not part of the conversation.
		}
	}
	flushAssistant()
	return msgs
}

// deltaText extracts assistant text from a content_delta's "delta" field, which
// on the wire is a string but may also be a {"text": string} map (mirrors the
// frontend reducer and e2e/helpers/agent.ts sendAgentMessage).
func deltaText(delta any) string {
	switch d := delta.(type) {
	case string:
		return d
	case map[string]any:
		if t, ok := d["text"].(string); ok {
			return t
		}
	}
	return ""
}

// --- internal worker events (§8.2) -------------------------------------------
//
// Two of the five internal events core emits are produced here, because the
// Runner is the only thing that knows the moment they describe: a job's query
// completed (worker.finished) or ended badly (worker.failed). Both fire ONLY
// for worker jobs — a session whose `worker` column is empty is a plain
// interactive session and emits nothing at all (§8.2).
//
// worker.finished is *the* composition primitive: its text is the whole
// exchange, so the next worker wakes up already holding it.

// WorkerEventStore is the durable surface the §8.2 internal emitters need: the
// session row (which names the project and the worker), the event that
// triggered the job (whose depth the emitted event adds one to), and the append
// itself. *agentdb.Store satisfies it.
//
// It is a seam rather than a concrete store so the Runner keeps its existing
// nil-dependency shape: a host that has not wired the event spine simply emits
// no internal events, and every pre-existing caller stays on the old path.
type WorkerEventStore interface {
	WorkerJobStore
	CreateProjectEvent(ctx context.Context, ev *agentdb.ProjectEvent) (*agentdb.ProjectEvent, error)
	// SetSessionAttentionRequested clears (or sets) the per-turn §9 stamp. The
	// emitter clears it once it has copied it onto the envelope — see
	// emitJobOutcome for why "that turn" has to mean that turn.
	SetSessionAttentionRequested(ctx context.Context, sessionID string, requested bool) error
}

// WorkerJobStore is the READ half — everything ResolveWorkerJob needs and
// nothing more. It is split out because two callers only ever resolve an
// envelope and never append or stamp anything (agentd's config.changed emitter
// and its lease reaper), and a fat interface would have made them declare
// methods they do not use.
type WorkerJobStore interface {
	GetSession(ctx context.Context, id string) (*agentdb.Session, error)
	SessionTriggerEvent(ctx context.Context, sessionID string) (*agentdb.ProjectEvent, error)
}

// WorkerJob is the §8.1 envelope identity of a running worker job — everything
// an internal event needs to stamp about *whose* job finished or failed.
// Build it with ResolveWorkerJob rather than by hand.
type WorkerJob struct {
	// Project is the tenancy namespace (the session's customer).
	Project string
	// Worker is the product-level worker whose job this is.
	Worker string
	// SessionID is the session the job ran in.
	SessionID string
	// Depth is the depth to stamp on the emitted event: the triggering event's
	// depth + 1, or 0 when a human started the job. This is the loop floor of
	// §8.4 — get it wrong and worker-to-worker cycles stop being bounded.
	Depth int
	// Interactive marks a job a human started by chatting, so subscriptions that
	// should not react to chats can filter it out.
	Interactive bool
	// AttentionRequested is the §9 stamp read off the session row: this turn
	// called `request_human_attention`. §8.2 copies it onto `worker.finished` so
	// reviewers can skip work that is deliberately half-done.
	AttentionRequested bool
}

// ResolveWorkerJob loads the envelope facts for a session's job.
//
// ok=false means "not a worker job" — the session's `worker` column is empty,
// so nothing should be emitted for it. That is a normal outcome, not an error.
//
// Depth and Interactive come from the same fact: whether a delivery links this
// session to a triggering event. If one does, this job is a link in a chain and
// its events sit one level deeper; if none does, a human started it, which is
// depth 0 and interactive (§8.1).
//
// Exported because E3 (the router) reuses it for the lease reaper, which must
// stamp the same envelope for a session it never streamed.
func ResolveWorkerJob(ctx context.Context, store WorkerJobStore, sessionID string) (WorkerJob, bool, error) {
	if store == nil {
		return WorkerJob{}, false, fmt.Errorf("resolve worker job: store is required")
	}
	if sessionID == "" {
		return WorkerJob{}, false, fmt.Errorf("resolve worker job: session id is required")
	}
	sess, err := store.GetSession(ctx, sessionID)
	if err != nil {
		return WorkerJob{}, false, fmt.Errorf("resolve worker job %s: %w", sessionID, err)
	}
	if strings.TrimSpace(sess.Worker) == "" {
		return WorkerJob{}, false, nil
	}
	job := WorkerJob{
		Project:            sess.Customer,
		Worker:             sess.Worker,
		SessionID:          sessionID,
		Interactive:        true,
		AttentionRequested: sess.AttentionRequested,
	}
	trigger, err := store.SessionTriggerEvent(ctx, sessionID)
	if err != nil {
		return WorkerJob{}, false, fmt.Errorf("resolve worker job %s: trigger event: %w", sessionID, err)
	}
	if trigger != nil {
		job.Depth = trigger.Envelope.Depth + 1
		job.Interactive = false
	}
	return job, true, nil
}

// EmitWorkerFinished appends the §8.2 `worker.finished` event for a completed
// job. transcript is the full rendered conversation (see renderTranscript) and
// becomes the event text verbatim — that text is what the next worker reads as
// its first message, so it is the exchange itself, never a summary of it.
//
// attentionRequested is passed in rather than derived: the
// `request_human_attention` tool is work-plan item H2, which owns setting it.
func EmitWorkerFinished(ctx context.Context, store WorkerEventStore, job WorkerJob, transcript string, attentionRequested bool) (*agentdb.ProjectEvent, error) {
	return appendWorkerEvent(ctx, store, agentdb.EventTypeWorkerFinished, job, transcript, "", attentionRequested)
}

// EmitWorkerFailed appends the §8.2 `worker.failed` event. reason is one of
// agentdb.FailureReasons: "error" when the session itself errored (the Runner's
// error path, below) or "lost" when a session's lease expired without the
// sandbox reporting back. The reason is a parameter precisely so E3's lease
// reaper emits through this same function rather than growing a second, subtly
// different stamping of the envelope:
//
//	agentkit.EmitWorkerFailed(ctx, store, job, agentdb.FailureReasonLost, msg)
func EmitWorkerFailed(ctx context.Context, store WorkerEventStore, job WorkerJob, reason, text string) (*agentdb.ProjectEvent, error) {
	if !agentdb.ValidFailureReason(reason) {
		return nil, fmt.Errorf("worker.failed: invalid reason %q (want one of %s)",
			reason, strings.Join(agentdb.FailureReasons, "|"))
	}
	return appendWorkerEvent(ctx, store, agentdb.EventTypeWorkerFailed, job, text, reason, false)
}

// appendWorkerEvent is the single place a worker-sourced internal event is
// stamped, so finished and failed can never disagree about the envelope.
func appendWorkerEvent(ctx context.Context, store WorkerEventStore, eventType string, job WorkerJob, text, reason string, attentionRequested bool) (*agentdb.ProjectEvent, error) {
	if store == nil {
		return nil, fmt.Errorf("%s: store is required", eventType)
	}
	if job.Worker == "" {
		return nil, fmt.Errorf("%s: worker is required", eventType)
	}
	if job.SessionID == "" {
		return nil, fmt.Errorf("%s: session id is required", eventType)
	}
	if job.Depth < 0 {
		return nil, fmt.Errorf("%s: depth must not be negative", eventType)
	}
	return store.CreateProjectEvent(ctx, &agentdb.ProjectEvent{
		Project: job.Project,
		Type:    eventType,
		Text:    text,
		Envelope: agentdb.EventEnvelope{
			Depth:              job.Depth,
			Source:             agentdb.EventSourceWorker,
			Worker:             job.Worker,
			SessionID:          job.SessionID,
			Interactive:        job.Interactive,
			AttentionRequested: attentionRequested,
			Reason:             reason,
		},
	})
}

// onQueryError is the MarkerHook for the `error` event: it stashes the error
// text for the turn so the worker.failed emitted after the turn settles carries
// what actually went wrong rather than a generic "the session errored". The
// hook is the only place that text is visible — events.Result records that a
// turn errored, but not why.
func (r *runnerImpl) onQueryError(_ context.Context, q events.QueryContext, ev events.Envelope) {
	text := errorEventText(ev)
	if text == "" {
		return
	}
	r.mu.Lock()
	r.queryErrors[q.SessionID+"\x00"+q.QueryID] = text
	r.mu.Unlock()
}

// takeQueryError reads and clears the stashed error text for a turn. Always
// called at the end of a turn, so the map cannot grow without bound.
func (r *runnerImpl) takeQueryError(sessionID, queryID string) string {
	key := sessionID + "\x00" + queryID
	r.mu.Lock()
	defer r.mu.Unlock()
	text := r.queryErrors[key]
	delete(r.queryErrors, key)
	return text
}

// errorEventText pulls a human-readable message out of an `error` envelope,
// falling back to the whole data payload so nothing is silently lost.
func errorEventText(ev events.Envelope) string {
	for _, key := range []string{"error", "message", "text"} {
		if s, ok := ev.Data[key].(string); ok {
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
		}
	}
	if len(ev.Data) == 0 {
		return ""
	}
	b, err := json.Marshal(ev.Data)
	if err != nil {
		return ""
	}
	return string(b)
}

// emitJobOutcome fires the internal event for a settled turn (§8.2). It runs
// after the pipeline has persisted the turn, which is what lets worker.finished
// carry a transcript that actually includes the turn that just finished.
//
// Nothing is emitted for a plain interactive session (no worker), for a
// cancelled turn (a human pressing stop did not finish a job), or when no
// WorkerEventStore is wired.
func (r *runnerImpl) emitJobOutcome(ctx context.Context, sessionID, queryID string, res events.Result, runErr error) {
	errText := r.takeQueryError(sessionID, queryID)
	if r.deps.WorkerEvents == nil {
		return
	}
	failed := runErr != nil || res.Status == "error"
	if !failed && res.Status == "cancelled" {
		return
	}
	// Detach from the request context: the client may already have disconnected,
	// and the event is the durable record of what the job did — losing it would
	// silently break every subscription downstream of this worker.
	ctx = context.WithoutCancel(ctx)

	job, ok, err := ResolveWorkerJob(ctx, r.deps.WorkerEvents, sessionID)
	if err != nil {
		log.Printf("agentkit: worker event %s: %v", sessionID, err)
		return
	}
	if !ok {
		return // vanilla session — §8.2: fires only for worker jobs
	}

	// §8.2's `attention_requested` describes THAT TURN, so the stamp
	// `request_human_attention` left on the session row is consumed here and
	// cleared: leaving it set would make every later turn of the same session
	// look like it too had asked for sign-off, which is its own bug (a reviewer
	// subscription filtering on the flag would fire for ever).
	//
	// Clearing loses nothing. What a human is actually owed survives in the two
	// places that are supposed to hold it: the open `attention_requests` row
	// (which the sweep resolves, §8.2) and the delivery parked at
	// `awaiting_human` (§8.4).
	//
	// It happens BEFORE the failed/finished split on purpose. `worker.failed`
	// carries no attention_requested field, so the alternative — keep the stamp
	// after a failed turn so the next one reports it — would emit a
	// `worker.finished` claiming a turn asked for a human when it did not.
	// A cancelled turn returns above and keeps its stamp: nothing settled, and
	// the human who pressed stop is by definition already in the thread.
	if job.AttentionRequested {
		if err := r.deps.WorkerEvents.SetSessionAttentionRequested(ctx, sessionID, false); err != nil {
			log.Printf("agentkit: clear attention_requested %s: %v", sessionID, err)
		}
	}

	if failed {
		if errText == "" {
			if runErr != nil {
				errText = runErr.Error()
			} else {
				errText = "the session errored"
			}
		}
		if _, err := EmitWorkerFailed(ctx, r.deps.WorkerEvents, job, agentdb.FailureReasonError, errText); err != nil {
			log.Printf("agentkit: emit worker.failed %s: %v", sessionID, err)
		}
		return
	}
	if _, err := EmitWorkerFinished(ctx, r.deps.WorkerEvents, job, r.renderTranscript(ctx, sessionID), job.AttentionRequested); err != nil {
		log.Printf("agentkit: emit worker.finished %s: %v", sessionID, err)
	}
}

// renderTranscript renders the session's whole conversation as the text of a
// worker.finished event. It reuses the rehydration reconstruction
// (reconstructConversation) deliberately — one renderer, so what a resumed
// harness is told it said and what the next worker reads can never drift.
func (r *runnerImpl) renderTranscript(ctx context.Context, sessionID string) string {
	evs, err := r.deps.Store.ListQueryEventsFlat(ctx, sessionID)
	if err != nil {
		log.Printf("agentkit: transcript %s: list events: %v", sessionID, err)
		return ""
	}
	return renderConversation(reconstructConversation(evs))
}

// renderConversation serialises a reconstructed conversation as plain text —
// the §8.1 payload is text, and this is the only shape it takes. Pure function.
func renderConversation(msgs []conversationMessage) string {
	var b strings.Builder
	for i, m := range msgs {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(m.Role)
		b.WriteString(":\n")
		b.WriteString(m.Content)
	}
	return b.String()
}

// workerEnvFor resolves the ExecutionEnvironment for a session using the
// tracked workerID. It is used by lifecycle methods that already know the
// session is tracked (Destroy, Snapshot, Status).
func (r *runnerImpl) workerEnvFor(sessionID string) (execenv.ExecutionEnvironment, error) {
	workerID := r.getWorkerID(sessionID)
	if workerID == "" {
		return nil, fmt.Errorf("runner: no tracked worker for session %q", sessionID)
	}
	// Look up the worker in the fleet.
	ctx := context.Background()
	workers, err := r.deps.Fleet.Workers(ctx)
	if err != nil {
		return nil, err
	}
	for _, w := range workers {
		if w.ID == workerID {
			return w.Env, nil
		}
	}
	return nil, fmt.Errorf("runner: tracked worker %q for session %q is no longer registered", workerID, sessionID)
}

func (r *runnerImpl) provisionForSession(ctx context.Context, sessionID string, img execenv.ImageRef, worker *fleet.Worker) (*execenv.Instance, error) {
	sess, err := r.deps.Store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	scope := extension.ContextScope{Customer: sess.Customer, Job: sess.Job, Persona: sess.Persona, UserEmail: sess.UserEmail}
	token, err := r.issueToken(ctx, scope, sessionID)
	if err != nil {
		return nil, err
	}
	inst, err := worker.Env.Provision(ctx, execenv.ProvisionSpec{
		SessionID: sessionID,
		Image:     img,
		Env:       r.sessionEnv(sessionID, token, ""),
		Labels:    map[string]string{"agentkit.managed": "true", "agentkit.session": sessionID},
		Mounts:    r.deps.Policy.Mounts,
		AgentPort: r.deps.Policy.AgentPort,
	})
	if err != nil {
		return nil, err
	}
	r.track(sessionID, worker.ID, inst)
	return inst, nil
}

func (r *runnerImpl) issueToken(ctx context.Context, scope extension.ContextScope, sessionID string) (string, error) {
	if r.deps.Claims == nil {
		return "", nil
	}
	return r.deps.Claims.Issue(ctx, scope, sessionID)
}

// turnSystemPrompt decides which system prompt a turn runs with. There is
// exactly one rule, and exactly one authority for each of its two cases:
//
//  1. The session row carries a `composed_prompt` (it was created from a
//     composed job, docs/product/02-workers.md §6.2). That string IS the
//     session's system prompt, verbatim, for the whole of its life. It was
//     composed once and deterministically at dispatch — core preamble, project
//     prompt, worker prompt, memory briefing — and written to the row precisely
//     so "every transcript is tied to the exact prompt that produced it". A
//     transcript whose prompt was re-derived per turn ties to nothing.
//
//  2. The row carries none (a plain interactive session). The host's
//     SessionContextProvider resolves the prompt per turn, exactly as before.
//     That is the right behaviour here: a chat session's prompt legitimately
//     follows the live project/worker configuration.
//
// The composed prompt is never *combined* with the provider's resolution. It
// already contains the project layer (ComposeJob step 2 prepends it), so adding
// the provider's would duplicate the project prompt and, worse, would let a
// mid-job `worker_prompt_write` leak into a running session — the one thing
// §6.2 says composition-at-start exists to prevent. One prompt, one writer.
//
// Reading it off the row rather than caching it in memory is what makes resume
// safe: a restored, re-provisioned or agentd-restarted session re-reads the same
// durable string, so it cannot silently change prompt mid-life.
func (r *runnerImpl) turnSystemPrompt(ctx context.Context, composed string, scope extension.ContextScope) (string, error) {
	if composed != "" {
		return composed, nil
	}
	return r.sessionContext(ctx, scope)
}

func (r *runnerImpl) sessionContext(ctx context.Context, scope extension.ContextScope) (string, error) {
	if r.deps.SessionContext == nil {
		return "", nil
	}
	sc, err := r.deps.SessionContext.Resolve(ctx, scope)
	if err != nil {
		return "", err
	}
	if sc == nil {
		return "", nil
	}
	return sc.SystemPrompt, nil
}

func (r *runnerImpl) sessionEnv(sessionID, token, model string) map[string]string {
	// Seed from the host-supplied static session env (model-provider config such
	// as ANTHROPIC_BASE_URL / ANTHROPIC_API_KEY, proxy settings, feature flags).
	// The in-image agent requires these to reach a model endpoint; without a
	// passthrough a host has no way to inject them. Session-specific keys below
	// win over anything the host set under the same name.
	env := make(map[string]string, len(r.deps.Policy.SessionEnv)+3)
	for k, v := range r.deps.Policy.SessionEnv {
		env[k] = v
	}
	env["SESSION_ID"] = sessionID
	env["SESSION_TOKEN"] = token
	// Override the host's static ANTHROPIC_API_KEY with the per-session JWT so
	// the Anthropic SDK sends it as x-api-key, authenticating proxy requests.
	// Hosts whose sessions authenticate to the model provider directly (no
	// proxy) opt out via Policy.DisableModelAPIKeyOverride.
	if !r.deps.Policy.DisableModelAPIKeyOverride {
		env["ANTHROPIC_API_KEY"] = token
	}
	if model != "" {
		env["DEFAULT_MODEL"] = model
	}
	return env
}

func (r *runnerImpl) onArtifactRegistered(ctx context.Context, q events.QueryContext, ev events.Envelope) {
	// Ported from doc 06 pattern 2: pull the registered file from the running
	// instance workspace via Exec+cat, then persist via ArtifactStore.Save.
	// Defensive: any per-artifact failure is skipped — it must never fail the turn.

	// 1. Extract filePath from the event data.
	filePath, _ := ev.Data["filePath"].(string)
	if filePath == "" {
		// Malformed event — skip silently.
		return
	}

	// 2. Resolve the running instance for this session.
	inst := r.get(q.SessionID)
	if inst == nil {
		// Session not tracked (e.g. arrived after Destroy) — skip.
		return
	}
	env, err := r.workerEnvFor(q.SessionID)
	if err != nil {
		// Worker gone — skip.
		return
	}

	// 3. Resolve to an absolute workspace path.
	absPath := filePath
	if !strings.HasPrefix(absPath, "/") {
		absPath = "/workspace/" + absPath
	}

	// 4. Build artifact metadata from the event fields (shared by both branches).
	label, _ := ev.Data["label"].(string)
	artifactType, _ := ev.Data["artifactType"].(string)
	description, _ := ev.Data["description"].(string)
	if artifactType == "" {
		artifactType = "file"
	}

	// 4b. Webapps: capture the whole build directory (the folder containing the
	// entry HTML, e.g. "dist/"), not just the entry file — otherwise the bundled
	// JS/CSS/font assets are never stored and the served iframe can only load
	// index.html. The single webapp artifact's FilePath becomes the directory, so
	// blobs land at agent-artifacts/{session}/{dir}/... where serveWebapp reads
	// them. Guard against an entry at the workspace root (dir ".") so we never tar
	// the entire workspace.
	if artifactType == "webapp" {
		if webappDir := path.Dir(filePath); webappDir != "." && webappDir != "/" && webappDir != "" {
			absDir := path.Dir(absPath)
			r.captureDirArtifact(ctx, q.SessionID, &artifacts.Artifact{
				SessionID:    q.SessionID,
				FilePath:     webappDir,
				Label:        label,
				ArtifactType: "webapp",
				Description:  description,
				Status:       artifacts.StatusLive,
				Source:       "auto",
			}, absDir)
			return
		}
	}

	// 5. Determine whether the registered path is a directory. Prefer the event
	// hint; otherwise probe the container. We check STDOUT for a sentinel (not the
	// exit code) so that an environment which can't run the probe falls back to the
	// file path rather than mis-detecting a directory.
	isDir, _ := ev.Data["isDir"].(bool)
	if !isDir {
		if res, derr := env.Exec(ctx, inst.ID, []string{"sh", "-c", `test -d "$1" && printf isdir`, "--", absPath}, execenv.ExecOptions{}); derr == nil && strings.TrimSpace(string(res.Stdout)) == "isdir" {
			isDir = true
		}
	}

	// 6. Capture: a directory is tarred out (stored as one blob per file by the
	// ArtifactStore); a regular file is cat'd and stored as a single blob.
	art := &artifacts.Artifact{
		SessionID:    q.SessionID,
		FilePath:     filePath,
		Label:        label,
		ArtifactType: artifactType,
		Description:  description,
		Status:       artifacts.StatusLive,
		Source:       "auto",
		IsDir:        isDir,
	}
	// 6b. A directory is captured via the shared helper (tar → per-file blobs);
	// a regular file is cat'd and stored as a single blob.
	if isDir {
		r.captureDirArtifact(ctx, q.SessionID, art, absPath)
		return
	}

	res, err := env.Exec(ctx, inst.ID, []string{"cat", absPath}, execenv.ExecOptions{})
	if err != nil {
		// Instance unreachable or file missing — skip rather than fail the turn.
		return
	}
	if r.deps.Enricher != nil {
		if enrichErr := r.deps.Enricher.Enrich(ctx, art); enrichErr != nil {
			// Non-fatal: proceed with un-enriched metadata.
			_ = enrichErr
		}
	}
	if _, err := r.deps.Artifacts.Save(ctx, art, bytes.NewReader(res.Stdout)); err != nil {
		// Non-fatal: bytes couldn't be stored, but we don't fail the turn.
		_ = err
	}
}

// onSkillHoisted captures a hoisted skill bundle (written by the in-container
// hoist_skill tool) as a kind:skill folder artifact. The durable skill-catalog
// write is layered on later (Doc C); this hook owns the artifact capture.
func (r *runnerImpl) onSkillHoisted(ctx context.Context, q events.QueryContext, ev events.Envelope) {
	artifactPath, _ := ev.Data["artifactPath"].(string)
	if artifactPath == "" {
		// Malformed event — skip silently.
		return
	}
	name, _ := ev.Data["name"].(string)

	absPath := artifactPath
	if !strings.HasPrefix(absPath, "/") {
		absPath = "/workspace/" + absPath
	}

	r.captureDirArtifact(ctx, q.SessionID, &artifacts.Artifact{
		SessionID:    q.SessionID,
		FilePath:     artifactPath,
		Label:        name,
		ArtifactType: "skill",
		Status:       artifacts.StatusLive,
		Source:       "auto",
	}, absPath)

	// Promote into the durable catalog (Doc C). Optional dependency; skip if absent.
	if r.deps.SkillCatalog == nil {
		return
	}
	sess, err := r.deps.Store.GetSession(ctx, q.SessionID)
	if err != nil || sess == nil {
		return
	}
	visibility, _ := ev.Data["visibility"].(string)
	if visibility != "private" {
		visibility = "organizational" // a hoist can never set public — that's the gated promote path.
	}
	requiresBuild, _ := ev.Data["requiresBuild"].(bool)
	var description string
	var manifestBytes []byte
	if m, ok := ev.Data["manifest"].(map[string]any); ok {
		description, _ = m["description"].(string)
		manifestBytes, _ = json.Marshal(m)
	}
	if promoteErr := r.deps.SkillCatalog.Promote(ctx, SkillPromotion{
		SessionID:     q.SessionID,
		ArtifactPath:  artifactPath,
		Name:          name,
		Description:   description,
		Visibility:    visibility,
		Customer:      sess.Customer,
		OwnerEmail:    sess.UserEmail,
		RequiresBuild: requiresBuild,
		Manifest:      manifestBytes,
	}); promoteErr != nil {
		// Non-fatal: the bundle is still captured as a downloadable artifact even if
		// cataloging fails — but surface it so a broken catalog write isn't silent.
		log.Printf("agentkit: onSkillHoisted %s: catalog promote failed: %v", q.SessionID, promoteErr)
	}
}

// onSkillInstalled records a live skill install onto the session's metadata so it
// can be hoisted onto a published image's skill_set. Deduped by name (latest wins).
// Non-fatal: a failure must never break the turn.
func (r *runnerImpl) onSkillInstalled(ctx context.Context, q events.QueryContext, ev events.Envelope) {
	name, _ := ev.Data["name"].(string)
	if name == "" {
		return
	}
	id, _ := ev.Data["id"].(string)
	sess, err := r.deps.Store.GetSession(ctx, q.SessionID)
	if err != nil || sess == nil {
		return
	}
	if sess.Metadata == nil {
		sess.Metadata = agentdb.JSONMap{}
	}
	existing, _ := sess.Metadata["installed_skills"].([]any)
	out := make([]any, 0, len(existing)+1)
	for _, e := range existing {
		if m, ok := e.(map[string]any); ok {
			if n, _ := m["name"].(string); n == name {
				continue // drop the older entry for this name
			}
		}
		out = append(out, e)
	}
	out = append(out, map[string]any{"id": id, "name": name})
	sess.Metadata["installed_skills"] = out
	if _, uErr := r.deps.Store.UpdateSession(ctx, sess); uErr != nil {
		log.Printf("agentkit: onSkillInstalled %s: update session failed: %v", q.SessionID, uErr)
	}
}

// captureDirArtifact tars the directory at absPath out of the session's running
// instance and persists it as a folder artifact (art.IsDir is forced true).
// Defensive: any failure is skipped — it must never fail the turn. Shared by
// onArtifactRegistered (dir branch) and onSkillHoisted.
func (r *runnerImpl) captureDirArtifact(ctx context.Context, sessionID string, art *artifacts.Artifact, absPath string) {
	inst := r.get(sessionID)
	if inst == nil {
		return
	}
	env, err := r.workerEnvFor(sessionID)
	if err != nil {
		return
	}
	res, err := env.Exec(ctx, inst.ID, []string{"tar", "-cf", "-", "-C", absPath, "."}, execenv.ExecOptions{})
	if err != nil {
		// tar failed (e.g. path disappeared) — skip rather than fail the turn.
		return
	}
	art.IsDir = true
	if r.deps.Enricher != nil {
		if enrichErr := r.deps.Enricher.Enrich(ctx, art); enrichErr != nil {
			_ = enrichErr
		}
	}
	if _, err := r.deps.Artifacts.Save(ctx, art, bytes.NewReader(res.Stdout)); err != nil {
		_ = err
	}
}

func (r *runnerImpl) nextQueryID(sessionID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	return fmt.Sprintf("q-%s-%d", sessionID, r.seq)
}

func (r *runnerImpl) track(sessionID, workerID string, inst *execenv.Instance) {
	r.mu.Lock()
	cp := *inst
	r.instances[sessionID] = &cp
	r.instanceWorkers[sessionID] = workerID
	r.lastActivity[sessionID] = time.Now()
	r.mu.Unlock()
}

func (r *runnerImpl) get(sessionID string) *execenv.Instance {
	r.mu.Lock()
	defer r.mu.Unlock()
	if inst, ok := r.instances[sessionID]; ok {
		cp := *inst
		return &cp
	}
	return nil
}

func (r *runnerImpl) getWorkerID(sessionID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.instanceWorkers[sessionID]
}

func (r *runnerImpl) forget(sessionID string) {
	r.mu.Lock()
	delete(r.instances, sessionID)
	delete(r.instanceWorkers, sessionID)
	delete(r.lastActivity, sessionID)
	r.mu.Unlock()
}

func (r *runnerImpl) touch(sessionID string) {
	r.mu.Lock()
	r.lastActivity[sessionID] = time.Now()
	r.mu.Unlock()
}

func (r *runnerImpl) idleSessions(olderThan time.Duration) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	now := time.Now()
	for sid, last := range r.lastActivity {
		if now.Sub(last) >= olderThan {
			out = append(out, sid)
		}
	}
	return out
}

// storeSink adapts the RunnerStore to events.Sink and tracks the pending
// flush count so the control loops can honour the flush guard.
type storeSink struct {
	store RunnerStore

	mu      sync.Mutex
	pending map[string]int
}

func (s *storeSink) BeginFlush(sessionID string) {
	s.mu.Lock()
	s.pending[sessionID]++
	s.mu.Unlock()
}

func (s *storeSink) EndFlush(sessionID string) {
	s.mu.Lock()
	if s.pending[sessionID] > 0 {
		s.pending[sessionID]--
	}
	s.mu.Unlock()
}

func (s *storeSink) PersistQueryEvents(ctx context.Context, sessionID, queryID string, evs []events.Envelope, searchText string) error {
	if s.store == nil {
		return nil
	}
	return s.store.PersistQueryEventsFlat(ctx, sessionID, queryID, evs, searchText)
}

// seedUserMessage durably records the prompt for (sessionID, queryID) before the
// turn is dispatched, so a transcript can never lose the human's own words —
// P8's append-only history has to include the half of the exchange the human
// wrote, whatever the model or the network then does.
//
// Detached from ctx for the same reason as the pipeline's persist: the usual way
// to reach here and then lose everything is the caller's context being cancelled.
// Best-effort — the turn still proceeds if the store rejects the seed, and the
// pipeline's own persist is the authoritative write.
func (r *runnerImpl) seedUserMessage(ctx context.Context, sessionID, queryID, content string) {
	if r.deps.Store == nil || content == "" {
		return
	}
	evs := []events.Envelope{{
		Type:      events.UserMessage,
		Data:      map[string]any{"content": content},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}}
	ctx = context.WithoutCancel(ctx)
	r.sink.BeginFlush(sessionID)
	defer r.sink.EndFlush(sessionID)
	if err := r.deps.Store.PersistQueryEventsFlat(ctx, sessionID, queryID, evs, events.ExtractSearchText(evs)); err != nil {
		log.Printf("agentkit: seed user message %s/%s: %v", sessionID, queryID, err)
	}
}

func (s *storeSink) pendingCount(sessionID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending[sessionID]
}

// --- user images (AG-7) ------------------------------------------------------

// lookupImageCache resolves an image from the two-level cache: the runner's
// in-memory handle map, then the content-addressed Registry. On a registry hit
// it persists a durable handle, caches it, and returns hit=true. On a full miss
// it returns hit=false and the caller must build.
func (r *runnerImpl) lookupImageCache(ctx context.Context, cacheKey string, bs imageregistry.BuildSpec, base execenv.ImageRef) (imageregistry.Handle, bool, error) {
	r.mu.Lock()
	if h, ok := r.userImageHandles[cacheKey]; ok {
		r.mu.Unlock()
		return h, true, nil
	}
	r.mu.Unlock()

	_, hit, err := r.deps.Registry.Resolve(ctx, bs)
	if err != nil {
		return imageregistry.Handle{}, false, fmt.Errorf("resolve: %w", err)
	}
	if !hit {
		return imageregistry.Handle{}, false, nil
	}
	caps := r.deps.Registry.Capabilities()
	existingRef, _, _ := r.deps.Registry.Resolve(ctx, bs)
	h, err := r.deps.Registry.Persist(ctx, existingRef, imageregistry.PersistOptions{
		SessionID:  "_user-image-cache_",
		PreferDiff: caps.SupportsDiff,
		BaseImage:  base,
	})
	if err != nil {
		return imageregistry.Handle{}, false, fmt.Errorf("cache-hit persist: %w", err)
	}
	r.mu.Lock()
	r.userImageHandles[cacheKey] = h
	r.mu.Unlock()
	return h, true, nil
}

// snapshotPersistCache snapshots the throwaway instance, persists it durably,
// caches the handle under cacheKey, and returns it. The caller owns teardown.
func (r *runnerImpl) snapshotPersistCache(ctx context.Context, worker fleet.Worker, instID execenv.InstanceID, base execenv.ImageRef, cacheKey string) (imageregistry.Handle, error) {
	caps := r.deps.Registry.Capabilities()
	snapRef, err := worker.Env.Snapshot(ctx, instID, execenv.SnapshotOptions{ForceFull: !caps.SupportsDiff})
	if err != nil {
		return imageregistry.Handle{}, fmt.Errorf("snapshot: %w", err)
	}
	h, err := r.deps.Registry.Persist(ctx, snapRef, imageregistry.PersistOptions{
		SessionID:  "_image-build_",
		PreferDiff: caps.SupportsDiff,
		BaseImage:  base,
	})
	if err != nil {
		return imageregistry.Handle{}, fmt.Errorf("persist snapshot: %w", err)
	}
	r.mu.Lock()
	r.userImageHandles[cacheKey] = h
	r.mu.Unlock()
	return h, nil
}

// ErrLaunchImageUnresolvable is returned by resolveLaunchImage when a session's
// WORKER IMAGE POINTER (§13) cannot be turned into a launch image. It is a
// sentinel so the caller — agentd's dispatcher, or an HTTP handler — can answer
// "this job cannot run" rather than string-matching, and so the one case §13.3
// forbids falling back on is distinguishable from an infrastructure hiccup.
var ErrLaunchImageUnresolvable = errors.New("launch image unresolvable")

// imageOrigin records WHICH configuration setting chose a launch image, so that
// a failure further down — the pull, the provision — can name the field an
// operator actually wrote instead of only the docker reference it became.
//
// Without it the entire diagnosis of a mistyped `base_image` is "no running
// instance", which is true of every launch failure there has ever been.
type imageOrigin struct {
	// Setting is the configuration field, e.g. "project_settings.base_image".
	// Empty when the image came from the request or the engine's own default —
	// nothing an operator can mis-write, so nothing to name.
	Setting string
	// Value is exactly what that field held.
	Value string
	// Project is the tenancy namespace the setting was read from (P5).
	Project string
	// Literal is true when Value named no catalogue image and was therefore
	// used verbatim as a registry reference. It is the difference between "your
	// curated image is broken" and "that is not a curated image at all, so we
	// tried to pull it" — which is usually the whole answer.
	Literal bool
}

// annotate wraps an image failure with the setting that chose the image.
// A zero origin returns err untouched, so nothing that is not configuration
// grows a misleading explanation.
func (o imageOrigin) annotate(img execenv.ImageRef, err error) error {
	if o.Setting == "" || err == nil {
		return err
	}
	if o.Literal {
		return fmt.Errorf("%s = %q (project %q) names no image in the §13 catalogue, so it was used as a literal registry reference and that reference failed: %w",
			o.Setting, o.Value, o.Project, err)
	}
	return fmt.Errorf("%s = %q (project %q) resolved through the §13 catalogue to %q, which failed: %w",
		o.Setting, o.Value, o.Project, img, err)
}

// resolveLaunchImage implements the launch-image priority
// (docs/product/08-images-and-skills.md §13.5, §13.6):
//
//	explicit Image  >  worker image pointer  >  custom image id  >
//	SessionContext.BaseImage  >  Policy.BaseImage
//
// Read from the product layer's side that is exactly §13.5's composition step 1,
// `worker.image > project base_image > global`: a worker JOB arrives with the
// composed image already in explicitImage (ComposeJob resolved the pointer
// through the same seam), and every other session on a worker arrives with the
// pointer unresolved on the session context, where this function resolves it.
// The host's global default travels on SessionContext.BaseImage; the project's
// `base_image` travels alongside it on ProjectBaseImage, because those two
// layers are NOT interchangeable — see below.
//
// Three of the links fail in DELIBERATELY DIFFERENT ways:
//
//   - the worker pointer fails the launch, loudly, on every error. §13.3:
//     "resolution failure fails the job loudly rather than silently falling
//     back to the project default — a worker that was pointed at an environment
//     and quietly got a different one is exactly the drift §13 exists to
//     prevent";
//   - the project's base_image resolves through the SAME catalogue seam, so the
//     string an operator is told to write means the same thing in both columns
//     — but a value the catalogue does not know is a literal registry reference
//     and is used verbatim, which is what it has always been and what the
//     standalone stack's `agentkit-sandbox:dev` depends on. Only that one
//     outcome falls through: a name the catalogue DOES know and cannot produce
//     (reaped, unmaterialisable, database unavailable) fails the launch, since
//     substituting a different image there is the same drift;
//   - a custom image id logs and falls through to the base image, as it always
//     has. That is the legacy user-image path, whose whole contract is that a
//     session still starts.
//
// The returned imageOrigin travels with the image so a later failure can name
// the setting; it is empty for everything but a configured image.
func (r *runnerImpl) resolveLaunchImage(ctx context.Context, explicitImage, customImageID, callerEmail, callerCustomer string, sctx *extension.SessionContext) (execenv.ImageRef, imageOrigin, error) {
	if explicitImage != "" {
		return execenv.ImageRef(explicitImage), imageOrigin{}, nil
	}
	if ref := workerImagePointer(sctx); ref != "" {
		image, err := r.resolveWorkerImage(ctx, callerCustomer, ref)
		if err != nil {
			return "", imageOrigin{}, err
		}
		return image, imageOrigin{Setting: "worker.image", Value: ref, Project: callerCustomer}, nil
	}
	if customImageID != "" && r.deps.CustomImages != nil {
		h, ok, err := r.deps.CustomImages.Resolve(ctx, customImageID, callerEmail, callerCustomer)
		switch {
		case err != nil:
			log.Printf("agentkit: custom image %s resolve failed, falling back: %v", customImageID, err)
		case !ok:
			log.Printf("agentkit: custom image %s not visible, falling back to base", customImageID)
		default:
			ref, mErr := r.deps.Registry.Materialize(ctx, h)
			if mErr == nil {
				return ref, imageOrigin{}, nil
			}
			log.Printf("agentkit: custom image %s materialize failed, falling back: %v", customImageID, mErr)
		}
	}
	if sctx != nil {
		if img := strings.TrimSpace(sctx.BaseImage); img != "" {
			if ptr := strings.TrimSpace(sctx.ProjectBaseImage); ptr != "" && ptr == img {
				return r.resolveProjectBaseImage(ctx, callerCustomer, ptr)
			}
			return execenv.ImageRef(img), imageOrigin{}, nil
		}
	}
	return execenv.ImageRef(r.deps.Policy.BaseImage), imageOrigin{}, nil
}

// projectBaseImageSetting is the operator-facing name of the setting, spelled
// once so the error, the log line and the tests cannot drift.
const projectBaseImageSetting = "project_settings.base_image"

// resolveProjectBaseImage turns `project_settings.base_image` into a launch
// image through the SAME Deps.Images seam the worker pointer uses (§13.3) —
// there is deliberately no second resolution path, because two of them is how
// one column comes to mean two things.
//
// It differs from resolveWorkerImage in exactly one place: ErrImageRefNotInCatalogue
// means the value is a plain registry reference and is returned verbatim. Every
// other error fails the launch, naming the setting and the value.
func (r *runnerImpl) resolveProjectBaseImage(ctx context.Context, project, ref string) (execenv.ImageRef, imageOrigin, error) {
	literal := imageOrigin{Setting: projectBaseImageSetting, Value: ref, Project: project, Literal: true}
	if r.deps.Images == nil {
		// No catalogue wired at all: every host that predates §13, and the
		// setting's original meaning. Verbatim, silently, as before.
		return execenv.ImageRef(ref), literal, nil
	}
	image, err := r.deps.Images.Resolve(ctx, project, ref)
	switch {
	case errors.Is(err, ErrImageRefNotInCatalogue):
		return execenv.ImageRef(ref), literal, nil
	case err != nil:
		return "", imageOrigin{}, fmt.Errorf("%w: %s = %q (project %q) names a §13 catalogue image that cannot be launched: %w",
			ErrLaunchImageUnresolvable, projectBaseImageSetting, ref, project, err)
	}
	if strings.TrimSpace(image) == "" {
		return "", imageOrigin{}, fmt.Errorf("%w: %s = %q (project %q) resolved to nothing",
			ErrLaunchImageUnresolvable, projectBaseImageSetting, ref, project)
	}
	// Worth a line: this is the one place a project's configured string stops
	// meaning what it literally says, so an operator can see the substitution
	// they asked for actually happened.
	log.Printf("agentkit: %s %s=%q resolved through the image catalogue to %q", project, projectBaseImageSetting, ref, image)
	return execenv.ImageRef(strings.TrimSpace(image)),
		imageOrigin{Setting: projectBaseImageSetting, Value: ref, Project: project}, nil
}

// workerImagePointer is the session context's §13 pointer, or "" — including
// when there is no context at all, which is every host that wires no provider.
func workerImagePointer(sctx *extension.SessionContext) string {
	if sctx == nil {
		return ""
	}
	return strings.TrimSpace(sctx.WorkerImage)
}

// resolveWorkerImage turns a §13 pointer into a launch image through
// Deps.Images. Every failure is an ErrLaunchImageUnresolvable — including a
// missing resolver, because a host whose sessions carry image pointers and
// whose Runner cannot resolve them is misconfigured, and launching such a
// session from the base image would be the silent substitution §13.3 forbids.
func (r *runnerImpl) resolveWorkerImage(ctx context.Context, project, ref string) (execenv.ImageRef, error) {
	if r.deps.Images == nil {
		return "", fmt.Errorf("%w: session points at worker image %q but no Deps.Images resolver is wired",
			ErrLaunchImageUnresolvable, ref)
	}
	image, err := r.deps.Images.Resolve(ctx, project, ref)
	if err != nil {
		return "", fmt.Errorf("%w: worker image %q in project %q: %w",
			ErrLaunchImageUnresolvable, ref, project, err)
	}
	if strings.TrimSpace(image) == "" {
		return "", fmt.Errorf("%w: worker image %q in project %q resolved to nothing",
			ErrLaunchImageUnresolvable, ref, project)
	}
	return execenv.ImageRef(strings.TrimSpace(image)), nil
}

// copyArtifactToInstance copies a single artifact ref into the throwaway instance.
// It reads the artifact bytes from the BlobStore (keyed by Container/Path) and
// streams them into the instance at Target via an Exec'd shell command — the only
// write mechanism available through the ExecutionEnvironment interface (which
// exposes Exec+Stdin but no PutArchive/WriteFile method).
func (r *runnerImpl) copyArtifactToInstance(ctx context.Context, env execenv.ExecutionEnvironment, id execenv.InstanceID, art ArtifactRef) error {
	target := art.Target
	if target == "" {
		target = "/workspace/" + art.Path
	}

	// 1. Ensure the parent directory exists inside the instance.
	// path.Dir handles the no-slash edge case (returns ".") and cleanly strips the
	// final path component regardless of platform separators.
	parentDir := path.Dir(target)
	if _, err := env.Exec(ctx, id, []string{"mkdir", "-p", parentDir}, execenv.ExecOptions{}); err != nil {
		return fmt.Errorf("mkdir %s: %w", parentDir, err)
	}

	// 2. Read artifact bytes from the BlobStore.
	if r.deps.Blobs == nil {
		return fmt.Errorf("read artifact blob %s/%s: Deps.Blobs is nil", art.Container, art.Path)
	}
	blobStore := r.deps.Blobs.Global(art.Container)
	rc, err := blobStore.Read(ctx, art.Path)
	if err != nil {
		return fmt.Errorf("read artifact blob %s/%s: %w", art.Container, art.Path, err)
	}
	defer rc.Close()

	// 3. Write the bytes into the instance by piping them through cat via stdin.
	// target is passed as a positional argument ($1) so the shell never interprets
	// its contents — spaces and metacharacters in the path are safe.
	if _, err := env.Exec(ctx, id, []string{"sh", "-c", `cat > "$1"`, "--", target}, execenv.ExecOptions{Stdin: rc}); err != nil {
		return fmt.Errorf("write artifact to %s: %w", target, err)
	}
	return nil
}

// Compile-time assertion.
var _ Runner = (*runnerImpl)(nil)
