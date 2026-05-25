package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"mqConnector/internal/ai"
)

// captureAudit is a local AuditLogger that records every row for
// inspection. Mirrors the helper in internal/ai/ai_test.go — copied
// to keep the test self-contained (no cross-package test imports).
type captureAudit struct {
	mu   sync.Mutex
	rows []ai.AuditRow
}

func (c *captureAudit) Log(_ context.Context, row ai.AuditRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append(c.rows, row)
}

// TestName_HappyPath_UnmarshalsAndAuditFires drives Name through the
// FakeProvider with a canned JSON response and asserts the parsed
// result + the audit row both land.
func TestName_HappyPath_UnmarshalsAndAuditFires(t *testing.T) {
	fake := ai.NewFakeProvider()
	aud := &captureAudit{}
	fake.SetAudit(aud)
	fake.SetStructured(ai.CapDLQClusterNaming, json.RawMessage(`{
		"title": "Missing customer.id on validate",
		"summary": "All failures occurred in the validate stage with the customer.id field absent. Likely caused by a producer change that dropped the field from the payload.",
		"suggestion": "Check the producer's recent deploys for a schema change."
	}`))

	cfg := ai.Config{Enabled: true, Features: []ai.Capability{ai.CapDLQClusterNaming}, AuditEvery: true}
	out, err := Name(context.Background(), fake, aud, cfg, NameRequest{
		Fingerprint:       "fp-1",
		Template:          "validation: missing field <field>",
		Count:             5,
		PipelinesAffected: []string{"p1"},
		FailingStages:     []string{"validate"},
		SampleErrors:      []string{"validation: missing field customer.id"},
	})
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	if !strings.Contains(out.Title, "customer.id") {
		t.Errorf("title = %q, want mention of customer.id", out.Title)
	}
	if out.Suggestion == "" || out.Summary == "" {
		t.Errorf("missing summary/suggestion: %+v", out)
	}
	// The audit row is emitted by the FakeProvider's emit hook —
	// confirms the production wiring (provider owns the audit emit)
	// is being exercised end-to-end.
	aud.mu.Lock()
	rows := append([]ai.AuditRow(nil), aud.rows...)
	aud.mu.Unlock()
	if len(rows) != 1 || rows[0].Feature != ai.CapDLQClusterNaming || rows[0].Outcome != "ok" {
		t.Errorf("audit rows = %+v", rows)
	}
}

// TestName_FeatureOff_ReturnsErrAINotAvailable proves the gate fires
// before any provider work happens.
func TestName_FeatureOff_ReturnsErrAINotAvailable(t *testing.T) {
	fake := ai.NewFakeProvider()
	fake.SetStructured(ai.CapDLQClusterNaming, json.RawMessage(`{"title":"x"}`))
	cfg := ai.Config{Enabled: false}
	_, err := Name(context.Background(), fake, ai.NoopAuditLogger{}, cfg, NameRequest{
		Fingerprint: "fp", Template: "t",
	})
	if !errors.Is(err, ErrAINotAvailable) {
		t.Errorf("err = %v, want errors.Is ErrAINotAvailable", err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("fake provider was called %d times, want 0 (gate must short-circuit)", len(fake.Calls()))
	}
}

// TestName_ProviderError_PropagatesAsAIError asserts a provider
// failure surfaces with an *ai.Error wrapper so the caller can
// inspect Kind for retry decisions.
func TestName_ProviderError_PropagatesAsAIError(t *testing.T) {
	fake := ai.NewFakeProvider()
	fake.SetError(ai.CapDLQClusterNaming, &ai.Error{Kind: "transport", Err: errors.New("EOF")})
	cfg := ai.Config{Enabled: true, Features: []ai.Capability{ai.CapDLQClusterNaming}}
	_, err := Name(context.Background(), fake, ai.NoopAuditLogger{}, cfg, NameRequest{
		Fingerprint: "fp", Template: "t",
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrAINotAvailable) {
		// Transport errors should map to ErrAINotAvailable via the
		// *ai.Error.Is hook so the caller can fall back without
		// enumerating Kind strings.
		t.Errorf("err = %v, want errors.Is ErrAINotAvailable", err)
	}
}

// TestName_BadJSON_FailsWithBadBody asserts a model that emits
// non-JSON is caught and wrapped as bad_body, not silently returned.
func TestName_BadJSON_FailsWithBadBody(t *testing.T) {
	fake := ai.NewFakeProvider()
	// Set an invalid JSON envelope. The fake validates the schema as
	// raw bytes; what matters is the unmarshal failure on the Name
	// side once we shape the result.
	fake.SetStructured(ai.CapDLQClusterNaming, json.RawMessage(`{"title": 12345}`))
	cfg := ai.Config{Enabled: true, Features: []ai.Capability{ai.CapDLQClusterNaming}}
	_, err := Name(context.Background(), fake, ai.NoopAuditLogger{}, cfg, NameRequest{
		Fingerprint: "fp", Template: "t",
	})
	if err == nil {
		t.Fatal("expected JSON parse failure")
	}
	var aiErr *ai.Error
	if !errors.As(err, &aiErr) || aiErr.Kind != "bad_body" {
		t.Errorf("err = %+v, want *ai.Error{Kind:bad_body}", err)
	}
}

// TestName_ClampsOverlongFields proves a model that ignores the
// length budget still produces UI-safe output.
func TestName_ClampsOverlongFields(t *testing.T) {
	long := strings.Repeat("x", 500)
	fake := ai.NewFakeProvider()
	fake.SetStructured(ai.CapDLQClusterNaming, json.RawMessage(`{
		"title":      "`+long+`",
		"summary":    "`+long+`",
		"suggestion": "`+long+`"
	}`))
	cfg := ai.Config{Enabled: true, Features: []ai.Capability{ai.CapDLQClusterNaming}}
	out, err := Name(context.Background(), fake, ai.NoopAuditLogger{}, cfg, NameRequest{
		Fingerprint: "fp", Template: "t",
	})
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	// 80 char budget + 3-byte UTF-8 ellipsis.
	if len(out.Title) > 83 {
		t.Errorf("title length = %d, want <= 83 (80 + UTF-8 ellipsis)", len(out.Title))
	}
	if len(out.Summary) > 243 {
		t.Errorf("summary length = %d, want <= 243", len(out.Summary))
	}
}

// TestRenderNamePrompt_IncludesEverything is a snapshot-style test on
// the prompt shape so a future change is visible in the diff (the
// sample LLM request body the report quotes comes from here).
func TestRenderNamePrompt_IncludesEverything(t *testing.T) {
	prompt := renderNamePrompt(NameRequest{
		Fingerprint:       "abc",
		Template:          "validation: missing field <field>",
		Count:             7,
		PipelinesAffected: []string{"p1", "p2"},
		FailingStages:     []string{"validate"},
		SampleErrors:      []string{"validation: missing field customer.id"},
	})
	for _, want := range []string{
		"Cluster fingerprint: abc",
		"validation: missing field <field>",
		"Failure count: 7",
		"Affected pipelines: p1, p2",
		"Failing stages: validate",
		"1. validation: missing field customer.id",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, prompt)
		}
	}
}
