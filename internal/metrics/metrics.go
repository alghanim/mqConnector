// Package metrics is the in-process metrics store with a Prometheus exposition
// renderer. Implements pipeline.MetricsSink.
package metrics

import (
	"fmt"
	"math"
	"sort"
	"strconv"
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
	// SourceDepth is the most recent broker-side backlog measurement
	// for the pipeline's source. -1 means "not sampled yet" or "source
	// connector doesn't report depth"; the Prometheus renderer skips
	// the series in that case so operators don't get a misleading 0.
	SourceDepth int64 `json:"source_depth,omitempty"`
	// Histogram exposed under JSON for the UI's percentile panel.
	// Buckets keyed by upper bound (ms) → cumulative count.
	LatencyHistogram []HistogramBucket `json:"latency_histogram,omitempty"`
}

// HistogramBucket is one row of the latency histogram.
type HistogramBucket struct {
	LE    float64 `json:"le"`    // upper bound in ms (+Inf encoded as math.MaxFloat64)
	Count uint64  `json:"count"` // cumulative count at-or-below this bound
}

// latencyBuckets are the upper bounds (in ms) for the per-message latency
// histogram. Chosen to cover the practical range of bridge latencies — sub-
// millisecond local pipelines up to multi-second cross-cluster hops. Values
// match the de-facto defaults the Prometheus client lib emits for HTTP
// histograms, scaled to ms.
var latencyBuckets = []float64{
	0.5, 1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000,
	// +Inf bucket is rendered separately so it lives outside the slice.
}

// entry is the locked, internal representation. The mutex stays here so the
// public Pipeline snapshot type can be safely copied.
type entry struct {
	mu             sync.Mutex
	data           Pipeline
	totalLatencyMs float64

	// Cumulative bucket counts for the latency histogram. The slice
	// shape matches latencyBuckets exactly; the +Inf bucket equals
	// MessagesProcessed (every observation falls at or below +Inf).
	latencyBucketCounts []uint64
	latencySumMs        float64
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
			SourceDepth: -1,
		},
		latencyBucketCounts: make([]uint64, len(latencyBuckets)),
	}
}

// SetSourceDepth records the latest broker-side backlog observation for
// a pipeline. A negative value means "unknown"; the renderer omits the
// series in that case. Called by the executor's depth-sampling
// goroutine every ~30s for pipelines whose source connector implements
// DepthReporter.
func (s *Store) SetSourceDepth(pipelineID string, depth int64) {
	s.mu.RLock()
	e, ok := s.pipelines[pipelineID]
	s.mu.RUnlock()
	if !ok {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.data.SourceDepth = depth
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
	// Histogram bookkeeping. Cumulative buckets: every observation at-or-
	// below an upper bound bumps that bucket AND every wider one. We track
	// just the bucket index for the lowest bound the value satisfies and
	// roll forward at render time? No — that's quadratic to render. Easier:
	// walk the bucket list once on insert and increment each bound that's
	// >= the observed value. With 14 buckets the inner loop is negligible.
	e.latencySumMs += latencyMs
	for i, ub := range latencyBuckets {
		if latencyMs <= ub {
			e.latencyBucketCounts[i]++
		}
	}
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
		snap := e.data
		// Include the histogram in the snapshot so the UI's percentile
		// view can render p50 / p95 / p99 client-side.
		if len(e.latencyBucketCounts) > 0 {
			snap.LatencyHistogram = make([]HistogramBucket, len(latencyBuckets)+1)
			for i, ub := range latencyBuckets {
				snap.LatencyHistogram[i] = HistogramBucket{LE: ub, Count: e.latencyBucketCounts[i]}
			}
			// +Inf catches everything by definition; cumulative count is
			// the same as MessagesProcessed.
			snap.LatencyHistogram[len(latencyBuckets)] = HistogramBucket{
				LE:    math.MaxFloat64,
				Count: uint64(e.data.MessagesProcessed),
			}
		}
		out[k] = snap
		e.mu.Unlock()
	}
	return out
}

// latencyHistogramFor returns the bucket counts + sum for one pipeline. Used
// by the Prometheus exposition; takes the entry's mutex internally.
func (s *Store) latencyHistogramFor(pipelineID string) (counts []uint64, sumMs float64, total int64, ok bool) {
	s.mu.RLock()
	e, ok := s.pipelines[pipelineID]
	s.mu.RUnlock()
	if !ok {
		return nil, 0, 0, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	counts = make([]uint64, len(e.latencyBucketCounts))
	copy(counts, e.latencyBucketCounts)
	return counts, e.latencySumMs, e.data.MessagesProcessed, true
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

	// Real histogram for end-to-end pipeline latency. Three series per
	// pipeline: _bucket{le="…"}, _sum, _count. Prometheus consumers (and
	// Grafana / PromQL functions like histogram_quantile) treat these as
	// the canonical histogram shape. Buckets are in milliseconds.
	b.WriteString("# HELP mqconnector_pipeline_latency_ms End-to-end pipeline latency (source receive → destination send) in milliseconds\n")
	b.WriteString("# TYPE mqconnector_pipeline_latency_ms histogram\n")
	// Stable rendering order so a Prometheus scrape sees the same series
	// names from one tick to the next — easier to diff for debugging.
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		m := all[id]
		counts, sumMs, total, ok := s.latencyHistogramFor(id)
		if !ok {
			continue
		}
		for i, ub := range latencyBuckets {
			fmt.Fprintf(&b,
				"mqconnector_pipeline_latency_ms_bucket{pipeline_id=%q,source=%q,dest=%q,le=%q} %d\n",
				m.PipelineID, m.SourceQueue, m.DestQueue, formatBound(ub), counts[i])
		}
		// +Inf bucket: every observation lands at-or-below +Inf by definition.
		fmt.Fprintf(&b,
			"mqconnector_pipeline_latency_ms_bucket{pipeline_id=%q,source=%q,dest=%q,le=\"+Inf\"} %d\n",
			m.PipelineID, m.SourceQueue, m.DestQueue, total)
		fmt.Fprintf(&b,
			"mqconnector_pipeline_latency_ms_sum{pipeline_id=%q,source=%q,dest=%q} %.2f\n",
			m.PipelineID, m.SourceQueue, m.DestQueue, sumMs)
		fmt.Fprintf(&b,
			"mqconnector_pipeline_latency_ms_count{pipeline_id=%q,source=%q,dest=%q} %d\n",
			m.PipelineID, m.SourceQueue, m.DestQueue, total)
	}

	// Per-pipeline source backlog. Only emitted for pipelines whose
	// source connector implements mq.DepthReporter — the absence of
	// the series for a given pipeline_id means "this source type
	// doesn't expose depth (e.g. NATS, MQTT QoS 0)" rather than 0.
	b.WriteString("# HELP mqconnector_source_depth Most recent broker-side backlog for the pipeline source (queue depth for RabbitMQ, consumer-group lag for Kafka)\n")
	b.WriteString("# TYPE mqconnector_source_depth gauge\n")
	for _, m := range all {
		if m.SourceDepth < 0 {
			continue
		}
		fmt.Fprintf(&b, "mqconnector_source_depth{pipeline_id=%q,source=%q} %d\n",
			m.PipelineID, m.SourceQueue, m.SourceDepth)
	}

	// Per-pipeline up/down — 1 if status is "connected", 0 otherwise.
	// Alertmanager-friendly: `mqconnector_pipeline_up == 0 for 5m` is
	// the canonical "pipeline is broken" condition.
	b.WriteString("# HELP mqconnector_pipeline_up Pipeline runtime status (1=connected, 0=error/stopped)\n")
	b.WriteString("# TYPE mqconnector_pipeline_up gauge\n")
	for _, m := range all {
		up := 0
		if m.Status == "connected" {
			up = 1
		}
		fmt.Fprintf(&b, "mqconnector_pipeline_up{pipeline_id=%q,source=%q,dest=%q,status=%q} %d\n",
			m.PipelineID, m.SourceQueue, m.DestQueue, m.Status, up)
	}

	// Total active pipelines — the trivially-aggregated counterpart to
	// /api/health.active_pipelines. Easier to alert on than walking
	// labels in PromQL.
	b.WriteString("# HELP mqconnector_active_pipelines Number of currently registered pipelines\n")
	b.WriteString("# TYPE mqconnector_active_pipelines gauge\n")
	fmt.Fprintf(&b, "mqconnector_active_pipelines %d\n", len(all))

	b.WriteString("# HELP mqconnector_uptime_seconds Uptime in seconds\n")
	b.WriteString("# TYPE mqconnector_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "mqconnector_uptime_seconds %.0f\n", s.Uptime().Seconds())

	return b.String()
}

// formatBound renders a histogram upper bound the way Prometheus expects.
// Whole-number bounds drop their decimal (`"100"` not `"100.000000"`),
// fractional bounds use the shortest accurate representation.
func formatBound(v float64) string {
	if v == math.Trunc(v) {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
