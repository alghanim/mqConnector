package server

import (
	"context"
	"time"

	"mqConnector/internal/explain"
	"mqConnector/internal/metrics"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/storage"
)

// buildExplainEngine wires the explain.Engine against the
// server's live dependencies. Returned engine is safe to use from
// concurrent handler goroutines — each Source method is itself
// concurrency-safe (the underlying repos hold a single *sql.DB,
// the metrics store guards its map, the breaker accessor takes
// the manager's mutex).
//
// Adapters live in this file rather than the explain package so
// the explain package stays free of *storage / *metrics /
// *pipeline imports beyond the leaf data types.
func buildExplainEngine(
	store *storage.Store,
	metricsStore *metrics.Store,
	pipelineMgr *pipeline.Manager,
) *explain.Engine {
	eng := &explain.Engine{}
	if metricsStore != nil {
		eng.Metrics = explainMetricsAdapter{store: metricsStore}
	}
	if store != nil {
		if store.DLQ != nil {
			eng.DLQ = explainDLQAdapter{repo: store.DLQ}
		}
		if store.Audit != nil {
			eng.Audit = explainAuditAdapter{repo: store.Audit}
		}
		if store.Pipelines != nil || store.PipelineRevisions != nil {
			eng.Pipelines = explainPipelinesAdapter{
				pipelines: store.Pipelines,
				revisions: store.PipelineRevisions,
			}
		}
	}
	if pipelineMgr != nil {
		eng.Breakers = explainBreakersAdapter{mgr: pipelineMgr}
	}
	return eng
}

// explainMetricsAdapter wraps *metrics.Store to satisfy
// explain.MetricsSource. The tenantID parameter is ignored —
// metrics are not tenant-keyed at the in-process store; tenant
// scoping happens at the pipeline-id selection layer (only
// pipelines visible to the caller end up being queried).
//
// Also satisfies explain.StageHistogramsSource so the latency
// explainer can pull per-stage histograms via the type assertion.
type explainMetricsAdapter struct {
	store *metrics.Store
}

// SnapshotPipeline implements explain.MetricsSource.
func (a explainMetricsAdapter) SnapshotPipeline(_, pipelineID string) (metrics.Pipeline, bool) {
	if a.store == nil {
		return metrics.Pipeline{}, false
	}
	snap := a.store.Snapshot()
	p, ok := snap[pipelineID]
	return p, ok
}

// Snapshot implements explain.MetricsSource.
func (a explainMetricsAdapter) Snapshot() map[string]metrics.Pipeline {
	if a.store == nil {
		return map[string]metrics.Pipeline{}
	}
	return a.store.Snapshot()
}

// StageHistogramsFor implements explain.StageHistogramsSource —
// optional extension picked up by the latency explainer via a
// type assertion.
func (a explainMetricsAdapter) StageHistogramsFor(_, pipelineID string) []metrics.StageHistogramSnapshot {
	if a.store == nil {
		return nil
	}
	return a.store.StageHistogramsFor(pipelineID)
}

// explainDLQAdapter wraps *storage.DLQRepo to satisfy
// explain.DLQSource. Delegates 1:1 to the repo helpers added in
// the same wave.
type explainDLQAdapter struct{ repo *storage.DLQRepo }

func (a explainDLQAdapter) RecentForPipeline(ctx context.Context, tenantID, pipelineID string, limit int, since time.Time) ([]*storage.DLQEntry, error) {
	if a.repo == nil {
		return nil, nil
	}
	return a.repo.RecentForPipeline(ctx, tenantID, pipelineID, limit, since)
}

func (a explainDLQAdapter) GetEntry(ctx context.Context, tenantID, id string) (*storage.DLQEntry, error) {
	if a.repo == nil {
		return nil, storage.ErrNotFound
	}
	return a.repo.Get(ctx, tenantID, id)
}

func (a explainDLQAdapter) ClusterByFingerprint(ctx context.Context, tenantID, fingerprint string) (storage.DLQClusterRow, error) {
	if a.repo == nil {
		return storage.DLQClusterRow{}, storage.ErrNotFound
	}
	return a.repo.ClusterByFingerprint(ctx, tenantID, fingerprint)
}

// explainAuditAdapter wraps *storage.AuditRepo.
type explainAuditAdapter struct{ repo *storage.AuditRepo }

func (a explainAuditAdapter) RecentForResource(ctx context.Context, tenantID, resource string, limit int) ([]*storage.AuditEntry, error) {
	if a.repo == nil {
		return nil, nil
	}
	return a.repo.RecentForResource(ctx, tenantID, resource, limit)
}

// explainPipelinesAdapter wraps both the pipeline + revision
// repos behind explain.PipelinesSource.
type explainPipelinesAdapter struct {
	pipelines *storage.PipelineRepo
	revisions *storage.PipelineRevisionRepo
}

func (a explainPipelinesAdapter) Get(ctx context.Context, tenantID, id string) (*storage.Pipeline, error) {
	if a.pipelines == nil {
		return nil, storage.ErrNotFound
	}
	return a.pipelines.Get(ctx, tenantID, id)
}

func (a explainPipelinesAdapter) LatestDeployedRevision(ctx context.Context, tenantID, pipelineID string) (*storage.PipelineRevision, error) {
	if a.revisions == nil {
		return nil, storage.ErrNotFound
	}
	return a.revisions.LatestDeployed(ctx, tenantID, pipelineID)
}

// explainBreakersAdapter wraps *pipeline.Manager. History returns
// an empty slice today — see the doc on explain.BreakersSource;
// a Wave-4 follow-up patch on the executor will start populating
// it.
type explainBreakersAdapter struct{ mgr *pipeline.Manager }

func (a explainBreakersAdapter) State(pipelineID string) string {
	if a.mgr == nil {
		return "unknown"
	}
	return a.mgr.CircuitStateForPipeline(pipelineID)
}

func (a explainBreakersAdapter) History(_ string) []explain.BreakerTransition {
	// Empty for v1 — Wave 4 follow-up will land transition
	// logging on the executor.
	return nil
}
