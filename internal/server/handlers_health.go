package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := s.health.Check(r.Context())
	code := http.StatusOK
	if status.Status == "unhealthy" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, status)
}

func (s *Server) handleMetricsJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"uptime":    s.metrics.Uptime().String(),
		"pipelines": s.metrics.Snapshot(),
	})
}

func (s *Server) handleMetricsPrometheus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	var b strings.Builder
	b.WriteString(s.metrics.Prometheus())
	s.writeOperationalMetrics(&b, r)
	// AI counter — emitted unconditionally so the series is
	// discoverable even when no calls have been made (the
	// renderer outputs HELP + TYPE lines either way).
	if s.aiCounter != nil {
		b.WriteString(s.aiCounter.Prometheus())
	}
	_, _ = w.Write([]byte(b.String()))
}

// writeOperationalMetrics appends series that don't live in the in-process
// pipeline metrics store: DLQ depth/age per pipeline, leadership state,
// encryption-key version. Operators alert on these directly — DLQ growth
// catches a stuck destination, leader-loss catches a split-brain, and
// key version reports rotation status.
func (s *Server) writeOperationalMetrics(b *strings.Builder, r *http.Request) {
	// DLQ depth + oldest-age. Best-effort: a slow query during a scrape
	// shouldn't tank the response; if the query errors, we omit the
	// series rather than poison the rest.
	if s.store != nil && s.store.DLQ != nil {
		ctx, cancel := contextWithTimeout(r, 2*time.Second)
		defer cancel()
		if stats, err := s.store.DLQ.Stats(ctx); err == nil {
			b.WriteString("# HELP mqconnector_dlq_depth Current DLQ row count per pipeline\n")
			b.WriteString("# TYPE mqconnector_dlq_depth gauge\n")
			for _, st := range stats {
				fmt.Fprintf(b, "mqconnector_dlq_depth{pipeline_id=%q} %d\n", st.PipelineID, st.Count)
			}
			b.WriteString("# HELP mqconnector_dlq_oldest_age_seconds Age of the oldest DLQ row per pipeline\n")
			b.WriteString("# TYPE mqconnector_dlq_oldest_age_seconds gauge\n")
			now := time.Now().UTC()
			for _, st := range stats {
				age := 0.0
				if !st.OldestAt.IsZero() {
					age = now.Sub(st.OldestAt).Seconds()
				}
				fmt.Fprintf(b, "mqconnector_dlq_oldest_age_seconds{pipeline_id=%q} %.0f\n",
					st.PipelineID, age)
			}
		}
	}

	// Leadership. Always emit something so operators can alert on the
	// gauge being absent. When leadership is disabled (single-process
	// deploy) we emit is_leader=1 — that's literally true; the local
	// replica is the only one and does run workers.
	b.WriteString("# HELP mqconnector_leader Whether this replica currently holds the leadership lease (1) or not (0)\n")
	b.WriteString("# TYPE mqconnector_leader gauge\n")
	b.WriteString("# HELP mqconnector_leader_lease_remaining_seconds Time until the current leadership lease expires\n")
	b.WriteString("# TYPE mqconnector_leader_lease_remaining_seconds gauge\n")
	if s.leadership == nil {
		fmt.Fprintf(b, "mqconnector_leader{self=%q,holder=%q,mode=%q} 1\n", "", "", "single")
		fmt.Fprintf(b, "mqconnector_leader_lease_remaining_seconds{self=%q,holder=%q,mode=%q} 0\n", "", "", "single")
	} else {
		st := s.leadership.Snapshot()
		isLeader := 0
		if st.IsLeader {
			isLeader = 1
		}
		remaining := 0.0
		if !st.Expires.IsZero() {
			remaining = time.Until(st.Expires).Seconds()
			if remaining < 0 {
				remaining = 0
			}
		}
		fmt.Fprintf(b, "mqconnector_leader{self=%q,holder=%q,mode=%q} %d\n",
			st.Self, st.Holder, "ha", isLeader)
		fmt.Fprintf(b, "mqconnector_leader_lease_remaining_seconds{self=%q,holder=%q,mode=%q} %.0f\n",
			st.Self, st.Holder, "ha", remaining)
	}

	// Master key version. 0 when encryption is disabled (dev-mode
	// only — prod startup refuses to come up without a key).
	b.WriteString("# HELP mqconnector_master_key_version Current AES-GCM master key version (0 when encryption disabled)\n")
	b.WriteString("# TYPE mqconnector_master_key_version gauge\n")
	keyVersion := 0
	if s.sealer != nil && s.sealer.Enabled() {
		keyVersion = s.sealer.Current()
	}
	fmt.Fprintf(b, "mqconnector_master_key_version %d\n", keyVersion)
}

// contextWithTimeout returns a child of r.Context() bounded by d.
// Pulled out as a helper because the operational-metrics renderer
// needs a short cap so a slow DLQ query doesn't block the scrape.
func contextWithTimeout(r *http.Request, d time.Duration) (ctx context.Context, cancel context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
