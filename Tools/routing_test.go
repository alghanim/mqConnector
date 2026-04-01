package tools

import (
	"mqConnector/models"
	"testing"
)

func TestEvaluateRoutingRules_Eq(t *testing.T) {
	msg := []byte(`{"region":"EU","priority":"high"}`)
	rules := []models.RoutingRule{
		{ConditionPath: "region", ConditionOperator: "eq", ConditionValue: "EU", Priority: 1, Enabled: true, DestConn: map[string]string{"queueName": "eu-queue"}},
		{ConditionPath: "region", ConditionOperator: "eq", ConditionValue: "US", Priority: 2, Enabled: true, DestConn: map[string]string{"queueName": "us-queue"}},
	}

	matched, err := EvaluateRoutingRules(msg, "JSON", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
	if matched[0].DestConn["queueName"] != "eu-queue" {
		t.Errorf("expected eu-queue, got %s", matched[0].DestConn["queueName"])
	}
}

func TestEvaluateRoutingRules_Contains(t *testing.T) {
	msg := []byte(`{"message":"Error: connection timeout"}`)
	rules := []models.RoutingRule{
		{ConditionPath: "message", ConditionOperator: "contains", ConditionValue: "Error", Priority: 1, Enabled: true},
	}

	matched, err := EvaluateRoutingRules(msg, "JSON", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 1 {
		t.Error("expected match for contains 'Error'")
	}
}

func TestEvaluateRoutingRules_Gt(t *testing.T) {
	msg := []byte(`{"amount":150.50}`)
	rules := []models.RoutingRule{
		{ConditionPath: "amount", ConditionOperator: "gt", ConditionValue: "100", Priority: 1, Enabled: true},
		{ConditionPath: "amount", ConditionOperator: "gt", ConditionValue: "200", Priority: 2, Enabled: true},
	}

	matched, err := EvaluateRoutingRules(msg, "JSON", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matched))
	}
}

func TestEvaluateRoutingRules_Regex(t *testing.T) {
	msg := []byte(`{"email":"john@example.com"}`)
	rules := []models.RoutingRule{
		{ConditionPath: "email", ConditionOperator: "regex", ConditionValue: `.*@example\.com$`, Priority: 1, Enabled: true},
	}

	matched, err := EvaluateRoutingRules(msg, "JSON", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 1 {
		t.Error("expected regex match for email")
	}
}

func TestEvaluateRoutingRules_Disabled(t *testing.T) {
	msg := []byte(`{"region":"EU"}`)
	rules := []models.RoutingRule{
		{ConditionPath: "region", ConditionOperator: "eq", ConditionValue: "EU", Priority: 1, Enabled: false},
	}

	matched, err := EvaluateRoutingRules(msg, "JSON", rules)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(matched) != 0 {
		t.Error("disabled rules should not match")
	}
}

func TestEvaluateRoutingRules_EmptyRules(t *testing.T) {
	msg := []byte(`{"test":true}`)
	matched, err := EvaluateRoutingRules(msg, "JSON", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched != nil {
		t.Error("expected nil for empty rules")
	}
}

func TestEvaluateCondition(t *testing.T) {
	tests := []struct {
		value    interface{}
		operator string
		condVal  string
		expected bool
	}{
		{"hello", "eq", "hello", true},
		{"hello", "eq", "world", false},
		{"hello", "neq", "world", true},
		{"hello world", "contains", "world", true},
		{"hello", "contains", "world", false},
		{150.0, "gt", "100", true},
		{50.0, "gt", "100", false},
		{50.0, "lt", "100", true},
		{"test@email.com", "regex", `.*@email\.com`, true},
		{"something", "exists", "", true},
		{nil, "exists", "", false},
	}

	for _, tt := range tests {
		result := evaluateCondition(tt.value, tt.operator, tt.condVal)
		if result != tt.expected {
			t.Errorf("evaluateCondition(%v, %s, %s) = %v, want %v",
				tt.value, tt.operator, tt.condVal, result, tt.expected)
		}
	}
}
