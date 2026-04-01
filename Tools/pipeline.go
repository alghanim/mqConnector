package tools

import (
	"encoding/json"
	"fmt"
	"mqConnector/models"

	"github.com/beevik/etree"
)

// Stage represents a single processing stage in a pipeline.
type Stage interface {
	Execute(message []byte, format string) ([]byte, string, error)
}

// RoutingResult holds the result of a routing stage evaluation.
type RoutingResult struct {
	DestConn map[string]string
}

// Pipeline is an ordered sequence of processing stages.
type Pipeline struct {
	Stages         []Stage
	RoutingResults []RoutingResult
}

// Execute runs all stages in order, passing the output of each to the next.
func (p *Pipeline) Execute(message []byte, format string) ([]byte, string, error) {
	current := message
	currentFormat := format

	for _, stage := range p.Stages {
		result, newFormat, err := stage.Execute(current, currentFormat)
		if err != nil {
			return nil, currentFormat, err
		}
		current = result
		if newFormat != "" {
			currentFormat = newFormat
		}
	}

	return current, currentFormat, nil
}

// FilterStage removes specified fields from the message.
type FilterStage struct {
	Paths []string
}

func (s *FilterStage) Execute(message []byte, format string) ([]byte, string, error) {
	if format == "XML" {
		return filterXML(message, s.Paths)
	} else if format == "JSON" {
		result, err := RemoveJSONPaths(message, s.Paths)
		if err != nil {
			return nil, format, err
		}
		return result, format, nil
	}
	return message, format, nil
}

func filterXML(message []byte, paths []string) ([]byte, string, error) {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(message); err != nil {
		return nil, "XML", fmt.Errorf("failed to parse XML for filtering: %v", err)
	}
	namespace := doc.Root().Space
	for _, p := range paths {
		RemoveElements(doc.Root(), namespace, p)
	}
	result, err := doc.WriteToString()
	if err != nil {
		return nil, "XML", err
	}
	return []byte(result), "XML", nil
}

// TransformStage applies transformation rules to the message.
type TransformStage struct {
	Rules []models.TransformRule
}

func (s *TransformStage) Execute(message []byte, format string) ([]byte, string, error) {
	result, err := ApplyTransforms(message, format, s.Rules)
	if err != nil {
		return nil, format, err
	}
	return result, format, nil
}

// TranslateStage converts the message between XML and JSON formats.
type TranslateStage struct {
	TargetFormat string
}

func (s *TranslateStage) Execute(message []byte, format string) ([]byte, string, error) {
	result, err := TranslateFormat(message, format, s.TargetFormat)
	if err != nil {
		return nil, format, err
	}
	newFormat := s.TargetFormat
	if newFormat == "same" || newFormat == "" {
		newFormat = format
	}
	return result, newFormat, nil
}

// RouteStage evaluates routing rules. It doesn't modify the message but stores
// routing results in the parent Pipeline.
type RouteStage struct {
	Rules    []models.RoutingRule
	Pipeline *Pipeline
}

func (s *RouteStage) Execute(message []byte, format string) ([]byte, string, error) {
	matched, err := EvaluateRoutingRules(message, format, s.Rules)
	if err != nil {
		return nil, format, err
	}
	for _, rule := range matched {
		s.Pipeline.RoutingResults = append(s.Pipeline.RoutingResults, RoutingResult{
			DestConn: rule.DestConn,
		})
	}
	return message, format, nil
}

// BuildPipeline constructs a Pipeline from PipelineStage configurations.
// If no stages are defined, returns nil (caller should use legacy processing).
func BuildPipeline(
	stages []models.PipelineStage,
	filterPaths []string,
	transforms []models.TransformRule,
	routingRules []models.RoutingRule,
	outputFormat string,
) *Pipeline {
	if len(stages) == 0 {
		return nil
	}

	pipeline := &Pipeline{}

	for _, stage := range stages {
		if !stage.Enabled {
			continue
		}
		switch stage.StageType {
		case "filter":
			paths := filterPaths
			// Override with stage-specific config if provided
			if stage.Config != "" {
				var cfg struct {
					Paths []string `json:"paths"`
				}
				if err := json.Unmarshal([]byte(stage.Config), &cfg); err == nil && len(cfg.Paths) > 0 {
					paths = cfg.Paths
				}
			}
			pipeline.Stages = append(pipeline.Stages, &FilterStage{Paths: paths})

		case "transform":
			pipeline.Stages = append(pipeline.Stages, &TransformStage{Rules: transforms})

		case "translate":
			format := outputFormat
			if stage.Config != "" {
				var cfg struct {
					OutputFormat string `json:"output_format"`
				}
				if err := json.Unmarshal([]byte(stage.Config), &cfg); err == nil && cfg.OutputFormat != "" {
					format = cfg.OutputFormat
				}
			}
			pipeline.Stages = append(pipeline.Stages, &TranslateStage{TargetFormat: format})

		case "route":
			pipeline.Stages = append(pipeline.Stages, &RouteStage{
				Rules:    routingRules,
				Pipeline: pipeline,
			})
		}
	}

	return pipeline
}
