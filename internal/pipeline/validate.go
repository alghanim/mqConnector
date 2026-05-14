package pipeline

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
)

// ValidateStage performs lightweight structural validation. Two schema types
// are supported:
//
//   - "json_schema" — a tiny subset of JSON Schema (type, required, properties)
//   - "xsd"         — a newline-separated list of required element names
//
// Full JSON Schema and XSD validation are explicitly out of scope; this stage
// is for "fast-reject on missing required fields" cases.
type ValidateStage struct {
	SchemaType string
	Content    string
}

func (s *ValidateStage) Name() string { return "validate" }

func (s *ValidateStage) Execute(_ context.Context, message []byte, format Format) ([]byte, Format, *Result, error) {
	if s.Content == "" {
		return message, format, nil, nil
	}
	switch strings.ToLower(s.SchemaType) {
	case "json_schema":
		if format != FormatJSON {
			return nil, format, nil, fmt.Errorf("validate: json_schema requires JSON messages")
		}
		if err := validateJSONSchema(message, s.Content); err != nil {
			return nil, format, nil, fmt.Errorf("validate: %w", err)
		}
	case "xsd":
		if format != FormatXML {
			return nil, format, nil, fmt.Errorf("validate: xsd requires XML messages")
		}
		if err := validateXMLStructure(message, s.Content); err != nil {
			return nil, format, nil, fmt.Errorf("validate: %w", err)
		}
	default:
		return nil, format, nil, fmt.Errorf("validate: unknown schema_type %q", s.SchemaType)
	}
	return message, format, nil, nil
}

func validateJSONSchema(message []byte, schemaContent string) error {
	var data any
	if err := json.Unmarshal(message, &data); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(schemaContent), &schema); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	return walkSchema(data, schema)
}

func walkSchema(value any, schema map[string]any) error {
	if t, ok := schema["type"].(string); ok {
		if err := checkType(value, t); err != nil {
			return err
		}
	}
	if req, ok := schema["required"].([]any); ok {
		m, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("expected object")
		}
		for _, name := range req {
			s, _ := name.(string)
			if _, present := m[s]; !present {
				return fmt.Errorf("required field %q missing", s)
			}
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		for k, sub := range props {
			subMap, ok := sub.(map[string]any)
			if !ok {
				continue
			}
			if v, present := m[k]; present {
				if err := walkSchema(v, subMap); err != nil {
					return fmt.Errorf("field %q: %w", k, err)
				}
			}
		}
	}
	return nil
}

func checkType(value any, expected string) error {
	switch expected {
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("expected object, got %T", value)
		}
	case "array":
		if _, ok := value.([]any); !ok {
			return fmt.Errorf("expected array, got %T", value)
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("expected string, got %T", value)
		}
	case "number", "integer":
		switch value.(type) {
		case float64, json.Number:
		default:
			return fmt.Errorf("expected number, got %T", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected boolean, got %T", value)
		}
	}
	return nil
}

func validateXMLStructure(message []byte, schemaContent string) error {
	if err := xml.Unmarshal(message, new(any)); err != nil {
		return fmt.Errorf("malformed XML: %w", err)
	}
	required := parseRequiredElements(schemaContent)
	if len(required) == 0 {
		return nil
	}
	s := string(message)
	for _, elem := range required {
		if !strings.Contains(s, "<"+elem) {
			return fmt.Errorf("required element <%s> missing", elem)
		}
	}
	return nil
}

func parseRequiredElements(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "#") {
			out = append(out, t)
		}
	}
	return out
}
