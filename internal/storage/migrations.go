package storage

import (
	"database/sql"
	"fmt"
)

// migrations are applied in order. Each migration runs in its own transaction
// and is recorded in the schema_migrations table. To add a new migration, append
// to the slice — never edit an existing one.
var migrations = []string{
	// 0001 — initial schema
	`
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS connections (
		id           TEXT PRIMARY KEY,
		name         TEXT NOT NULL,
		type         TEXT NOT NULL CHECK(type IN ('ibm','rabbitmq','kafka')),
		queue_manager TEXT NOT NULL DEFAULT '',
		conn_name    TEXT NOT NULL DEFAULT '',
		channel      TEXT NOT NULL DEFAULT '',
		username     TEXT NOT NULL DEFAULT '',
		password     TEXT NOT NULL DEFAULT '',
		queue_name   TEXT NOT NULL DEFAULT '',
		url          TEXT NOT NULL DEFAULT '',
		brokers      TEXT NOT NULL DEFAULT '',
		topic        TEXT NOT NULL DEFAULT '',
		created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_connections_name ON connections(name);

	CREATE TABLE IF NOT EXISTS pipelines (
		id              TEXT PRIMARY KEY,
		name            TEXT NOT NULL,
		source_id       TEXT NOT NULL REFERENCES connections(id) ON DELETE RESTRICT,
		destination_id  TEXT NOT NULL REFERENCES connections(id) ON DELETE RESTRICT,
		output_format   TEXT NOT NULL DEFAULT 'same' CHECK(output_format IN ('same','json','xml')),
		schema_id       TEXT REFERENCES schemas(id) ON DELETE SET NULL,
		filter_paths    TEXT NOT NULL DEFAULT '[]', -- JSON array of strings
		enabled         INTEGER NOT NULL DEFAULT 1,
		created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_pipelines_enabled ON pipelines(enabled);

	CREATE TABLE IF NOT EXISTS stages (
		id           TEXT PRIMARY KEY,
		pipeline_id  TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
		stage_order  INTEGER NOT NULL,
		stage_type   TEXT NOT NULL CHECK(stage_type IN ('filter','transform','translate','route','script','validate')),
		stage_config TEXT NOT NULL DEFAULT '{}', -- JSON
		enabled      INTEGER NOT NULL DEFAULT 1
	);
	CREATE INDEX IF NOT EXISTS idx_stages_pipeline ON stages(pipeline_id, stage_order);

	CREATE TABLE IF NOT EXISTS transforms (
		id             TEXT PRIMARY KEY,
		pipeline_id    TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
		transform_type TEXT NOT NULL CHECK(transform_type IN ('rename','mask','move','set','delete')),
		source_path    TEXT NOT NULL DEFAULT '',
		target_path    TEXT NOT NULL DEFAULT '',
		mask_pattern   TEXT NOT NULL DEFAULT '',
		mask_replace   TEXT NOT NULL DEFAULT '',
		set_value      TEXT NOT NULL DEFAULT '',
		ord            INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_transforms_pipeline ON transforms(pipeline_id, ord);

	CREATE TABLE IF NOT EXISTS routing_rules (
		id                 TEXT PRIMARY KEY,
		pipeline_id        TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
		condition_path     TEXT NOT NULL DEFAULT '',
		condition_operator TEXT NOT NULL CHECK(condition_operator IN ('eq','neq','contains','regex','gt','lt','exists')),
		condition_value    TEXT NOT NULL DEFAULT '',
		destination_id     TEXT NOT NULL REFERENCES connections(id) ON DELETE RESTRICT,
		priority           INTEGER NOT NULL DEFAULT 100,
		enabled            INTEGER NOT NULL DEFAULT 1
	);
	CREATE INDEX IF NOT EXISTS idx_routing_pipeline ON routing_rules(pipeline_id, priority);

	CREATE TABLE IF NOT EXISTS scripts (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		body        TEXT NOT NULL DEFAULT '',
		enabled     INTEGER NOT NULL DEFAULT 1,
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS schemas (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		schema_type TEXT NOT NULL CHECK(schema_type IN ('json_schema','xsd')),
		content     TEXT NOT NULL DEFAULT '',
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS dlq (
		id              TEXT PRIMARY KEY,
		pipeline_id     TEXT REFERENCES pipelines(id) ON DELETE SET NULL,
		source_queue    TEXT NOT NULL DEFAULT '',
		original_msg    BLOB NOT NULL,
		error_reason    TEXT NOT NULL DEFAULT '',
		retry_count     INTEGER NOT NULL DEFAULT 0,
		last_retry_at   DATETIME,
		created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_dlq_pipeline ON dlq(pipeline_id);
	CREATE INDEX IF NOT EXISTS idx_dlq_created ON dlq(created_at DESC);
	`,
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for i, m := range migrations {
		version := i + 1
		var existing int
		err := db.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, version).Scan(&existing)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if existing > 0 {
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.Exec(m); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}
