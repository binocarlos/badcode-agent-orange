package agentdb

// Ticket is the persistence row for a board work item (contract §4). It is the
// storage shape only; the domain type is orchestrator.Ticket (mapped in
// orchestrator/pgstore). Ungated work state — NOT part of the versioned board log.
type Ticket struct {
	ID           string    `json:"id" gorm:"primaryKey;type:varchar(36)"`
	ProjectID    string    `json:"project_id" gorm:"type:varchar(64);not null;default:'';index:idx_tickets_project"`
	Title        string    `json:"title" gorm:"type:text;not null;default:''"`
	Objective    string    `json:"objective" gorm:"type:text;not null;default:''"`
	Acceptance   string    `json:"acceptance" gorm:"type:text;not null;default:''"`
	Status       string    `json:"status" gorm:"type:varchar(20);not null;default:'backlog';index:idx_tickets_status"`
	Scope        JSONArray `json:"scope" gorm:"type:jsonb;not null;default:'{}'"`
	Result       JSONArray `json:"result" gorm:"type:jsonb;not null;default:'{}'"`
	PendingPost  JSONArray `json:"pending_post" gorm:"type:jsonb;not null;default:'{}'"`
	PublishedRef string    `json:"published_ref" gorm:"type:varchar(255);not null;default:''"`
	Disposition  string    `json:"disposition" gorm:"type:varchar(20);not null;default:''"`
	AttemptNotes JSONArray `json:"attempt_notes" gorm:"type:jsonb;not null;default:'[]'"`
	DependsOn    JSONArray `json:"depends_on" gorm:"type:jsonb;not null;default:'[]'"`
	Parent       string    `json:"parent" gorm:"type:varchar(36);not null;default:''"`
	Attempts     int       `json:"attempts" gorm:"not null;default:0"`
	BoardRev     string    `json:"board_rev" gorm:"type:varchar(36);not null;default:''"`
	CreatedAt    int64     `json:"created_at" gorm:"not null;default:0"`
	UpdatedAt    int64     `json:"updated_at" gorm:"not null;default:0"`
}

func (Ticket) TableName() string { return "tickets" }
