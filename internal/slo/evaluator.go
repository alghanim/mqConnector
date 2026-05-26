package slo

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"mqConnector/internal/metrics"
)

// MetricsSource is the minimal read-shape the evaluator needs against
// the in-process metrics store. The adapter in cmd/mqconnector wraps
// *metrics.Store + *metrics.History to satisfy it; tests substitute
// fakes.
type MetricsSource interface {
	// Snapshot returns the current value of every per-pipeline
	// counter / gauge in a flat (label-set, value) list. The
	// "__name__" label MUST be set on each row so the evaluator
	// can dispatch by metric name.
	Snapshot() []Sample
	// ValueAt returns the historical value of a named cumulative
	// counter (or gauge) for one pipeline at (now - ago). ok=false
	// when no such sample exists yet (cold ring).
	ValueAt(metric string, labels map[string]string, ago time.Duration) (float64, bool)
}

// Sample is one row of MetricsSource.Snapshot. labels MUST include
// __name__; the evaluator selects rows by that label.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// FiringAlert is one alert currently in the firing state.
//
// Wire-stable: the JSON tags here are the canonical
// /api/v1/alerts/active row shape. Field additions are non-breaking;
// renames break the frontend AlertRibbon.
type FiringAlert struct {
	Name        string            `json:"name"`
	Severity    string            `json:"severity"`
	Value       float64           `json:"value"`
	Threshold   string            `json:"threshold,omitempty"`
	StartedAt   time.Time         `json:"started_at"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Group       string            `json:"group,omitempty"`
	Expr        string            `json:"expr,omitempty"`
}

// Evaluator periodically evaluates a fixed set of Rules against a
// MetricsSource and tracks the standard inactive → pending → firing
// state machine per (rule × label set).
type Evaluator struct {
	Rules    []Rule
	Metrics  MetricsSource
	Clock    func() time.Time
	Interval time.Duration
	Logger   *slog.Logger

	// Pre-parsed ASTs. Built once in newEvaluator / NewEvaluator;
	// rules whose expr failed to parse are dropped (the loader
	// already filtered the obviously-bad cases).
	parsed []parsedRule
	// recordings: name → expression node. Populated when an external
	// caller hands us recording rules separately (we don't read them
	// from the YAML — the loader skips them). The map is read by
	// evalSelector when an expression references a metric whose name
	// matches a recording rule.
	recordings map[string]astNode

	mu      sync.RWMutex
	pending map[string]time.Time // alertKey → first-seen-true
	firing  map[string]FiringAlert
	// rateLimit guards per-rule warning logs so a permanently
	// broken rule doesn't spam the operator's log stream.
	rateLimit map[string]time.Time
}

type parsedRule struct {
	rule Rule
	ast  astNode
}

// alertKey is the per-(rule, label-set) identity used to track
// pending / firing state across ticks.
func alertKey(rule string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(rule)
	for _, k := range keys {
		b.WriteByte(0x1f) // unit-separator, can't appear in legal labels
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}
	return b.String()
}

// NewEvaluator builds an Evaluator from a list of Rules (already
// loaded via LoadFile / LoadDir). Rules whose expression fails to
// parse are dropped and a warn is logged.
//
// Optional recording rules can be passed in for recording-rule
// reference resolution. The map is { name → expr text }; expressions
// that fail to parse are dropped with a warn.
func NewEvaluator(rules []Rule, recordings map[string]string, metricsSrc MetricsSource, logger *slog.Logger) *Evaluator {
	if logger == nil {
		logger = slog.Default()
	}
	e := &Evaluator{
		Rules:      rules,
		Metrics:    metricsSrc,
		Clock:      time.Now,
		Interval:   30 * time.Second,
		Logger:     logger,
		recordings: map[string]astNode{},
		pending:    map[string]time.Time{},
		firing:     map[string]FiringAlert{},
		rateLimit:  map[string]time.Time{},
	}
	// Parse all rule expressions up-front so the hot path only does
	// AST evaluation.
	for _, r := range rules {
		ast, err := parsePromQL(r.Expr)
		if err != nil {
			logger.Warn("slo: skipping rule with unparseable expr",
				"alert", r.Name, "expr", r.Expr, "err", err)
			continue
		}
		e.parsed = append(e.parsed, parsedRule{rule: r, ast: ast})
	}
	for name, expr := range recordings {
		ast, err := parsePromQL(expr)
		if err != nil {
			logger.Warn("slo: skipping recording rule with unparseable expr",
				"name", name, "expr", expr, "err", err)
			continue
		}
		e.recordings[name] = ast
	}
	return e
}

// RecordingRulesFromFile harvests the recording-rule expressions out
// of a Prometheus rule file so they can be passed to NewEvaluator.
// Recording rules are NOT alerting rules — LoadFile skips them — but
// the evaluator still needs their text to resolve references like
// `mqconnector:availability:ratio5m` inside alerting expressions.
func RecordingRulesFromFile(path string) (map[string]string, error) {
	rules, err := harvestRecordings(path)
	if err != nil {
		return nil, err
	}
	return rules, nil
}

// RecordingRulesFromDir is the directory-walking sibling of
// RecordingRulesFromFile.
func RecordingRulesFromDir(dir string) (map[string]string, error) {
	return harvestRecordingsDir(dir)
}

// Run loops on Interval. Cancel via ctx.
func (e *Evaluator) Run(ctx context.Context) {
	t := time.NewTicker(e.Interval)
	defer t.Stop()
	// First tick immediately so /alerts/active isn't empty for 30s on
	// startup if anything is already firing.
	e.tick()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tick()
		}
	}
}

// Snapshot returns a stable copy of the currently-firing alerts,
// ordered by severity DESC (critical → warning → info → other), then
// started_at DESC (newest first). Safe to call from any goroutine.
func (e *Evaluator) Snapshot() []FiringAlert {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]FiringAlert, 0, len(e.firing))
	for _, a := range e.firing {
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		si := severityRank(out[i].Severity)
		sj := severityRank(out[j].Severity)
		if si != sj {
			return si > sj
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// RuleCount returns how many rules the evaluator is exercising. Used
// by tests + a /api/health probe to confirm SLO loading worked.
func (e *Evaluator) RuleCount() int {
	return len(e.parsed)
}

func severityRank(s string) int {
	switch strings.ToLower(s) {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 0
}

// tick is one evaluation pass. The state machine:
//
//   - For each rule, evaluate the expression.
//   - For each label set in the result vector:
//   - If pending tracker doesn't have it: insert now.
//   - If pending duration ≥ rule.For: insert into firing.
//   - For pending/firing entries whose label set is absent in this
//     tick: drop them (back to inactive).
func (e *Evaluator) tick() {
	if e.Metrics == nil {
		return
	}
	now := e.Clock()
	src := buildSourceSnapshot(e.Metrics)

	e.mu.Lock()
	defer e.mu.Unlock()

	// Track the keys we saw alive this tick so absent ones can be
	// pruned back to inactive.
	alive := map[string]bool{}

	for _, pr := range e.parsed {
		ctx := &evalContext{
			current:    src,
			historyAt:  metricsHistoryAdapter(e.Metrics),
			recordings: e.recordings,
			expanding:  map[string]bool{},
		}
		out, err := evalNode(pr.ast, ctx)
		if err != nil {
			// Throttle per-rule warns to once per 5min so a
			// permanently broken rule doesn't spam logs.
			if last, ok := e.rateLimit[pr.rule.Name]; !ok || now.Sub(last) > 5*time.Minute {
				e.Logger.Warn("slo: rule evaluation failed",
					"alert", pr.rule.Name, "err", err)
				e.rateLimit[pr.rule.Name] = now
			}
			continue
		}
		// Threshold rendering — best effort.
		threshold := renderThreshold(pr.rule.Expr)
		for _, s := range out {
			k := alertKey(pr.rule.Name, s.labels)
			alive[k] = true
			// Already firing — refresh the value, keep StartedAt.
			if fa, ok := e.firing[k]; ok {
				fa.Value = s.value
				e.firing[k] = fa
				continue
			}
			// In pending — promote when For: has elapsed.
			if t0, ok := e.pending[k]; ok {
				if now.Sub(t0) >= pr.rule.For {
					labels := mergeLabels(pr.rule.Labels, s.labels)
					sev := labels["severity"]
					e.firing[k] = FiringAlert{
						Name:        pr.rule.Name,
						Severity:    sev,
						Value:       s.value,
						Threshold:   threshold,
						StartedAt:   t0,
						Annotations: pr.rule.Annotations,
						Labels:      labels,
						Group:       pr.rule.Group,
						Expr:        pr.rule.Expr,
					}
					delete(e.pending, k)
				}
				continue
			}
			// First sighting — enter pending.
			if pr.rule.For == 0 {
				labels := mergeLabels(pr.rule.Labels, s.labels)
				sev := labels["severity"]
				e.firing[k] = FiringAlert{
					Name:        pr.rule.Name,
					Severity:    sev,
					Value:       s.value,
					Threshold:   threshold,
					StartedAt:   now,
					Annotations: pr.rule.Annotations,
					Labels:      labels,
					Group:       pr.rule.Group,
					Expr:        pr.rule.Expr,
				}
				continue
			}
			e.pending[k] = now
		}
	}
	// Prune anything that wasn't alive this tick.
	for k := range e.pending {
		if !alive[k] {
			delete(e.pending, k)
		}
	}
	for k := range e.firing {
		if !alive[k] {
			delete(e.firing, k)
		}
	}
}

// mergeLabels combines static rule labels with the evaluated label
// set. Static labels win when keys collide (matches Prometheus
// semantics — the rule labels block is authoritative).
func mergeLabels(ruleLabels, vecLabels map[string]string) map[string]string {
	out := make(map[string]string, len(ruleLabels)+len(vecLabels))
	for k, v := range vecLabels {
		out[k] = v
	}
	for k, v := range ruleLabels {
		out[k] = v
	}
	return out
}

// renderThreshold extracts the right-hand side of the top-level
// comparison if there is one. Best-effort string-grep — falls back to
// an empty string when the expression isn't a simple `EXPR > NUM`.
// Used purely for the human-readable Threshold field; the actual
// comparison happens in the evaluator.
func renderThreshold(expr string) string {
	// Walk for the rightmost top-level comparison; very rough.
	for _, op := range []string{">=", "<=", "==", "!=", ">", "<"} {
		if i := strings.LastIndex(expr, op); i >= 0 {
			rhs := strings.TrimSpace(expr[i+len(op):])
			if rhs != "" {
				// Trim parenthesised wrapper noise.
				rhs = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(rhs, "("), ")"))
				if len(rhs) > 80 {
					rhs = rhs[:77] + "..."
				}
				return op + " " + rhs
			}
		}
	}
	return ""
}

// buildSourceSnapshot pulls the current-value snapshot from
// MetricsSource into the labelSample form the expression evaluator
// reads. The "__name__" label is preserved on each row.
func buildSourceSnapshot(src MetricsSource) []labelSample {
	rows := src.Snapshot()
	out := make([]labelSample, 0, len(rows))
	for _, r := range rows {
		out = append(out, labelSample{labels: r.Labels, value: r.Value})
	}
	return out
}

// metricsHistoryAdapter bridges MetricsSource.ValueAt into the
// signature evalRate expects.
func metricsHistoryAdapter(src MetricsSource) func(name string, labels map[string]string, ago int64) (float64, bool) {
	return func(name string, labels map[string]string, ago int64) (float64, bool) {
		return src.ValueAt(name, labels, time.Duration(ago))
	}
}

// ─── recording-rule harvester ─────────────────────────────────────

func harvestRecordings(path string) (map[string]string, error) {
	rs, err := loadRawRules(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range rs {
		if r.Record != "" {
			out[r.Record] = strings.TrimSpace(r.Expr)
		}
	}
	return out, nil
}

func harvestRecordingsDir(dir string) (map[string]string, error) {
	out := map[string]string{}
	files, err := listYAML(dir)
	if err != nil {
		return nil, err
	}
	for _, p := range files {
		rs, err := harvestRecordings(p)
		if err != nil {
			continue
		}
		for k, v := range rs {
			out[k] = v
		}
	}
	return out, nil
}

// ─── store adapter for the live metrics package ────────────────

// SnapshotFromMetricsStore renders the metrics.Store's per-pipeline
// snapshot into the flat Sample form MetricsSource.Snapshot expects.
// The pure-data Pipeline scalars (counts + AvgLatencyMs +
// SourceDepth) are emitted as Samples; the per-pipeline +
// per-stage histogram series are emitted as separate bucket samples
// matching the Prometheus exposition (`…_bucket{le="…"} N`).
//
// This is the single source of truth for "what metrics can the SLO
// evaluator see"; the cmd-level adapter just delegates here.
func SnapshotFromMetricsStore(store *metrics.Store) []Sample {
	if store == nil {
		return nil
	}
	all := store.Snapshot()
	out := make([]Sample, 0, len(all)*10)
	for _, p := range all {
		base := map[string]string{
			"pipeline_id": p.PipelineID,
			"source":      p.SourceQueue,
			"dest":        p.DestQueue,
		}
		emitScalar(&out, "mqconnector_messages_processed_total", base, float64(p.MessagesProcessed))
		emitScalar(&out, "mqconnector_messages_failed_total", base, float64(p.MessagesFailed))
		emitScalar(&out, "mqconnector_bytes_processed_total", base, float64(p.BytesProcessed))
		emitScalar(&out, "mqconnector_dedup_skipped_total", base, float64(p.DedupSkipped))
		emitScalar(&out, "mqconnector_validate_attempts_total", base, float64(p.ValidateAttempts))
		emitScalar(&out, "mqconnector_validate_failures_total", base, float64(p.ValidateFailures))
		emitScalar(&out, "mqconnector_shadow_sent_total", base, float64(p.ShadowSent))
		emitScalar(&out, "mqconnector_shadow_failed_total", base, float64(p.ShadowFailed))
		emitScalar(&out, "mqconnector_avg_latency_ms", base, p.AvgLatencyMs)
		// pipeline_up: 1 if status="connected", 0 otherwise.
		up := 0.0
		if p.Status == "connected" {
			up = 1
		}
		upLbl := copyMap(base)
		upLbl["status"] = p.Status
		emitScalar(&out, "mqconnector_pipeline_up", upLbl, up)
		if p.SourceDepth >= 0 {
			depthLbl := map[string]string{"pipeline_id": p.PipelineID, "source": p.SourceQueue}
			emitScalar(&out, "mqconnector_source_depth", depthLbl, float64(p.SourceDepth))
		}
		// Latency histogram buckets.
		if len(p.LatencyHistogram) > 0 {
			for _, b := range p.LatencyHistogram {
				bucketLbl := copyMap(base)
				bucketLbl["le"] = formatLEBound(b.LE)
				emitScalar(&out, "mqconnector_pipeline_latency_ms_bucket", bucketLbl, float64(b.Count))
			}
		}
	}
	emitScalar(&out, "mqconnector_uptime_seconds", map[string]string{}, store.Uptime().Seconds())
	return out
}

func emitScalar(out *[]Sample, name string, lbls map[string]string, v float64) {
	labels := copyMap(lbls)
	labels["__name__"] = name
	*out = append(*out, Sample{Labels: labels, Value: v})
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func formatLEBound(le float64) string {
	if le > 1e300 {
		return "+Inf"
	}
	return fmt.Sprintf("%g", le)
}

// StoreSource is the production MetricsSource adapter: a metrics.Store
// for the current snapshot + a metrics.History for the 5-minute
// rate() lookback. Both are optional; a nil History returns
// (0, false) from ValueAt so rate() short-circuits to zero rather
// than computing an arbitrary spike against the same-tick value.
type StoreSource struct {
	Store   *metrics.Store
	History *metrics.History
}

// Snapshot implements MetricsSource.
func (s StoreSource) Snapshot() []Sample {
	return SnapshotFromMetricsStore(s.Store)
}

// ValueAt implements MetricsSource. The labels map MUST include
// `pipeline_id` for non-zero results — the in-process History keys on
// pipeline id alone.
func (s StoreSource) ValueAt(metric string, labels map[string]string, ago time.Duration) (float64, bool) {
	if s.History == nil {
		return 0, false
	}
	pipelineID := labels["pipeline_id"]
	if pipelineID == "" {
		return 0, false
	}
	return s.History.ValueAt(metric, pipelineID, ago)
}
