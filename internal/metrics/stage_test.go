package metrics

import (
	"strings"
	"testing"
)

func TestRecordStageDuration_BucketsObservation(t *testing.T) {
	m := New()
	m.Register("p1", "src", "dst")
	m.RecordStageDuration("p1", "validate", 1.5)
	m.RecordStageDuration("p1", "validate", 12.0)
	m.RecordStageDuration("p1", "transform", 0.3)

	text := m.Prometheus()
	if !strings.Contains(text, "mqconnector_stage_duration_ms_bucket") {
		t.Fatalf("missing stage_duration_ms metric:\n%s", text)
	}
	if !strings.Contains(text, `stage="validate"`) {
		t.Fatalf("missing validate stage label:\n%s", text)
	}
	if !strings.Contains(text, `stage="transform"`) {
		t.Fatalf("missing transform stage label:\n%s", text)
	}
	if !strings.Contains(text, `mqconnector_stage_duration_ms_count{pipeline_id="p1",source="src",dest="dst",stage="validate"} 2`) {
		t.Fatalf("expected count=2 for validate:\n%s", text)
	}
	if !strings.Contains(text, `mqconnector_stage_duration_ms_count{pipeline_id="p1",source="src",dest="dst",stage="transform"} 1`) {
		t.Fatalf("expected count=1 for transform:\n%s", text)
	}
}

func TestRecordStageDuration_UnknownPipelineIsNoop(t *testing.T) {
	m := New()
	// No Register call → recording is a silent no-op (matches the
	// RecordSuccess / RecordFailure semantics so race-y deregistration
	// during shutdown doesn't crash).
	m.RecordStageDuration("ghost", "x", 1.0)
	text := m.Prometheus()
	if strings.Contains(text, "stage=\"x\"") {
		t.Fatalf("ghost pipeline must not appear in exposition")
	}
}

func TestRecordStageDuration_EmptyStageNameIgnored(t *testing.T) {
	m := New()
	m.Register("p1", "s", "d")
	m.RecordStageDuration("p1", "", 1.0)
	text := m.Prometheus()
	if strings.Contains(text, "stage=\"\"") {
		t.Fatalf("empty stage name must not emit a label series")
	}
}
