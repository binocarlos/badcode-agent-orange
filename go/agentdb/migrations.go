package agentdb

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"gorm.io/gorm"
)

type migration struct {
	Name string
	SQL  string
}

var agentMigrations = []migration{
	{
		Name: "001_agent_sessions",
		SQL: `
			CREATE TABLE IF NOT EXISTS agent_sessions (
				id VARCHAR(36) PRIMARY KEY,
				created_at BIGINT NOT NULL DEFAULT 0,
				updated_at BIGINT NOT NULL DEFAULT 0,
				user_email VARCHAR(255) NOT NULL,
				customer VARCHAR(255) NOT NULL,
				job VARCHAR(255) DEFAULT '',
				workflow_id VARCHAR(100) NOT NULL,
				persona VARCHAR(255) DEFAULT '',
				status VARCHAR(50) DEFAULT 'active',
				current_node VARCHAR(100) DEFAULT '',
				node_results JSONB DEFAULT '{}',
				metadata JSONB DEFAULT '{}'
			);
			CREATE INDEX IF NOT EXISTS idx_agent_sessions_user_email ON agent_sessions(user_email);
			CREATE INDEX IF NOT EXISTS idx_agent_sessions_customer ON agent_sessions(customer);
			CREATE INDEX IF NOT EXISTS idx_agent_sessions_status ON agent_sessions(status);
		`,
	},
	{
		Name: "002_agent_artifacts",
		SQL: `
			CREATE TABLE IF NOT EXISTS agent_artifacts (
				id VARCHAR(36) PRIMARY KEY,
				created_at BIGINT NOT NULL DEFAULT 0,
				updated_at BIGINT NOT NULL DEFAULT 0,
				session_id VARCHAR(36) REFERENCES agent_sessions(id) ON DELETE CASCADE,
				user_email VARCHAR(255),
				customer VARCHAR(255),
				job VARCHAR(255),
				file_path VARCHAR(1024),
				file_name VARCHAR(255),
				file_size BIGINT DEFAULT 0,
				mime_type VARCHAR(255),
				label VARCHAR(255),
				description TEXT DEFAULT '',
				artifact_type VARCHAR(50),
				source VARCHAR(50),
				azure_blob_path VARCHAR(1024) DEFAULT '',
				status VARCHAR(50) DEFAULT 'live',
				publish_to_files BOOLEAN DEFAULT FALSE
			);
			CREATE INDEX IF NOT EXISTS idx_agent_artifacts_session_id ON agent_artifacts(session_id);
			CREATE INDEX IF NOT EXISTS idx_agent_artifacts_user_email ON agent_artifacts(user_email);
			CREATE INDEX IF NOT EXISTS idx_agent_artifacts_status ON agent_artifacts(status);
		`,
	},
	{
		Name: "003_agent_messages",
		SQL: `
			CREATE TABLE IF NOT EXISTS agent_messages (
				id VARCHAR(36) PRIMARY KEY,
				created_at BIGINT NOT NULL DEFAULT 0,
				session_id VARCHAR(36) NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
				query_id VARCHAR(100) DEFAULT '',
				phase_node VARCHAR(100) DEFAULT '',
				role VARCHAR(20) NOT NULL,
				content TEXT DEFAULT '',
				tool_name VARCHAR(255) DEFAULT '',
				tool_input JSONB DEFAULT '{}',
				sequence_num INT NOT NULL DEFAULT 0,
				metadata JSONB DEFAULT '{}'
			);
			CREATE INDEX IF NOT EXISTS idx_agent_messages_session_id ON agent_messages(session_id);
		`,
	},
	{
		Name: "004_agent_session_title",
		SQL:  `ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS title VARCHAR(255) DEFAULT '';`,
	},
	{
		Name: "005_agent_messages_tsv",
		SQL: `
			ALTER TABLE agent_messages ADD COLUMN IF NOT EXISTS content_tsv TSVECTOR;
			CREATE INDEX IF NOT EXISTS idx_agent_messages_tsv ON agent_messages USING GIN(content_tsv);
			CREATE OR REPLACE FUNCTION agent_messages_tsv_trigger() RETURNS trigger AS $$
			BEGIN
				NEW.content_tsv := to_tsvector('english', COALESCE(NEW.content, ''));
				RETURN NEW;
			END
			$$ LANGUAGE plpgsql;
			DROP TRIGGER IF EXISTS agent_messages_tsv_update ON agent_messages;
			CREATE TRIGGER agent_messages_tsv_update BEFORE INSERT OR UPDATE ON agent_messages
			FOR EACH ROW EXECUTE FUNCTION agent_messages_tsv_trigger();
			UPDATE agent_messages SET content_tsv = to_tsvector('english', COALESCE(content, ''))
			WHERE content_tsv IS NULL;
		`,
	},
	{
		Name: "006_agent_session_snapshots",
		SQL: `
			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS snapshot_state VARCHAR(50) DEFAULT '';
			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS snapshot_image_tag VARCHAR(512) DEFAULT '';
			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS snapshot_base_image VARCHAR(512) DEFAULT '';
			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS archive_blob_path VARCHAR(1024) DEFAULT '';
			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS archive_size_bytes BIGINT DEFAULT 0;
		`,
	},
	{
		Name: "007_agent_session_archive_diff_size",
		SQL:  `ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS archive_diff_size BIGINT DEFAULT 0;`,
	},
	{
		Name: "008_agent_artifact_publish",
		SQL:  `ALTER TABLE agent_artifacts ADD COLUMN IF NOT EXISTS publish_to_files BOOLEAN DEFAULT FALSE;`,
	},
	{
		Name: "009_drop_agent_apps",
		SQL:  `DROP TABLE IF EXISTS agent_apps`,
	},
	{
		Name: "010_agent_query_events",
		SQL: `
			CREATE TABLE IF NOT EXISTS agent_query_events (
				id VARCHAR(36) PRIMARY KEY,
				session_id VARCHAR(36) NOT NULL REFERENCES agent_sessions(id) ON DELETE CASCADE,
				query_id VARCHAR(100) NOT NULL,
				events JSONB NOT NULL DEFAULT '[]',
				search_text TEXT DEFAULT '',
				created_at BIGINT NOT NULL DEFAULT 0,
				UNIQUE(session_id, query_id)
			);
			CREATE INDEX IF NOT EXISTS idx_agent_query_events_session ON agent_query_events(session_id);
		`,
	},
	{
		Name: "011_agent_session_snapshot_handle_worker",
		SQL: `
			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS snapshot_handle TEXT DEFAULT '';
			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS worker_id VARCHAR(100) DEFAULT '';
		`,
	},
	{
		Name: "012_agent_artifact_is_dir",
		SQL:  `ALTER TABLE agent_artifacts ADD COLUMN IF NOT EXISTS is_dir BOOLEAN DEFAULT FALSE;`,
	},
	{
		Name: "013_agent_skills",
		SQL: `
			CREATE TABLE IF NOT EXISTS agent_skills (
				id VARCHAR(36) PRIMARY KEY,
				created_at BIGINT NOT NULL DEFAULT 0,
				updated_at BIGINT NOT NULL DEFAULT 0,
				name VARCHAR(255) NOT NULL,
				description TEXT DEFAULT '',
				visibility VARCHAR(20) NOT NULL DEFAULT 'organizational',
				customer VARCHAR(255) NOT NULL DEFAULT '',
				owner_email VARCHAR(255) NOT NULL DEFAULT '',
				requires_build BOOLEAN DEFAULT FALSE,
				content_hash VARCHAR(64) DEFAULT '',
				blob_prefix VARCHAR(1024) DEFAULT '',
				manifest JSONB DEFAULT '{}',
				source_session_id VARCHAR(36) DEFAULT '',
				promoted_by VARCHAR(255) DEFAULT ''
			);
			CREATE INDEX IF NOT EXISTS idx_agent_skills_lookup ON agent_skills(visibility, customer, name);
			CREATE INDEX IF NOT EXISTS idx_agent_skills_owner ON agent_skills(owner_email);
		`,
	},
	{
		Name: "014_agent_custom_images",
		SQL: `
			CREATE TABLE IF NOT EXISTS agent_custom_images (
				id VARCHAR(36) PRIMARY KEY,
				created_at BIGINT NOT NULL DEFAULT 0,
				updated_at BIGINT NOT NULL DEFAULT 0,
				name VARCHAR(255) NOT NULL,
				description TEXT DEFAULT '',
				visibility VARCHAR(20) NOT NULL DEFAULT 'organizational',
				customer VARCHAR(255) NOT NULL DEFAULT '',
				owner_email VARCHAR(255) NOT NULL DEFAULT '',
				content_hash VARCHAR(64) DEFAULT '',
				registry_handle TEXT DEFAULT '',
				skill_set TEXT DEFAULT '',
				requires_build BOOLEAN DEFAULT FALSE
			);
			CREATE INDEX IF NOT EXISTS idx_agent_custom_images_lookup ON agent_custom_images(visibility, customer, name);
			CREATE INDEX IF NOT EXISTS idx_agent_custom_images_owner ON agent_custom_images(owner_email);
		`,
	},
	{
		Name: "015_agent_custom_images_base_image_id",
		SQL: `
			ALTER TABLE agent_custom_images ADD COLUMN IF NOT EXISTS base_image_id VARCHAR(36) DEFAULT '';
			CREATE INDEX IF NOT EXISTS idx_agent_custom_images_base ON agent_custom_images(base_image_id);
		`,
	},
	{
		Name: "016_agent_session_installation",
		SQL:  `ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS installation TEXT NOT NULL DEFAULT '';`,
	},
	{
		Name: "017_agent_session_custom_image_id",
		SQL:  `ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS custom_image_id TEXT NOT NULL DEFAULT '';`,
	},
	{
		Name: "018_agent_custom_images_provenance",
		SQL: `
			ALTER TABLE agent_custom_images ADD COLUMN IF NOT EXISTS base_installation TEXT DEFAULT '';
			ALTER TABLE agent_custom_images ADD COLUMN IF NOT EXISTS source_session_id VARCHAR(36) DEFAULT '';
			ALTER TABLE agent_custom_images ADD COLUMN IF NOT EXISTS focus TEXT DEFAULT '';
		`,
	},
	{
		Name: "019_agent_conversation_index",
		SQL: `
			CREATE EXTENSION IF NOT EXISTS vector;
			CREATE TABLE IF NOT EXISTS agent_conversation_index (
				session_id        VARCHAR(36) PRIMARY KEY REFERENCES agent_sessions(id) ON DELETE CASCADE,
				customer          VARCHAR(255) NOT NULL,
				job               VARCHAR(255) NOT NULL DEFAULT '',
				user_email        VARCHAR(255) NOT NULL DEFAULT '',
				workflow_id       VARCHAR(100) NOT NULL DEFAULT '',
				title             VARCHAR(255) NOT NULL DEFAULT '',
				summary           TEXT NOT NULL DEFAULT '',
				summary_embedding vector(1536),
				transcript_tsv    TSVECTOR,
				message_count     INT NOT NULL DEFAULT 0,
				last_activity_at  BIGINT NOT NULL DEFAULT 0,
				indexed_at        BIGINT NOT NULL DEFAULT 0,
				source_hash       TEXT NOT NULL DEFAULT ''
			);
			CREATE INDEX IF NOT EXISTS idx_aci_customer ON agent_conversation_index(customer);
			CREATE INDEX IF NOT EXISTS idx_aci_tsv ON agent_conversation_index USING GIN(transcript_tsv);
			CREATE INDEX IF NOT EXISTS idx_aci_embedding ON agent_conversation_index USING hnsw(summary_embedding vector_cosine_ops);
		`,
	},
	{
		Name: "020_board_revisions",
		SQL: `
			CREATE TABLE IF NOT EXISTS board_revisions (
				id          VARCHAR(36) PRIMARY KEY,
				parent_id   VARCHAR(36) DEFAULT '',
				seq         BIGSERIAL UNIQUE,
				status      VARCHAR(20) NOT NULL DEFAULT 'applied',
				author      VARCHAR(255) NOT NULL DEFAULT '',
				message     TEXT NOT NULL DEFAULT '',
				ops         JSONB NOT NULL DEFAULT '[]',
				created_at  BIGINT NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_board_revisions_status ON board_revisions(status);
			CREATE TABLE IF NOT EXISTS board_head (
				singleton   BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
				revision_id VARCHAR(36) NOT NULL REFERENCES board_revisions(id)
			);
		`,
	},
}

// runMigrations creates the tracking table and applies pending migrations.
func runMigrations(gdb *gorm.DB) error {
	ctx := context.Background()
	sqlDB, err := gdb.DB()
	if err != nil {
		return fmt.Errorf("agentdb: get sql.DB: %w", err)
	}

	_, err = sqlDB.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS agentdb_migrations (
			name VARCHAR(255) PRIMARY KEY,
			applied_at TIMESTAMPTZ DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("agentdb: create migration table: %w", err)
	}

	applied := map[string]bool{}
	rows, err := sqlDB.QueryContext(ctx, "SELECT name FROM agentdb_migrations")
	if err != nil {
		return fmt.Errorf("agentdb: query applied migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		applied[name] = true
	}

	for _, m := range agentMigrations {
		if applied[m.Name] {
			continue
		}
		if err := runOneMigration(sqlDB, m); err != nil {
			return fmt.Errorf("agentdb: migration %s failed: %w", m.Name, err)
		}
		log.Printf("[agentdb] applied migration %s", m.Name)
	}
	return nil
}

func runOneMigration(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(m.SQL); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO agentdb_migrations (name) VALUES ($1)", m.Name); err != nil {
		return err
	}
	return tx.Commit()
}
