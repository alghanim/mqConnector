package validation

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
)

// Validate dispatches to the correct validator based on format and schema type.
func Validate(message []byte, format string, schemaType string, schemaContent string) error {
	if schemaContent == "" {
		return nil
	}

	switch schemaType {
	case "JSON_SCHEMA":
		return ValidateJSONSchema(message, format, schemaContent)
	case "XSD":
		return ValidateXMLStructure(message, format, schemaContent)
	default:
		return fmt.Errorf("unsupported schema type: %s", schemaType)
	}
}

// ValidateJSONSchema validates a message against a JSON Schema.
// For XML messages, the message is first conceptually checked as JSON.
// This performs structural validation: required fields and type checking.
func ValidateJSONSchema(message []byte, format string, schemaContent string) error {
	var data interface{}

	if format == "JSON" {
		if err := json.Unmarshal(message, &data); err != nil {
			return fmt.Errorf("invalid JSON: %v", err)
		}
	} else {
		return fmt.Errorf("JSON_SCHEMA validation only supports JSON messages")
	}

	// Parse the schema to extract required fields
	var schemaMap map[string]interface{}
	if err := json.Unmarshal([]byte(schemaContent), &schemaMap); err != nil {
		return fmt.Errorf("invalid JSON schema: %v", err)
	}

	return validateAgainstSchema(data, schemaMap)
}

// validateAgainstSchema performs basic structural validation.
func validateAgainstSchema(data interface{}, schemaMap map[string]interface{}) error {
	// Check type
	if expectedType, ok := schemaMap["type"].(string); ok {
		if err := checkType(data, expectedType); err != nil {
			return err
		}
	}

	// Check required fields for objects
	if required, ok := schemaMap["required"].([]interface{}); ok {
		dataMap, ok := data.(map[string]interface{})
		if !ok {
			return fmt.Errorf("expected object but got %T", data)
		}
		for _, req := range required {
			fieldName, ok := req.(string)
			if !ok {
				continue
			}
			if _, exists := dataMap[fieldName]; !exists {
				return fmt.Errorf("required field '%s' is missing", fieldName)
			}
		}
	}

	// Check properties
	if properties, ok := schemaMap["properties"].(map[string]interface{}); ok {
		dataMap, ok := data.(map[string]interface{})
		if !ok {
			return nil // not an object, skip property checks
		}
		for propName, propSchema := range properties {
			if propValue, exists := dataMap[propName]; exists {
				propSchemaMap, ok := propSchema.(map[string]interface{})
				if !ok {
					continue
				}
				if err := validateAgainstSchema(propValue, propSchemaMap); err != nil {
					return fmt.Errorf("field '%s': %v", propName, err)
				}
			}
		}
	}

	return nil
}

// checkType validates that data matches the expected JSON Schema type.
func checkType(data interface{}, expectedType string) error {
	switch expectedType {
	case "object":
		if _, ok := data.(map[string]interface{}); !ok {
			return fmt.Errorf("expected object but got %T", data)
		}
	case "array":
		if _, ok := data.([]interface{}); !ok {
			return fmt.Errorf("expected array but got %T", data)
		}
	case "string":
		if _, ok := data.(string); !ok {
			return fmt.Errorf("expected string but got %T", data)
		}
	case "number", "integer":
		if _, ok := data.(float64); !ok {
			return fmt.Errorf("expected number but got %T", data)
		}
	case "boolean":
		if _, ok := data.(bool); !ok {
			return fmt.Errorf("expected boolean but got %T", data)
		}
	}
	return nil
}

// ValidateXMLStructure performs basic structural validation of XML messages.
// It checks that the XML is well-formed and that required elements exist.
// Full XSD validation is not supported in pure Go — this validates structure only.
func ValidateXMLStructure(message []byte, format string, schemaContent string) error {
	if format != "XML" {
		return fmt.Errorf("XSD validation only supports XML messages")
	}

	// Check well-formedness
	if err := xml.Unmarshal(message, new(interface{})); err != nil {
		return fmt.Errorf("malformed XML: %v", err)
	}

	// Parse the schema content as a simple required-elements list (one per line)
	// Format: each line is a required element name
	requiredElements := parseRequiredElements(schemaContent)
	if len(requiredElements) == 0 {
		return nil
	}

	xmlStr := string(message)
	for _, elem := range requiredElements {
		if !strings.Contains(xmlStr, "<"+elem) {
			return fmt.Errorf("required XML element '<%s>' not found", elem)
		}
	}

	return nil
}

// parseRequiredElements parses a newline-separated list of required element names.
func parseRequiredElements(content string) []string {
	var elements []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			elements = append(elements, trimmed)
		}
	}
	return elements
}
