package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"github.com/clbanning/mxj/v2"

	"mqConnector/internal/storage"
)

// TransformStage applies rename/mask/move/set/delete operations to the
// message. Rules are sorted by Order before execution.
type TransformStage struct {
	Rules []*storage.Transform
}

func (s *TransformStage) Name() string { return "transform" }

func (s *TransformStage) Execute(_ context.Context, message []byte, format Format) ([]byte, Format, *Result, error) {
	if len(s.Rules) == 0 {
		return message, format, nil, nil
	}

	data, err := decodeStructured(message, format)
	if err != nil {
		return nil, format, nil, fmt.Errorf("transform: %w", err)
	}
	if data == nil {
		return message, format, nil, nil
	}

	rules := make([]*storage.Transform, len(s.Rules))
	copy(rules, s.Rules)
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Order < rules[j].Order })

	for _, r := range rules {
		if err := applyRule(data, r); err != nil {
			return nil, format, nil, fmt.Errorf("transform %s on %s: %w", r.TransformType, r.SourcePath, err)
		}
	}

	out, err := encodeStructured(data, format)
	if err != nil {
		return nil, format, nil, fmt.Errorf("transform: %w", err)
	}
	return out, format, nil, nil
}

func applyRule(data map[string]any, r *storage.Transform) error {
	switch r.TransformType {
	case "rename", "move":
		v, err := getNestedValue(data, r.SourcePath)
		if err != nil {
			return nil // missing source → silent skip
		}
		deleteNestedValue(data, r.SourcePath)
		return setNestedValue(data, r.TargetPath, v)
	case "mask":
		v, err := getNestedValue(data, r.SourcePath)
		if err != nil {
			return nil
		}
		re, err := regexp.Compile(r.MaskPattern)
		if err != nil {
			return fmt.Errorf("invalid mask pattern: %w", err)
		}
		return setNestedValue(data, r.SourcePath, re.ReplaceAllString(fmt.Sprintf("%v", v), r.MaskReplace))
	case "set":
		return setNestedValue(data, r.SourcePath, r.SetValue)
	case "delete":
		deleteNestedValue(data, r.SourcePath)
		return nil
	default:
		return fmt.Errorf("unknown transform type %q", r.TransformType)
	}
}

func decodeStructured(message []byte, format Format) (map[string]any, error) {
	switch format {
	case FormatJSON:
		var data map[string]any
		if err := json.Unmarshal(message, &data); err != nil {
			return nil, fmt.Errorf("parse json: %w", err)
		}
		return data, nil
	case FormatXML:
		mv, err := mxj.NewMapXml(message)
		if err != nil {
			return nil, fmt.Errorf("parse xml: %w", err)
		}
		return map[string]any(mv), nil
	default:
		return nil, nil
	}
}

func encodeStructured(data map[string]any, format Format) ([]byte, error) {
	switch format {
	case FormatJSON:
		return json.Marshal(data)
	case FormatXML:
		return mxj.Map(data).Xml()
	default:
		return nil, fmt.Errorf("cannot encode %s", format)
	}
}
