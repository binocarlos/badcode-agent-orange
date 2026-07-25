package agentdb

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONMap is a map[string]any stored as JSONB in PostgreSQL.
type JSONMap map[string]any

func (m JSONMap) Value() (driver.Value, error) {
	if m == nil {
		return "{}", nil
	}
	return json.Marshal(m)
}

func (m *JSONMap) Scan(value any) error {
	if value == nil {
		*m = JSONMap{}
		return nil
	}
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("unsupported type %T for JSONMap", value)
	}
	var decoded JSONMap
	if err := json.Unmarshal(bytes, &decoded); err != nil {
		return fmt.Errorf("failed to unmarshal JSONMap: %w", err)
	}
	*m = decoded
	return nil
}

// JSONArray is a json.RawMessage stored as JSONB in PostgreSQL.
type JSONArray json.RawMessage

func (a JSONArray) Value() (driver.Value, error) {
	if a == nil {
		return "[]", nil
	}
	return []byte(a), nil
}

func (a JSONArray) MarshalJSON() ([]byte, error) {
	if a == nil {
		return []byte("[]"), nil
	}
	return []byte(a), nil
}

func (a *JSONArray) UnmarshalJSON(data []byte) error {
	*a = JSONArray(data)
	return nil
}

func (a *JSONArray) Scan(value any) error {
	if value == nil {
		*a = JSONArray("[]")
		return nil
	}
	switch v := value.(type) {
	case []byte:
		*a = JSONArray(v)
	case string:
		*a = JSONArray(v)
	default:
		return fmt.Errorf("unsupported type %T for JSONArray", value)
	}
	return nil
}

// Session represents an agent session row.
type Session struct {
	ID                string  `json:"id" gorm:"primaryKey;type:varchar(36)"`
	CreatedAt         int64   `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt         int64   `json:"updated_at" gorm:"autoUpdateTime"`
	UserEmail         string  `json:"user_email" gorm:"type:varchar(255);not null;index:idx_agent_sessions_user_email"`
	Customer          string  `json:"customer" gorm:"type:varchar(255);not null;index:idx_agent_sessions_customer"`
	Job               string  `json:"job,omitempty" gorm:"type:varchar(255);default:''"`
	WorkflowID        string  `json:"workflow_id" gorm:"type:varchar(100);not null"`
	Persona           string  `json:"persona,omitempty" gorm:"type:varchar(255);default:''"`
	Title             string  `json:"title" gorm:"type:varchar(255);default:''"`
	Status            string  `json:"status" gorm:"type:varchar(50);default:'active';index:idx_agent_sessions_status"`
	CurrentNode       string  `json:"current_node,omitempty" gorm:"type:varchar(100);default:''"`
	NodeResults       JSONMap `json:"node_results,omitempty" gorm:"type:jsonb;default:'{}'"`
	Metadata          JSONMap `json:"metadata,omitempty" gorm:"type:jsonb;default:'{}'"`
	SnapshotState     string  `json:"snapshot_state,omitempty" gorm:"type:varchar(50);default:''"`
	SnapshotImageTag  string  `json:"snapshot_image_tag,omitempty" gorm:"type:varchar(512);default:''"`
	SnapshotBaseImage string  `json:"snapshot_base_image,omitempty" gorm:"type:varchar(512);default:''"`
	ArchiveBlobPath   string  `json:"archive_blob_path,omitempty" gorm:"type:varchar(1024);default:''"`
	ArchiveSizeBytes  int64   `json:"archive_size_bytes,omitempty" gorm:"default:0"`
	ArchiveDiffSize   int64   `json:"archive_diff_size,omitempty" gorm:"default:0"`
	SnapshotHandle    string  `json:"snapshot_handle,omitempty" gorm:"type:text;default:''"`
	WorkerID          string  `json:"worker_id,omitempty" gorm:"type:varchar(100);default:''"`
	Installation      string  `json:"installation,omitempty" gorm:"type:text;default:''"`
	CustomImageID     string  `json:"custom_image_id,omitempty" gorm:"type:text;default:''"`
	// MCPServers is the session's MCP server config (§4.5). Safe to persist and
	// display whole: values are ${VAR} references, never secrets (§4.4).
	MCPServers MCPServers `json:"mcp_servers,omitempty" gorm:"type:jsonb;default:'{}'"`
	// Worker is the product-level worker (persona) whose job this session is —
	// spec 02-workers §6.5. Empty for plain vanilla sessions. NOT the same thing
	// as WorkerID above, which is the fleet-placement binding (which host runs
	// the container).
	Worker string `json:"worker,omitempty" gorm:"type:text;default:''"`
	// ComposedPrompt is the full system prompt ComposeJob produced for this
	// session, written once at composition time so every transcript is tied to
	// the exact prompt that produced it (§6.2). Provenance, not a version store.
	ComposedPrompt string `json:"composed_prompt,omitempty" gorm:"type:text;default:''"`
	// LeaseExpiresAt is the unix-seconds deadline of the router's session lease;
	// the reaper fails jobs whose lease lapsed (04-events-and-schedules §8.4).
	// 0 = no lease held.
	LeaseExpiresAt int64  `json:"lease_expires_at,omitempty" gorm:"default:0"`
	ArtifactCount  int    `json:"artifact_count" gorm:"->;<-:false"`
	MessageCount   int    `json:"message_count" gorm:"->;<-:false"`
	ToolCallCount  int    `json:"tool_call_count" gorm:"->;<-:false"`
	ContainerState string `json:"container_state" gorm:"-"`
}

func (Session) TableName() string { return "agent_sessions" }

// Message represents an agent message row.
type Message struct {
	ID          string  `json:"id" gorm:"primaryKey;type:varchar(36)"`
	CreatedAt   int64   `json:"created_at" gorm:"autoCreateTime"`
	SessionID   string  `json:"session_id" gorm:"type:varchar(36);not null;index:idx_agent_messages_session_id"`
	QueryID     string  `json:"query_id,omitempty" gorm:"type:varchar(100);default:''"`
	PhaseNode   string  `json:"phase_node,omitempty" gorm:"type:varchar(100);default:''"`
	Role        string  `json:"role" gorm:"type:varchar(20);not null"`
	Content     string  `json:"content" gorm:"type:text;default:''"`
	ToolName    string  `json:"tool_name,omitempty" gorm:"type:varchar(255);default:''"`
	ToolInput   JSONMap `json:"tool_input,omitempty" gorm:"type:jsonb;default:'{}'"`
	SequenceNum int     `json:"sequence_num" gorm:"not null;default:0"`
	Metadata    JSONMap `json:"metadata,omitempty" gorm:"type:jsonb;default:'{}'"`
}

func (Message) TableName() string { return "agent_messages" }

// QueryEvents stores compacted SSE events for a query.
type QueryEvents struct {
	ID         string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	SessionID  string    `json:"session_id" gorm:"type:varchar(36);not null;uniqueIndex:idx_aqe_session_query"`
	QueryID    string    `json:"query_id" gorm:"type:varchar(100);not null;uniqueIndex:idx_aqe_session_query"`
	Events     JSONArray `json:"events" gorm:"type:jsonb;default:'[]'"`
	SearchText string    `json:"search_text" gorm:"type:text;default:''"`
	CreatedAt  int64     `json:"created_at" gorm:"autoCreateTime"`
}

func (QueryEvents) TableName() string { return "agent_query_events" }

// Artifact represents a file artifact created during a session.
type Artifact struct {
	ID             string `json:"id" gorm:"primaryKey;type:varchar(36)"`
	CreatedAt      int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt      int64  `json:"updated_at" gorm:"autoUpdateTime"`
	SessionID      string `json:"session_id" gorm:"type:varchar(36);index:idx_agent_artifacts_session_id"`
	UserEmail      string `json:"user_email" gorm:"type:varchar(255);index:idx_agent_artifacts_user_email"`
	Customer       string `json:"customer" gorm:"type:varchar(255)"`
	Job            string `json:"job" gorm:"type:varchar(255)"`
	FilePath       string `json:"file_path" gorm:"type:varchar(1024)"`
	FileName       string `json:"file_name" gorm:"type:varchar(255)"`
	FileSize       int64  `json:"file_size"`
	MimeType       string `json:"mime_type" gorm:"type:varchar(255)"`
	Label          string `json:"label" gorm:"type:varchar(255)"`
	Description    string `json:"description" gorm:"type:text"`
	ArtifactType   string `json:"artifact_type" gorm:"type:varchar(50)"`
	Source         string `json:"source" gorm:"type:varchar(50)"`
	AzureBlobPath  string `json:"azure_blob_path" gorm:"type:varchar(1024)"`
	Status         string `json:"status" gorm:"type:varchar(50);default:'live';index:idx_agent_artifacts_status"`
	PublishToFiles bool   `json:"publish_to_files" gorm:"default:false"`
	IsDir          bool   `json:"is_dir" gorm:"default:false"`
}

func (Artifact) TableName() string { return "agent_artifacts" }

// Skill is a durable, cross-session catalog entry promoted from a hoisted skill
// bundle. Independent of the session it came from (no FK cascade).
type Skill struct {
	ID              string  `json:"id" gorm:"primaryKey;type:varchar(36)"`
	CreatedAt       int64   `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt       int64   `json:"updated_at" gorm:"autoUpdateTime"`
	Name            string  `json:"name" gorm:"type:varchar(255);index:idx_agent_skills_lookup,priority:3"`
	Description     string  `json:"description" gorm:"type:text"`
	Visibility      string  `json:"visibility" gorm:"type:varchar(20);default:'organizational';index:idx_agent_skills_lookup,priority:1"`
	Customer        string  `json:"customer" gorm:"type:varchar(255);index:idx_agent_skills_lookup,priority:2"`
	OwnerEmail      string  `json:"owner_email" gorm:"type:varchar(255);index:idx_agent_skills_owner"`
	RequiresBuild   bool    `json:"requires_build" gorm:"default:false"`
	ContentHash     string  `json:"content_hash" gorm:"type:varchar(64)"`
	BlobPrefix      string  `json:"blob_prefix" gorm:"type:varchar(1024)"`
	Manifest        JSONMap `json:"manifest" gorm:"type:jsonb;default:'{}'"`
	SourceSessionID string  `json:"source_session_id" gorm:"type:varchar(36)"`
	PromotedBy      string  `json:"promoted_by" gorm:"type:varchar(255);default:''"`
}

func (Skill) TableName() string { return "agent_skills" }

// CustomImage is a built, content-addressed container image composed from an
// ordered set of library skills (see agent-library/go/runner_composition.go).
// Like Skill, it is a first-class catalog entity (not session-scoped) and uses
// the same strict customer-scoping visibility rules — except it is never public.
//
// Since migration 025 the same table also carries the §13 catalogue: named,
// versioned, labeled, append-only image records written by CreateCustomImage
// and read by ResolveCustomImage / ListCustomImages (customimages.go). §13 rows
// are exactly those with Version >= 1, and their project namespace is the
// `customer` column — see the namespace note at the top of customimages.go.
type CustomImage struct {
	ID               string `json:"id" gorm:"primaryKey;type:varchar(36)"`
	CreatedAt        int64  `json:"created_at" gorm:"autoCreateTime"`
	UpdatedAt        int64  `json:"updated_at" gorm:"autoUpdateTime"`
	Name             string `json:"name" gorm:"type:varchar(255);index:idx_agent_custom_images_lookup,priority:3"`
	Description      string `json:"description" gorm:"type:text"`
	Visibility       string `json:"visibility" gorm:"type:varchar(20);default:'organizational';index:idx_agent_custom_images_lookup,priority:1"`
	Customer         string `json:"customer" gorm:"type:varchar(255);index:idx_agent_custom_images_lookup,priority:2"`
	OwnerEmail       string `json:"owner_email" gorm:"type:varchar(255);index:idx_agent_custom_images_owner"`
	ContentHash      string `json:"content_hash" gorm:"type:varchar(64)"`
	RegistryHandle   string `json:"registry_handle" gorm:"type:text"`                                         // JSON-encoded imageregistry.Handle
	SkillSet         string `json:"skill_set" gorm:"type:text"`                                               // JSON-encoded ordered [{skillId,name,content_hash}]
	RequiresBuild    bool   `json:"requires_build" gorm:"default:false"`                                      // true iff any included skill had install.sh
	BaseImageID      string `json:"base_image_id" gorm:"type:varchar(36);index:idx_agent_custom_images_base"` // lineage: custom image this was built on ("" = built on platform base)
	BaseInstallation string `json:"base_installation,omitempty" gorm:"type:text;default:''"`                  // installation name when built directly on a platform installation
	Focus            string `json:"focus,omitempty" gorm:"type:text;default:''"`                              // CLAUDE.md focus applied in this layer

	// ── §13 catalogue columns (migration 025) ──────────────────────────────
	//
	// None of these carries a gorm `default:` tag on purpose: GORM omits a
	// zero-valued field from the INSERT when a default is declared, which would
	// silently turn "version 0 / no labels / not reaped" into whatever the DDL
	// says. The migration still carries DEFAULTs, for rows written outside GORM.

	// Version is the second half of the §13.2 identity `name:version` — a
	// monotonic, gap-free integer allocated per (project, name) starting at 1.
	// Zero marks a pre-§13 row written by the legacy latest-wins
	// UpsertCustomImage path: such rows are not in the catalogue, are never
	// listed by ListCustomImages, and never resolve (§13.3).
	Version int `json:"version"`
	// Labels are the commit message of a version (§13.2) — the same grammar and
	// the same limits as memory labels (labels.go: one validator, one selector
	// parser, one jsonb translator for the whole system).
	Labels LabelSet `json:"labels" gorm:"type:jsonb"`
	// CreatedByWorker names the worker that burned this version (§13.2).
	CreatedByWorker string `json:"created_by_worker,omitempty" gorm:"type:text"`
	// CreatedBySession names the session it was burned from — permalinkable like
	// a memory hit (§7.3). It deliberately maps onto the pre-existing
	// `source_session_id` column (migration 018), which already meant exactly
	// "session this image was burned from": a second column with the same
	// meaning is drift waiting to happen.
	CreatedBySession string `json:"created_by_session,omitempty" gorm:"column:source_session_id;type:varchar(36)"`
	// ReapedAt tombstones a version whose bytes the snapshot_ttl_days reaper
	// (§5, B4) has deleted: unix seconds, 0 = live. The record outlives the
	// bytes so the catalogue stays honest — resolving a reaped version fails
	// loudly with ErrCustomImageReaped rather than pointing at nothing (§13.7).
	ReapedAt int64 `json:"reaped_at,omitempty"`

	// ── §5 snapshot TTL metadata (migration 027, B4) ────────────────────────
	//
	// §5 requires every snapshot to carry {source session, created_at, expiry,
	// last_resumed_at}. Source session is CreatedBySession and created_at is
	// CreatedAt, so these two complete the tuple. Both are unix SECONDS, like
	// CreatedAt and ReapedAt on this table.

	// ExpiresAt is the instant the reaper may delete this version's bytes. It is
	// stamped at burn time from the project's snapshot_ttl_days and is a promise
	// the reaper honours even if the setting changes afterwards. 0 = never —
	// both "the project set snapshot_ttl_days: 0" and rows burned before B4,
	// which carry no promise and are therefore kept.
	ExpiresAt int64 `json:"expires_at,omitempty"`
	// LastResumedAt is the last time a session launched from this version.
	// Informational: §5 sets the expiry at snapshot time, so resuming does NOT
	// extend it — this is what tells an operator whether a soon-to-expire image
	// is still in use.
	LastResumedAt int64 `json:"last_resumed_at,omitempty"`
}

func (CustomImage) TableName() string { return "agent_custom_images" }

// SessionQuery holds filter parameters for listing sessions.
type SessionQuery struct {
	ID                      string
	UserEmail               string
	Customer                string
	Job                     string
	Status                  string
	HasMessages             bool
	ExcludeWorkflowIDPrefix string
	ExcludeUserEmails       []string
	Limit                   int
	Offset                  int
}

// MessageQuery holds filter parameters for listing messages.
type MessageQuery struct {
	SessionID string
	PhaseNode string
	Limit     int
	Offset    int
}

// MessageSearchQuery holds parameters for full-text search.
type MessageSearchQuery struct {
	UserEmail         string
	Customer          string
	Query             string
	Job               string
	Role              string
	ExcludeUserEmails []string
	Limit             int
}

// MessageSearchResult is a single search result.
type MessageSearchResult struct {
	SessionID    string  `json:"session_id"`
	SessionTitle string  `json:"session_title"`
	UserEmail    string  `json:"user_email"`
	Role         string  `json:"role"`
	Content      string  `json:"content"`
	CreatedAt    int64   `json:"created_at"`
	Job          string  `json:"job"`
	WorkflowID   string  `json:"workflow_id"`
	Rank         float64 `json:"rank"`
}

// TokenUsageSummary aggregates token usage for a session.
type TokenUsageSummary struct {
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}
