package dlq

import (
	"encoding/json"
	"strings"
	"testing"

	"mqConnector/internal/storage"
)

func TestRedactor_NoRules(t *testing.T) {
	r := NewRedactor()
	out, mutated := r.Apply([]byte(`{"a":1}`), nil)
	if mutated {
		t.Fatalf("no rules → mutated=false expected")
	}
	if string(out) != `{"a":1}` {
		t.Fatalf("no rules → payload unchanged expected, got %s", out)
	}
}

func TestRedactor_RegexRule(t *testing.T) {
	r := NewRedactor()
	rules := []storage.DLQRedactionRule{
		{
			RuleKind:    "regex",
			Pattern:     `\b\d{3}-\d{2}-\d{4}\b`,
			MaskReplace: "[SSN]",
			Enabled:     true,
		},
	}
	out, mutated := r.Apply([]byte(`patient ssn 123-45-6789 admitted`), rules)
	if !mutated {
		t.Fatalf("regex rule should have fired")
	}
	if !strings.Contains(string(out), "[SSN]") {
		t.Fatalf("mask not applied: %s", out)
	}
	if strings.Contains(string(out), "123-45-6789") {
		t.Fatalf("raw SSN leaked: %s", out)
	}
}

func TestRedactor_JSONPathDotKey(t *testing.T) {
	r := NewRedactor()
	rules := []storage.DLQRedactionRule{
		{RuleKind: "jsonpath", Pattern: "$.patient.ssn", MaskReplace: "***", Enabled: true},
	}
	in := []byte(`{"patient":{"name":"Ali","ssn":"123-45-6789"},"meta":{"v":1}}`)
	out, mutated := r.Apply(in, rules)
	if !mutated {
		t.Fatalf("jsonpath rule should have fired")
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output not JSON: %v", err)
	}
	if got["patient"].(map[string]any)["ssn"] != "***" {
		t.Fatalf("ssn not masked: %v", got)
	}
	// Sibling field is untouched.
	if got["patient"].(map[string]any)["name"] != "Ali" {
		t.Fatalf("sibling field corrupted: %v", got)
	}
}

func TestRedactor_JSONPathRecursive(t *testing.T) {
	r := NewRedactor()
	rules := []storage.DLQRedactionRule{
		{RuleKind: "jsonpath", Pattern: "$..card_number", MaskReplace: "[REDACTED]", Enabled: true},
	}
	in := []byte(`{"items":[{"card_number":"4111","amt":10},{"card_number":"5222","amt":20}],"total":30}`)
	out, mutated := r.Apply(in, rules)
	if !mutated {
		t.Fatalf("recursive rule should have fired")
	}
	if strings.Contains(string(out), "4111") || strings.Contains(string(out), "5222") {
		t.Fatalf("card numbers leaked: %s", out)
	}
	if !strings.Contains(string(out), "[REDACTED]") {
		t.Fatalf("mask absent: %s", out)
	}
}

func TestRedactor_JSONPathArrayWildcard(t *testing.T) {
	r := NewRedactor()
	rules := []storage.DLQRedactionRule{
		{RuleKind: "jsonpath", Pattern: "$.tokens[*]", MaskReplace: "x", Enabled: true},
	}
	in := []byte(`{"tokens":["a","b","c"]}`)
	out, mutated := r.Apply(in, rules)
	if !mutated {
		t.Fatalf("wildcard rule should have fired")
	}
	if string(out) != `{"tokens":["x","x","x"]}` {
		t.Fatalf("wildcard didn't mask all elements: %s", out)
	}
}

func TestRedactor_JSONPathOnNonJSON(t *testing.T) {
	r := NewRedactor()
	rules := []storage.DLQRedactionRule{
		{RuleKind: "jsonpath", Pattern: "$.x", MaskReplace: "*", Enabled: true},
	}
	out, mutated := r.Apply([]byte(`<xml>not json</xml>`), rules)
	if mutated {
		t.Fatalf("non-JSON payload should be a no-op for jsonpath, got mutated=true")
	}
	if string(out) != `<xml>not json</xml>` {
		t.Fatalf("non-JSON payload corrupted: %s", out)
	}
}

func TestRedactor_DisabledRulesSkipped(t *testing.T) {
	r := NewRedactor()
	rules := []storage.DLQRedactionRule{
		{RuleKind: "regex", Pattern: `secret`, MaskReplace: "X", Enabled: false},
	}
	out, mutated := r.Apply([]byte(`secret value`), rules)
	if mutated {
		t.Fatalf("disabled rule should be skipped")
	}
	if string(out) != "secret value" {
		t.Fatalf("disabled rule still mutated: %s", out)
	}
}

func TestRedactor_OrderedComposition(t *testing.T) {
	r := NewRedactor()
	rules := []storage.DLQRedactionRule{
		{RuleKind: "regex", Pattern: `4\d{15}`, MaskReplace: "[CC]", Enabled: true},
		{RuleKind: "jsonpath", Pattern: "$..pwd", MaskReplace: "[PW]", Enabled: true},
	}
	in := []byte(`{"creditcard":"4111111111111111","auth":{"pwd":"hunter2"}}`)
	out, mutated := r.Apply(in, rules)
	if !mutated {
		t.Fatalf("both rules should have fired")
	}
	if strings.Contains(string(out), "4111111111111111") {
		t.Fatalf("CC leaked: %s", out)
	}
	if strings.Contains(string(out), "hunter2") {
		t.Fatalf("password leaked: %s", out)
	}
}

func TestRedactor_NoMatchNoMutate(t *testing.T) {
	r := NewRedactor()
	rules := []storage.DLQRedactionRule{
		{RuleKind: "regex", Pattern: `secret`, MaskReplace: "X", Enabled: true},
	}
	out, mutated := r.Apply([]byte(`{"a":1}`), rules)
	if mutated {
		t.Fatalf("no match → mutated=false expected")
	}
	if string(out) != `{"a":1}` {
		t.Fatalf("payload changed even though no match: %s", out)
	}
}

func TestRedactor_MalformedRegexSkipped(t *testing.T) {
	r := NewRedactor()
	rules := []storage.DLQRedactionRule{
		{RuleKind: "regex", Pattern: `(`, MaskReplace: "X", Enabled: true},
		{RuleKind: "regex", Pattern: `secret`, MaskReplace: "Y", Enabled: true},
	}
	out, mutated := r.Apply([]byte(`one secret thing`), rules)
	if !mutated {
		t.Fatalf("valid rule should still fire alongside malformed one")
	}
	if !strings.Contains(string(out), "Y") {
		t.Fatalf("second rule didn't apply: %s", out)
	}
}

func TestValidateRules(t *testing.T) {
	good := []storage.DLQRedactionRule{
		{RuleKind: "regex", Pattern: `\d+`, MaskReplace: "X"},
		{RuleKind: "jsonpath", Pattern: "$.a.b", MaskReplace: "Y"},
		{RuleKind: "jsonpath", Pattern: "$..nested", MaskReplace: "Z"},
		{RuleKind: "jsonpath", Pattern: "simple_key", MaskReplace: "W"},
	}
	if err := ValidateRules(good); err != nil {
		t.Fatalf("good ruleset rejected: %v", err)
	}
	bad := []storage.DLQRedactionRule{
		{RuleKind: "regex", Pattern: `(`, MaskReplace: "X"},
	}
	if err := ValidateRules(bad); err == nil {
		t.Fatalf("malformed regex should have errored")
	}
	weird := []storage.DLQRedactionRule{
		{RuleKind: "fancy", Pattern: "x", MaskReplace: "y"},
	}
	if err := ValidateRules(weird); err == nil {
		t.Fatalf("unknown rule_kind should have errored")
	}
}
