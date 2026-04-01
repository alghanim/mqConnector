package tools

import (
	"encoding/json"
	"fmt"
	"mqConnector/models"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/clbanning/mxj/v2"
)

// EvaluateRoutingRules evaluates all routing rules against a message and returns matched rules.
// Rules are evaluated in priority order; all matching rules are returned.
func EvaluateRoutingRules(message []byte, format string, rules []models.RoutingRule) ([]models.RoutingRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}

	// Sort by priority
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority < rules[j].Priority
	})

	var matched []models.RoutingRule
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		value, err := extractValueFromMessage(message, format, rule.ConditionPath)
		if err != nil {
			// Path not found — only "exists" operator should match
			if rule.ConditionOperator == "exists" {
				// value not found means exists=false, skip
				continue
			}
			continue
		}

		if evaluateCondition(value, rule.ConditionOperator, rule.ConditionValue) {
			matched = append(matched, rule)
		}
	}

	return matched, nil
}

// extractValueFromMessage extracts a value at a dot-separated path from a JSON or XML message.
func extractValueFromMessage(message []byte, format, path string) (interface{}, error) {
	var data map[string]interface{}

	if format == "JSON" {
		if err := json.Unmarshal(message, &data); err != nil {
			return nil, fmt.Errorf("failed to parse JSON: %v", err)
		}
	} else if format == "XML" {
		mv, err := mxj.NewMapXml(message)
		if err != nil {
			return nil, fmt.Errorf("failed to parse XML: %v", err)
		}
		data = map[string]interface{}(mv)
	} else {
		return nil, fmt.Errorf("unsupported format: %s", format)
	}

	return getNestedValue(data, path)
}

// evaluateCondition evaluates a single condition against a value.
func evaluateCondition(value interface{}, operator, conditionValue string) bool {
	strValue := fmt.Sprintf("%v", value)

	switch operator {
	case "eq":
		return strValue == conditionValue
	case "neq":
		return strValue != conditionValue
	case "contains":
		return strings.Contains(strValue, conditionValue)
	case "regex":
		matched, err := regexp.MatchString(conditionValue, strValue)
		if err != nil {
			return false
		}
		return matched
	case "gt":
		numVal, err1 := strconv.ParseFloat(strValue, 64)
		numCond, err2 := strconv.ParseFloat(conditionValue, 64)
		if err1 != nil || err2 != nil {
			return strValue > conditionValue // string comparison fallback
		}
		return numVal > numCond
	case "lt":
		numVal, err1 := strconv.ParseFloat(strValue, 64)
		numCond, err2 := strconv.ParseFloat(conditionValue, 64)
		if err1 != nil || err2 != nil {
			return strValue < conditionValue
		}
		return numVal < numCond
	case "exists":
		return value != nil
	default:
		return false
	}
}
