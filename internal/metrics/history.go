package metrics

import (
	"sync"
	"time"
)

// history.go — bounded ring of per-pipeline snapshots used by the SLO
// evaluator to implement rate() against in-process metrics.
//
// rate(METRIC[5m]) needs two samples (now + 5min-ago). The live Store
// only exposes "now". History wraps a Store and samples it on a fixed
// interval, keeping the last N snapshots.
//
// Memory shape: one Pipeline snapshot per ring slot per pipeline. At
// the default 10-slot ring (5min / 30s) and ~100 pipelines, that's
// ~100KB. Well under any budget.
//
// History is fed by a single goroutine started via Run; reads from
// the SLO evaluator come through ValueAt, which takes the RLock.

// History is a thread-safe rolling window of metrics snapshots.
type History struct {
	store    *Store
	clock    func() time.Time
	interval time.Duration
	keep     int

	mu    sync.RWMutex
	frame []historyFrame
}

type historyFrame struct {
	at        time.Time
	pipelines map[string]Pipeline
}

// NewHistory builds a History bound to store. Defaults: 30s interval,
// 10 slots (= 5 minutes). The window covers the rate(…[5m]) the
// project's rules use.
func NewHistory(store *Store) *History {
	return &History{
		store:    store,
		clock:    time.Now,
		interval: 30 * time.Second,
		keep:     10,
	}
}

// WithInterval overrides the sampling interval. Used in tests so they
// don't need to wait 30 real-time seconds.
func (h *History) WithInterval(d time.Duration) *History {
	h.interval = d
	return h
}

// WithKeep overrides the ring size. Default 10.
func (h *History) WithKeep(n int) *History {
	if n > 0 {
		h.keep = n
	}
	return h
}

// WithClock overrides the wall clock. Tests use this to drive the
// History forward deterministically.
func (h *History) WithClock(f func() time.Time) *History {
	h.clock = f
	return h
}

// Sample captures one snapshot of the metrics store into the ring.
// Exported so tests (and the SLO evaluator on startup) can prime the
// ring without waiting for the periodic Run loop.
func (h *History) Sample() {
	if h.store == nil {
		return
	}
	snap := h.store.Snapshot()
	h.mu.Lock()
	defer h.mu.Unlock()
	h.frame = append(h.frame, historyFrame{at: h.clock(), pipelines: snap})
	if len(h.frame) > h.keep {
		// Trim from the front — oldest first.
		h.frame = h.frame[len(h.frame)-h.keep:]
	}
}

// Run blocks until ctx is done, sampling on h.interval. Cancelling
// the context returns nil.
func (h *History) Run(stop <-chan struct{}) {
	t := time.NewTicker(h.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			h.Sample()
		}
	}
}

// ValueAt returns the value of the named metric for one pipeline at
// the historical timestamp at-or-just-before (now - ago). ok=false
// when the ring doesn't contain a frame that old yet.
//
// The metric name is one of the cumulative counters the Store
// exposes: "mqconnector_messages_processed_total",
// "mqconnector_messages_failed_total", etc. Only counters and gauges
// that map to scalar fields on Pipeline are looked up here — full
// histogram series live elsewhere.
func (h *History) ValueAt(metric, pipelineID string, ago time.Duration) (float64, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.frame) == 0 {
		return 0, false
	}
	now := h.clock()
	target := now.Add(-ago)
	// Find the newest frame at-or-before target.
	idx := -1
	for i := len(h.frame) - 1; i >= 0; i-- {
		if !h.frame[i].at.After(target) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return 0, false
	}
	p, ok := h.frame[idx].pipelines[pipelineID]
	if !ok {
		return 0, false
	}
	return pipelineMetricValue(p, metric), true
}

// pipelineMetricValue returns the named metric off a Pipeline snapshot.
// Returns 0 for unknown metric names so the caller can decide whether
// to treat that as "0 in history" or "metric unsupported"; the SLO
// evaluator only ever queries the known counter names.
func pipelineMetricValue(p Pipeline, metric string) float64 {
	switch metric {
	case "mqconnector_messages_processed_total":
		return float64(p.MessagesProcessed)
	case "mqconnector_messages_failed_total":
		return float64(p.MessagesFailed)
	case "mqconnector_bytes_processed_total":
		return float64(p.BytesProcessed)
	case "mqconnector_dedup_skipped_total":
		return float64(p.DedupSkipped)
	case "mqconnector_validate_attempts_total":
		return float64(p.ValidateAttempts)
	case "mqconnector_validate_failures_total":
		return float64(p.ValidateFailures)
	case "mqconnector_shadow_sent_total":
		return float64(p.ShadowSent)
	case "mqconnector_shadow_failed_total":
		return float64(p.ShadowFailed)
	case "mqconnector_avg_latency_ms":
		return p.AvgLatencyMs
	case "mqconnector_source_depth":
		return float64(p.SourceDepth)
	}
	return 0
}

// FrameCount returns how many snapshots are currently in the ring.
// Exposed for tests + the SLO evaluator's startup wait so it doesn't
// fire false-positive alerts on a cold cache.
func (h *History) FrameCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.frame)
}
