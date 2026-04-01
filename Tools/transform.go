package tools

import (
	"encoding/json"
	"fmt"
	"mqConnector/models"
	"regexp"
	"sort"
	"strings"

	"github.com/clbanning/mxj/v2"
)

// ApplyTransforms applies a list of transformation rules to a message.
// Rules are applied in order (sorted by Order field).
func ApplyTransforms(message []byte, format string, rules []models.TransformRule) ([]byte, error) {
	if len(rules) == 0 {
		return message, nil
	}

	// Sort by order
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Order < rules[j].Order
	})

	var data map[string]interface{}

	if format == "XML" {
		mv, err := mxj.NewMapXml(message)
		if err != nil {
			return nil, fmt.Errorf("failed to parse XML for transformation: %v", err)
		}
		data = map[string]interface{}(mv)
	} else if format == "JSON" {
		if err := json.Unmarshal(message, &data); err != nil {
			return nil, fmt.Errorf("failed to parse JSON for transformation: %v", err)
		}
	} else {
		return message, nil
	}

	for _, rule := range rules {
		var err error
		switch rule.TransformType {
		case "rename":
			err = renameField(data, rule.SourcePath, rule.TargetPath)
		case "mask":
			err = maskFieldValue(data, rule.SourcePath, rule.MaskPattern, rule.MaskReplace)
		case "move":
			err = moveField(data, rule.SourcePath, rule.TargetPath)
		default:
			return nil, fmt.Errorf("unsupported transform type: %s", rule.TransformType)
		}
		if err != nil {
			return nil, fmt.Errorf("transform %s failed on path '%s': %v", rule.TransformType, rule.SourcePath, err)
		}
	}

	// Convert back to original format
	if format == "XML" {
		xmlData, err := mxj.Map(data).Xml()
		if err != nil {
			return nil, fmt.Errorf("failed to convert transformed data back to XML: %v", err)
		}
		return xmlData, nil
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transformed data to JSON: %v", err)
	}
	return jsonData, nil
}

// renameField renames a field at sourcePath to targetPath (target must be a sibling or simple rename).
func renameField(data map[string]interface{}, sourcePath, targetPath string) error {
	value, err := getNestedValue(data, sourcePath)
	if err != nil {
		return nil // field doesn't exist, skip silently
	}
	deleteNestedValue(data, sourcePath)
	return setNestedValue(data, targetPath, value)
}

// maskFieldValue applies a regex mask to a field's string value.
func maskFieldValue(data map[string]interface{}, sourcePath, pattern, replacement string) error {
	value, err := getNestedValue(data, sourcePath)
	if err != nil {
		return nil // field doesn't exist, skip silently
	}

	strValue := fmt.Sprintf("%v", value)
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("invalid mask pattern '%s': %v", pattern, err)
	}

	masked := re.ReplaceAllString(strValue, replacement)
	return setNestedValue(data, sourcePath, masked)
}

// moveField moves a field from sourcePath to targetPath.
func moveField(data map[string]interface{}, sourcePath, targetPath string) error {
	return renameField(data, sourcePath, targetPath)
}

// getNestedValue retrieves a value at a dot-separated path.
func getNestedValue(data map[string]interface{}, path string) (interface{}, error) {
	keys := strings.Split(path, ".")
	current := interface{}(data)

	for _, key := range keys {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("path '%s' is invalid: intermediate value is not an object", path)
		}
		val, exists := m[key]
		if !exists {
			return nil, fmt.Errorf("path '%s' not found at key '%s'", path, key)
		}
		current = val
	}

	return current, nil
}

// setNestedValue sets a value at a dot-separated path, creating intermediate maps as needed.
func setNestedValue(data map[string]interface{}, path string, value interface{}) error {
	keys := strings.Split(path, ".")
	current := data

	for i, key := range keys {
		if i == len(keys)-1 {
			current[key] = value
			return nil
		}
		next, exists := current[key]
		if !exists {
			newMap := make(map[string]interface{})
			current[key] = newMap
			current = newMap
		} else if nextMap, ok := next.(map[string]interface{}); ok {
			current = nextMap
		} else {
			return fmt.Errorf("cannot create nested path: '%s' is not an object", key)
		}
	}

	return nil
}

// deleteNestedValue deletes a value at a dot-separated path.
func deleteNestedValue(data map[string]interface{}, path string) {
	keys := strings.Split(path, ".")
	current := data

	for i, key := range keys {
		if i == len(keys)-1 {
			delete(current, key)
			return
		}
		next, exists := current[key]
		if !exists {
			return
		}
		nextMap, ok := next.(map[string]interface{})
		if !ok {
			return
		}
		current = nextMap
	}
}
