package storage

import (
	"context"
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
	// 0002 — audit log
	`
	CREATE TABLE IF NOT EXISTS audit_log (
		id          TEXT PRIMARY KEY,
		at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		actor       TEXT NOT NULL DEFAULT '',         -- preferred_username from JWT
		actor_sub   TEXT NOT NULL DEFAULT '',         -- JWT sub
		action      TEXT NOT NULL,                    -- HTTP verb
		resource    TEXT NOT NULL DEFAULT '',         -- /api/v1/connections/abc
		status      INTEGER NOT NULL DEFAULT 0,       -- response status code
		request_id  TEXT NOT NULL DEFAULT '',         -- X-Request-Id for cross-ref to logs
		remote_ip   TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX IF NOT EXISTS idx_audit_at      ON audit_log(at DESC);
	CREATE INDEX IF NOT EXISTS idx_audit_actor   ON audit_log(actor);
	CREATE INDEX IF NOT EXISTS idx_audit_resource ON audit_log(resource);
	`,
	// 0003 — multi-tenancy. Every domain row gets a tenant_id; existing
	// rows are backfilled to the seeded "default" tenant so a single-
	// tenant deploy keeps working without operator intervention.
	`
	CREATE TABLE IF NOT EXISTS tenants (
		id          TEXT PRIMARY KEY,
		slug        TEXT NOT NULL UNIQUE,           -- URL-safe handle
		name        TEXT NOT NULL,
		status      TEXT NOT NULL DEFAULT 'active'
		            CHECK(status IN ('active','suspended','disabled')),
		max_pipelines       INTEGER NOT NULL DEFAULT 0,  -- 0 = unlimited
		max_msgs_per_minute INTEGER NOT NULL DEFAULT 0,  -- 0 = unlimited
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_tenants_slug ON tenants(slug);

	-- Seed the "default" tenant up-front so the backfill below has
	-- something to point at. Operators in a single-tenant deploy never
	-- need to know this exists.
	INSERT OR IGNORE INTO tenants (id, slug, name)
	VALUES ('00000000-0000-0000-0000-000000000000', 'default', 'Default tenant');

	-- Add tenant_id to every domain table. SQLite ALTER TABLE only
	-- supports ADD COLUMN, which is what we need — every existing row
	-- gets the seeded default tenant.
	-- SQLite refuses ADD COLUMN that combines NOT NULL + non-NULL DEFAULT
	-- + REFERENCES (Cannot add a REFERENCES column with non-NULL default
	-- value). The reference is documentary anyway — SQLite only enforces
	-- FKs declared at CREATE TABLE time on existing rows when foreign_keys
	-- pragma is on. Drop the inline FK clause here; the index below plus
	-- application-level checks are the actual guarantee.
	ALTER TABLE connections   ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';
	ALTER TABLE pipelines     ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';
	ALTER TABLE stages        ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';
	ALTER TABLE transforms    ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';
	ALTER TABLE routing_rules ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';
	ALTER TABLE scripts       ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';
	ALTER TABLE schemas       ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';
	ALTER TABLE dlq           ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';
	ALTER TABLE audit_log     ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000';

	-- Tenant-scoped lookups dominate every list query.
	CREATE INDEX IF NOT EXISTS idx_connections_tenant   ON connections(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_pipelines_tenant     ON pipelines(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_stages_tenant        ON stages(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_transforms_tenant    ON transforms(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_routing_rules_tenant ON routing_rules(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_scripts_tenant       ON scripts(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_schemas_tenant       ON schemas(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_dlq_tenant           ON dlq(tenant_id, created_at DESC);
	CREATE INDEX IF NOT EXISTS idx_audit_tenant         ON audit_log(tenant_id, at DESC);
	`,
	// 0004 — tenant memberships. Maps a SimpleAuth user (by JWT `sub`) to
	// a tenant with a role. Authorization decisions read this table; the
	// JWT itself does not carry tenant/role claims, so we can revoke access
	// without re-issuing tokens.
	//
	// Roles, weakest → strongest:
	//   - viewer    : read-only on all resources
	//   - operator  : viewer + enable/disable pipelines + retry/delete DLQ
	//   - admin     : operator + CRUD connections/pipelines/scripts/schemas
	//   - owner     : admin + member management + tenant settings
	//
	// Auto-bootstrap: the existing SimpleAuth admin user is granted owner
	// of the default tenant so single-tenant deploys keep working without
	// operator intervention. The grant is keyed by username (admin) rather
	// than a literal sub — replaced on first login by the real sub via
	// the membership auto-upgrade path in internal/auth.
	`
	CREATE TABLE IF NOT EXISTS tenant_memberships (
		tenant_id  TEXT NOT NULL,
		user_sub   TEXT NOT NULL,                     -- JWT sub OR username for the bootstrap entry
		username   TEXT NOT NULL DEFAULT '',          -- display only; auto-populated on first login
		role       TEXT NOT NULL CHECK(role IN ('viewer','operator','admin','owner')),
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (tenant_id, user_sub)
	);
	CREATE INDEX IF NOT EXISTS idx_memberships_user ON tenant_memberships(user_sub);

	-- Bootstrap owner row keyed by the standard SimpleAuth bootstrap user.
	-- Resolved to the real sub on first login.
	INSERT OR IGNORE INTO tenant_memberships (tenant_id, user_sub, username, role)
	VALUES ('00000000-0000-0000-0000-000000000000', 'bootstrap:admin', 'admin', 'owner');
	`,
	// 0005 — tamper-evident audit log. Each row carries a SHA-256 hash of
	// (prev_hash || canonical(row)). The verifier walks the chain in
	// insertion order and reports the first row where the recomputed hash
	// disagrees — a single-row mutation is therefore detectable in O(n).
	//
	// `prev_hash` of the very first row in a tenant chain is the empty
	// string. We chain per tenant so tenants stay logically independent
	// and archival prunes one tenant without breaking another's chain.
	//
	// audit_log_diffs (separate table): optional before/after JSON for
	// PUT mutations. Joined on audit_id so list views aren't bloated.
	`
	ALTER TABLE audit_log ADD COLUMN hash      TEXT NOT NULL DEFAULT '';
	ALTER TABLE audit_log ADD COLUMN prev_hash TEXT NOT NULL DEFAULT '';

	CREATE INDEX IF NOT EXISTS idx_audit_chain ON audit_log(tenant_id, at ASC, id ASC);

	CREATE TABLE IF NOT EXISTS audit_log_diffs (
		audit_id  TEXT PRIMARY KEY REFERENCES audit_log(id) ON DELETE CASCADE,
		before    TEXT NOT NULL DEFAULT '',
		after     TEXT NOT NULL DEFAULT ''
	);
	`,
	// 0006 — broker TLS / mTLS. Each connection optionally carries paths
	// to PEM files for server-cert verification (tls_ca_file) and client
	// mTLS auth (tls_cert_file + tls_key_file). tls_insecure_skip_verify
	// is a dev-only escape hatch; production deploys leave it 0.
	//
	// Files-on-disk rather than inline PEMs keeps the SQLite row small
	// and lets ops rotate certs without rewriting every connection row.
	// The connection.Open path reads the files at dial time, so an
	// updated cert takes effect on the next reconnect.
	`
	ALTER TABLE connections ADD COLUMN tls_ca_file              TEXT NOT NULL DEFAULT '';
	ALTER TABLE connections ADD COLUMN tls_cert_file            TEXT NOT NULL DEFAULT '';
	ALTER TABLE connections ADD COLUMN tls_key_file             TEXT NOT NULL DEFAULT '';
	ALTER TABLE connections ADD COLUMN tls_insecure_skip_verify INTEGER NOT NULL DEFAULT 0;
	`,
	// 0007 — API tokens for headless / automation clients. Stored as
	// sha256 hashes (never the secret itself). The "prefix" column is
	// the first 8 chars of the user-visible secret so the UI can list
	// rows like "mqct_abc12345…" without needing the full token (which
	// is shown exactly once at creation time and never again).
	//
	// role is the token's scope — must be ≤ the creating user's role at
	// creation time, enforced by the handler. expires_at is nullable for
	// non-expiring tokens; revoked_at is nullable for active tokens. A
	// token is valid iff revoked_at IS NULL AND (expires_at IS NULL OR
	// expires_at > NOW).
	`
	CREATE TABLE IF NOT EXISTS api_tokens (
		id           TEXT PRIMARY KEY,
		tenant_id    TEXT NOT NULL,
		user_sub     TEXT NOT NULL,         -- who created the token (audit trail)
		name         TEXT NOT NULL,         -- human label
		prefix       TEXT NOT NULL,         -- first 8 chars of the secret, for UI display
		token_hash   TEXT NOT NULL UNIQUE,  -- sha256 hex of the secret
		role         TEXT NOT NULL CHECK(role IN ('viewer','operator','admin','owner')),
		created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at   DATETIME NULL,
		last_used_at DATETIME NULL,
		revoked_at   DATETIME NULL
	);
	CREATE INDEX IF NOT EXISTS idx_api_tokens_tenant ON api_tokens(tenant_id);
	CREATE INDEX IF NOT EXISTS idx_api_tokens_hash   ON api_tokens(token_hash);
	`,
	// 0008 — webhooks. Operators register outbound HTTP endpoints to
	// receive event notifications when certain things happen in the
	// system (pipeline lifecycle, DLQ pushes). The body is signed with
	// HMAC-SHA256 against the per-webhook secret so receivers can
	// verify authenticity without TLS-based identity.
	//
	// `events` is a comma-separated list of event-type filters
	// ("pipeline.started,pipeline.error,dlq.pushed") or "*" for all.
	// `last_*` columns capture the last delivery attempt for the UI's
	// status display; they're best-effort and never block the request
	// that triggered the event.
	`
	CREATE TABLE IF NOT EXISTS webhooks (
		id              TEXT PRIMARY KEY,
		tenant_id       TEXT NOT NULL,
		name            TEXT NOT NULL,
		url             TEXT NOT NULL,
		secret          TEXT NOT NULL,
		events          TEXT NOT NULL DEFAULT '*',
		enabled         INTEGER NOT NULL DEFAULT 1,
		last_status     INTEGER NOT NULL DEFAULT 0,
		last_error      TEXT NOT NULL DEFAULT '',
		last_attempt_at DATETIME NULL,
		created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_webhooks_tenant ON webhooks(tenant_id);
	`,
	// 0009 — broker-specific config fields shared across MQTT, NATS,
	// AMQP 1.0. Adding once means one migration covers all three new
	// connector types instead of three back-to-back column additions.
	//
	// client_id     MQTT + AMQP 1.0 (per-client identity).
	// stream_name   NATS JetStream — the stream a consumer is bound to.
	// consumer_name NATS JetStream — durable consumer name.
	// qos           MQTT delivery quality of service (0|1|2). 0 by
	//                 default. Ignored by the other connectors.
	//
	// Existing columns reused: url (all three), queue_name → used as
	// MQTT topic / NATS subject / AMQP address, username/password,
	// the tls_* set from migration 0006.
	`
	ALTER TABLE connections ADD COLUMN client_id     TEXT NOT NULL DEFAULT '';
	ALTER TABLE connections ADD COLUMN stream_name   TEXT NOT NULL DEFAULT '';
	ALTER TABLE connections ADD COLUMN consumer_name TEXT NOT NULL DEFAULT '';
	ALTER TABLE connections ADD COLUMN qos           INTEGER NOT NULL DEFAULT 0;
	`,
	// 0010 — relax the CHECK constraints that gate Phase 22.
	//
	// Migration 0009 added the *columns* for MQTT/NATS/AMQP 1.0, but the
	// original `connections.type` CHECK clause from migration 0001 still
	// allows only ibm/rabbitmq/kafka, so the API layer's "type" validation
	// passes but the INSERT fails downstream with constraint failed (275).
	// Two more CHECKs need the same treatment to fully unlock Phase 22:
	//   - schemas.schema_type     → +protobuf
	//   - pipelines.output_format → +protobuf
	//
	// SQLite has no ALTER TABLE for CHECK, so we recreate each table with
	// the loosened constraint and copy data over. The legacy_* renames
	// keep the operation atomic — if the COPY fails for any reason the
	// rollback puts the original table back in place. Indexes + FK
	// targets are re-established explicitly.
	`
	-- pipelines.source_id / destination_id reference connections(id), so
	-- the recreate-with-new-CHECK dance needs FKs off. The migrate()
	-- driver disables PRAGMA foreign_keys before BEGIN and restores it
	-- after COMMIT, so this migration runs FK-free.

	-- connections.type — recreate with mqtt/nats/amqp10 in the CHECK
	CREATE TABLE connections_new (
		id            TEXT PRIMARY KEY,
		name          TEXT NOT NULL,
		type          TEXT NOT NULL CHECK(type IN ('ibm','rabbitmq','kafka','mqtt','nats','amqp10')),
		queue_manager TEXT NOT NULL DEFAULT '',
		conn_name     TEXT NOT NULL DEFAULT '',
		channel       TEXT NOT NULL DEFAULT '',
		username      TEXT NOT NULL DEFAULT '',
		password      TEXT NOT NULL DEFAULT '',
		queue_name    TEXT NOT NULL DEFAULT '',
		url           TEXT NOT NULL DEFAULT '',
		brokers       TEXT NOT NULL DEFAULT '',
		topic         TEXT NOT NULL DEFAULT '',
		created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		tenant_id     TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
		tls_ca_file              TEXT NOT NULL DEFAULT '',
		tls_cert_file            TEXT NOT NULL DEFAULT '',
		tls_key_file             TEXT NOT NULL DEFAULT '',
		tls_insecure_skip_verify INTEGER NOT NULL DEFAULT 0,
		client_id     TEXT NOT NULL DEFAULT '',
		stream_name   TEXT NOT NULL DEFAULT '',
		consumer_name TEXT NOT NULL DEFAULT '',
		qos           INTEGER NOT NULL DEFAULT 0
	);
	INSERT INTO connections_new (
		id, name, type, queue_manager, conn_name, channel, username, password,
		queue_name, url, brokers, topic, created_at, updated_at, tenant_id,
		tls_ca_file, tls_cert_file, tls_key_file, tls_insecure_skip_verify,
		client_id, stream_name, consumer_name, qos
	) SELECT
		id, name, type, queue_manager, conn_name, channel, username, password,
		queue_name, url, brokers, topic, created_at, updated_at, tenant_id,
		tls_ca_file, tls_cert_file, tls_key_file, tls_insecure_skip_verify,
		client_id, stream_name, consumer_name, qos
	FROM connections;
	DROP TABLE connections;
	ALTER TABLE connections_new RENAME TO connections;
	CREATE INDEX IF NOT EXISTS idx_connections_name   ON connections(name);
	CREATE INDEX IF NOT EXISTS idx_connections_tenant ON connections(tenant_id);

	-- schemas.schema_type — recreate with protobuf in the CHECK
	CREATE TABLE schemas_new (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		schema_type TEXT NOT NULL CHECK(schema_type IN ('json_schema','xsd','protobuf')),
		content     TEXT NOT NULL DEFAULT '',
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		tenant_id   TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000'
	);
	INSERT INTO schemas_new (
		id, name, schema_type, content, created_at, updated_at, tenant_id
	) SELECT
		id, name, schema_type, content, created_at, updated_at, tenant_id
	FROM schemas;
	DROP TABLE schemas;
	ALTER TABLE schemas_new RENAME TO schemas;
	CREATE INDEX IF NOT EXISTS idx_schemas_tenant ON schemas(tenant_id);

	-- pipelines.output_format — recreate with protobuf in the CHECK
	CREATE TABLE pipelines_new (
		id              TEXT PRIMARY KEY,
		name            TEXT NOT NULL,
		source_id       TEXT NOT NULL REFERENCES connections(id) ON DELETE RESTRICT,
		destination_id  TEXT NOT NULL REFERENCES connections(id) ON DELETE RESTRICT,
		output_format   TEXT NOT NULL DEFAULT 'same' CHECK(output_format IN ('same','json','xml','protobuf')),
		schema_id       TEXT REFERENCES schemas(id) ON DELETE SET NULL,
		filter_paths    TEXT NOT NULL DEFAULT '[]',
		enabled         INTEGER NOT NULL DEFAULT 1,
		created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		tenant_id       TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000'
	);
	INSERT INTO pipelines_new (
		id, name, source_id, destination_id, output_format, schema_id,
		filter_paths, enabled, created_at, updated_at, tenant_id
	) SELECT
		id, name, source_id, destination_id, output_format, schema_id,
		filter_paths, enabled, created_at, updated_at, tenant_id
	FROM pipelines;
	DROP TABLE pipelines;
	ALTER TABLE pipelines_new RENAME TO pipelines;
	CREATE INDEX IF NOT EXISTS idx_pipelines_enabled ON pipelines(enabled);
	CREATE INDEX IF NOT EXISTS idx_pipelines_tenant  ON pipelines(tenant_id);
	`,
	// 0011 — DLQ retry policy + per-pipeline concurrency.
	//
	// pipelines.workers          — N parallel goroutines drain the
	//                              source per pipeline. Default 1.
	//                              Bounded at 16 in the API layer.
	// pipelines.retry_max        — DLQ reaper attempts per row. 0 = use
	//                              the service default (3).
	// pipelines.retry_backoff_ms — base backoff between retries (ms).
	//                              Actual wait is backoff * 2^attempt,
	//                              capped at 10 minutes.
	// dlq.next_retry_at          — when the reaper next tries this row.
	//                              NULL = manual retry only.
	`
	ALTER TABLE pipelines ADD COLUMN workers          INTEGER NOT NULL DEFAULT 1;
	ALTER TABLE pipelines ADD COLUMN retry_max        INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE pipelines ADD COLUMN retry_backoff_ms INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE dlq       ADD COLUMN next_retry_at    DATETIME NULL;
	CREATE INDEX IF NOT EXISTS idx_dlq_next_retry ON dlq(next_retry_at)
	  WHERE next_retry_at IS NOT NULL;
	`,
	// 0012 — Kafka consumer-group id override.
	//
	// Empty (the default) lets the Kafka connector auto-derive a stable
	// group from brokers + topic, which is the right answer for the
	// "one logical consumer per source connection" model. Operators
	// who need two pipelines to read the same topic with independent
	// offsets set this to two different strings.
	`
	ALTER TABLE connections ADD COLUMN group_id TEXT NOT NULL DEFAULT '';
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

		// PRAGMA foreign_keys is a per-connection setting and is silently
		// ignored inside a transaction. database/sql pools connections,
		// so a bare db.Exec("PRAGMA …") lands on a different connection
		// than the subsequent db.Begin(). Pin one connection for the
		// entire migration so the PRAGMA, BEGIN, statements, COMMIT, and
		// the matching PRAGMA-restore all execute on the same conn.
		conn, err := db.Conn(context.Background())
		if err != nil {
			return fmt.Errorf("acquire conn for migration %d: %w", version, err)
		}
		if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
			_ = conn.Close()
			return fmt.Errorf("disable FK for migration %d: %w", version, err)
		}

		tx, err := conn.BeginTx(context.Background(), nil)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.Exec(m); err != nil {
			_ = tx.Rollback()
			_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
			_ = conn.Close()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			_ = tx.Rollback()
			_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
			_ = conn.Close()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
			_ = conn.Close()
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
		if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
			_ = conn.Close()
			return fmt.Errorf("re-enable FK after migration %d: %w", version, err)
		}
		_ = conn.Close()
	}
	return nil
}
