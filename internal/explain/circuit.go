package explain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mqConnector/internal/storage"
)

// circuitExplainer answers "why is this pipeline's outbound
// circuit in its current state?". Subject "circuit", id is the
// pipeline id.
//
// Reads: BreakersSource.State + History, DLQSource.RecentForPipeline
// (for representative failure reasons), AuditSource.RecentForResource
// (correlate breaker state with the most recent pipeline edit /
// deploy), MetricsSource.SnapshotPipeline (processed / failed
// counters + last error).
type circuitExplainer struct{ e *Engine }

// circuitRecentDLQLimit bounds the DLQ scan. Five rows give the
// operator a sense of the failure flavours without making the
// prompt-shaped output unwieldy.
const circuitRecentDLQLimit = 5

// circuitAuditResourcePrefix matches every PUT/POST on a
// pipeline's child resources (stages, transforms, deploy,
// revisions/{n}/rollback). The AuditRepo's List path uses a
// LIKE 'prefix%' under the hood so this glob style returns the
// whole subtree.
func circuitAuditResourcePrefix(pipelineID string) string {
	return "/api/v1/pipelines/" + pipelineID
}

// Explain dispatches to the explain root. See package-level docs
// for the wire shape.
func (c *circuitExplainer) Explain(ctx context.Context, pipelineID, tenantID string) (Explanation, error) {
	if c.e == nil || pipelineID == "" {
		return Explanation{}, ErrNotFound
	}
	// Existence check. Returning ErrNotFound here lets the HTTP
	// handler emit a clean 404 instead of a misleading degraded
	// Explanation for a pipeline that simply doesn't exist.
	if c.e.Pipelines != nil {
		if _, err := c.e.Pipelines.Get(ctx, tenantID, pipelineID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return Explanation{}, ErrNotFound
			}
			// Other error → fall through to degraded explanation;
			// best-effort beats failing the whole call.
		}
	}

	state := "unknown"
	if c.e.Breakers != nil {
		state = c.e.Breakers.State(pipelineID)
	}

	// Pull supporting telemetry. Each branch logs nothing — the
	// HTTP layer's slog catches sub-source warnings via the
	// Sources we report.
	var snap, hasSnap = lookupPipelineSnapshot(c.e.Metrics, tenantID, pipelineID)
	dlqEntries, _ := recentDLQEntries(ctx, c.e.DLQ, tenantID, pipelineID, circuitRecentDLQLimit)
	auditEntries, _ := recentAuditForResource(ctx, c.e.Audit, tenantID, circuitAuditResourcePrefix(pipelineID), 5)

	headline, severity := circuitHeadline(state, snap, hasSnap, len(dlqEntries))
	facts := emptyFacts()

	facts = append(facts, Fact{
		Label:  "Current state",
		Value:  state,
		Source: "pipeline.Manager.CircuitStateForPipeline",
	})

	if hasSnap {
		facts = append(facts, Fact{
			Label:  "Processed (cumulative)",
			Value:  fmt.Sprintf("%d", snap.MessagesProcessed),
			Source: "mqconnector_messages_processed_total",
		})
		facts = append(facts, Fact{
			Label:  "Failed (cumulative)",
			Value:  fmt.Sprintf("%d", snap.MessagesFailed),
			Source: "mqconnector_messages_failed_total",
		})
		if snap.LastError != "" {
			facts = append(facts, Fact{
				Label:  "Last error",
				Value:  truncate(snap.LastError, 200),
				Source: "metrics.Pipeline.LastError",
			})
		}
	}

	// Sample up to three representative failure reasons from
	// recent DLQ entries. Operators triage faster when they can
	// see the actual failure text alongside the state token.
	maxSamples := 3
	if len(dlqEntries) < maxSamples {
		maxSamples = len(dlqEntries)
	}
	for i := 0; i < maxSamples; i++ {
		entry := dlqEntries[i]
		facts = append(facts, Fact{
			Label:  fmt.Sprintf("Recent failure #%d", i+1),
			Value:  truncate(entry.ErrorReason, 200),
			Source: "storage.DLQRepo",
			AsOf:   entry.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	// Last deploy / config edit gives temporal context to the
	// breaker state — "trip happened after deploy at T" is one
	// of the most common root causes.
	if len(auditEntries) > 0 {
		last := auditEntries[0]
		facts = append(facts, Fact{
			Label:  "Last pipeline edit",
			Value:  fmt.Sprintf("%s by %s", last.Resource, last.Actor),
			Source: "storage.AuditRepo",
			AsOf:   last.At.UTC().Format(time.RFC3339),
		})
	}

	exp := Explanation{
		Subject:  "circuit",
		ID:       pipelineID,
		Headline: headline,
		Severity: severity,
		Facts:    facts,
		AsOf:     c.e.now(),
		Sources:  []string{"metrics.Snapshot", "storage.DLQRepo", "storage.AuditRepo", "pipeline.Manager"},
	}

	// Timeline section when the breaker source supplies a
	// transition log. Today's BreakersSource.History returns
	// empty — Wave 4 follow-up patch on the executor will start
	// populating it. Keeping the renderer wired now means the
	// follow-up is a pure data change.
	if c.e.Breakers != nil {
		hist := c.e.Breakers.History(pipelineID)
		if len(hist) > 0 {
			data, _ := json.Marshal(hist)
			exp.Sections = append(exp.Sections, Section{
				Kind:  "timeline",
				Title: "Breaker transitions",
				Data:  data,
			})
		}
	}
	return exp, nil
}

// circuitHeadline produces the per-state headline + severity.
// Three states + an unknown bucket. processed/failed counts only
// influence the wording when the snapshot is present — we never
// invent numbers.
func circuitHeadline(state string, snap pipelineSnapshot, hasSnap bool, dlqCount int) (string, Severity) {
	switch state {
	case "open":
		if hasSnap && snap.MessagesFailed > 0 {
			return fmt.Sprintf("Circuit is OPEN — %d send failures observed (cumulative).", snap.MessagesFailed),
				SeverityCritical
		}
		if dlqCount > 0 {
			return fmt.Sprintf("Circuit is OPEN — %d recent DLQ entries on this pipeline.", dlqCount),
				SeverityCritical
		}
		return "Circuit is OPEN — outbound sends are short-circuited.", SeverityCritical
	case "half-open":
		return "Circuit is HALF-OPEN — cool-down elapsed, a probe send is in flight.", SeverityWarning
	case "closed":
		if hasSnap && snap.MessagesProcessed > 0 {
			return fmt.Sprintf("Circuit is CLOSED — %d messages flowed successfully.", snap.MessagesProcessed),
				SeverityInfo
		}
		return "Circuit is CLOSED — pipeline is healthy.", SeverityInfo
	default:
		return "Circuit state is UNKNOWN — pipeline is not running on this replica.", SeverityInfo
	}
}
