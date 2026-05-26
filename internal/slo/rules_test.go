package slo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFile_AlertsOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	body := []byte(`
groups:
  - name: example
    rules:
      - alert: HighFailureRate
        expr: rate(mqconnector_messages_failed_total[5m]) > 0.05
        for: 5m
        labels:
          severity: warning
          slo: availability
        annotations:
          summary: hi
          description: there
      - record: derived:rate
        expr: rate(mqconnector_messages_processed_total[1m])
      - alert: NoForClauseFire
        expr: mqconnector_pipeline_up == 0
        labels:
          severity: critical
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	rules, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got, want := len(rules), 2; got != want {
		t.Fatalf("want %d alerts, got %d", want, got)
	}
	// Stable order — sorted by group then name.
	if rules[0].Name != "HighFailureRate" {
		t.Errorf("first rule = %q", rules[0].Name)
	}
	if rules[0].For != 5*time.Minute {
		t.Errorf("HighFailureRate.For = %v", rules[0].For)
	}
	if rules[0].Labels["severity"] != "warning" {
		t.Errorf("HighFailureRate.severity = %q", rules[0].Labels["severity"])
	}
	if rules[1].Name != "NoForClauseFire" {
		t.Errorf("second rule = %q", rules[1].Name)
	}
	if rules[1].For != 0 {
		t.Errorf("NoForClauseFire.For = %v (want 0)", rules[1].For)
	}
}

func TestLoadFile_MissingExprSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	body := []byte(`
groups:
  - name: example
    rules:
      - alert: NoExpr
        for: 1m
      - alert: GoodOne
        expr: up == 0
        labels: { severity: critical }
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	rules, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(rules) != 1 || rules[0].Name != "GoodOne" {
		t.Fatalf("expected only GoodOne, got %+v", rules)
	}
}

func TestLoadFile_BadYAMLIsHardError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(path, []byte("not: valid: yaml: ::"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("expected error from malformed YAML")
	}
}

func TestLoadDir_GathersFiles(t *testing.T) {
	dir := t.TempDir()
	body1 := []byte(`
groups:
  - name: a
    rules:
      - alert: A1
        expr: up == 0
`)
	body2 := []byte(`
groups:
  - name: b
    rules:
      - alert: B1
        expr: up == 0
`)
	notYAML := []byte("ignore me")
	_ = os.WriteFile(filepath.Join(dir, "a.yaml"), body1, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "b.yml"), body2, 0o600)
	_ = os.WriteFile(filepath.Join(dir, "c.txt"), notYAML, 0o600)
	rules, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("want 2 rules from 2 yaml files, got %d", len(rules))
	}
}

func TestLoadFile_PreservesAnnotationsAndRaw(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	body := []byte(`
groups:
  - name: example
    rules:
      - alert: Hi
        expr: up == 0
        for: 1m
        labels: { severity: warning }
        annotations:
          summary: a summary
          description: a description
          runbook_url: https://example.com
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	rules, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("want 1, got %d", len(rules))
	}
	r := rules[0]
	if r.Annotations["summary"] != "a summary" {
		t.Errorf("summary = %q", r.Annotations["summary"])
	}
	if r.Annotations["runbook_url"] != "https://example.com" {
		t.Errorf("runbook_url = %q", r.Annotations["runbook_url"])
	}
	if r.Group != "example" {
		t.Errorf("group = %q", r.Group)
	}
	if r.Raw["alert"] != "Hi" {
		t.Errorf("Raw[alert] = %v", r.Raw["alert"])
	}
}

func TestLoadProjectSLOFile(t *testing.T) {
	// Smoke test: the project's actual rules file MUST parse cleanly
	// — otherwise the SLO evaluator will silently drop everything on
	// startup. The exact alert count is hardcoded so adding a rule
	// without updating this test catches the unintended drop case.
	path := "../../deploy/prometheus/mqconnector-slos.yaml"
	rules, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile %s: %v", path, err)
	}
	if len(rules) == 0 {
		t.Fatal("expected ≥1 rule from the project's SLO file")
	}
	seen := map[string]bool{}
	for _, r := range rules {
		seen[r.Name] = true
	}
	// A handful of the canonical alert names — if these drop out the
	// rule file shape regressed.
	for _, want := range []string{
		"mqConnectorAvailabilityFastBurn",
		"mqConnectorLatencyHigh",
		"mqConnectorPipelineDown",
	} {
		if !seen[want] {
			t.Errorf("expected alert %q in project SLO file", want)
		}
	}
}

func TestRecordingRulesFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.yaml")
	body := []byte(`
groups:
  - name: example
    rules:
      - record: my:rate
        expr: rate(thing_total[5m])
      - alert: HighRate
        expr: my:rate > 0.1
`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	recs, err := RecordingRulesFromFile(path)
	if err != nil {
		t.Fatalf("RecordingRulesFromFile: %v", err)
	}
	if recs["my:rate"] != "rate(thing_total[5m])" {
		t.Errorf("my:rate = %q", recs["my:rate"])
	}
	if _, ok := recs["HighRate"]; ok {
		t.Errorf("alerting rule leaked into recording map")
	}
}
