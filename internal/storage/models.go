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
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Pipeline is one source→destination flow with an ordered list of stages.
type Pipeline struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	Name          string    `json:"name"`
	SourceID      string    `json:"source_id"`
	DestinationID string    `json:"destination_id"`
	OutputFormat  string    `json:"output_format"` // "same" | "json" | "xml"
	SchemaID      string    `json:"schema_id,omitempty"`
	FilterPaths   []string  `json:"filter_paths"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
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
	CreatedAt   time.Time  `json:"created_at"`
}
