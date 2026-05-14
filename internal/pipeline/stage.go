package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

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
type TranslateConfig struct {
	OutputFormat string `json:"output_format"` // "json"|"xml"|"same"
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
type ValidateConfig struct {
	SchemaID   string `json:"schema_id"`
	SchemaType string `json:"schema_type"` // json_schema | xsd
	Content    string `json:"content"`
}

// BuildContext bundles every input BuildStages needs to materialise stages
// from storage rows.
type BuildContext struct {
	Pipeline     *storage.Pipeline
	StageRows    []*storage.Stage
	Transforms   []*storage.Transform
	RoutingRules []*storage.RoutingRule
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
			stages = append(stages, &TranslateStage{Target: Format(cfg.OutputFormat)})
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
			stages = append(stages, &ValidateStage{
				SchemaType: cfg.SchemaType,
				Content:    cfg.Content,
			})
		default:
			return nil, fmt.Errorf("unknown stage type %q", row.StageType)
		}
	}
	return stages, nil
}
