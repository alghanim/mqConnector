package server

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"mqConnector/internal/auth"
	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
)

// topologyResponse is the single-shot aggregator the Live Topology page
// consumes. One request collects every broker connection, every
// pipeline that flows between them, and the live health/throughput/depth
// signals attached to each. Best-effort: a failure in one sub-source
// (e.g. DLQ count query) logs a warn and zero-fills that column rather
// than failing the whole response — topology is a snapshot view, not a
// transactional API.
type topologyResponse struct {
	GeneratedAt time.Time            `json:"generated_at"`
	TenantID    string               `json:"tenant_id"`
	Connections []topologyConnection `json:"connections"`
	Pipelines   []topologyPipeline   `json:"pipelines"`
}

// topologyConnection is the per-broker view: identity, type, and the
// two live signals the topology page renders next to each node.
type topologyConnection struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Topic string `json:"topic,omitempty"`
	// Depth is the most recent broker-side backlog measurement for
	// any pipeline whose source is this connection. Nil means
	// "unknown" — either no running pipeline samples this source, or
	// the connector type doesn't implement DepthReporter. Treat
	// absence as unknown, not zero.
	Depth *int64 `json:"depth,omitempty"`
	// Connected is true iff the pool currently holds at least one
	// open client keyed under this broker — the pool keys are
	// pipeline-derived (source-{pipelineID}, dest-{pipelineID},
	// route-{connID}, shadow-{pipelineID}), so the handler walks the
	// pipeline list to derive which keys belong to this connection.
	Connected bool `json:"connected"`
}

// topologyPipeline is the per-pipeline view rendered as an edge on the
// topology graph. Field semantics intentionally mirror the
// /api/v1/pipelines list shape so the UI can reuse its existing row
// renderer; the topology-only additions (msg_per_min, dlq_depth,
// circuit_state, route_destination_ids) come from the aggregator's
// extra sub-source reads.
type topologyPipeline struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	SourceID      string `json:"source_id"`
	DestinationID string `json:"destination_id"`
	Enabled       bool   `json:"enabled"`
	// MsgPerMin is the rate over the last sampling interval, computed
	// as delta(messages_processed) / elapsed * 60. First request yields
	// 0 — there's no prior sample to delta against; subsequent requests
	// reflect throughput observed between the two most recent calls.
	MsgPerMin int64 `json:"msg_per_min"`
	// Cumulative counters from the metrics store, point-in-time.
	Processed    int64   `json:"processed"`
	Failed       int64   `json:"failed"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
	// DlqDepth is the count of dlq rows currently attached to this
	// pipeline (NOT pruned). Independent of retry state.
	DlqDepth int64 `json:"dlq_depth"`
	// CircuitState is the outbound breaker state for the destination
	// send: "closed" (healthy), "open" (failing), "half-open"
	// (probing), or "unknown" (no breaker activity observed yet, or
	// the pipeline isn't running on this replica).
	CircuitState string `json:"circuit_state"`
	// Shadow / canary fields (Wave 1.3.0). Omitted when no shadow
	// destination is configured.
	ShadowDestID  string `json:"shadow_destination_id,omitempty"`
	ShadowPercent int    `json:"shadow_percent,omitempty"`
	// RouteDestIDs is the deduplicated set of destination connection
	// IDs reachable via this pipeline's routing rules. Order matches
	// rule priority (ascending). Omitted when empty.
	RouteDestIDs []string `json:"route_destination_ids,omitempty"`
	// Status mirrors what /api/v1/pipelines/{id} returns:
	// "connected" | "error" | "disabled" | "idle".
	Status    string `json:"status"`
	LastError string `json:"last_error,omitempty"`
}

// topologyRateSampler keeps a tiny per-pipeline history of the last
// observed (timestamp, cumulative messages_processed) so we can
// compute msg-per-min as a real rate rather than guessing from the
// cumulative counter alone. The frontend SystemPulse / dashboard
// sparkline does the same trick client-side; this is the server-side
// equivalent for the single-shot topology aggregator.
//
// Memory shape: one int64+time per ever-seen pipeline ID; entries are
// not actively pruned (a deleted pipeline simply stops being read). On
// a deployment with thousands of pipelines the worst case is a few
// hundred KB — well under any reasonable budget.
type topologyRateSampler struct {
	mu      sync.Mutex
	samples map[string]topologyRateSample
}

type topologyRateSample struct {
	at        time.Time
	processed int64
}

func newTopologyRateSampler() *topologyRateSampler {
	return &topologyRateSampler{samples: map[string]topologyRateSample{}}
}

// observe records the latest cumulative counter for one pipeline and
// returns the per-minute rate against the prior sample. First call
// (no prior) returns 0. Resetting counters (e.g. process restart) is
// guarded against — a negative delta returns 0 rather than a wildly
// negative rate.
func (s *topologyRateSampler) observe(pipelineID string, processed int64, now time.Time) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, ok := s.samples[pipelineID]
	s.samples[pipelineID] = topologyRateSample{at: now, processed: processed}
	if !ok {
		return 0
	}
	elapsed := now.Sub(prev.at).Seconds()
	if elapsed <= 0 {
		return 0
	}
	delta := processed - prev.processed
	if delta <= 0 {
		return 0
	}
	return int64(float64(delta) / elapsed * 60.0)
}

// handleTopology returns the single aggregator snapshot. Viewer
// minimum — sits in the same authenticated group as every other
// /api/v1/* endpoint. Tenant-scoped: every sub-source query is
// filtered by the caller's resolved tenant.
func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tenant := auth.TenantID(ctx)
	logger := logging.FromContext(ctx)

	// Read sources in a fixed order so the response is deterministic.
	// Sub-source failures degrade gracefully — a partial answer beats
	// a 500.
	conns, err := s.store.Connections.List(ctx, tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pipes, err := s.store.Pipelines.List(ctx, tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Metrics snapshot — built ONCE for the whole response so per-
	// pipeline lookups are O(1).
	var snapshot map[string]metrics.Pipeline
	if s.metrics != nil {
		snapshot = s.metrics.Snapshot()
	}

	// DLQ depth per pipeline. Best-effort: a failed query returns an
	// empty map and the per-pipeline column shows 0 for everyone.
	dlqByPipeline := map[string]int64{}
	if s.store != nil && s.store.DLQ != nil {
		if depths, err := s.store.DLQ.CountByPipeline(ctx, tenant); err != nil {
			logger.Warn("topology: dlq count by pipeline failed", "err", err)
		} else {
			dlqByPipeline = depths
		}
	}

	// Pool key snapshot for the "connected" column. Building the set
	// once and intersecting against pipeline-derived keys avoids the
	// N*M cost of one pool.Has per (connection, pipeline) pair.
	poolKeys := map[string]struct{}{}
	if s.pool != nil {
		for _, k := range s.pool.Keys() {
			poolKeys[k] = struct{}{}
		}
	}

	// Per-pipeline routing rules (best-effort per pipeline).
	// One query per pipeline is fine — pipelines are bounded by tenant
	// size and the routing_rules table is indexed on pipeline_id.
	// A failure on any one pipeline logs a warn and leaves
	// route_destination_ids empty for that pipeline.
	routesByPipeline := map[string][]string{}
	if s.store != nil && s.store.RoutingRules != nil {
		for _, p := range pipes {
			rules, err := s.store.RoutingRules.ListByPipeline(ctx, tenant, p.ID)
			if err != nil {
				logger.Warn("topology: list routing rules failed",
					"err", err, "pipeline_id", p.ID)
				continue
			}
			if len(rules) == 0 {
				continue
			}
			seen := make(map[string]bool, len(rules))
			dests := make([]string, 0, len(rules))
			for _, rr := range rules {
				if !rr.Enabled || rr.DestinationID == "" {
					continue
				}
				if seen[rr.DestinationID] {
					continue
				}
				seen[rr.DestinationID] = true
				dests = append(dests, rr.DestinationID)
			}
			if len(dests) > 0 {
				routesByPipeline[p.ID] = dests
			}
		}
	}

	// Build pipeline rows first so we can also derive per-connection
	// "connected" + "depth" from the pipeline → connection mapping in
	// a single pass.
	now := time.Now().UTC()
	if s.topologyRates == nil {
		// Lazy init protected by the server-wide lock the route
		// middleware already holds; concurrent topology calls land
		// here at most once.
		s.topologyRatesMu.Lock()
		if s.topologyRates == nil {
			s.topologyRates = newTopologyRateSampler()
		}
		s.topologyRatesMu.Unlock()
	}

	pipelineRows := make([]topologyPipeline, 0, len(pipes))
	// connDepth holds the largest observed depth across every pipeline
	// whose source is this connection; "largest" rather than "first"
	// gives the most actionable signal when multiple pipelines share a
	// source.
	connDepth := map[string]int64{}
	connDepthSeen := map[string]bool{}
	// connConnected is true iff any pool key tied to a pipeline using
	// this connection (as source, destination, route destination, or
	// shadow destination) is currently live.
	connConnected := map[string]bool{}

	for _, p := range pipes {
		row := topologyPipeline{
			ID:            p.ID,
			Name:          p.Name,
			SourceID:      p.SourceID,
			DestinationID: p.DestinationID,
			Enabled:       p.Enabled,
			ShadowDestID:  p.ShadowDestinationID,
			ShadowPercent: p.ShadowPercent,
			RouteDestIDs:  routesByPipeline[p.ID],
			DlqDepth:      dlqByPipeline[p.ID],
		}
		// Metric-derived fields. Missing snapshot entry → counters
		// stay at zero, status defaults to "idle" / "disabled" below.
		if m, ok := snapshot[p.ID]; ok {
			row.Processed = m.MessagesProcessed
			row.Failed = m.MessagesFailed
			row.AvgLatencyMs = m.AvgLatencyMs
			row.Status = m.Status
			row.LastError = m.LastError
			row.MsgPerMin = s.topologyRates.observe(p.ID, m.MessagesProcessed, now)
			if m.SourceDepth >= 0 && p.SourceID != "" {
				cur, seen := connDepth[p.SourceID]
				if !seen || m.SourceDepth > cur {
					connDepth[p.SourceID] = m.SourceDepth
					connDepthSeen[p.SourceID] = true
				}
			}
		}
		if row.Status == "" {
			if !p.Enabled {
				row.Status = "disabled"
			} else {
				row.Status = "idle"
			}
		}
		// Breaker state — "unknown" when the pipeline isn't running
		// on this replica or hasn't published a state yet.
		if s.pipeline != nil {
			row.CircuitState = s.pipeline.CircuitStateForPipeline(p.ID)
		} else {
			row.CircuitState = "unknown"
		}

		// Mark every connection this pipeline touches as connected
		// if any pipeline-derived pool key for it is live. The pool
		// key naming is owned by the executor — see internal/pipeline/
		// executor.go (source-{pipelineID}, dest-{pipelineID},
		// route-{connID}, shadow-{pipelineID}). The check covers both
		// single-worker keys and the worker-suffixed variants
		// (-w1, -w2, …).
		if len(poolKeys) > 0 {
			if connectionLivePool(poolKeys, "source-"+p.ID) {
				connConnected[p.SourceID] = true
			}
			if _, ok := poolKeys["dest-"+p.ID]; ok {
				connConnected[p.DestinationID] = true
			}
			if p.ShadowDestinationID != "" {
				if _, ok := poolKeys["shadow-"+p.ID]; ok {
					connConnected[p.ShadowDestinationID] = true
				}
			}
			for _, rd := range row.RouteDestIDs {
				if _, ok := poolKeys["route-"+rd]; ok {
					connConnected[rd] = true
				}
			}
		}

		pipelineRows = append(pipelineRows, row)
	}

	// Build connection rows last so we can populate Depth + Connected
	// from the per-pipeline pass above without a second loop.
	connectionRows := make([]topologyConnection, 0, len(conns))
	for _, c := range conns {
		row := topologyConnection{
			ID:        c.ID,
			Name:      c.Name,
			Type:      c.Type,
			Topic:     c.Topic,
			Connected: connConnected[c.ID],
		}
		if connDepthSeen[c.ID] {
			d := connDepth[c.ID]
			row.Depth = &d
		}
		connectionRows = append(connectionRows, row)
	}

	writeJSON(w, http.StatusOK, topologyResponse{
		GeneratedAt: now,
		TenantID:    tenant,
		Connections: connectionRows,
		Pipelines:   pipelineRows,
	})
}

// connectionLivePool reports whether any key in keys matches base or
// base+"-w<N>" for any worker index. The pool uses base for the
// single-worker historical key and suffixes per-worker keys when
// workers > 1 — see executor.go runWorker.
func connectionLivePool(keys map[string]struct{}, base string) bool {
	if _, ok := keys[base]; ok {
		return true
	}
	prefix := base + "-w"
	for k := range keys {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}
