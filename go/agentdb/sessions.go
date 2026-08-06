package agentdb

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/binocarlos/badcode-agent-orange/imageregistry"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// MCPServerConfig describes one Model Context Protocol server made available to
// a session's in-image harness for the lifetime of the session
// (docs/product/01-session-config.md §4).
//
// Exactly one transport is configured per server: stdio (Command/Args/Env) or
// http/sse (URL/Headers). Values in Env and Headers may be whole-value ${VAR}
// references — the *name* of an environment variable of the session container,
// resolved at MCP-process spawn time. They are never secret values, which is
// precisely what makes persisting and displaying this config safe by
// construction (§4.4).
//
// The canonical definition lives here rather than in the root agentkit package
// because agentkit imports agentdb (the reverse would be an import cycle);
// agentkit re-exports it as an alias so the Go surface reads as the spec writes
// it.
type MCPServerConfig struct {
	// Command is the stdio transport: an executable inside the container.
	Command string `json:"command,omitempty"`
	// Args are the stdio command's arguments.
	Args []string `json:"args,omitempty"`
	// Env is the environment for the stdio process. Values may be ${VAR} references.
	Env map[string]string `json:"env,omitempty"`
	// URL is the http/sse transport, reachable FROM INSIDE the container.
	URL string `json:"url,omitempty"`
	// Headers are sent with http/sse requests. Values may be ${VAR} references.
	Headers map[string]string `json:"headers,omitempty"`
}

// MCPServers is a name-keyed set of MCP server configs, stored as JSONB.
type MCPServers map[string]MCPServerConfig

// mcpServerNamePattern constrains server names: the sandbox derives tool names
// (mcp__<name>__*) from them, so anything exotic would produce untypeable tools.
var mcpServerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// envRefPattern is the one supported substitution form: a whole-value ${VAR}
// reference. No partial interpolation, no defaults, no nesting (§4.1).
var envRefPattern = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// Validate reports whether the config names exactly one transport and uses only
// whole-value ${VAR} references. A partially interpolated value (e.g.
// "Bearer ${TOKEN}") is rejected rather than silently persisted: the sandbox
// resolves whole values only, so such a value would reach the MCP server as the
// literal string — the silent-credential failure §4.1 forbids.
func (c MCPServerConfig) Validate() error {
	switch {
	case c.Command == "" && c.URL == "":
		return errors.New("exactly one transport required: set Command (stdio) or URL (http)")
	case c.Command != "" && c.URL != "":
		return errors.New("exactly one transport allowed: Command (stdio) and URL (http) are mutually exclusive")
	}
	if c.Command == "" {
		if len(c.Args) > 0 {
			return errors.New("args require the stdio transport (Command)")
		}
		if len(c.Env) > 0 {
			return errors.New("env requires the stdio transport (Command)")
		}
	}
	if c.URL == "" && len(c.Headers) > 0 {
		return errors.New("headers require the http transport (URL)")
	}
	for _, field := range []struct {
		name   string
		values map[string]string
	}{{"env", c.Env}, {"headers", c.Headers}} {
		for k, v := range field.values {
			if k == "" {
				return fmt.Errorf("%s: empty key", field.name)
			}
			if strings.Contains(v, "${") && !envRefPattern.MatchString(v) {
				return fmt.Errorf("%s %q: %q is not a whole-value ${VAR} reference (no partial interpolation)", field.name, k, v)
			}
		}
	}
	return nil
}

// Validate checks every server name and config.
func (m MCPServers) Validate() error {
	for name, cfg := range m {
		if !mcpServerNamePattern.MatchString(name) {
			return fmt.Errorf("mcp server name %q: must match %s", name, mcpServerNamePattern)
		}
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("mcp server %q: %w", name, err)
		}
	}
	return nil
}

// Value implements driver.Valuer (JSONB).
func (m MCPServers) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	return json.Marshal(m)
}

// Scan implements sql.Scanner (JSONB).
func (m *MCPServers) Scan(value any) error {
	if value == nil {
		*m = MCPServers{}
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported type %T for MCPServers", value)
	}
	var decoded MCPServers
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return fmt.Errorf("failed to unmarshal MCPServers: %w", err)
	}
	if decoded == nil {
		decoded = MCPServers{}
	}
	*m = decoded
	return nil
}

// Session naming (migration 035). A name is OPTIONAL; when present it is the
// project-unique, immutable handle described on Session.Name.
var (
	// ErrSessionNameTaken means another session in the same project already
	// holds this name. Terminal, never retryable: the caller must pick another
	// name (the HTTP layer answers 409).
	ErrSessionNameTaken = errors.New("agentdb: session name already taken")
	// ErrSessionNameInvalid wraps every name-shape rejection (400, not 409 —
	// re-trying with the same name will never work).
	ErrSessionNameInvalid = errors.New("agentdb: invalid session name")
	// ErrSessionNotFound is returned by GetSessionByName for a name that is
	// absent, malformed, or in another project — deliberately the same answer
	// for all three, so a caller cannot probe another project's names.
	ErrSessionNotFound = errors.New("agentdb: session not found")
)

// sessionNameRe is the same kebab-case shape as worker names, skill names and
// project ids (`validProjectID` in cmd/agentd/googleauth.go). Names travel in
// URL paths — /embed/session/<name>, /agent/sessions/by-name/<name> — so the
// charset is deliberately the smallest one that is unambiguous in a path
// segment: nothing to escape, nothing to traverse with, no case to fold.
var sessionNameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// maxSessionNameLen matches the limit the project map applies to project ids.
const maxSessionNameLen = 64

// ValidateSessionName is the identity rule, exported so the HTTP layer can
// refuse a bad name before a session is provisioned rather than after. The
// empty string is NOT a valid name — "unnamed" is expressed by not calling
// this, and letting "" through here would make the empty name a lookup key that
// matches every anonymous chat session in the project.
func ValidateSessionName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrSessionNameInvalid)
	}
	if len(name) > maxSessionNameLen {
		return fmt.Errorf("%w: name is %d chars, max %d", ErrSessionNameInvalid, len(name), maxSessionNameLen)
	}
	if !sessionNameRe.MatchString(name) {
		return fmt.Errorf("%w: name %q is not kebab-case (lowercase letters, digits and single hyphens)", ErrSessionNameInvalid, name)
	}
	return nil
}

// isSessionNameCollision reports whether err is migration 035's unique-index
// violation rather than some other unique violation — chiefly a duplicate
// primary key, which the caller must not be told is a name clash. Both backends
// name the offending index or column in the message, and only this index has
// "name" in it (the table is `agent_sessions`, the PK index `agent_sessions_pkey`
// — neither contains the substring). Same shape as isConfigSeqCollision.
func isSessionNameCollision(err error) bool {
	if !isUniqueViolation(err) {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "name")
}

func (s *Store) CreateSession(ctx context.Context, session *Session) (*Session, error) {
	if session.ID == "" {
		session.ID = uuid.New().String()
	}
	if session.Customer == "" {
		return nil, fmt.Errorf("customer is required")
	}
	if session.WorkflowID == "" {
		return nil, fmt.Errorf("workflow_id is required")
	}
	if session.UserEmail == "" {
		return nil, fmt.Errorf("user_email is required")
	}
	if session.Name != "" {
		if err := ValidateSessionName(session.Name); err != nil {
			return nil, err
		}
	}
	if err := s.gdb.WithContext(ctx).Create(session).Error; err != nil {
		// Uniqueness is the index's job, not a prior SELECT's: two racing
		// creates of one name would both pass a pre-check and one of them would
		// still have to lose here.
		if session.Name != "" && isSessionNameCollision(err) {
			return nil, fmt.Errorf("%w: %q in project %q", ErrSessionNameTaken, session.Name, session.Customer)
		}
		return nil, fmt.Errorf("failed to create agent session: %w", err)
	}
	return session, nil
}

// GetSessionByName resolves a project-scoped session name to its row. It is the
// only lookup an embedding application needs: it stores the name it chose and
// never has to persist a session uuid.
//
// There is no by-name UPDATE or DELETE counterpart, and no rename anywhere in
// this store — see Session.Name.
func (s *Store) GetSessionByName(ctx context.Context, customer, name string) (*Session, error) {
	if customer == "" {
		// A caller bug, not a miss: an unscoped name lookup would cross the
		// tenancy boundary the (customer, name) index is built on.
		return nil, fmt.Errorf("cannot get agent session by name without customer")
	}
	if err := ValidateSessionName(name); err != nil {
		// A name that cannot exist is reported as absent rather than invalid:
		// the caller's only sane response is the same 404 either way, and it
		// keeps "" from matching the unnamed rows.
		return nil, fmt.Errorf("%w: %q in project %q", ErrSessionNotFound, name, customer)
	}
	var session Session
	err := s.gdb.WithContext(ctx).Where("customer = ? AND name = ?", customer, name).First(&session).Error
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("%w: %q in project %q", ErrSessionNotFound, name, customer)
		}
		return nil, fmt.Errorf("failed to get agent session by name: %w", err)
	}
	return &session, nil
}

func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	if id == "" {
		return nil, fmt.Errorf("cannot get agent session without ID")
	}
	var session Session
	if err := s.gdb.WithContext(ctx).Where("id = ?", id).First(&session).Error; err != nil {
		return nil, fmt.Errorf("failed to get agent session: %w", err)
	}
	return &session, nil
}

// SessionExists reports whether a session row is present, WITHOUT the
// "not found is an error" conflation GetSession has. It answers the archive
// loop's question — "is this container's session gone, or did the snapshot just
// fail?" — and it answers it by counting rows rather than by pattern-matching an
// error string, so a database that is merely unreachable returns an error and
// never a confident "absent". Callers must treat any error as "assume present".
//
// It satisfies agentkit.SessionExistenceChecker.
func (s *Store) SessionExists(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, fmt.Errorf("cannot check agent session without ID")
	}
	var n int64
	if err := s.gdb.WithContext(ctx).Model(&Session{}).Where("id = ?", id).Count(&n).Error; err != nil {
		return false, fmt.Errorf("failed to check agent session %q: %w", id, err)
	}
	return n > 0, nil
}

func (s *Store) UpdateSession(ctx context.Context, session *Session) (*Session, error) {
	if session.ID == "" {
		return nil, fmt.Errorf("cannot update agent session without ID")
	}
	result := s.gdb.WithContext(ctx).Save(session)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to update agent session: %w", result.Error)
	}
	return session, nil
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("cannot delete agent session without ID")
	}
	result := s.gdb.WithContext(ctx).Delete(&Session{}, "id = ?", id)
	if result.Error != nil {
		return fmt.Errorf("failed to delete agent session: %w", result.Error)
	}
	return nil
}

func (s *Store) ListSessions(ctx context.Context, query *SessionQuery) ([]*Session, error) {
	var sessions []*Session
	db := s.gdb.WithContext(ctx).Model(&Session{}).
		Select(`agent_sessions.*,
			COALESCE(ac.cnt, 0) as artifact_count,
			COALESCE(mc.cnt, 0) as message_count,
			COALESCE(tc.cnt, 0) as tool_call_count`).
		Joins("LEFT JOIN (SELECT session_id, COUNT(*) as cnt FROM agent_artifacts GROUP BY session_id) ac ON agent_sessions.id = ac.session_id").
		Joins("LEFT JOIN (SELECT session_id, COUNT(*) as cnt FROM agent_messages GROUP BY session_id) mc ON agent_sessions.id = mc.session_id").
		Joins("LEFT JOIN (SELECT session_id, COUNT(*) as cnt FROM agent_messages WHERE tool_name != '' GROUP BY session_id) tc ON agent_sessions.id = tc.session_id")

	if query != nil {
		if query.ID != "" {
			db = db.Where("agent_sessions.id = ?", query.ID)
		}
		if query.UserEmail != "" {
			db = db.Where("agent_sessions.user_email = ?", query.UserEmail)
		}
		if query.Customer != "" {
			db = db.Where("agent_sessions.customer = ?", query.Customer)
		}
		if query.Job != "" {
			db = db.Where("agent_sessions.job = ?", query.Job)
		}
		if query.Status != "" {
			db = db.Where("agent_sessions.status = ?", query.Status)
		}
		if query.Worker != "" {
			db = db.Where("agent_sessions.worker = ?", query.Worker)
		}
		if query.HasMessages {
			db = db.Where("EXISTS (SELECT 1 FROM agent_messages WHERE agent_messages.session_id = agent_sessions.id)")
		}
		if query.ExcludeWorkflowIDPrefix != "" {
			db = db.Where("agent_sessions.workflow_id NOT LIKE ?", query.ExcludeWorkflowIDPrefix+"%")
		}
		if len(query.ExcludeUserEmails) > 0 {
			// The column side is lowercased, so the argument side must be too —
			// otherwise mixed-case exclusion input silently matches nothing.
			db = db.Where("LOWER(agent_sessions.user_email) NOT IN ?", lowerAll(query.ExcludeUserEmails))
		}
	}

	limit := 50
	if query != nil && query.Limit > 0 {
		limit = query.Limit
	}
	offset := 0
	if query != nil && query.Offset > 0 {
		offset = query.Offset
	}

	if err := db.Order("agent_sessions.updated_at DESC, agent_sessions.created_at DESC").Limit(limit).Offset(offset).Find(&sessions).Error; err != nil {
		return nil, fmt.Errorf("failed to list agent sessions: %w", err)
	}
	if sessions == nil {
		sessions = []*Session{}
	}
	return sessions, nil
}

// GetSessionTokenSummary sums the token usage stored on a session's query
// events. The jsonb shape it reads is captured and explained in token_usage.go
// — do not change the path here without re-reading a real stored envelope.
//
// TotalCostUSD is reported as 0: `totalCostUsd` IS on the stored envelope, but
// it is a float that would need its own careful aggregation and nothing reads
// it, so this stays honest about summing only what it sums.
func (s *Store) GetSessionTokenSummary(ctx context.Context, sessionID string) (*TokenUsageSummary, error) {
	var summary TokenUsageSummary
	err := s.gdb.WithContext(ctx).Raw(`
		SELECT
			COALESCE(SUM(`+usageInputSQL+`), 0) AS input_tokens,
			COALESCE(SUM(`+usageOutputSQL+`), 0) AS output_tokens,
			0::float AS total_cost_usd
		FROM agent_query_events AS qe
		`+usageEnvelopes("qe")+`
		WHERE qe.session_id = ?`, sessionID).Scan(&summary).Error
	if err != nil {
		return nil, fmt.Errorf("failed to get session token summary: %w", err)
	}
	return &summary, nil
}

// CountSessionsBySnapshotState returns the number of sessions per
// snapshot_state (e.g. running, archived), optionally filtered by
// customer. Used by host admin dashboards.
func (s *Store) CountSessionsBySnapshotState(ctx context.Context, customer string) (map[string]int64, error) {
	var rows []struct {
		SnapshotState string
		Count         int64
	}
	db := s.gdb.WithContext(ctx).
		Model(&Session{}).
		Group("snapshot_state")
	if customer != "" {
		db = db.Where("customer = ?", customer)
	}
	if err := db.Select("snapshot_state, COUNT(*) as count").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to count sessions by snapshot state: %w", err)
	}
	counts := make(map[string]int64, len(rows))
	for _, r := range rows {
		counts[r.SnapshotState] = r.Count
	}
	return counts, nil
}

func (s *Store) GetSessionArchiveStats(ctx context.Context, customer string) (archived int64, totalBytes int64, err error) {
	var result struct {
		Count      int64
		TotalBytes int64
	}

	db := s.gdb.WithContext(ctx).
		Model(&Session{}).
		Where("snapshot_state = ?", "archived")

	if customer != "" {
		db = db.Where("customer = ?", customer)
	}

	err = db.Select("COUNT(*) as count, COALESCE(SUM(archive_size_bytes), 0) as total_bytes").Scan(&result).Error
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get archive stats: %w", err)
	}

	return result.Count, result.TotalBytes, nil
}

// isNotFound reports whether err represents a "record not found" condition from
// GORM. Used to distinguish "no binding" (not-found) from real store errors in
// GetWorkerBinding / SetWorkerBinding so pseudo-session IDs (e.g. the
// composition-build placeholder "_composition-image-build_") can participate in
// fleet placement without requiring a real session row.
func isNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound) ||
		(err != nil && strings.Contains(err.Error(), "record not found"))
}

func (s *Store) GetWorkerBinding(ctx context.Context, sessionID string) (string, bool, error) {
	row, err := s.GetSession(ctx, sessionID)
	if err != nil {
		if isNotFound(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if row.WorkerID == "" {
		return "", false, nil
	}
	return row.WorkerID, true, nil
}

func (s *Store) SetWorkerBinding(ctx context.Context, sessionID, workerID string) error {
	row, err := s.GetSession(ctx, sessionID)
	if err != nil {
		if isNotFound(err) {
			// Pseudo-session IDs have no DB row — binding is memory-only in the fleet.
			return nil
		}
		return err
	}
	row.WorkerID = workerID
	_, err = s.UpdateSession(ctx, row)
	return err
}

func (s *Store) ClearWorkerBinding(ctx context.Context, sessionID string) error {
	return s.SetWorkerBinding(ctx, sessionID, "")
}

func (s *Store) GetSnapshotHandle(ctx context.Context, sessionID string) (imageregistry.Handle, bool, error) {
	row, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return imageregistry.Handle{}, false, err
	}
	if row.SnapshotHandle == "" {
		return imageregistry.Handle{}, false, nil
	}
	var h imageregistry.Handle
	if err := json.Unmarshal([]byte(row.SnapshotHandle), &h); err != nil {
		return imageregistry.Handle{}, false, fmt.Errorf("agentdb: decode snapshot handle: %w", err)
	}
	return h, true, nil
}

func (s *Store) SetSnapshotHandle(ctx context.Context, sessionID string, h imageregistry.Handle) error {
	row, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	row.SnapshotHandle = string(b)
	_, err = s.UpdateSession(ctx, row)
	return err
}

// GetSessionMCPServers returns the MCP server config persisted on a session
// row. A session that never had any returns an empty (non-nil) map, so callers
// can merge into it without a nil check.
func (s *Store) GetSessionMCPServers(ctx context.Context, sessionID string) (MCPServers, error) {
	row, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if row.MCPServers == nil {
		return MCPServers{}, nil
	}
	return row.MCPServers, nil
}

// SetSessionMCPServers validates and replaces the MCP server config on a
// session row. The write is wholesale — no patch semantics — matching how the
// rest of the config surface behaves; passing an empty map clears it.
// Validation is loud: a malformed config is never persisted.
func (s *Store) SetSessionMCPServers(ctx context.Context, sessionID string, servers MCPServers) error {
	if err := servers.Validate(); err != nil {
		return err
	}
	row, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if servers == nil {
		servers = MCPServers{}
	}
	row.MCPServers = servers
	_, err = s.UpdateSession(ctx, row)
	return err
}

func (s *Store) ListSessionUsers(ctx context.Context, customer string) ([]string, error) {
	var emails []string
	err := s.gdb.WithContext(ctx).
		Model(&Session{}).
		Where("customer = ?", customer).
		Distinct("user_email").
		Order("user_email").
		Pluck("user_email", &emails).Error
	if err != nil {
		return nil, fmt.Errorf("failed to list agent session users: %w", err)
	}
	if emails == nil {
		emails = []string{}
	}
	return emails, nil
}
