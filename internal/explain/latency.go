package explain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"mqConnector/internal/metrics"
	"mqConnector/internal/storage"
)

// latencyExplainer answers "where is my latency going?". Subject
// "latency", id is the pipeline id.
//
// Reads:
//   - MetricsSource.SnapshotPipeline → overall latency histogram +
//     avg latency + source depth.
//   - StageHistograms (via the wider Source — adapters can expose
//     them) → per-stage p50/p95/p99 for the waterfall section.
//
// The headline calls out a dominant stage when one stage's p99 is
// >30% of the summed per-stage p99. Otherwise the headline reports
// the overall total p99 as "within envelope".
type latencyExplainer struct{ e *Engine }

// latencyDominanceThreshold is the share of total p99 a single
// stage must exceed for the headline to call it out. 30% is the
// trade-off — high enough to avoid noise on a four-stage pipeline
// where every stage is ~25%, low enough to catch the genuine
// outlier.
const latencyDominanceThreshold = 0.30

// StageHistogramsSource is an optional extension to MetricsSource.
// Adapters that can produce per-stage histograms implement it; the
// latency explainer falls back to the overall histogram alone
// when the source doesn't.
type StageHistogramsSource interface {
	StageHistogramsFor(tenantID, pipelineID string) []metrics.StageHistogramSnapshot
}

// Explain dispatches per-subject; see Engine.Explain for the wire
// shape.
func (l *latencyExplainer) Explain(ctx context.Context, pipelineID, tenantID string) (Explanation, error) {
	if l.e == nil || pipelineID == "" {
		return Explanation{}, ErrNotFound
	}
	if l.e.Pipelines != nil {
		if _, err := l.e.Pipelines.Get(ctx, tenantID, pipelineID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return Explanation{}, ErrNotFound
			}
		}
	}

	snap, hasSnap := lookupPipelineSnapshot(l.e.Metrics, tenantID, pipelineID)
	stages := l.fetchStages(tenantID, pipelineID)

	stagePctiles := stageLatencies(stages)
	totalP99 := float64(0)
	for _, s := range stagePctiles {
		totalP99 += s.P99Ms
	}

	overallP50 := float64(0)
	overallP95 := float64(0)
	overallP99 := float64(0)
	if hasSnap {
		overallP50 = percentile(snap.LatencyHistogram, 0.50)
		overallP95 = percentile(snap.LatencyHistogram, 0.95)
		overallP99 = percentile(snap.LatencyHistogram, 0.99)
	}

	dominant, dominantShare := dominantStage(stagePctiles, totalP99)
	headline, severity := latencyHeadline(dominant, dominantShare, overallP99, totalP99, len(stagePctiles))

	facts := emptyFacts()
	if hasSnap {
		facts = append(facts, Fact{
			Label:  "Avg latency",
			Value:  fmt.Sprintf("%.2f ms", snap.AvgLatencyMs),
			Source: "mqconnector_avg_latency_ms",
		})
		facts = append(facts, Fact{
			Label:  "Overall p50 / p95 / p99",
			Value:  fmt.Sprintf("%.2f / %.2f / %.2f ms", overallP50, overallP95, overallP99),
			Source: "mqconnector_pipeline_latency_ms",
		})
		if snap.SourceDepth >= 0 {
			facts = append(facts, Fact{
				Label:  "Source broker depth",
				Value:  fmt.Sprintf("%d", snap.SourceDepth),
				Source: "mqconnector_source_depth",
			})
		}
		if snap.DedupSkipped > 0 && snap.MessagesProcessed > 0 {
			hitRate := float64(snap.DedupSkipped) / float64(snap.MessagesProcessed+snap.DedupSkipped)
			facts = append(facts, Fact{
				Label:  "Dedup hit rate",
				Value:  fmt.Sprintf("%.2f%% (%d skipped / %d processed)", hitRate*100, snap.DedupSkipped, snap.MessagesProcessed),
				Source: "mqconnector_dedup_skipped_total",
			})
		}
	}

	for _, s := range stagePctiles {
		facts = append(facts, Fact{
			Label:  fmt.Sprintf("Stage %s p50/p95/p99", s.Name),
			Value:  fmt.Sprintf("%.2f / %.2f / %.2f ms", s.P50Ms, s.P95Ms, s.P99Ms),
			Source: "mqconnector_stage_duration_ms",
		})
	}

	exp := Explanation{
		Subject:  "latency",
		ID:       pipelineID,
		Headline: headline,
		Severity: severity,
		Facts:    facts,
		AsOf:     l.e.now(),
		Sources:  []string{"metrics.Snapshot", "metrics.StageHistogramsFor"},
	}

	// Stages section feeds the WaterfallStages.svelte renderer.
	// Even when no stage dominates, the section is useful — it
	// gives the operator the waterfall view they came for.
	if len(stagePctiles) > 0 {
		data, _ := json.Marshal(map[string]any{
			"stages":    stagePctiles,
			"total_p99": totalP99,
		})
		exp.Sections = append(exp.Sections, Section{
			Kind:  "stages",
			Title: "Per-stage latency waterfall",
			Data:  data,
		})
	}
	return exp, nil
}

// fetchStages reads the per-stage histograms via the optional
// extension interface. Implemented as a separate method so the
// caller path stays linear and the cast is local.
func (l *latencyExplainer) fetchStages(tenantID, pipelineID string) []metrics.StageHistogramSnapshot {
	if l.e == nil || l.e.Metrics == nil {
		return nil
	}
	ext, ok := l.e.Metrics.(StageHistogramsSource)
	if !ok {
		return nil
	}
	return ext.StageHistogramsFor(tenantID, pipelineID)
}

// StageLatency is one row of the per-stage percentile output.
// Exported so the JSON-marshalled section data has stable field
// names for the WaterfallStages.svelte renderer.
type StageLatency struct {
	Name  string  `json:"name"`
	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
	Count uint64  `json:"count"`
	SumMs float64 `json:"sum_ms"`
}

// stageLatencies projects the per-stage histograms into the
// percentile shape. Sorted by p99 desc so the dominant stage
// floats to the top — both for the section data and for
// downstream consumers that just want "the slow stage first".
func stageLatencies(stages []metrics.StageHistogramSnapshot) []StageLatency {
	if len(stages) == 0 {
		return nil
	}
	buckets := metrics.LatencyBuckets()
	out := make([]StageLatency, 0, len(stages))
	for _, s := range stages {
		// Build the cumulative bucket slice the percentile helper
		// expects. Bucket counts already cumulative — pair each
		// with the upper bound at the same index, and add the
		// +Inf bucket at the end.
		hist := make([]metrics.HistogramBucket, 0, len(buckets)+1)
		for i, ub := range buckets {
			if i < len(s.BucketCounts) {
				hist = append(hist, metrics.HistogramBucket{LE: ub, Count: s.BucketCounts[i]})
			}
		}
		hist = append(hist, metrics.HistogramBucket{LE: math.MaxFloat64, Count: s.Count})
		out = append(out, StageLatency{
			Name:  s.StageName,
			P50Ms: percentile(hist, 0.50),
			P95Ms: percentile(hist, 0.95),
			P99Ms: percentile(hist, 0.99),
			Count: s.Count,
			SumMs: s.SumMs,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].P99Ms > out[j].P99Ms })
	return out
}

// dominantStage returns the highest-p99 stage and its share of
// totalP99. Returns ("", 0) when there are no stages or totalP99
// is zero. The threshold check lives in the caller — this helper
// just reports the math.
func dominantStage(stages []StageLatency, totalP99 float64) (string, float64) {
	if len(stages) == 0 || totalP99 <= 0 {
		return "", 0
	}
	top := stages[0]
	return top.Name, top.P99Ms / totalP99
}

// latencyHeadline maps the dominance check to a tiered headline.
// stageCount is used to choose between the "no observations" and
// "envelope" branches.
func latencyHeadline(dominant string, share, overallP99, totalP99 float64, stageCount int) (string, Severity) {
	if stageCount == 0 && overallP99 == 0 {
		return "No latency observations yet for this pipeline.", SeverityInfo
	}
	if dominant != "" && share >= latencyDominanceThreshold {
		// A genuine outlier stage. Severity tracks how dominant
		// the stage is — >70% is critical, in-between warns.
		sev := SeverityWarning
		if share >= 0.70 {
			sev = SeverityCritical
		}
		return fmt.Sprintf("Stage %q dominates: p99 share %.0f%% of total per-stage p99 (%.2f ms).",
			dominant, share*100, totalP99), sev
	}
	if totalP99 > 0 {
		return fmt.Sprintf("Latency within expected envelope: total per-stage p99 = %.2f ms.", totalP99),
			SeverityInfo
	}
	return fmt.Sprintf("Latency within expected envelope: overall p99 = %.2f ms.", overallP99),
		SeverityInfo
}
