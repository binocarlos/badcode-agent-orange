package agentdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"hash/fnv"
	"log"
	"time"

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
	{
		// J2 + B4. Two things the fold (§15.6) and the snapshot reaper (§5, §13.7)
		// need.
		//
		//  1. config_events.seq — a monotonic per-project sequence. §15.6 folds in
		//     `created_at`/`id` order, but created_at is milliseconds and id is a
		//     RANDOM uuid, so two writes to the same key inside one millisecond
		//     fold in an arbitrary order and the fold can disagree with the
		//     projection it exists to reproduce. seq is allocated inside the
		//     config-event transaction, so seq order IS commit order. The unique
		//     index — not the read of MAX(seq) — is what makes allocation correct
		//     under concurrency; the loser of a race re-reads and retries, exactly
		//     as image-version allocation does. Existing rows are backfilled
		//     deterministically by (created_at, id): the best order available for
		//     history written before the column existed.
		//
		//  2. agent_custom_images.expires_at / last_resumed_at — the §5 snapshot
		//     metadata tuple {source session, created_at, expiry, last_resumed_at}.
		//     "source session" is the pre-existing source_session_id column and
		//     created_at already exists, so only these two are new. expires_at is
		//     stamped at burn time from the project's snapshot_ttl_days and is the
		//     promise the reaper honours; 0 means never — both "the project set 0"
		//     and pre-B4 rows, which were burned under no TTL promise at all.
		Name: "027_config_event_seq_and_snapshot_ttl",
		SQL: `
			ALTER TABLE config_events ADD COLUMN IF NOT EXISTS seq BIGINT NOT NULL DEFAULT 0;
			UPDATE config_events ce SET seq = sub.rn
				FROM (
					SELECT id, row_number() OVER (PARTITION BY project ORDER BY created_at, id) AS rn
					FROM config_events
				) sub
				WHERE ce.id = sub.id AND ce.seq = 0;
			CREATE UNIQUE INDEX IF NOT EXISTS idx_config_events_project_seq ON config_events(project, seq);

			ALTER TABLE agent_custom_images ADD COLUMN IF NOT EXISTS expires_at BIGINT NOT NULL DEFAULT 0;
			ALTER TABLE agent_custom_images ADD COLUMN IF NOT EXISTS last_resumed_at BIGINT NOT NULL DEFAULT 0;
			CREATE INDEX IF NOT EXISTS idx_agent_custom_images_expiry
				ON agent_custom_images(customer, expires_at) WHERE version > 0 AND reaped_at = 0;
		`,
	},
	{
		// I3. Migration 025 gave agent_skills its §14 columns; this adds the one
		// column those columns turned out to need, and the catalogue index.
		//
		// `revision` is the append ordinal per (project, name). §14.1 gives a
		// skill no version and this does not add one — nothing resolves by
		// revision and there is no `name:revision` reference form. It exists
		// because "newest wins" needed a deterministic meaning: created_at on
		// this table is SECONDS and id is a random uuid, so two teachings of one
		// skill inside a second would otherwise order by coin toss and
		// `skill_get` could hand back the superseded document.
		//
		// The unique index — not the read of MAX(revision) — is what makes
		// allocation correct under concurrency; a racing writer loses the insert
		// and retries, exactly as image-version allocation does. Both indexes
		// are partial on the §14 discriminator (markdown <> ''), so the legacy
		// host-built population neither collides with them nor bloats them.
		Name: "028_skill_revisions",
		SQL: `
			ALTER TABLE agent_skills ADD COLUMN IF NOT EXISTS revision INT NOT NULL DEFAULT 0;
			CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_skills_revision
				ON agent_skills(customer, name, revision) WHERE markdown <> '';
			CREATE INDEX IF NOT EXISTS idx_agent_skills_catalogue
				ON agent_skills(customer, created_at DESC, name) WHERE markdown <> '';
		`,
	},
	{
		// The router's two hot sweeps (E3). No new columns — everything §8.4
		// needs already exists (migration 021 added `lease_expires_at`, 023/024
		// the delivery tuple) — only the indexes those sweeps run every poll:
		//
		//  1. expired-lease sessions. Partial on `lease_expires_at > 0` because
		//     the overwhelming majority of session rows hold no lease at all,
		//     and the reaper only ever asks about the ones that do.
		//  2. queued deliveries per project, which is both the FIFO drain
		//     (§8.4 step 7) and the running-job counts the capacity gates read.
		Name: "029_router_sweeps",
		SQL: `
			CREATE INDEX IF NOT EXISTS idx_agent_sessions_lease
				ON agent_sessions(lease_expires_at) WHERE lease_expires_at > 0;
			CREATE INDEX IF NOT EXISTS idx_event_deliveries_project_status
				ON event_deliveries(project, status, created_at);
		`,
	},
	{
		// J3. The `config.changed` watermark (§15.4, §15.8).
		//
		// Emission happens AFTER the config-event transaction commits, never
		// inside it, and the spec asks for at-least-once: "a crash between
		// commit and emit is repaired by a retry rather than by a lost event".
		// `emitted_at` is what makes the repair queue a cheap indexed read
		// instead of a scan that re-derives emission state per row.
		//
		// It is the only mutable column on config_events, and it records
		// nothing anybody decided — the same kind of runtime watermark as
		// project_events.delivered. The append-only invariant of §15.1 is about
		// the RECORD, which is never rewritten and never deleted.
		//
		// Backfill: rows written before this landed are stamped with their own
		// created_at rather than 0. They pre-date the emitter entirely, so
		// leaving them unstamped would make the first sweep after deploy
		// announce the project's whole history as if it had just happened.
		Name: "030_config_event_emitted",
		SQL: `
			ALTER TABLE config_events ADD COLUMN IF NOT EXISTS emitted_at BIGINT NOT NULL DEFAULT 0;
			UPDATE config_events SET emitted_at = created_at WHERE emitted_at = 0;
			CREATE INDEX IF NOT EXISTS idx_config_events_unemitted
				ON config_events(created_at) WHERE emitted_at = 0;
		`,
	},
	{
		// The provision-failure streak (§8.6). 53 abandoned `* * * * *` rows,
		// each failing to provision every minute, between them held every host
		// port and made the whole stack unable to start anything — for as long
		// as it took a human to notice and delete the rows.
		//
		// §8.6 already disables a schedule whose worker is gone. A schedule that
		// can never provision is the same class of problem, so it gets the same
		// answer, with a counter to make it bounded rather than clever.
		//
		// Both columns are RUNTIME STATE on a configuration row, like
		// project_events.delivered: they record an observation, not a decision.
		// The decision they eventually cause — the disable — goes through
		// DisableSchedule and lands in the config log with its rationale.
		//
		// DEFAULT here rather than a gorm `default:` tag, per the store
		// convention: GORM substitutes a declared default for a zero value on
		// write, which would make a reset-to-0 unwritable.
		Name: "031_schedule_provision_failures",
		SQL: `
			ALTER TABLE schedules ADD COLUMN IF NOT EXISTS provision_failures INT NOT NULL DEFAULT 0;
			ALTER TABLE schedules ADD COLUMN IF NOT EXISTS last_provision_error TEXT NOT NULL DEFAULT '';
		`,
	},
	{
		// Why a session failed to start. agentd provisions in a background
		// goroutine and used to keep only `status = "error"` from a failure;
		// the reason — including the §13 pointer diagnostic that names the
		// setting, its value, the project and which interpretation the string
		// was given — was discarded at the `if err != nil` and never logged.
		// The caller's next message then took the no-instance-and-no-snapshot
		// path and was told the session was LOST and should be re-created.
		// Three engineers misdiagnosed that same message in one day.
		//
		// A column rather than a key in `metadata`, for the same reason
		// migration 031 gave schedules `last_provision_error` its own column:
		// `metadata` is a host-writable grab-bag that stores replace wholesale
		// (Save writes the whole jsonb), so a reserved key there is one host
		// write away from vanishing, and cannot be queried or indexed. This is
		// runtime state on a runtime row — an observation, not a decision — so
		// it writes no config event.
		//
		// DEFAULT here rather than a gorm `default:` tag, per the store
		// convention: GORM substitutes a declared default for a zero value on
		// write, which would make the clear-on-success unwritable — and a
		// reason that outlives its cause is worse than no reason at all.
		Name: "032_session_create_error",
		SQL: `
			ALTER TABLE agent_sessions ADD COLUMN IF NOT EXISTS create_error TEXT NOT NULL DEFAULT '';
		`,
	},
	{
		// Artifact metadata becomes durable (extension/dbartifacts). The
		// artifacts.Artifact interface type carries a free-form Meta map — today
		// only Meta["dirDigest"], the content digest of a directory artifact's
		// entries — and this table had nowhere to put it, so a durable index
		// would have silently dropped it on every dir capture.
		//
		// A column, not a key in some other jsonb blob, because it is the only
		// field of the portable type without a home here; DEFAULT lives in this
		// SQL rather than a gorm `default:` tag per the store convention.
		//
		// The (session_id, file_path) index backs the dedup key the
		// ArtifactStore contract is defined on: every Save is a lookup on that
		// pair. Deliberately NOT unique — the table predates this change and may
		// already hold duplicate pairs written by the legacy CreateArtifact
		// path, so a unique index could fail the migration at boot on a live
		// database. Save still de-dups: it reads the pair inside its
		// transaction. See docs/06-artifacts.md.
		Name: "033_agent_artifacts_meta",
		SQL: `
			ALTER TABLE agent_artifacts ADD COLUMN IF NOT EXISTS meta JSONB NOT NULL DEFAULT '{}';
			CREATE INDEX IF NOT EXISTS idx_agent_artifacts_session_path ON agent_artifacts(session_id, file_path);
			CREATE INDEX IF NOT EXISTS idx_agent_artifacts_customer ON agent_artifacts(customer);
		`,
	},
	{
		// Frozen workers (F1, docs/product/10-topology-library.md §3): a frozen
		// worker's configuration cannot be changed by other workers — the core
		// MCP server refuses worker_update / worker_prompt_write against it —
		// only by humans through the JWT-guarded HTTP API. The causal-isolation
		// primitive for measurement instruments.
		//
		// DEFAULT here rather than a gorm `default:` tag, per the store
		// convention (see Worker.Enabled): GORM omits zero-valued fields that
		// declare a default, which would make `frozen: false` unwritable — an
		// unfreeze that silently persisted as frozen would lock a worker away
		// from the very humans the flag exists to reserve it for.
		Name: "034_workers_frozen",
		SQL: `
			ALTER TABLE workers ADD COLUMN IF NOT EXISTS frozen BOOLEAN NOT NULL DEFAULT FALSE;
		`,
	},
}

// migrationLockKey is the Postgres advisory-lock key that serialises migration
// application. Every process that migrates this schema must compute the same
// number, so it is derived deterministically from a fixed string rather than
// picked by hand — and TestMigrationLockKeyIsStable pins the value, because a
// key that silently changed between two agentd versions would mean two booting
// replicas no longer exclude each other and the race is back.
//
// Masked to 63 bits so the key is positive: it keeps the halves that appear in
// pg_locks (classid = key>>32, objid = key) straightforward to reason about.
var migrationLockKey = func() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("agentdb:migrations"))
	return int64(h.Sum64() & 0x7fffffffffffffff)
}()

// migrationLockWait bounds how long a booting process will wait for a peer that
// is mid-migration. Generous, because the wait is legitimate — the peer is
// applying DDL to the same database — but finite, because a boot that hangs
// forever with no output is harder to diagnose than one that fails saying why.
const migrationLockWait = 5 * time.Minute

// runMigrations creates the tracking table and applies pending migrations.
func runMigrations(gdb *gorm.DB) error { return applyMigrations(gdb, agentMigrations) }

// applyMigrations is runMigrations with the list injectable, so tests can drive
// it with a deliberately broken migration.
//
// # Why the whole read-and-apply is under a lock
//
// The original shape was: create the tracking table, SELECT the applied set,
// then apply whatever is missing. With no lock anywhere, two processes starting
// together both read the same set, both conclude the same migration is pending,
// and both run it. The loser dies on
// `duplicate key value violates unique constraint "agentdb_migrations_pkey"`
// and Open fails. That is not theoretical: `go test ./agentdb/... ./cmd/agentd/...`
// runs one binary per package in parallel against one database, and it bit us
// twice in a single day (migrations 032 and 033), both times on a new
// migration's very first application. In production the same race is two agentd
// replicas booting together, one of which crashes on start.
//
// `CREATE TABLE IF NOT EXISTS` is inside the locked region too, not before it.
// IF NOT EXISTS is not a concurrency primitive: two sessions that both find the
// table absent both proceed to create it, and the loser fails on
// `pg_type_typname_nsp_index`. That was the *first* error the reproduction
// produced, before it ever reached the pkey collision.
//
// # Why a session lock and not pg_advisory_xact_lock
//
// pg_advisory_xact_lock releases at the end of its transaction, so covering the
// read-and-apply with one would mean wrapping every migration in a single
// transaction. That changes failure semantics for the worse: today a failure in
// migration 30 leaves 1–29 committed and applied, and the next boot resumes
// from there; under one big transaction the failure would roll all of them back
// and every boot would redo the lot. So: a session-level lock, held across the
// whole loop, with each migration keeping its own transaction.
//
// # Why the loser applies nothing rather than failing
//
// pg_advisory_lock blocks rather than erroring, and the applied set is read
// *after* acquisition. The loser therefore wakes to the winner's committed
// state, finds nothing pending, and boots. That is exactly what a starting
// replica wants — succeed, having done no work — and it is why moving the SELECT
// inside the lock matters as much as taking the lock at all. Serialising only
// the INSERT would leave the loser re-running the migration body.
//
// # Why everything runs on one pinned connection
//
// A session-level advisory lock belongs to the backend session that took it, so
// it has to be taken on a specific *sql.Conn rather than on the pool — ask the
// pool to unlock and it may hand back a different connection, leaving the lock
// held forever. Having pinned one connection for the lock, the migrations run on
// that same connection rather than borrowing more from the pool: a pool capped
// at one connection would otherwise deadlock against its own lock holder, which
// is a worse boot failure than the race. One connection, one session, one lock.
func applyMigrations(gdb *gorm.DB, migrations []migration) error {
	ctx := context.Background()
	sqlDB, err := gdb.DB()
	if err != nil {
		return fmt.Errorf("agentdb: get sql.DB: %w", err)
	}

	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("agentdb: migration connection: %w", err)
	}
	// Registered before the unlock below, so it runs after it: release the
	// lock, then hand the connection back.
	defer conn.Close()

	if usesAdvisoryLocks(gdb) {
		if err := lockMigrations(ctx, conn); err != nil {
			return err
		}
		// Runs on every exit including a migration that errors mid-way and a
		// panic out of the loop. A leaked session-level advisory lock would
		// wedge every subsequent boot on this database — a permanent outage
		// traded for an intermittent race, which is no trade at all.
		defer unlockMigrations(conn)
	}

	_, err = conn.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS agentdb_migrations (
			name VARCHAR(255) PRIMARY KEY,
			applied_at TIMESTAMPTZ DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("agentdb: create migration table: %w", err)
	}

	applied := map[string]bool{}
	rows, err := conn.QueryContext(ctx, "SELECT name FROM agentdb_migrations")
	if err != nil {
		return fmt.Errorf("agentdb: query applied migrations: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		applied[name] = true
	}
	err = rows.Err()
	rows.Close() // explicit: the pinned connection is unusable until it closes
	if err != nil {
		return fmt.Errorf("agentdb: read applied migrations: %w", err)
	}

	for _, m := range migrations {
		if applied[m.Name] {
			continue
		}
		if err := runOneMigration(ctx, conn, m); err != nil {
			return fmt.Errorf("agentdb: migration %s failed: %w", m.Name, err)
		}
		log.Printf("[agentdb] applied migration %s", m.Name)
	}
	return nil
}

// usesAdvisoryLocks is the dialect gate: only Postgres has advisory locks.
//
// sqlite has no equivalent and needs none. The sqlite stores in this repo are
// per-test temp files opened by a single process — there is no second writer to
// exclude — and sqlite serialises the writers it does have at the file level.
// Reaching for pg_advisory_lock there would fail on a function sqlite has never
// heard of, and breaking every sqlite-backed test would be a worse outcome than
// the race this is fixing.
//
// Today the gate is purely defensive: Open() constructs a Postgres dialector and
// is the only caller of runMigrations, and the tracking table's own
// `TIMESTAMPTZ DEFAULT NOW()` is not sqlite-parseable anyway. It is here so that
// if a sqlite-backed caller ever does appear, it meets the old unlocked path
// rather than an unknown-function error — and so the reason is written down.
//
// gorm.DB embeds *gorm.Config, so Dialector is a promoted field and reading it
// off a zero-value DB panics — hence the Config check before the Dialector one.
func usesAdvisoryLocks(gdb *gorm.DB) bool {
	if gdb == nil || gdb.Config == nil || gdb.Dialector == nil {
		return false
	}
	return gdb.Dialector.Name() == "postgres"
}

// lockMigrations takes the exclusive advisory lock on the given pinned
// connection, blocking (up to migrationLockWait) if a peer holds it.
func lockMigrations(ctx context.Context, conn *sql.Conn) error {
	// Try first so the common case (nobody else booting) costs one round trip
	// and says nothing, and the uncommon case explains the pause in the log
	// instead of looking like a hang.
	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", migrationLockKey).Scan(&got); err != nil {
		return fmt.Errorf("agentdb: acquire migration lock: %w", err)
	}
	if got {
		return nil
	}

	log.Printf("[agentdb] another process is migrating this database; waiting up to %s", migrationLockWait)
	waitCtx, cancel := context.WithTimeout(ctx, migrationLockWait)
	defer cancel()
	if _, err := conn.ExecContext(waitCtx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("agentdb: wait for migration lock (a peer has held it for over %s): %w", migrationLockWait, err)
	}
	log.Printf("[agentdb] migration lock acquired")
	return nil
}

// unlockMigrations releases the advisory lock, and makes sure the connection
// cannot go back into the pool still holding it.
func unlockMigrations(conn *sql.Conn) {
	// context.Background(), never a caller's: a cancelled context must not be
	// able to skip the release.
	if _, err := conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
		// The unlock did not land. Either the backend is already gone — in
		// which case Postgres has released every lock it held — or the session
		// is in a state we cannot reason about. Returning driver.ErrBadConn
		// from Raw marks the *sql.Conn bad so Close destroys the backend
		// session instead of returning it to the pool still locked. Ending the
		// session releases the lock either way; that is the guarantee this
		// falls back on.
		log.Printf("[agentdb] releasing migration lock: %v (discarding the connection)", err)
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
}

func runOneMigration(ctx context.Context, conn *sql.Conn, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO agentdb_migrations (name) VALUES ($1)", m.Name); err != nil {
		return err
	}
	return tx.Commit()
}
