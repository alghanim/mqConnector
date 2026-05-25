package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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

	-- schemas is declared before pipelines so the pipelines.schema_id
	-- FK resolves without a forward reference. SQLite tolerates
	-- forward FKs; Postgres validates at CREATE TABLE time. Order
	-- here makes both backends happy.
	CREATE TABLE IF NOT EXISTS schemas (
		id          TEXT PRIMARY KEY,
		name        TEXT NOT NULL,
		schema_type TEXT NOT NULL CHECK(schema_type IN ('json_schema','xsd')),
		content     TEXT NOT NULL DEFAULT '',
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

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
	// 0013 — explicit system_admin flag on memberships.
	//
	// Replaces the implicit "owner of the default tenant" check with
	// a flag that can be granted to additional users. The previous
	// rule is preserved by backfilling: any owner of the default
	// tenant gets system_admin=1 on this migration's run.
	//
	// system_admin is a cross-tenant escalation: holders can create
	// tenants, audit-verify across tenants, and rotate the master
	// encryption key. It is intentionally coarse — a future role
	// model could split it further, but the current set of operations
	// it gates are all "platform operator" not "tenant owner."
	`
	ALTER TABLE tenant_memberships ADD COLUMN system_admin INTEGER NOT NULL DEFAULT 0;
	UPDATE tenant_memberships
	   SET system_admin = 1
	 WHERE tenant_id = '00000000-0000-0000-0000-000000000000' AND role = 'owner';
	`,
	// 0014 — per-pipeline message budget. Independent of the
	// tenant-level admin API rate limit; this one gates how many
	// messages a single pipeline can drain per minute, so a
	// misbehaving pipeline can't starve its neighbours on the same
	// destination broker. 0 = unlimited (legacy).
	`
	ALTER TABLE pipelines ADD COLUMN max_msgs_per_minute INTEGER NOT NULL DEFAULT 0;
	`,
	// 0015 — WASM plugin blobs.
	//
	// Operators upload precompiled .wasm modules; pipelines reference
	// them via stage_type='wasm' + stage_config carrying the plugin
	// name. See docs/PLUGIN_DESIGN.md for the contract.
	//
	// The blob lives in SQLite because plugins are typically small
	// (KiB to low-MiB), the upload is rare (operator-driven), and
	// keeping it in the database makes backup + restore atomic with
	// the rest of the configuration. sha256 is the canonical id so
	// two uploads of the same blob dedupe at the storage layer.
	//
	// `name` is the operator-visible label used in stage_config.
	// Scoped to tenant — different tenants can ship plugins with the
	// same name without colliding.
	`
	CREATE TABLE IF NOT EXISTS plugins (
		id            TEXT PRIMARY KEY,
		tenant_id     TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
		name          TEXT NOT NULL,
		sha256        TEXT NOT NULL,
		blob          BLOB NOT NULL,
		size_bytes    INTEGER NOT NULL DEFAULT 0,
		uploaded_by   TEXT NOT NULL DEFAULT '',
		uploaded_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(tenant_id, name)
	);
	CREATE INDEX IF NOT EXISTS idx_plugins_tenant ON plugins(tenant_id);
	`,
	// 0016 — add 'wasm' to the stages.stage_type CHECK. Same
	// recreate-and-copy dance as migration 0010; SQLite has no
	// ALTER for CHECK constraints. wasm stages reference a plugin
	// by name through stage_config={"plugin":"…"} — Build() resolves
	// it via the plugins table.
	`
	CREATE TABLE stages_new (
		id           TEXT PRIMARY KEY,
		pipeline_id  TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
		stage_order  INTEGER NOT NULL,
		stage_type   TEXT NOT NULL CHECK(stage_type IN ('filter','transform','translate','route','script','validate','wasm')),
		stage_config TEXT NOT NULL DEFAULT '{}',
		enabled      INTEGER NOT NULL DEFAULT 1,
		tenant_id    TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000'
	);
	INSERT INTO stages_new (id, pipeline_id, stage_order, stage_type, stage_config, enabled, tenant_id)
	SELECT id, pipeline_id, stage_order, stage_type, stage_config, enabled, tenant_id FROM stages;
	DROP TABLE stages;
	ALTER TABLE stages_new RENAME TO stages;
	CREATE INDEX IF NOT EXISTS idx_stages_pipeline ON stages(pipeline_id, stage_order);
	CREATE INDEX IF NOT EXISTS idx_stages_tenant   ON stages(tenant_id);
	`,
	// 0017 — Kafka initial-offset override.
	//
	// Empty = "newest" (the safe upgrade default the connector picks).
	// "oldest" replays history from the broker's retention head.
	// See the InitialOffset doc comment on storage.Connection +
	// internal/mq/connector_kafka.go initialOffsetFromConfig.
	`
	ALTER TABLE connections ADD COLUMN initial_offset TEXT NOT NULL DEFAULT '';
	`,
	// 0018 — per-pipeline role grants.
	//
	// Extends the tenant-scoped role model with an optional per-pipeline
	// override. Default behaviour is unchanged: a user with a tenant
	// membership sees and operates every pipeline in that tenant at
	// their tenant role. The override applies when finer control is
	// needed — e.g. tenant T has hundreds of pipelines but user U should
	// only see two of them.
	//
	// Semantics (see PipelineGrantsRepo.EffectiveRole):
	//   - No row for (pipeline_id, user_sub) → fall back to the user's
	//     tenant role.
	//   - Row exists → the user's effective role on this pipeline is
	//     max(tenant role, granted role). Grants only ESCALATE; they
	//     never demote below the tenant role.
	//   - To hide a pipeline from a tenant-role-of-viewer user, an
	//     admin sets pipeline_visible=0 on the tenant membership flag
	//     (separate concern, not in this migration).
	//
	// This migration is foundation for the handler-side integration:
	// list/get/update endpoints will filter through the resolver.
	// Endpoints landing in a follow-on commit.
	`
	CREATE TABLE IF NOT EXISTS pipeline_grants (
		pipeline_id TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
		user_sub    TEXT NOT NULL,
		role        TEXT NOT NULL CHECK(role IN ('viewer','operator','admin','owner')),
		created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (pipeline_id, user_sub)
	);
	CREATE INDEX IF NOT EXISTS idx_pipeline_grants_user ON pipeline_grants(user_sub);
	`,
	// 0019 — DLQ payload redaction.
	//
	// Per-pipeline redaction rules let operators strip PII/PHI from DLQ
	// payloads before they're persisted. Compliance posture: the row's
	// payload column (the historical original_msg) now holds the
	// redacted form, which is what every list/get response returns.
	// The unredacted bytes are stored in raw_msg, sealed under the
	// envelope-encryption master key, and only readable through the
	// dedicated raw-view endpoint — which requires admin role AND
	// produces an explicit audit-log entry for every read.
	//
	// When a pipeline has no redaction rules (the default), Push writes
	// raw_msg empty and redacted=0; behaviour for those rows is byte-
	// for-byte identical to pre-0019. Rules are pipeline-scoped (the
	// tenant boundary follows the pipeline FK) and ordered — the engine
	// walks them in `ord` order against each payload.
	//
	// rule_kind:
	//   - 'jsonpath' — path expression matching one or more JSON values;
	//     each matched value is replaced with mask_replace. Non-JSON
	//     payloads skip JSONPath rules cleanly.
	//   - 'regex'    — Go regexp; first match groups are replaced with
	//     mask_replace via ReplaceAllString.
	`
	ALTER TABLE dlq ADD COLUMN raw_msg BLOB NOT NULL DEFAULT '';
	ALTER TABLE dlq ADD COLUMN redacted INTEGER NOT NULL DEFAULT 0;

	CREATE TABLE IF NOT EXISTS dlq_redaction_rules (
		id           TEXT PRIMARY KEY,
		pipeline_id  TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
		tenant_id    TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
		rule_kind    TEXT NOT NULL CHECK(rule_kind IN ('jsonpath','regex')),
		pattern      TEXT NOT NULL,
		mask_replace TEXT NOT NULL DEFAULT '[REDACTED]',
		ord          INTEGER NOT NULL DEFAULT 0,
		enabled      INTEGER NOT NULL DEFAULT 1,
		created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_dlq_redaction_pipeline
	  ON dlq_redaction_rules(pipeline_id, ord);
	`,
	// 0020 — destination-side dedup window.
	//
	// Opt-in per pipeline. When dedup_window_seconds > 0 the executor
	// computes SHA-256 of the post-stage outbound payload and checks
	// the pipeline_dedup table before forwarding. A row that's still
	// inside the window is a duplicate and the send is skipped; the
	// source is committed (the operator already promised this message
	// is "the same" so re-publishing it would break the contract).
	//
	// This upgrades the at-least-once delivery contract to effectively-
	// once for the configured window — without changing the global
	// contract, so non-idempotent destinations (counters, billing,
	// notifications) can opt in pipeline-by-pipeline.
	//
	// Storage: (pipeline_id, payload_hash) is unique; first_seen_at
	// pins the window's start, last_seen_at is bumped on every hit so
	// a long burst of dupes keeps refreshing the entry. The retention
	// sweeper prunes rows whose last_seen_at + window has expired.
	`
	ALTER TABLE pipelines ADD COLUMN dedup_window_seconds INTEGER NOT NULL DEFAULT 0;

	CREATE TABLE IF NOT EXISTS pipeline_dedup (
		pipeline_id   TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
		payload_hash  TEXT NOT NULL,
		first_seen_at DATETIME NOT NULL,
		last_seen_at  DATETIME NOT NULL,
		hits          INTEGER NOT NULL DEFAULT 1,
		PRIMARY KEY (pipeline_id, payload_hash)
	);
	CREATE INDEX IF NOT EXISTS idx_pipeline_dedup_last_seen
	  ON pipeline_dedup(last_seen_at);
	`,
	// 0021 — pipeline shadow mode.
	//
	// Operators rolling out a new destination broker (broker upgrade,
	// migration to a new cluster, evaluating a candidate consumer)
	// need to compare what the candidate sees against what production
	// is sending. Shadow mode: when shadow_destination_id is set,
	// the executor — after a successful send to the primary
	// destination — ALSO publishes the same payload to the shadow
	// destination for shadow_percent of messages. Failures on the
	// shadow path are counted (mqconnector_shadow_failed_total) but
	// NEVER affect the prod commit-to-source decision.
	`
	ALTER TABLE pipelines ADD COLUMN shadow_destination_id TEXT
	  REFERENCES connections(id) ON DELETE SET NULL;
	ALTER TABLE pipelines ADD COLUMN shadow_percent INTEGER NOT NULL DEFAULT 0;
	`,
	// 0022 — pipeline revisions (Pipeline Studio foundation).
	//
	// Append-only history of a pipeline's full configuration. Saving a
	// pipeline writes a new revision row with deployed_at = NULL;
	// deploying writes the snapshot back to the live tables and stamps
	// deployed_at. The snapshot column holds canonical JSON of the
	// pipeline + stages + transforms + routing_rules; snapshot_hash is
	// the SHA-256 of that bytes so the repo can dedupe no-op saves at
	// the storage layer rather than the handler.
	//
	// Indexes: (pipeline_id, revision_number DESC) is the workhorse for
	// the "show me the latest N revisions of this pipeline" view; the
	// secondary (tenant_id, created_at DESC) supports the future cross-
	// pipeline activity feed without a scan.
	//
	// pipelines.requires_approval is added in the same migration as
	// Wave-5 forward-compat. Default false, unsurfaced for now; the
	// deploy handler in Wave 5 will gate publish on it.
	`
	CREATE TABLE IF NOT EXISTS pipeline_revisions (
		id                 TEXT PRIMARY KEY,
		tenant_id          TEXT NOT NULL,
		pipeline_id        TEXT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
		revision_number    INTEGER NOT NULL,
		snapshot           TEXT NOT NULL,
		snapshot_hash      TEXT NOT NULL,
		author_sub         TEXT NOT NULL,
		author_username    TEXT NOT NULL DEFAULT '',
		change_summary     TEXT NOT NULL DEFAULT '',
		created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		deployed_at        DATETIME,
		deploy_request_id  TEXT NOT NULL DEFAULT '',
		UNIQUE (pipeline_id, revision_number)
	);
	CREATE INDEX IF NOT EXISTS idx_pipeline_revisions_pipeline
	  ON pipeline_revisions(pipeline_id, revision_number DESC);
	CREATE INDEX IF NOT EXISTS idx_pipeline_revisions_tenant
	  ON pipeline_revisions(tenant_id, created_at DESC);

	ALTER TABLE pipelines ADD COLUMN requires_approval BOOLEAN NOT NULL DEFAULT 0;
	`,
	// 0023 — DLQ Intelligence: clustering columns.
	//
	// Wave 3 surfaces DLQ rows as a sorted-by-impact list of error
	// clusters rather than a flat stream. The fingerprint + template
	// pair is computed once at insert time by internal/dlq/cluster
	// and persisted alongside the row so cluster-rollup queries are
	// cheap indexed GROUP BYs instead of a per-row regex pass.
	//
	// error_fingerprint  — 16-char hex SimHash over the tokenised
	//                      template. Stable across minor variation
	//                      (different UUIDs / timestamps / ids
	//                      collapse to the same bucket).
	// error_template     — human-readable error with variable parts
	//                      replaced by <PATH>/<INT>/<UUID>/etc.
	//                      placeholders. Shown in the cluster
	//                      panel header.
	// failing_stage_name — stage type ("validate", "transform", …)
	//                      that emitted the failure. Empty for the
	//                      send-side failure path that has no stage
	//                      attribution.
	// failing_stage_index— position in the stage list where the
	//                      failure happened. 0 when not attributed.
	//
	// Both indexes are tenant-scoped: every list query the cluster
	// console issues filters by tenant_id first, and the secondary
	// key (fingerprint / stage name) narrows from there. Defaults
	// keep existing rows valid — the cluster console treats empty
	// fingerprint rows as "unclustered" and surfaces them in a
	// dedicated bucket.
	`
	ALTER TABLE dlq ADD COLUMN error_fingerprint   TEXT NOT NULL DEFAULT '';
	ALTER TABLE dlq ADD COLUMN error_template      TEXT NOT NULL DEFAULT '';
	ALTER TABLE dlq ADD COLUMN failing_stage_name  TEXT NOT NULL DEFAULT '';
	ALTER TABLE dlq ADD COLUMN failing_stage_index INTEGER NOT NULL DEFAULT 0;

	CREATE INDEX IF NOT EXISTS idx_dlq_fingerprint
	  ON dlq(tenant_id, error_fingerprint);
	CREATE INDEX IF NOT EXISTS idx_dlq_failing_stage
	  ON dlq(tenant_id, failing_stage_name);
	`,
}

// postgresMigrationOverrides supersedes specific entries in `migrations`
// when the dialect is Postgres. The SQLite versions of these migrations
// use the recreate-and-rename idiom (CREATE TABLE _new; INSERT; DROP;
// RENAME) because SQLite has no ALTER for CHECK constraints. Postgres
// rejects the DROP step due to foreign-key references from sibling
// tables, so we ship native ALTER TABLE … DROP CONSTRAINT … ADD
// CONSTRAINT statements that produce the same logical schema without
// rebuilding the table.
//
// Keep in sync with the SQLite version's *effect*; the column shapes,
// indexes and FK targets stay identical, so neither dialect drifts.
var postgresMigrationOverrides = map[int]string{
	// 0010 — relax CHECK constraints to admit MQTT/NATS/AMQP1.0
	// connections, protobuf schemas, and protobuf pipeline output.
	// SQLite recreates each table; Postgres just swaps the CHECK.
	// The IF EXISTS guards make the migration idempotent against
	// hand-built Postgres deployments where someone might have
	// renamed the constraint.
	10: `
		ALTER TABLE connections DROP CONSTRAINT IF EXISTS connections_type_check;
		ALTER TABLE connections ADD CONSTRAINT connections_type_check
		  CHECK (type IN ('ibm','rabbitmq','kafka','mqtt','nats','amqp10'));

		ALTER TABLE schemas DROP CONSTRAINT IF EXISTS schemas_schema_type_check;
		ALTER TABLE schemas ADD CONSTRAINT schemas_schema_type_check
		  CHECK (schema_type IN ('json_schema','xsd','protobuf'));

		ALTER TABLE pipelines DROP CONSTRAINT IF EXISTS pipelines_output_format_check;
		ALTER TABLE pipelines ADD CONSTRAINT pipelines_output_format_check
		  CHECK (output_format IN ('same','json','xml','protobuf'));
	`,
	// 0016 — admit the 'wasm' stage type. SQLite recreates the
	// stages table; Postgres just swaps the CHECK.
	16: `
		ALTER TABLE stages DROP CONSTRAINT IF EXISTS stages_stage_type_check;
		ALTER TABLE stages ADD CONSTRAINT stages_stage_type_check
		  CHECK (stage_type IN ('filter','transform','translate','route','script','validate','wasm'));
	`,
}

func migrate(db *sql.DB, dialect Dialect) error {
	// Schema_migrations table — same DDL works for both dialects.
	// DATETIME and CURRENT_TIMESTAMP are accepted by both as
	// timestamp-with-default; the `?` -> `$1` switch happens via
	// rewritePlaceholders below for the parameterised queries.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	checkSQL := rewritePlaceholders(
		`SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, dialect)
	recordSQL := rewritePlaceholders(
		`INSERT INTO schema_migrations (version) VALUES (?)`, dialect)

	for i, m := range migrations {
		version := i + 1
		var existing int
		err := db.QueryRow(checkSQL, version).Scan(&existing)
		if err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if existing > 0 {
			continue
		}

		// Translate the SQLite migration body to the target dialect.
		// SQLite-specific syntax mostly works on Postgres (DATETIME,
		// BLOB, CHECK constraints are accepted as aliases) but
		// `INSERT OR IGNORE` is unique and must become `ON CONFLICT
		// DO NOTHING`.
		body := translateMigrationToDialect(m, dialect)

		// Per-version Postgres native body. A handful of migrations
		// use SQLite-only idioms (table recreate-and-rename for CHECK
		// constraint changes) that don't translate via simple
		// substitution. Those carry an entirely separate Postgres
		// body here; we substitute before exec.
		if dialect == DialectPostgres {
			if pg, ok := postgresMigrationOverrides[version]; ok {
				body = pg
			}
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
		// Foreign-key gymnastics are SQLite-only; Postgres handles
		// constraint deferral via SET CONSTRAINTS, which we don't need
		// for these migrations.
		if dialect == DialectSQLite {
			if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = OFF`); err != nil {
				_ = conn.Close()
				return fmt.Errorf("disable FK for migration %d: %w", version, err)
			}
		}

		tx, err := conn.BeginTx(context.Background(), nil)
		if err != nil {
			_ = conn.Close()
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		if _, err := tx.Exec(body); err != nil {
			_ = tx.Rollback()
			if dialect == DialectSQLite {
				_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
			}
			_ = conn.Close()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.Exec(recordSQL, version); err != nil {
			_ = tx.Rollback()
			if dialect == DialectSQLite {
				_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
			}
			_ = conn.Close()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			if dialect == DialectSQLite {
				_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
			}
			_ = conn.Close()
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
		if dialect == DialectSQLite {
			if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`); err != nil {
				_ = conn.Close()
				return fmt.Errorf("re-enable FK after migration %d: %w", version, err)
			}
		}
		_ = conn.Close()
	}
	return nil
}

// translateMigrationToDialect handles the small set of substitutions
// needed when running a SQLite-flavoured migration against Postgres.
// The migrations are authored against SQLite; Postgres accepts most
// of the SQL verbatim. The replacements below are the items that
// don't translate without help:
//
//   - INSERT OR IGNORE INTO X ... VALUES (...);
//     → INSERT INTO X ... VALUES (...) ON CONFLICT DO NOTHING;
//   - BLOB → bytea (postgres binary column type)
//   - DATETIME → TIMESTAMP (canonical postgres name; pgx round-trip
//     of time.Time works cleanly through TIMESTAMP)
//
// CHECK constraints, CURRENT_TIMESTAMP defaults, INTEGER NOT NULL
// DEFAULT 0|1 booleans, and the recreate-and-copy table patterns
// all work on Postgres as-is.
func translateMigrationToDialect(sql string, dialect Dialect) string {
	if dialect != DialectPostgres {
		return sql
	}
	// Per-statement scan: rewrite each statement up to its `;`
	// independently. The earlier naive global-replace approach
	// over-applied ON CONFLICT to every statement including
	// CREATE TABLE, which then errored at apply time.
	var out []byte
	for len(sql) > 0 {
		idx := indexStatementEnd(sql)
		var stmt string
		if idx < 0 {
			stmt = sql
			sql = ""
		} else {
			stmt = sql[:idx+1]
			sql = sql[idx+1:]
		}
		out = append(out, translatePostgresStatement(stmt)...)
	}
	return string(out)
}

// indexStatementEnd returns the index of the next `;` outside of
// quoted strings. Returns -1 when there's no terminator.
func indexStatementEnd(s string) int {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == ';' && !inSingle && !inDouble:
			return i
		}
	}
	return -1
}

// translatePostgresStatement applies postgres substitutions to a
// single SQL statement. Pure string transform; unit-testable.
func translatePostgresStatement(stmt string) string {
	upper := strings.ToUpper(stmt)
	insertIgnore := strings.Contains(upper, "INSERT OR IGNORE INTO")
	stmt = strings.ReplaceAll(stmt, "INSERT OR IGNORE INTO", "INSERT INTO")
	if insertIgnore && !strings.Contains(strings.ToUpper(stmt), "ON CONFLICT") {
		if semi := strings.LastIndex(stmt, ";"); semi >= 0 {
			stmt = stmt[:semi] + " ON CONFLICT DO NOTHING" + stmt[semi:]
		}
	}
	// BLOB → bytea (binary).
	stmt = strings.ReplaceAll(stmt, "BLOB NOT NULL", "bytea NOT NULL")
	stmt = strings.ReplaceAll(stmt, " BLOB,", " bytea,")
	stmt = strings.ReplaceAll(stmt, " BLOB\n", " bytea\n")
	// DATETIME → TIMESTAMP.
	stmt = strings.ReplaceAll(stmt, "DATETIME NOT NULL", "TIMESTAMP NOT NULL")
	stmt = strings.ReplaceAll(stmt, "DATETIME NULL", "TIMESTAMP NULL")
	stmt = strings.ReplaceAll(stmt, "DATETIME,", "TIMESTAMP,")
	stmt = strings.ReplaceAll(stmt, "DATETIME\n", "TIMESTAMP\n")
	stmt = strings.ReplaceAll(stmt, "DATETIME ", "TIMESTAMP ")
	return stmt
}
