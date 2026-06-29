package agentdb

import "encoding/json"

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
// Postgres backs Seq with BIGSERIAL (see migration 020).
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
