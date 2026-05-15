package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"mqConnector/internal/storage"
)

// RouteStage evaluates content-based routing rules against the message. The
// message itself is unchanged; matching rule destinations are returned via
// Result.Destinations for the executor to fan out to.
type RouteStage struct {
	Rules []*storage.RoutingRule
}

func (s *RouteStage) Name() string { return "route" }

func (s *RouteStage) Execute(_ context.Context, message []byte, format Format) ([]byte, Format, *Result, error) {
	if len(s.Rules) == 0 {
		return message, format, nil, nil
	}

	rules := make([]*storage.RoutingRule, 0, len(s.Rules))
	for _, r := range s.Rules {
		if r.Enabled {
			rules = append(rules, r)
		}
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Priority < rules[j].Priority })
	if len(rules) == 0 {
		return message, format, nil, nil
	}

	data, err := decodeStructured(message, format)
	if err != nil {
		return nil, format, nil, fmt.Errorf("route: %w", err)
	}

	result := &Result{}
	seen := map[string]struct{}{}
	for _, r := range rules {
		value, vErr := getNestedValue(data, r.ConditionPath)
		matched := false
		if vErr == nil {
			matched = evaluateCondition(value, r.ConditionOperator, r.ConditionValue)
		} else if r.ConditionOperator == "exists" {
			matched = false // path not found → exists=false
		}
		if matched {
			if _, dup := seen[r.DestinationID]; !dup {
				seen[r.DestinationID] = struct{}{}
				result.Destinations = append(result.Destinations, r.DestinationID)
			}
		}
	}
	if len(result.Destinations) == 0 {
		return message, format, nil, nil
	}
	return message, format, result, nil
}

// evaluateCondition returns whether the given value satisfies the predicate
// op(value, conditionValue). Errors in conversion fall back to string compare.
func evaluateCondition(value any, op, conditionValue string) bool {
	s := fmt.Sprintf("%v", value)
	switch op {
	case "eq":
		return s == conditionValue
	case "neq":
		return s != conditionValue
	case "contains":
		return strings.Contains(s, conditionValue)
	case "regex":
		matched, err := regexp.MatchString(conditionValue, s)
		return err == nil && matched
	case "gt":
		ln, err1 := strconv.ParseFloat(s, 64)
		rn, err2 := strconv.ParseFloat(conditionValue, 64)
		if err1 != nil || err2 != nil {
			return s > conditionValue
		}
		return ln > rn
	case "lt":
		ln, err1 := strconv.ParseFloat(s, 64)
		rn, err2 := strconv.ParseFloat(conditionValue, 64)
		if err1 != nil || err2 != nil {
			return s < conditionValue
		}
		return ln < rn
	case "exists":
		return value != nil
	default:
		return false
	}
}

