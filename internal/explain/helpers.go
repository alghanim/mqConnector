package explain

import (
	"context"
	"sort"
	"strings"
	"time"

	"mqConnector/internal/metrics"
	"mqConnector/internal/storage"
)

// pipelineSnapshot is the subset of metrics.Pipeline that the
// explainers care about, plus a stable copy of the latency
// histogram. Kept as a thin alias so the explainers don't have to
// guard against nil maps on every read.
type pipelineSnapshot = metrics.Pipeline

// lookupPipelineSnapshot fetches the snapshot for one pipeline.
// Returns (zero, false) when the source is nil or the pipeline
// isn't registered. Treating "no metrics" as a soft fail lets the
// explainers continue with whatever other signal they have.
func lookupPipelineSnapshot(src MetricsSource, tenantID, pipelineID string) (pipelineSnapshot, bool) {
	if src == nil {
		return pipelineSnapshot{}, false
	}
	return src.SnapshotPipeline(tenantID, pipelineID)
}

// recentDLQEntries reads the latest DLQ entries for a pipeline.
// All errors are swallowed and returned as an empty slice — the
// explainers degrade rather than fail the whole call. The
// returned error is therefore informational; today every caller
// in this package ignores it.
func recentDLQEntries(ctx context.Context, src DLQSource, tenantID, pipelineID string, limit int) ([]*storage.DLQEntry, error) {
	if src == nil || pipelineID == "" {
		return nil, nil
	}
	return src.RecentForPipeline(ctx, tenantID, pipelineID, limit, time.Time{})
}

// recentAuditForResource reads the latest audit rows for a
// resource prefix. Same best-effort contract as recentDLQEntries.
func recentAuditForResource(ctx context.Context, src AuditSource, tenantID, resourcePrefix string, limit int) ([]*storage.AuditEntry, error) {
	if src == nil || resourcePrefix == "" {
		return nil, nil
	}
	return src.RecentForResource(ctx, tenantID, resourcePrefix, limit)
}

// truncate returns s clamped to max bytes with an ellipsis if it
// was cut. Conservative for multi-byte boundaries — walks back to
// a safe rune boundary so we never emit malformed UTF-8.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut-1]&0xC0) == 0x80 {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return s[:cut] + "…"
}

// percentile reads the cumulative latency histogram from a
// snapshot and returns the latency at the given quantile (0.0 to
// 1.0). Uses linear interpolation within the bucket containing
// the target rank. Returns 0 when there are no observations.
//
// The histogram shape is the same shape exposed by
// metrics.Store.Snapshot: cumulative counts per upper-bound, with
// a final +Inf bucket whose count equals the total.
func percentile(buckets []metrics.HistogramBucket, q float64) float64 {
	if len(buckets) == 0 || q < 0 {
		return 0
	}
	if q > 1 {
		q = 1
	}
	total := uint64(0)
	for _, b := range buckets {
		if b.Count > total {
			total = b.Count
		}
	}
	if total == 0 {
		return 0
	}
	target := q * float64(total)
	var prevCount uint64
	var prevLE float64
	for _, b := range buckets {
		if float64(b.Count) >= target {
			// Linear interpolation inside the bucket [prevLE, b.LE].
			// When b.LE is the +Inf bucket (math.MaxFloat64) we
			// can't interpolate meaningfully — fall back to
			// prevLE which is the largest finite bound at-or-above
			// the target rank.
			if b.LE > 1e15 {
				if prevLE > 0 {
					return prevLE
				}
				return 0
			}
			bw := b.LE - prevLE
			bc := float64(b.Count - prevCount)
			if bc <= 0 {
				return prevLE
			}
			frac := (target - float64(prevCount)) / bc
			if frac < 0 {
				frac = 0
			}
			return prevLE + frac*bw
		}
		prevCount = b.Count
		prevLE = b.LE
	}
	return prevLE
}

// stageHistogramFromSnapshot extracts a per-stage cumulative
// histogram from the pipeline snapshot. The snapshot embeds only
// the overall histogram (LatencyHistogram); per-stage histograms
// are read from the metrics store via a separate accessor. This
// helper is a placeholder for explainers that want to use whatever
// is available on the snapshot — today it just returns the
// pipeline-wide histogram, which the latency explainer combines
// with the per-stage data from the metrics store directly.
func stageHistogramFromSnapshot(snap pipelineSnapshot) []metrics.HistogramBucket {
	out := make([]metrics.HistogramBucket, len(snap.LatencyHistogram))
	copy(out, snap.LatencyHistogram)
	return out
}

// distinctErrorTemplates groups recent DLQ entries by template
// and returns the templates sorted by occurrence count
// (descending). Used by the drift + dlq explainers to surface the
// top-3 failure flavours rather than dumping raw error strings.
func distinctErrorTemplates(entries []*storage.DLQEntry, max int) []templateCount {
	counts := map[string]int{}
	first := map[string]time.Time{}
	for _, e := range entries {
		t := strings.TrimSpace(e.ErrorTemplate)
		if t == "" {
			t = strings.TrimSpace(e.ErrorReason)
		}
		if t == "" {
			continue
		}
		counts[t]++
		if ts, ok := first[t]; !ok || e.CreatedAt.Before(ts) {
			first[t] = e.CreatedAt
		}
	}
	out := make([]templateCount, 0, len(counts))
	for t, n := range counts {
		out = append(out, templateCount{Template: t, Count: n, FirstSeen: first[t]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Template < out[j].Template
	})
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// templateCount is one row of the distinct-template grouping.
// Exported field tags are absent because this isn't part of the
// wire shape — the explainers project it into Fact rows.
type templateCount struct {
	Template  string
	Count     int
	FirstSeen time.Time
}
