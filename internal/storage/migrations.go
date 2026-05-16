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
