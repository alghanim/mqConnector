package storage

import "time"

// Connection is the persisted form of an MQ endpoint.
//
// TenantID scopes the row to one tenant. The HTTP layer never lets a
// caller set it directly — the auth middleware writes it from the
// request's resolved tenant. Storage repo methods take tenantID as an
// explicit parameter and re-write the struct field from the argument
// to make accidental cross-tenant writes a compile error.
type Connection struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenant_id"`
	Name         string    `json:"name"`
	Type         string    `json:"type"` // "ibm" | "rabbitmq" | "kafka"
	QueueManager string    `json:"queue_manager,omitempty"`
	ConnName     string    `json:"conn_name,omitempty"`
	Channel      string    `json:"channel,omitempty"`
	Username     string    `json:"username,omitempty"`
	Password     string    `json:"password,omitempty"`
	QueueName    string    `json:"queue_name,omitempty"`
	URL          string    `json:"url,omitempty"`
	Brokers      string    `json:"brokers,omitempty"`
	Topic        string    `json:"topic,omitempty"`
	// Broker TLS — see migration 0006. Paths point at PEM files on
	// the connector host; the dialers read them at connect-time so a
	// rotated cert takes effect on the next reconnect.
	TLSCAFile             string `json:"tls_ca_file,omitempty"`
	TLSCertFile           string `json:"tls_cert_file,omitempty"`
	TLSKeyFile            string `json:"tls_key_file,omitempty"`
	TLSInsecureSkipVerify bool   `json:"tls_insecure_skip_verify,omitempty"`
	// MQTT / NATS / AMQP 1.0 — see migration 0009.
	// ClientID is used by MQTT (must be unique per broker) and AMQP
	// 1.0 (link/container name). NATS uses StreamName + ConsumerName
	// for JetStream subscriptions. QoS is the MQTT delivery
	// guarantee (0=at-most-once, 1=at-least-once, 2=exactly-once).
	ClientID     string `json:"client_id,omitempty"`
	StreamName   string `json:"stream_name,omitempty"`
	ConsumerName string `json:"consumer_name,omitempty"`
	QoS          int    `json:"qos,omitempty"`
	// Kafka consumer-group override. Empty → connector hashes
	// brokers+topic into a stable group so restarts pick up where they
	// left off (the right answer for "one logical consumer per source
	// connection"). Set explicitly when two pipelines on the same
	// Kafka source need independent offsets.
	GroupID string `json:"group_id,omitempty"`
	// InitialOffset controls where a fresh Kafka consumer group
	// attaches: "newest" (default, upgrade-safe) or "oldest"
	// (replay from broker retention head). Ignored once the group
	// has any committed offset — the broker's stored offset wins.
	InitialOffset string    `json:"initial_offset,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Pipeline is one source→destination flow with an ordered list of stages.
type Pipeline struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	Name          string    `json:"name"`
	SourceID      string    `json:"source_id"`
	DestinationID string    `json:"destination_id"`
	OutputFormat  string    `json:"output_format"` // "same" | "json" | "xml" | "protobuf"
	SchemaID      string    `json:"schema_id,omitempty"`
	FilterPaths   []string  `json:"filter_paths"`
	Enabled       bool      `json:"enabled"`
	// Workers is the number of goroutines that drain the source in
	// parallel for this pipeline. Defaults to 1. Bounded at 16 in the
	// API layer; a single I/O-bound stage benefits most from 2–4.
	Workers int `json:"workers,omitempty"`
	// RetryMax bounds the number of times the DLQ reaper will attempt
	// to re-publish a failed message before giving up. 0 means "use the
	// service-wide default (3)".
	RetryMax int `json:"retry_max,omitempty"`
	// RetryBackoffMs is the base backoff between retries in
	// milliseconds. The actual wait is RetryBackoffMs * 2^attempt
	// (exponential), capped at 10 minutes. 0 means "use 5000 (5s)".
	RetryBackoffMs int `json:"retry_backoff_ms,omitempty"`
	// MaxMsgsPerMinute caps the per-pipeline throughput at the source
	// drain. 0 = unlimited (legacy behaviour). Used to isolate a
	// misbehaving pipeline so it can't starve other pipelines on the
	// same destination broker or overwhelm a downstream that has its
	// own SLA. Independent of the per-tenant rate limit that gates
	// admin API calls.
	MaxMsgsPerMinute int       `json:"max_msgs_per_minute,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Stage is one step in a pipeline's processing chain.
//
// TenantID is denormalised onto every child row so a tenant-scoped query
// against stages/transforms/routing_rules doesn't need a JOIN. The
// repo's Replace methods write it from the pipeline's tenant — callers
// don't manage it directly.
type Stage struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	PipelineID  string `json:"pipeline_id"`
	StageOrder  int    `json:"stage_order"`
	StageType   string `json:"stage_type"` // filter|transform|translate|route|script|validate
	StageConfig string `json:"stage_config"`
	Enabled     bool   `json:"enabled"`
}

// Transform is one rename/mask/move/set/delete rule attached to a pipeline.
type Transform struct {
	ID            string `json:"id"`
	TenantID      string `json:"tenant_id"`
	PipelineID    string `json:"pipeline_id"`
	TransformType string `json:"transform_type"` // rename|mask|move|set|delete
	SourcePath    string `json:"source_path"`
	TargetPath    string `json:"target_path,omitempty"`
	MaskPattern   string `json:"mask_pattern,omitempty"`
	MaskReplace   string `json:"mask_replace,omitempty"`
	SetValue      string `json:"set_value,omitempty"`
	Order         int    `json:"order"`
}

// RoutingRule is one content-based routing predicate attached to a pipeline.
type RoutingRule struct {
	ID                string `json:"id"`
	TenantID          string `json:"tenant_id"`
	PipelineID        string `json:"pipeline_id"`
	ConditionPath     string `json:"condition_path"`
	ConditionOperator string `json:"condition_operator"` // eq|neq|contains|regex|gt|lt|exists
	ConditionValue    string `json:"condition_value"`
	DestinationID     string `json:"destination_id"`
	Priority          int    `json:"priority"`
	Enabled           bool   `json:"enabled"`
}

// Script is a reusable transform body. Bodies are also embeddable directly in
// a script-type stage; this collection is for sharing across pipelines.
type Script struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenant_id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Body        string    `json:"body"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Schema is a validation schema (JSON Schema or required-element XSD list).
type Schema struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	Name       string    `json:"name"`
	SchemaType string    `json:"schema_type"` // json_schema | xsd
	Content    string    `json:"content"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DLQEntry is a failed message awaiting retry or inspection.
type DLQEntry struct {
	ID          string     `json:"id"`
	TenantID    string     `json:"tenant_id"`
	PipelineID  string     `json:"pipeline_id,omitempty"`
	SourceQueue string     `json:"source_queue,omitempty"`
	OriginalMsg []byte     `json:"original_msg"`
	ErrorReason string     `json:"error_reason"`
	RetryCount  int        `json:"retry_count"`
	LastRetryAt *time.Time `json:"last_retry_at,omitempty"`
	// NextRetryAt is when the DLQ retry reaper will next attempt to
	// re-publish this row. NULL means "manual retry only — no auto
	// reaping". Set by Push() to time.Now() + backoff if the pipeline's
	// retry policy is non-zero.
	NextRetryAt *time.Time `json:"next_retry_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}
