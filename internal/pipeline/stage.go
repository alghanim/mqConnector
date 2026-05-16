package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"mqConnector/internal/storage"
)

// Stage is the unit of message processing. Each stage receives the current
// message and format, transforms it, and returns the next message and
// (possibly updated) format.
//
// Side-effects beyond message mutation are returned via Result. The Route
// stage uses Result.Destinations to fan out the message to one or more
// destination connections; if Result.Destinations is non-empty, the default
// destination is bypassed.
type Stage interface {
	Name() string
	Execute(ctx context.Context, message []byte, format Format) ([]byte, Format, *Result, error)
}

// Result reports out-of-band effects from a stage execution.
type Result struct {
	// Destinations, if non-empty, instructs the executor to forward the
	// resulting message to each of these connection IDs instead of the
	// pipeline's default destination.
	Destinations []string
}

// Spec is the storage-shaped description of a stage; BuildStages turns this
// into a concrete Stage.
type Spec struct {
	Type    string          // filter|transform|translate|route|script|validate
	Config  json.RawMessage // raw JSON config from storage.stages.stage_config
	Enabled bool
}

// FilterConfig is the JSON payload for a `filter` stage.
type FilterConfig struct {
	Paths []string `json:"paths"`
}

// TranslateConfig is the JSON payload for a `translate` stage.
//
// SchemaID + ProtoMessage are only consulted when OutputFormat is
// "protobuf" or when the incoming format is "protobuf". The schema
// row's SchemaType must be "protobuf" and its Content must hold a
// base64-encoded FileDescriptorSet (output of
// `protoc --descriptor_set_out=...`).
type TranslateConfig struct {
	OutputFormat string `json:"output_format"` // "json" | "xml" | "protobuf" | "same"
	SchemaID     string `json:"schema_id,omitempty"`
	ProtoMessage string `json:"proto_message,omitempty"` // FQN, e.g. "acme.orders.Order"
}

// RouteConfig is the JSON payload for a `route` stage. The stage holds the
// rules in-memory and evaluates them per-message.
type RouteConfig struct {
	// In storage, routing rules are stored separately; this stage receives
	// them in-memory at build time. Keeping the JSON-side empty avoids
	// double-storage.
}

// ScriptConfig is the JSON payload for a `script` stage.
type ScriptConfig struct {
	Script string `json:"script"`
}

// ValidateConfig is the JSON payload for a `validate` stage.
//
// ProtoMessage is the fully-qualified message name when SchemaType is
// "protobuf" (e.g. "acme.orders.Order"). The schema's stored Content
// in that case is the base64-encoded FileDescriptorSet.
type ValidateConfig struct {
	SchemaID     string `json:"schema_id"`
	SchemaType   string `json:"schema_type"` // json_schema | xsd | protobuf
	Content      string `json:"content"`
	ProtoMessage string `json:"proto_message,omitempty"`
}

// BuildContext bundles every input BuildStages needs to materialise stages
// from storage rows.
//
// Schemas keys are storage schema IDs. The validate stage prefers a
// schema_id reference (via stage config OR the pipeline-level SchemaID) over
// the deprecated inline content/schema_type fields — the inline form is kept
// for backwards compatibility with stage configs that were authored before
// the schemas collection existed.
type BuildContext struct {
	Pipeline     *storage.Pipeline
	StageRows    []*storage.Stage
	Transforms   []*storage.Transform
	RoutingRules []*storage.RoutingRule
	Schemas      map[string]*storage.Schema
}

// Build constructs the concrete Stage list for the pipeline.
func Build(ctx BuildContext) ([]Stage, error) {
	stages := make([]Stage, 0, len(ctx.StageRows))
	for _, row := range ctx.StageRows {
		if !row.Enabled {
			continue
		}
		switch row.StageType {
		case "filter":
			cfg := FilterConfig{}
			if row.StageConfig != "" {
				_ = json.Unmarshal([]byte(row.StageConfig), &cfg)
			}
			if len(cfg.Paths) == 0 {
				cfg.Paths = ctx.Pipeline.FilterPaths
			}
			stages = append(stages, &FilterStage{Paths: cfg.Paths})
		case "transform":
			stages = append(stages, &TransformStage{Rules: ctx.Transforms})
		case "translate":
			cfg := TranslateConfig{}
			if row.StageConfig != "" {
				_ = json.Unmarshal([]byte(row.StageConfig), &cfg)
			}
			if cfg.OutputFormat == "" {
				cfg.OutputFormat = ctx.Pipeline.OutputFormat
			}
			// Optional protobuf schema. Operators set SchemaID +
			// ProtoMessage on the stage config when either side of the
			// translation is protobuf. Loading is lazy enough that a
			// JSON↔XML stage with no schema doesn't pay the cost.
			var proto *ProtoSchema
			if cfg.SchemaID != "" {
				schema := ctx.Schemas[cfg.SchemaID]
				if schema == nil {
					return nil, fmt.Errorf("translate stage references schema %q but it is not loaded", cfg.SchemaID)
				}
				if !strings.EqualFold(schema.SchemaType, "protobuf") &&
					!strings.EqualFold(schema.SchemaType, "proto") {
					return nil, fmt.Errorf("translate stage's schema %q is %q, expected protobuf",
						cfg.SchemaID, schema.SchemaType)
				}
				ps, err := LoadProtoSchema(schema.Content, cfg.ProtoMessage)
				if err != nil {
					return nil, fmt.Errorf("translate stage: load proto schema: %w", err)
				}
				proto = ps
			}
			stages = append(stages, &TranslateStage{
				Target: Format(cfg.OutputFormat),
				Proto:  proto,
			})
		case "route":
			stages = append(stages, &RouteStage{Rules: ctx.RoutingRules})
		case "script":
			cfg := ScriptConfig{}
			if row.StageConfig != "" {
				_ = json.Unmarshal([]byte(row.StageConfig), &cfg)
			}
			stages = append(stages, &ScriptStage{Script: cfg.Script})
		case "validate":
			cfg := ValidateConfig{}
			if row.StageConfig != "" {
				_ = json.Unmarshal([]byte(row.StageConfig), &cfg)
			}
			// Resolution precedence:
			//   1. SchemaID on the stage config
			//   2. Pipeline-level SchemaID
			//   3. Inline schema_type + content (legacy)
			schemaID := cfg.SchemaID
			if schemaID == "" && ctx.Pipeline != nil {
				schemaID = ctx.Pipeline.SchemaID
			}
			if schemaID != "" {
				schema := ctx.Schemas[schemaID]
				if schema == nil {
					return nil, fmt.Errorf("validate stage references schema %q but it is not loaded", schemaID)
				}
				stages = append(stages, &ValidateStage{
					SchemaType:   schema.SchemaType,
					Content:      schema.Content,
					ProtoMessage: cfg.ProtoMessage,
				})
				continue
			}
			stages = append(stages, &ValidateStage{
				SchemaType:   cfg.SchemaType,
				Content:      cfg.Content,
				ProtoMessage: cfg.ProtoMessage,
			})
		default:
			return nil, fmt.Errorf("unknown stage type %q", row.StageType)
		}
	}
	return stages, nil
}
