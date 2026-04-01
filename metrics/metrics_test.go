package metrics

import (
	"strings"
	"testing"
)

func TestMetricsStore_RegisterAndUnregister(t *testing.T) {
	store := &MetricsStore{
		connections: make(map[string]*ConnectionMetrics),
	}

	store.Register("filter1", "sourceQ", "destQ")

	all := store.GetAll()
	if len(all) != 1 {
		t.Fatalf("expected 1 connection, got %d", len(all))
	}
	if all["filter1"].SourceQueue != "sourceQ" {
		t.Errorf("expected sourceQ, got %s", all["filter1"].SourceQueue)
	}

	store.Unregister("filter1")
	all = store.GetAll()
	if len(all) != 0 {
		t.Error("expected 0 connections after unregister")
	}
}

func TestMetricsStore_RecordSuccess(t *testing.T) {
	store := &MetricsStore{
		connections: make(map[string]*ConnectionMetrics),
	}
	store.Register("filter1", "src", "dst")

	store.RecordSuccess("filter1", 1024, 50.0)
	store.RecordSuccess("filter1", 2048, 30.0)

	all := store.GetAll()
	m := all["filter1"]

	if m.MessagesProcessed != 2 {
		t.Errorf("expected 2 messages, got %d", m.MessagesProcessed)
	}
	if m.BytesProcessed != 3072 {
		t.Errorf("expected 3072 bytes, got %d", m.BytesProcessed)
	}
	if m.AvgLatencyMs != 40.0 {
		t.Errorf("expected avg latency 40ms, got %.2f", m.AvgLatencyMs)
	}
}

func TestMetricsStore_RecordFailure(t *testing.T) {
	store := &MetricsStore{
		connections: make(map[string]*ConnectionMetrics),
	}
	store.Register("filter1", "src", "dst")

	store.RecordFailure("filter1")
	store.RecordFailure("filter1")

	all := store.GetAll()
	if all["filter1"].MessagesFailed != 2 {
		t.Errorf("expected 2 failures, got %d", all["filter1"].MessagesFailed)
	}
}

func TestMetricsStore_SetStatus(t *testing.T) {
	store := &MetricsStore{
		connections: make(map[string]*ConnectionMetrics),
	}
	store.Register("filter1", "src", "dst")

	store.SetStatus("filter1", "error", "connection lost")

	all := store.GetAll()
	if all["filter1"].Status != "error" {
		t.Errorf("expected 'error' status, got %s", all["filter1"].Status)
	}
	if all["filter1"].LastError != "connection lost" {
		t.Errorf("expected error message, got %s", all["filter1"].LastError)
	}
}

func TestMetricsStore_ActiveCount(t *testing.T) {
	store := &MetricsStore{
		connections: make(map[string]*ConnectionMetrics),
	}

	if store.ActiveCount() != 0 {
		t.Error("expected 0 active")
	}

	store.Register("a", "s", "d")
	store.Register("b", "s", "d")

	if store.ActiveCount() != 2 {
		t.Errorf("expected 2 active, got %d", store.ActiveCount())
	}
}

func TestMetricsStore_FormatPrometheus(t *testing.T) {
	store := &MetricsStore{
		connections: make(map[string]*ConnectionMetrics),
	}
	store.Register("filter1", "srcQ", "dstQ")
	store.RecordSuccess("filter1", 100, 10.0)

	output := store.FormatPrometheus()

	if !strings.Contains(output, "mqconnector_messages_processed_total") {
		t.Error("expected messages_processed metric")
	}
	if !strings.Contains(output, `filter_id="filter1"`) {
		t.Error("expected filter_id label")
	}
	if !strings.Contains(output, "mqconnector_uptime_seconds") {
		t.Error("expected uptime metric")
	}
}

func TestMetricsStore_NonexistentFilter(t *testing.T) {
	store := &MetricsStore{
		connections: make(map[string]*ConnectionMetrics),
	}

	// Should not panic
	store.RecordSuccess("nonexistent", 100, 10.0)
	store.RecordFailure("nonexistent")
	store.SetStatus("nonexistent", "error", "test")
}
