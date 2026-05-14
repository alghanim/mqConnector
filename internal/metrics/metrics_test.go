package metrics

import (
	"strings"
	"testing"
)

func TestRegisterAndUnregister(t *testing.T) {
	s := New()
	s.Register("p1", "src", "dst")
	if s.ActiveCount() != 1 {
		t.Errorf("expected 1 pipeline, got %d", s.ActiveCount())
	}
	s.Unregister("p1")
	if s.ActiveCount() != 0 {
		t.Errorf("expected 0 after unregister")
	}
}

func TestRecordSuccess(t *testing.T) {
	s := New()
	s.Register("p1", "src", "dst")
	s.RecordSuccess("p1", 100, 10.0)
	s.RecordSuccess("p1", 200, 20.0)
	snap := s.Snapshot()
	if snap["p1"].MessagesProcessed != 2 {
		t.Errorf("processed: %d", snap["p1"].MessagesProcessed)
	}
	if snap["p1"].BytesProcessed != 300 {
		t.Errorf("bytes: %d", snap["p1"].BytesProcessed)
	}
	if snap["p1"].AvgLatencyMs != 15.0 {
		t.Errorf("avg latency: %f", snap["p1"].AvgLatencyMs)
	}
}

func TestRecordFailure(t *testing.T) {
	s := New()
	s.Register("p1", "src", "dst")
	s.RecordFailure("p1")
	s.RecordFailure("p1")
	if s.Snapshot()["p1"].MessagesFailed != 2 {
		t.Errorf("failures: %d", s.Snapshot()["p1"].MessagesFailed)
	}
}

func TestSetStatus(t *testing.T) {
	s := New()
	s.Register("p1", "src", "dst")
	s.SetStatus("p1", "error", "boom")
	snap := s.Snapshot()
	if snap["p1"].Status != "error" || snap["p1"].LastError != "boom" {
		t.Errorf("status not updated: %+v", snap["p1"])
	}
}

func TestPrometheus(t *testing.T) {
	s := New()
	s.Register("p1", "src", "dst")
	s.RecordSuccess("p1", 100, 10.0)
	out := s.Prometheus()
	if !strings.Contains(out, "mqconnector_messages_processed_total") {
		t.Error("missing processed metric")
	}
	if !strings.Contains(out, `pipeline_id="p1"`) {
		t.Error("missing pipeline_id label")
	}
}

func TestNonexistentPipeline_NoPanic(t *testing.T) {
	s := New()
	s.RecordSuccess("ghost", 1, 1)
	s.RecordFailure("ghost")
	s.SetStatus("ghost", "x", "y")
}
