package pipeline

import (
	"fmt"
	"strings"
)

// getNestedValue retrieves a value at a dot-separated path.
func getNestedValue(data map[string]any, path string) (any, error) {
	keys := strings.Split(path, ".")
	var current any = data
	for _, k := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("path %q: %q is not an object", path, k)
		}
		v, exists := m[k]
		if !exists {
			return nil, fmt.Errorf("path %q: key %q missing", path, k)
		}
		current = v
	}
	return current, nil
}

// setNestedValue sets a value at a dot-separated path, creating intermediate
// maps as needed.
func setNestedValue(data map[string]any, path string, value any) error {
	keys := strings.Split(path, ".")
	current := data
	for i, k := range keys {
		if i == len(keys)-1 {
			current[k] = value
			return nil
		}
		next, exists := current[k]
		if !exists {
			m := map[string]any{}
			current[k] = m
			current = m
			continue
		}
		nm, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("path %q: %q already set to non-object", path, k)
		}
		current = nm
	}
	return nil
}

// deleteNestedValue removes a value at a dot-separated path. Missing paths are
// silently ignored.
func deleteNestedValue(data map[string]any, path string) {
	keys := strings.Split(path, ".")
	current := data
	for i, k := range keys {
		if i == len(keys)-1 {
			delete(current, k)
			return
		}
		next, exists := current[k]
		if !exists {
			return
		}
		nm, ok := next.(map[string]any)
		if !ok {
			return
		}
		current = nm
	}
}

// removeJSONPaths returns a copy of the JSON document with the given paths
// removed. Invalid paths are silently skipped.
func removeJSONPaths(data map[string]any, paths []string) {
	for _, p := range paths {
		deleteNestedValue(data, p)
	}
}
