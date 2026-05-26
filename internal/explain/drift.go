package explain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mqConnector/internal/storage"
)

// driftExplainer answers "why is validation failing more / less
// than expected?". Subject "drift", id is the pipeline id.
//
// The canonical use case is producer schema drift — when
// mqconnector_validate_failures_total rises against
// _attempts_total, a producer changed contract.
//
// Reads:
//   - MetricsSource.SnapshotPipeline → validate attempts / failures.
//   - DLQSource.RecentForPipeline    → group by error_template
//     for top failure flavours.
//   - AuditSource.RecentForResource   → "last validate-stage edit"
//     — narrative section if
//     the timing correlates.
type driftExplainer struct{ e *Engine }

// driftRecentLimit bounds the DLQ scan used for template
// grouping. Generous enough to surface multiple distinct
// templates without dragging hundreds of rows.
const driftRecentLimit = 50

// Tier thresholds for the validate-failure ratio. <1% is normal
// noise (warm-up, transient producer issues); 1-20% is a
// developing problem worth highlighting; >20% is a producer that
// definitively broke contract.
const (
	driftWarnRatio = 0.01
	driftCritRatio = 0.20
)

// Explain dispatches per-subject; see Engine.Explain for the
// wire shape.
func (d *driftExplainer) Explain(ctx context.Context, pipelineID, tenantID string) (Explanation, error) {
	if d.e == nil || pipelineID == "" {
		return Explanation{}, ErrNotFound
	}
	if d.e.Pipelines != nil {
		if _, err := d.e.Pipelines.Get(ctx, tenantID, pipelineID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return Explanation{}, ErrNotFound
			}
		}
	}

	snap, hasSnap := lookupPipelineSnapshot(d.e.Metrics, tenantID, pipelineID)
	dlqEntries, _ := recentDLQEntries(ctx, d.e.DLQ, tenantID, pipelineID, driftRecentLimit)
	// We narrow the audit prefix to the stages route because the
	// validate stage's config travels with /api/v1/pipelines/{id}/stages.
	stagesAudit, _ := recentAuditForResource(ctx, d.e.Audit,
		tenantID, "/api/v1/pipelines/"+pipelineID+"/stages", 5)

	var attempts, failures int64
	if hasSnap {
		attempts = snap.ValidateAttempts
		failures = snap.ValidateFailures
	}
	ratio := float64(0)
	if attempts > 0 {
		ratio = float64(failures) / float64(attempts)
	}
	headline, severity := driftHeadline(attempts, failures, ratio)

	facts := emptyFacts()
	facts = append(facts, Fact{
		Label:  "Validate attempts (cumulative)",
		Value:  fmt.Sprintf("%d", attempts),
		Source: "mqconnector_validate_attempts_total",
	})
	facts = append(facts, Fact{
		Label:  "Validate failures (cumulative)",
		Value:  fmt.Sprintf("%d", failures),
		Source: "mqconnector_validate_failures_total",
	})
	facts = append(facts, Fact{
		Label:  "Failure ratio",
		Value:  fmt.Sprintf("%.2f%%", ratio*100),
		Source: "derived",
	})

	topTemplates := distinctErrorTemplates(dlqEntries, 3)
	for i, tc := range topTemplates {
		facts = append(facts, Fact{
			Label:  fmt.Sprintf("Top error template #%d (%d hits)", i+1, tc.Count),
			Value:  truncate(tc.Template, 200),
			Source: "storage.DLQRepo",
			AsOf:   tc.FirstSeen.UTC().Format(time.RFC3339),
		})
	}

	var lastEdit *storage.AuditEntry
	if len(stagesAudit) > 0 {
		lastEdit = stagesAudit[0]
		facts = append(facts, Fact{
			Label:  "Last validate-stage edit",
			Value:  fmt.Sprintf("%s by %s (status=%d)", lastEdit.Action, lastEdit.Actor, lastEdit.Status),
			Source: "storage.AuditRepo",
			AsOf:   lastEdit.At.UTC().Format(time.RFC3339),
		})
	}

	exp := Explanation{
		Subject:  "drift",
		ID:       pipelineID,
		Headline: headline,
		Severity: severity,
		Facts:    facts,
		AsOf:     d.e.now(),
		Sources:  []string{"metrics.Snapshot", "storage.DLQRepo", "storage.AuditRepo"},
	}

	// Narrative section when there's a correlated stages edit
	// within the last 24h AND a non-trivial failure ratio. The
	// claim is hedged — "failures correlate" not "failures are
	// caused by" — because the explainer can't prove causality.
	if lastEdit != nil && ratio >= driftWarnRatio &&
		d.e.now().Sub(lastEdit.At) < 24*time.Hour {
		narrative := fmt.Sprintf(
			"Validate-stage configuration was edited at %s by %s; failures since then correlate with that change.",
			lastEdit.At.UTC().Format(time.RFC3339), lastEdit.Actor)
		data, _ := json.Marshal(map[string]string{"text": narrative})
		exp.Sections = append(exp.Sections, Section{
			Kind:  "narrative",
			Title: "Recent change",
			Data:  data,
		})
	}
	return exp, nil
}

// driftHeadline maps (attempts, failures, ratio) to a tiered
// headline. Three branches, deterministic — operators can predict
// what they'll see from the counter values.
func driftHeadline(attempts, failures int64, ratio float64) (string, Severity) {
	if attempts == 0 {
		return "No validate-stage observations yet — no drift signal available.", SeverityInfo
	}
	pct := ratio * 100
	switch {
	case ratio >= driftCritRatio:
		return fmt.Sprintf("Validate failure rate is %.2f%% (%d/%d) — likely producer schema drift.", pct, failures, attempts),
			SeverityCritical
	case ratio >= driftWarnRatio:
		return fmt.Sprintf("Validate failure rate is %.2f%% (%d/%d) — elevated, worth investigating.", pct, failures, attempts),
			SeverityWarning
	default:
		return fmt.Sprintf("Validate failure rate is %.2f%% (%d/%d) — within normal envelope.", pct, failures, attempts),
			SeverityInfo
	}
}
