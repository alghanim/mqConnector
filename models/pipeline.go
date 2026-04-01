package models

import pbmodels "github.com/pocketbase/pocketbase/models"

// RoutineConfig bundles all configuration for a message processing routine.
type RoutineConfig struct {
	FilterPaths   []string
	SourceConn    map[string]string
	DefaultDest   map[string]string
	OutputFormat  string // "same", "XML", "JSON"
	Transforms    []TransformRule
	RoutingRules  []RoutingRule
	Stages        []PipelineStage
	SchemaType    string // "", "JSON_SCHEMA", "XSD"
	SchemaContent string
	DLQFunc       func(msg []byte, reason string)
	FilterRecord  *pbmodels.Record
}

// TransformRule represents a field transformation operation.
type TransformRule struct {
	TransformType  string // "rename", "mask", "move"
	SourcePath     string
	TargetPath     string
	MaskPattern    string
	MaskReplace    string
	Order          int
}

// RoutingRule represents a content-based routing condition.
type RoutingRule struct {
	ConditionPath     string
	ConditionOperator string // "eq", "neq", "contains", "regex", "gt", "lt", "exists"
	ConditionValue    string
	DestConn          map[string]string
	Priority          int
	Enabled           bool
}

// PipelineStage represents a single stage in a processing pipeline.
type PipelineStage struct {
	StageOrder int
	StageType  string // "filter", "transform", "route", "translate"
	Config     string // JSON config
	Enabled    bool
}
