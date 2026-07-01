package agentdb

// TelemetryRun is the persistence row for one recorded scope execution (contract
// §5 Telemetry.Run). The domain type is orchestrator.Run (mapped in
// orchestrator/pgstore). Append-only "show your work" substrate.
type TelemetryRun struct {
	ID            string `json:"id" gorm:"primaryKey;type:varchar(36)"`
	Seq           int64  `json:"seq" gorm:"column:seq;uniqueIndex:idx_runs_seq"`
	Scope         string `json:"scope" gorm:"type:varchar(255);not null;default:''"`
	BoardRevision string `json:"board_revision" gorm:"type:varchar(36);not null;default:''"`
	Prompt        string `json:"prompt" gorm:"type:text;not null;default:''"`
	Output        string `json:"output" gorm:"type:text;not null;default:''"`
	CreatedAt     int64  `json:"created_at" gorm:"autoCreateTime"`
}

func (TelemetryRun) TableName() string { return "runs" }
