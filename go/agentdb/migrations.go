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
		Name: "019_agent_sessions_mcp_servers",
		SQL:  `ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS mcp_servers JSONB NOT NULL DEFAULT '{}';`,
	},
	{
		Name: "020_project_settings",
		SQL: `
			CREATE TABLE IF NOT EXISTS project_settings (
				project VARCHAR(255) PRIMARY KEY,
				base_image TEXT NOT NULL DEFAULT '',
				system_prompt TEXT NOT NULL DEFAULT '',
				mcp_config JSONB NOT NULL DEFAULT '{}',
				attention_channel JSONB NOT NULL DEFAULT '{}',
				max_concurrent_jobs INT NOT NULL DEFAULT 4,
				daily_tokens_soft BIGINT NOT NULL DEFAULT 0,
				daily_tokens_hard BIGINT NOT NULL DEFAULT 0,
				briefing_max_bytes INT NOT NULL DEFAULT 2048,
				snapshot_ttl_days INT NOT NULL DEFAULT 30,
				updated_at BIGINT NOT NULL DEFAULT 0
			);
		`,
	},
	{
		Name: "021_workers",
		SQL: `
			CREATE TABLE IF NOT EXISTS workers (
				project VARCHAR(255) NOT NULL,
				name VARCHAR(255) NOT NULL,
				description TEXT NOT NULL DEFAULT '',
				system_prompt TEXT NOT NULL DEFAULT '',
				mcp_config JSONB NOT NULL DEFAULT '{}',
				image TEXT NOT NULL DEFAULT '',
				max_instances INT NOT NULL DEFAULT 1,
				briefing JSONB DEFAULT NULL,
				enabled BOOLEAN NOT NULL DEFAULT TRUE,
				created_at BIGINT NOT NULL DEFAULT 0,
				updated_at BIGINT NOT NULL DEFAULT 0,
				PRIMARY KEY (project, name)
			);
			CREATE INDEX IF NOT EXISTS idx_workers_project ON workers(project);
			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS worker TEXT NOT NULL DEFAULT '';
			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS composed_prompt TEXT NOT NULL DEFAULT '';
			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS lease_expires_at BIGINT NOT NULL DEFAULT 0;
			CREATE INDEX IF NOT EXISTS idx_agent_sessions_worker ON agent_sessions(worker);
		`,
	},
	{
		// Append-only labeled memory (spec §7.1). content_tsv is a stored
		// generated column (no trigger to keep in sync); content_embedding is
		// added only when pgvector is available, so a plain Postgres still
		// migrates cleanly and search degrades to keyword-only (§7.6.5).
		Name: "022_memories",
		SQL: `
			CREATE TABLE IF NOT EXISTS memories (
				id VARCHAR(36) PRIMARY KEY,
				project TEXT NOT NULL,
				labels JSONB NOT NULL DEFAULT '{}',
				content TEXT NOT NULL DEFAULT '',
				content_tsv TSVECTOR GENERATED ALWAYS AS (to_tsvector('english'::regconfig, COALESCE(content, ''))) STORED,
				created_by_worker TEXT NOT NULL DEFAULT '',
				created_by_session TEXT NOT NULL DEFAULT '',
				created_at BIGINT NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_memories_project_created ON memories(project, created_at DESC, id DESC);
			CREATE INDEX IF NOT EXISTS idx_memories_labels ON memories USING GIN(labels);
			CREATE INDEX IF NOT EXISTS idx_memories_tsv ON memories USING GIN(content_tsv);
			DO $$
			BEGIN
				IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector') THEN
					BEGIN
						CREATE EXTENSION IF NOT EXISTS vector;
						EXECUTE 'ALTER TABLE memories ADD COLUMN IF NOT EXISTS content_embedding vector(1536)';
						EXECUTE 'CREATE INDEX IF NOT EXISTS idx_memories_embedding ON memories USING hnsw (content_embedding vector_cosine_ops)';
					EXCEPTION WHEN OTHERS THEN
						RAISE NOTICE 'agentdb: pgvector setup skipped (%) — memory search degrades to keyword-only', SQLERRM;
					END;
				END IF;
			END $$;
		`,
	},
	{
		// The event spine (spec §8.1–§8.4): the append-only event log, the
		// subscriptions that route it, and one delivery row per
		// (event, subscription) attempt — the at-least-once idempotency guard
		// and the job-history spine the UI renders.
		//
		// Deliberately absent: a per-subscription `concurrency` column and a
		// `dropped` delivery status. Both were superseded 2026-07-25 by the
		// worker-level `max_instances` gate — deliveries for a worker at
		// capacity stay `pending`, so nothing ever produces `dropped`.
		Name: "023_events_subscriptions_deliveries",
		SQL: `
			CREATE TABLE IF NOT EXISTS project_events (
				id VARCHAR(36) PRIMARY KEY,
				project VARCHAR(255) NOT NULL,
				type VARCHAR(255) NOT NULL,
				text TEXT NOT NULL DEFAULT '',
				envelope JSONB NOT NULL DEFAULT '{}',
				occurred_at BIGINT NOT NULL DEFAULT 0,
				created_at BIGINT NOT NULL DEFAULT 0,
				delivered BOOLEAN NOT NULL DEFAULT FALSE
			);
			CREATE INDEX IF NOT EXISTS idx_project_events_project ON project_events(project);
			CREATE INDEX IF NOT EXISTS idx_project_events_type ON project_events(type);
			CREATE INDEX IF NOT EXISTS idx_project_events_undelivered ON project_events(delivered, occurred_at);

			CREATE TABLE IF NOT EXISTS subscriptions (
				id VARCHAR(36) PRIMARY KEY,
				project VARCHAR(255) NOT NULL,
				event_type VARCHAR(255) NOT NULL,
				filter JSONB NOT NULL DEFAULT '{}',
				worker VARCHAR(255) NOT NULL,
				max_firings_per_hour INT NOT NULL DEFAULT 0,
				enabled BOOLEAN NOT NULL DEFAULT TRUE,
				created_at BIGINT NOT NULL DEFAULT 0,
				updated_at BIGINT NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_subscriptions_project ON subscriptions(project);
			CREATE INDEX IF NOT EXISTS idx_subscriptions_enabled ON subscriptions(project, enabled);

			CREATE TABLE IF NOT EXISTS event_deliveries (
				id VARCHAR(36) PRIMARY KEY,
				project VARCHAR(255) NOT NULL,
				event_id VARCHAR(36) NOT NULL,
				subscription_id VARCHAR(36) NOT NULL,
				session_id VARCHAR(36) NOT NULL DEFAULT '',
				status VARCHAR(30) NOT NULL DEFAULT 'pending',
				started_at BIGINT NOT NULL DEFAULT 0,
				ended_at BIGINT NOT NULL DEFAULT 0,
				created_at BIGINT NOT NULL DEFAULT 0,
				updated_at BIGINT NOT NULL DEFAULT 0,
				CONSTRAINT event_deliveries_status_check CHECK (
					status IN ('pending','running','ok','failed','awaiting_human','rate_limited')
				)
			);
			CREATE UNIQUE INDEX IF NOT EXISTS idx_event_deliveries_pair
				ON event_deliveries(event_id, subscription_id);
			CREATE INDEX IF NOT EXISTS idx_event_deliveries_project ON event_deliveries(project);
			CREATE INDEX IF NOT EXISTS idx_event_deliveries_status ON event_deliveries(project, status);
		`,
	},
	{
		// Schedules (§8.6), their firing ledger, the shared dispatch gate's
		// columns, and the durable half of request_human_attention (§9).
		//
		// `schedules` is configuration and joins the config log (§15.3):
		// schedule_create / schedule_update / schedule_delete.
		//
		// `schedule_firings` is NOT configuration — it is the occurrence ledger
		// that makes firing idempotent. The UNIQUE (schedule_id, scheduled_for)
		// index is the whole mechanism: a crash/retry re-claims the same
		// occurrence and loses, so it cannot double-fire (§8.6). scheduled_for is
		// the LOCAL WALL-CLOCK minute, which is also the DST answer — see the
		// ScheduleFiring doc comment.
		//
		// event_deliveries gains `worker` and `schedule_id`. `worker` is
		// denormalised deliberately: the §8.4 step 7 per-worker gate has to count
		// and queue deliveries for a worker, and a schedule firing has no
		// subscription row to join through. The alternative — a synthetic
		// subscription per schedule — would put rows in the user's routing table
		// that no human created. `schedule_id` records which schedule a firing
		// came from; it is empty for event-matched deliveries. Schedule-fired
		// deliveries put the schedule id in `subscription_id` too, so the existing
		// UNIQUE (event_id, subscription_id) idempotency index keeps working
		// unchanged for both dispatch paths.
		//
		// agent_sessions gains `attention_requested` — the §9 stamp that §8.2
		// copies onto the worker.finished envelope; `attention_requests` carries
		// the optional expiry the sweep turns into human.attention.timeout.
		Name: "024_schedules_and_attention",
		SQL: `
			CREATE TABLE IF NOT EXISTS schedules (
				id VARCHAR(36) PRIMARY KEY,
				project VARCHAR(255) NOT NULL,
				worker VARCHAR(255) NOT NULL,
				cron VARCHAR(255) NOT NULL,
				input TEXT NOT NULL DEFAULT '',
				enabled BOOLEAN NOT NULL DEFAULT TRUE,
				created_at BIGINT NOT NULL DEFAULT 0,
				updated_at BIGINT NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_schedules_project ON schedules(project);
			CREATE INDEX IF NOT EXISTS idx_schedules_enabled ON schedules(enabled);

			CREATE TABLE IF NOT EXISTS schedule_firings (
				id VARCHAR(36) PRIMARY KEY,
				schedule_id VARCHAR(36) NOT NULL,
				scheduled_for VARCHAR(20) NOT NULL,
				project VARCHAR(255) NOT NULL,
				event_id VARCHAR(36) NOT NULL DEFAULT '',
				fired_at BIGINT NOT NULL DEFAULT 0
			);
			CREATE UNIQUE INDEX IF NOT EXISTS idx_schedule_firings_occurrence
				ON schedule_firings(schedule_id, scheduled_for);
			CREATE INDEX IF NOT EXISTS idx_schedule_firings_project ON schedule_firings(project);

			ALTER TABLE event_deliveries ADD COLUMN IF NOT EXISTS worker VARCHAR(255) NOT NULL DEFAULT '';
			ALTER TABLE event_deliveries ADD COLUMN IF NOT EXISTS schedule_id VARCHAR(36) NOT NULL DEFAULT '';
			CREATE INDEX IF NOT EXISTS idx_event_deliveries_worker
				ON event_deliveries(project, worker, status);

			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS attention_requested BOOLEAN NOT NULL DEFAULT FALSE;

			CREATE TABLE IF NOT EXISTS attention_requests (
				id VARCHAR(36) PRIMARY KEY,
				project VARCHAR(255) NOT NULL,
				session_id VARCHAR(36) NOT NULL,
				worker VARCHAR(255) NOT NULL DEFAULT '',
				message TEXT NOT NULL DEFAULT '',
				session_url TEXT NOT NULL DEFAULT '',
				channel VARCHAR(30) NOT NULL DEFAULT '',
				delivered BOOLEAN NOT NULL DEFAULT FALSE,
				expires_at BIGINT NOT NULL DEFAULT 0,
				created_at BIGINT NOT NULL DEFAULT 0,
				answered_at BIGINT NOT NULL DEFAULT 0,
				timed_out_at BIGINT NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_attention_requests_project ON attention_requests(project);
			CREATE INDEX IF NOT EXISTS idx_attention_requests_session ON attention_requests(session_id);
			CREATE INDEX IF NOT EXISTS idx_attention_requests_open
				ON attention_requests(expires_at)
				WHERE answered_at = 0 AND timed_out_at = 0;
		`,
	},
	{
		// Named, versioned, labeled images (§13) and the columns §14's skills
		// need (I3 builds the skill store and tools on top of them).
		//
		// The §13 catalogue is not a new table: it is the existing
		// agent_custom_images catalogue given an identity (§13.6). Its project
		// namespace is the existing `customer` column — one namespace column,
		// not two that can drift; J1 already treats it as the project when it
		// writes `image_create` config events.
		//
		// version 0 means "pre-§13 row", written by the legacy latest-wins
		// UpsertCustomImage path, so the unique index is PARTIAL: legacy rows
		// (which may repeat a name across visibility scopes) keep working, while
		// every catalogue row is uniquely (customer, name, version). That index
		// is also the concurrency guard for version allocation — two racing
		// burns of the same name collide on it and one retries, which is what
		// keeps versions gap-free.
		//
		// reaped_at is the §13.7 tombstone: the snapshot_ttl_days reaper (B4)
		// deletes bytes and stamps the row, so the catalogue never points at
		// bytes that are gone — resolution fails loudly instead.
		//
		// The agent_skills half is columns ONLY (I3 owns the store and the
		// tools). Both tables reuse their existing `source_session_id` column as
		// §13.2/§14's created_by_session provenance rather than growing a twin.
		Name: "025_image_versions_and_skill_columns",
		SQL: `
			ALTER TABLE agent_custom_images ADD COLUMN IF NOT EXISTS version INT NOT NULL DEFAULT 0;
			ALTER TABLE agent_custom_images ADD COLUMN IF NOT EXISTS labels JSONB NOT NULL DEFAULT '{}';
			ALTER TABLE agent_custom_images ADD COLUMN IF NOT EXISTS created_by_worker TEXT NOT NULL DEFAULT '';
			ALTER TABLE agent_custom_images ADD COLUMN IF NOT EXISTS reaped_at BIGINT NOT NULL DEFAULT 0;
			CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_custom_images_version
				ON agent_custom_images(customer, name, version) WHERE version > 0;
			CREATE INDEX IF NOT EXISTS idx_agent_custom_images_catalogue
				ON agent_custom_images(customer, created_at DESC, version DESC) WHERE version > 0;
			CREATE INDEX IF NOT EXISTS idx_agent_custom_images_labels
				ON agent_custom_images USING GIN(labels);

			ALTER TABLE agent_skills ADD COLUMN IF NOT EXISTS labels JSONB NOT NULL DEFAULT '{}';
			ALTER TABLE agent_skills ADD COLUMN IF NOT EXISTS markdown TEXT NOT NULL DEFAULT '';
			ALTER TABLE agent_skills ADD COLUMN IF NOT EXISTS install_sh TEXT NOT NULL DEFAULT '';
			ALTER TABLE agent_skills ADD COLUMN IF NOT EXISTS created_by_worker TEXT NOT NULL DEFAULT '';
			CREATE INDEX IF NOT EXISTS idx_agent_skills_labels ON agent_skills USING GIN(labels);
		`,
	},
	{
		// The config log (§15) — append-only record of every configuration
		// mutation. payload is the FULL new state, never a diff. Nothing on the
		// hot path reads this table; the ordinary tables stay the projections.
		Name: "026_config_events",
		SQL: `
			CREATE TABLE IF NOT EXISTS config_events (
				id VARCHAR(36) PRIMARY KEY,
				project TEXT NOT NULL DEFAULT '',
				actor_worker TEXT NOT NULL DEFAULT '',
				actor_session TEXT NOT NULL DEFAULT '',
				action TEXT NOT NULL,
				payload JSONB NOT NULL DEFAULT '{}',
				rationale TEXT NOT NULL DEFAULT '',
				created_at BIGINT NOT NULL DEFAULT 0
			);
			CREATE INDEX IF NOT EXISTS idx_config_events_project ON config_events(project);
			CREATE INDEX IF NOT EXISTS idx_config_events_project_created ON config_events(project, created_at DESC, id DESC);
			CREATE INDEX IF NOT EXISTS idx_config_events_project_action ON config_events(project, action);
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
