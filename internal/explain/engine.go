package explain

import (
	"context"
	"errors"
	"time"

	"mqConnector/internal/metrics"
	"mqConnector/internal/storage"
)

// Errors returned by Engine.Explain. Handlers map them to HTTP
// status codes (400 unknown subject, 404 unknown id, 500 anything
// else).
var (
	// ErrUnknownSubject is returned when subject is not one of the
	// known explainer keys. Handlers map this to 400.
	ErrUnknownSubject = errors.New("explain: unknown subject")
	// ErrNotFound is returned when the subject is valid but no
	// matching id exists (e.g. unknown pipeline_id for "circuit",
	// unknown fingerprint for "dlq_cluster"). Handlers map this to
	// 404. Cross-tenant ids look identical to unknown ids — by
	// design, no information leak.
	ErrNotFound = errors.New("explain: id not found for subject")
)

// Engine is the dispatch root. Holds dependencies on existing
// services (metrics store, DLQ repo, audit repo, pipeline manager
// for breaker state) — does NOT own any state of its own. The
// adapter pattern (cmd/mqconnector/main.go wires real services to
// the Source interfaces) keeps the explain package free of
// storage/metrics/pipeline package imports beyond the data types.
type Engine struct {
	// Metrics is the per-pipeline + global counters/histograms
	// source. nil disables metric facts gracefully (latency
	// explainer falls back to "no metrics observed").
	Metrics MetricsSource
	// DLQ is the DLQ row + cluster query source. nil disables
	// DLQ-derived facts.
	DLQ DLQSource
	// Audit is the audit-trail source. nil disables audit-derived
	// facts (e.g. "last deploy at").
	Audit AuditSource
	// Pipelines is the pipeline + revision lookup source. nil
	// disables pipeline existence checks (subjects keyed by
	// pipeline_id then fall back to "no pipeline metadata").
	Pipelines PipelinesSource
	// Breakers is the per-pipeline circuit breaker state source.
	// nil disables breaker facts in the circuit explainer.
	Breakers BreakersSource
	// Clock returns "now" — overridable so tests can pin the
	// AsOf field for deterministic assertions. Defaults to
	// time.Now when nil.
	Clock func() time.Time
}

// now returns the engine's clock or time.Now if unset. Always UTC.
func (e *Engine) now() time.Time {
	if e.Clock != nil {
		return e.Clock().UTC()
	}
	return time.Now().UTC()
}

// Explain dispatches to the right explainer for the given subject.
// Unknown subject → ErrUnknownSubject. Subject not found
// (e.g. unknown pipeline id for "circuit") → ErrNotFound. Any
// other failure inside an explainer is treated as "partial signal"
// and returned with an Explanation carrying whatever facts the
// explainer did manage to collect — never a non-nil error from
// here for a sub-source failure.
func (e *Engine) Explain(ctx context.Context, subject, id, tenantID string) (Explanation, error) {
	if e == nil {
		return Explanation{}, ErrUnknownSubject
	}
	x, ok := e.explainerFor(subject)
	if !ok {
		return Explanation{}, ErrUnknownSubject
	}
	return x.Explain(ctx, id, tenantID)
}

// explainer is the per-subject contract. Implementations are tiny
// structs holding only the Engine pointer (so they can reach the
// shared Sources + Clock). Stateless from the engine's point of
// view.
type explainer interface {
	Explain(ctx context.Context, id, tenantID string) (Explanation, error)
}

// explainerFor returns the explainer registered for the subject.
// Centralised so adding a new subject is one line. Test for
// presence via the second return — empty subject returns
// (nil, false).
func (e *Engine) explainerFor(subject string) (explainer, bool) {
	switch subject {
	case "circuit":
		return &circuitExplainer{e: e}, true
	case "drift":
		return &driftExplainer{e: e}, true
	case "latency":
		return &latencyExplainer{e: e}, true
	case "dlq_cluster":
		return &dlqRootCauseExplainer{e: e, mode: dlqModeCluster}, true
	case "dlq_entry":
		return &dlqRootCauseExplainer{e: e, mode: dlqModeEntry}, true
	}
	return nil, false
}

// MetricsSource is the minimal read-shape the explainers need
// against the in-process metrics store. Adapters wrap
// internal/metrics.Store and implement these two methods.
type MetricsSource interface {
	// SnapshotPipeline returns the per-pipeline counters +
	// histograms. ok=false when no entry exists for the pipeline
	// (typical for a pipeline that has never been registered on
	// this replica).
	SnapshotPipeline(tenantID, pipelineID string) (metrics.Pipeline, bool)
	// Snapshot returns a copy of every per-pipeline entry. Used
	// by explainers that need to compare a target pipeline
	// against the global picture.
	Snapshot() map[string]metrics.Pipeline
}

// DLQSource is the minimal DLQ-repo surface the explainers consume.
// Each method is tenant-scoped — callers pass the tenant id
// explicitly and adapters forward it to the underlying repo.
type DLQSource interface {
	// RecentForPipeline returns up to `limit` DLQ rows whose
	// pipeline_id matches and whose created_at is at or after
	// `since`. Newest-first. since.IsZero() means no lower bound.
	RecentForPipeline(ctx context.Context, tenantID, pipelineID string, limit int, since time.Time) ([]*storage.DLQEntry, error)
	// GetEntry returns one DLQ row tenant-scoped. ErrNotFound
	// when the row doesn't exist or belongs to a different
	// tenant.
	GetEntry(ctx context.Context, tenantID, id string) (*storage.DLQEntry, error)
	// ClusterByFingerprint returns the rollup row for a single
	// fingerprint within the tenant. ErrNotFound when no rows
	// share the fingerprint.
	ClusterByFingerprint(ctx context.Context, tenantID, fingerprint string) (storage.DLQClusterRow, error)
}

// AuditSource is the minimal audit-repo surface. Used to correlate
// state changes with config deploys (e.g. "last validate-stage
// edit at T").
type AuditSource interface {
	// RecentForResource returns the most recent audit rows whose
	// resource path begins with `resource`. Newest-first.
	// tenant-scoped. limit caps the result; the underlying repo
	// has its own per-page cap as a safety net.
	RecentForResource(ctx context.Context, tenantID, resource string, limit int) ([]*storage.AuditEntry, error)
}

// PipelinesSource is the minimal pipeline + revision repo surface.
// Used to confirm a subject id maps to a real pipeline (so the
// handler can return 404 rather than a misleading degraded
// Explanation) and to surface deploy timestamps.
type PipelinesSource interface {
	// Get returns the pipeline by id, tenant-scoped. ErrNotFound
	// when no row matches.
	Get(ctx context.Context, tenantID, id string) (*storage.Pipeline, error)
	// LatestDeployedRevision returns the most recent revision
	// that has deployed_at != NULL for the pipeline. ErrNotFound
	// when no revision has been deployed.
	LatestDeployedRevision(ctx context.Context, tenantID, pipelineID string) (*storage.PipelineRevision, error)
}

// BreakersSource is the minimal breaker-state surface. State is
// trivial today (one map lookup against the manager); History is
// not yet tracked — implementations may return an empty slice.
type BreakersSource interface {
	// State returns the current breaker token for a pipeline:
	// "closed" | "open" | "half-open" | "unknown". Mirrors
	// pipeline.Manager.CircuitStateForPipeline exactly.
	State(pipelineID string) string
	// History returns the breaker transition log for a pipeline.
	//
	// Implementations may return an empty slice for now — Wave 2
	// only exposed the current state, not a transition log.
	// Wave 4 will add transition logging in a follow-up patch
	// on the executor; until then the circuit explainer falls
	// back to a single-state Fact when this returns empty.
	History(pipelineID string) []BreakerTransition
}

// BreakerTransition is one row of the breaker state-change log.
// At is wall-clock; From/To are state tokens; Reason is the last
// error reason if the implementation tracks it.
type BreakerTransition struct {
	At     time.Time `json:"at"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Reason string    `json:"reason,omitempty"`
}

// emptyFacts returns an empty (non-nil) facts slice — explainers
// guarantee the wire shape is `[]` rather than `null` for
// consumers that strict-parse the type.
func emptyFacts() []Fact { return []Fact{} }
