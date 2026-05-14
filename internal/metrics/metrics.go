// Package metrics is the in-process metrics store with a Prometheus exposition
// renderer. Implements pipeline.MetricsSink.
package metrics

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Pipeline is the snapshot value type — pure data, safe to copy.
type Pipeline struct {
	PipelineID        string    `json:"pipeline_id"`
	SourceQueue       string    `json:"source_queue"`
	DestQueue         string    `json:"dest_queue"`
	MessagesProcessed int64     `json:"messages_processed"`
	MessagesFailed    int64     `json:"messages_failed"`
	BytesProcessed    int64     `json:"bytes_processed"`
	LastMessageTime   time.Time `json:"last_message_time"`
	AvgLatencyMs      float64   `json:"avg_latency_ms"`
	Status            string    `json:"status"`
	LastError         string    `json:"last_error,omitempty"`
}

// entry is the locked, internal representation. The mutex stays here so the
// public Pipeline snapshot type can be safely copied.
type entry struct {
	mu             sync.Mutex
	data           Pipeline
	totalLatencyMs float64
}

// Store is the thread-safe global metrics store.
type Store struct {
	pipelines map[string]*entry
	mu        sync.RWMutex
	startTime time.Time
}

// New returns a fresh Store.
func New() *Store {
	return &Store{
		pipelines: map[string]*entry{},
		startTime: time.Now(),
	}
}

// Register adds a pipeline. Replaces any prior entry for the same id.
func (s *Store) Register(pipelineID, sourceQueue, destQueue string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pipelines[pipelineID] = &entry{
		data: Pipeline{
			PipelineID:  pipelineID,
			SourceQueue: sourceQueue,
			DestQueue:   destQueue,
			Status:      "connected",
		},
	}
}

// Unregister removes a pipeline.
func (s *Store) Unregister(pipelineID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pipelines, pipelineID)
}

// SetStatus updates status and last-error for a pipeline.
func (s *Store) SetStatus(pipelineID, status, lastError string) {
	s.mu.RLock()
	e, ok := s.pipelines[pipelineID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.data.Status = status
	e.data.LastError = lastError
}

// RecordSuccess records a successfully processed message.
func (s *Store) RecordSuccess(pipelineID string, bytes int64, latencyMs float64) {
	s.mu.RLock()
	e, ok := s.pipelines[pipelineID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.data.MessagesProcessed++
	e.data.BytesProcessed += bytes
	e.data.LastMessageTime = time.Now()
	e.totalLatencyMs += latencyMs
	e.data.AvgLatencyMs = e.totalLatencyMs / float64(e.data.MessagesProcessed)
}

// RecordFailure records a failed message.
func (s *Store) RecordFailure(pipelineID string) {
	s.mu.RLock()
	e, ok := s.pipelines[pipelineID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.data.MessagesFailed++
}

// Snapshot returns a copy of all per-pipeline metrics.
func (s *Store) Snapshot() map[string]Pipeline {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Pipeline, len(s.pipelines))
	for k, e := range s.pipelines {
		e.mu.Lock()
		out[k] = e.data
		e.mu.Unlock()
	}
	return out
}

// Uptime returns how long the store has been running.
func (s *Store) Uptime() time.Duration { return time.Since(s.startTime) }

// ActiveCount returns the number of registered pipelines.
func (s *Store) ActiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.pipelines)
}

// Prometheus returns the metrics in Prometheus text exposition format.
func (s *Store) Prometheus() string {
	all := s.Snapshot()
	var b strings.Builder

	b.WriteString("# HELP mqconnector_messages_processed_total Total messages processed per pipeline\n")
	b.WriteString("# TYPE mqconnector_messages_processed_total counter\n")
	for _, m := range all {
		fmt.Fprintf(&b, "mqconnector_messages_processed_total{pipeline_id=%q,source=%q,dest=%q} %d\n",
			m.PipelineID, m.SourceQueue, m.DestQueue, m.MessagesProcessed)
	}

	b.WriteString("# HELP mqconnector_messages_failed_total Total failed messages per pipeline\n")
	b.WriteString("# TYPE mqconnector_messages_failed_total counter\n")
	for _, m := range all {
		fmt.Fprintf(&b, "mqconnector_messages_failed_total{pipeline_id=%q,source=%q,dest=%q} %d\n",
			m.PipelineID, m.SourceQueue, m.DestQueue, m.MessagesFailed)
	}

	b.WriteString("# HELP mqconnector_bytes_processed_total Total bytes processed per pipeline\n")
	b.WriteString("# TYPE mqconnector_bytes_processed_total counter\n")
	for _, m := range all {
		fmt.Fprintf(&b, "mqconnector_bytes_processed_total{pipeline_id=%q,source=%q,dest=%q} %d\n",
			m.PipelineID, m.SourceQueue, m.DestQueue, m.BytesProcessed)
	}

	b.WriteString("# HELP mqconnector_avg_latency_ms Average processing latency in milliseconds\n")
	b.WriteString("# TYPE mqconnector_avg_latency_ms gauge\n")
	for _, m := range all {
		fmt.Fprintf(&b, "mqconnector_avg_latency_ms{pipeline_id=%q,source=%q,dest=%q} %.2f\n",
			m.PipelineID, m.SourceQueue, m.DestQueue, m.AvgLatencyMs)
	}

	b.WriteString("# HELP mqconnector_uptime_seconds Uptime in seconds\n")
	b.WriteString("# TYPE mqconnector_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "mqconnector_uptime_seconds %.0f\n", s.Uptime().Seconds())

	return b.String()
}
