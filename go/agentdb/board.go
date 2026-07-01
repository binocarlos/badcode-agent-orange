package agentdb

import (
	"context"
	"encoding/json"
)

// OpKind is the kind of mutation a changeset op performs on a board entity.
type OpKind string

const (
	OpAdd    OpKind = "add"
	OpUpdate OpKind = "update"
	OpRemove OpKind = "remove"
)

// Op is one mutation within a changeset. Body is the full entity body for
// add/update and is empty for remove.
type Op struct {
	Op         OpKind          `json:"op"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	Body       json.RawMessage `json:"body,omitempty"`
}

// Changeset is a proposed batch of ops appended to the board log as one revision.
type Changeset struct {
	ParentID string
	Author   string
	Message  string
	Ops      []Op
}

// BoardRevision is one immutable entry in the append-only board log (the source
// of truth). Ops folded in ascending Seq order reconstruct the board state.
// Postgres backs Seq with BIGSERIAL (see migration 020). CONTRACT for the
// future Append impl: let Postgres assign seq (omit it from the INSERT, then
// read it back) — never write a zero Seq. gorm only omits a zero-valued field
// when it carries a `default` tag, and seq deliberately has none (sqlite can't
// honor autoIncrement on a non-PK column), so a naive Create(&BoardRevision{})
// would write seq=0 explicitly, override the sequence, and collide on the
// UNIQUE constraint — breaking the monotonic fold ordering seq exists to give.
type BoardRevision struct {
	ID        string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	ParentID  string    `json:"parent_id" gorm:"type:varchar(36);default:''"`
	Seq       int64     `json:"seq" gorm:"column:seq;uniqueIndex:idx_board_revisions_seq"`
	Status    string    `json:"status" gorm:"type:varchar(20);not null;default:'applied';index:idx_board_revisions_status"`
	Author    string    `json:"author" gorm:"type:varchar(255);not null;default:''"`
	Message   string    `json:"message" gorm:"type:text;not null;default:''"`
	Ops       JSONArray `json:"ops" gorm:"type:jsonb;not null;default:'[]'"`
	CreatedAt int64     `json:"created_at" gorm:"autoCreateTime"`
}

func (BoardRevision) TableName() string { return "board_revisions" }

// BoardHead is the single-row pointer to the currently-live applied revision.
type BoardHead struct {
	Singleton  bool   `json:"-" gorm:"primaryKey;column:singleton;default:true"`
	RevisionID string `json:"revision_id" gorm:"type:varchar(36);not null"`
}

func (BoardHead) TableName() string { return "board_head" }

// BoardStaff is a reusable scope template (a "staff member"): role prompt
// fragment refs, assigned skills, model tier, memory view, and self-archiving
// strategy. Subscriptions are NOT here — they are standalone (BoardSubscription).
type BoardStaff struct {
	ID              string    `json:"id" gorm:"primaryKey;type:varchar(64)"`
	RoleFragments   JSONArray `json:"role_fragments" gorm:"type:jsonb;not null;default:'[]'"`
	Skills          JSONArray `json:"skills" gorm:"type:jsonb;not null;default:'[]'"`
	ModelTier       string    `json:"model_tier" gorm:"type:varchar(20);not null;default:'mid'"`
	MemoryNamespace string    `json:"memory_namespace" gorm:"type:varchar(255);not null;default:''"`
	SelfArchiving   JSONMap   `json:"self_archiving" gorm:"type:jsonb;not null;default:'{}'"`
	Budget          JSONMap   `json:"budget" gorm:"type:jsonb;not null;default:'{}'"`
	LastChangedIn   string    `json:"last_changed_in" gorm:"type:varchar(36);not null;default:''"`
}

func (BoardStaff) TableName() string { return "board_staff" }

// BoardEventType is one entry in the org event-bus taxonomy (distinct from the
// intra-session SSE vocabulary in go/events). An empty PayloadSchema means the
// event declares no payload shape.
type BoardEventType struct {
	ID            string  `json:"id" gorm:"primaryKey;type:varchar(64)"`
	Kind          string  `json:"kind" gorm:"type:varchar(20);not null;default:'lifecycle'"`
	Description   string  `json:"description" gorm:"type:text;not null;default:''"`
	PayloadSchema JSONMap `json:"payload_schema" gorm:"type:jsonb;not null;default:'{}'"`
	LastChangedIn string  `json:"last_changed_in" gorm:"type:varchar(36);not null;default:''"`
}

func (BoardEventType) TableName() string { return "board_event_types" }

// BoardSubscription is a standalone candidate binding: event -> reaction, where
// a reaction is a staff member or a pipeline. ApplicabilityCondition is the
// ReasoningBank "when X" match key. Cross-references are logical (validated
// before apply), not FK-enforced.
type BoardSubscription struct {
	ID                     string `json:"id" gorm:"primaryKey;type:varchar(64)"`
	EventType              string `json:"event_type" gorm:"type:varchar(64);not null;index:idx_board_subs_event,priority:1"`
	ReactionKind           string `json:"reaction_kind" gorm:"type:varchar(20);not null"`
	ReactionRef            string `json:"reaction_ref" gorm:"type:varchar(64);not null"`
	ApplicabilityCondition string `json:"applicability_condition" gorm:"type:text;not null;default:''"`
	// NB: no `default:true` in the gorm tag on purpose — gorm omits zero-valued
	// fields that carry a default tag, which would silently turn an explicit
	// Enabled:false into true. The production SQL keeps DEFAULT TRUE for raw
	// inserts; gorm callers set Enabled explicitly.
	Enabled       bool   `json:"enabled" gorm:"not null;index:idx_board_subs_event,priority:2"`
	LastChangedIn string `json:"last_changed_in" gorm:"type:varchar(36);not null;default:''"`
}

func (BoardSubscription) TableName() string { return "board_subscriptions" }

// BoardPipeline is a named ordered sequence of stages run by run_pipeline. The
// stage inner schema is left as opaque JSON until run_pipeline is specified.
type BoardPipeline struct {
	ID            string    `json:"id" gorm:"primaryKey;type:varchar(64)"`
	Description   string    `json:"description" gorm:"type:text;not null;default:''"`
	Stages        JSONArray `json:"stages" gorm:"type:jsonb;not null;default:'[]'"`
	LastChangedIn string    `json:"last_changed_in" gorm:"type:varchar(36);not null;default:''"`
}

func (BoardPipeline) TableName() string { return "board_pipelines" }

// BoardPromptFragment is a versioned prompt fragment composed into worker
// prompts at dispatch. Its version is the board revision it lives in (no
// separate version field).
type BoardPromptFragment struct {
	ID            string `json:"id" gorm:"primaryKey;type:varchar(64)"`
	Kind          string `json:"kind" gorm:"type:varchar(20);not null;default:'role'"`
	Body          string `json:"body" gorm:"type:text;not null;default:''"`
	LastChangedIn string `json:"last_changed_in" gorm:"type:varchar(36);not null;default:''"`
}

func (BoardPromptFragment) TableName() string { return "board_prompt_fragments" }

// Board is the fully-folded state of the board at one revision — the read-side
// aggregate returned by BoardStore.Current / AsOf.
type Board struct {
	Revision      string
	Staff         []BoardStaff
	EventTypes    []BoardEventType
	Subscriptions []BoardSubscription
	Pipelines     []BoardPipeline
	Fragments     []BoardPromptFragment
}

// BoardStore is the seam over the architecture board. The implementation is
// Postgres-backed (a later spec); a git-backed implementation is a future swap.
// Current/AsOf read folded state; Head returns the live applied revision id;
// Append writes a changeset and returns the new revision id; Revisions returns the
// append-only log in seq order (the "show your work" story timeline — v1 contracts
// §10b E-2, surfaced by the §8 /api/board/revisions endpoint).
type BoardStore interface {
	Current(ctx context.Context) (Board, error)
	AsOf(ctx context.Context, revisionID string) (Board, error)
	Head(ctx context.Context) (revisionID string, err error)
	Append(ctx context.Context, cs Changeset) (revisionID string, err error)
	Revisions(ctx context.Context) ([]BoardRevision, error)
}
