package tools

import (
	"fmt"
	"strconv"
	"strings"
)

// splitScriptLines splits a script into individual lines, handling semicolons.
func splitScriptLines(script string) []string {
	// Split by newlines first, then by semicolons
	var lines []string
	for _, line := range strings.Split(script, "\n") {
		for _, part := range strings.Split(line, ";") {
			trimmed := strings.TrimSpace(part)
			if trimmed != "" {
				lines = append(lines, trimmed)
			}
		}
	}
	return lines
}

// trimLine trims whitespace and trailing semicolons.
func trimLine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(line, ";")
	return strings.TrimSpace(line)
}

// isDeleteOp checks if a line is a delete operation.
func isDeleteOp(line string) bool {
	return strings.HasPrefix(line, "delete ")
}

// isAssignOp checks if a line is an assignment operation.
func isAssignOp(line string) bool {
	return strings.Contains(line, "=") && !strings.HasPrefix(line, "//")
}

// evalDelete handles: delete msg.field.path
func evalDelete(line string, data map[string]interface{}) error {
	// "delete msg.field.path" -> extract "field.path"
	path := strings.TrimPrefix(line, "delete ")
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "msg.")
	if path == "" || path == "msg" {
		return fmt.Errorf("cannot delete root msg object")
	}
	deleteNestedValue(data, path)
	return nil
}

// evalAssign handles: msg.field = value
func evalAssign(line string, data map[string]interface{}) error {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid assignment")
	}

	lhs := strings.TrimSpace(parts[0])
	rhs := strings.TrimSpace(parts[1])

	// Extract target path from lhs
	targetPath := strings.TrimPrefix(lhs, "msg.")
	if targetPath == lhs {
		// Not a msg.xxx assignment, skip
		return nil
	}

	// Evaluate rhs
	value, err := evalExpression(rhs, data)
	if err != nil {
		return err
	}

	return setNestedValue(data, targetPath, value)
}

// evalExpression evaluates the right-hand side of an assignment.
func evalExpression(expr string, data map[string]interface{}) (interface{}, error) {
	expr = strings.TrimSpace(expr)

	// Check for arithmetic: msg.a + msg.b, msg.a * msg.b, etc.
	for _, op := range []string{" + ", " - ", " * ", " / "} {
		if strings.Contains(expr, op) {
			parts := strings.SplitN(expr, op, 2)
			if len(parts) == 2 {
				left, err := evalExpression(strings.TrimSpace(parts[0]), data)
				if err != nil {
					return nil, err
				}
				right, err := evalExpression(strings.TrimSpace(parts[1]), data)
				if err != nil {
					return nil, err
				}

				leftNum, leftOk := toFloat64(left)
				rightNum, rightOk := toFloat64(right)

				if leftOk && rightOk {
					switch strings.TrimSpace(op) {
					case "+":
						return leftNum + rightNum, nil
					case "-":
						return leftNum - rightNum, nil
					case "*":
						return leftNum * rightNum, nil
					case "/":
						if rightNum == 0 {
							return nil, fmt.Errorf("division by zero")
						}
						return leftNum / rightNum, nil
					}
				}

				// String concatenation for +
				if strings.TrimSpace(op) == "+" {
					return fmt.Sprintf("%v%v", left, right), nil
				}
			}
		}
	}

	// String literal: "value" or 'value'
	if (strings.HasPrefix(expr, "\"") && strings.HasSuffix(expr, "\"")) ||
		(strings.HasPrefix(expr, "'") && strings.HasSuffix(expr, "'")) {
		return expr[1 : len(expr)-1], nil
	}

	// Boolean literals
	if expr == "true" {
		return true, nil
	}
	if expr == "false" {
		return false, nil
	}

	// Null
	if expr == "null" || expr == "nil" {
		return nil, nil
	}

	// Numeric literal
	if num, err := strconv.ParseFloat(expr, 64); err == nil {
		return num, nil
	}

	// Integer literal
	if num, err := strconv.ParseInt(expr, 10, 64); err == nil {
		return num, nil
	}

	// msg.field reference
	if strings.HasPrefix(expr, "msg.") {
		path := strings.TrimPrefix(expr, "msg.")
		val, err := getNestedValue(data, path)
		if err != nil {
			return nil, nil // field not found, return nil
		}
		return val, nil
	}

	// Date.now() — returns Unix timestamp in milliseconds
	if expr == "Date.now()" {
		// Return current time in ms (import would be needed, use a constant approach)
		return float64(currentTimeMs()), nil
	}

	// Fallback: return as string
	return expr, nil
}

// toFloat64 tries to convert an interface{} to float64.
func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case int32:
		return float64(n), true
	case string:
		if f, err := strconv.ParseFloat(n, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}
