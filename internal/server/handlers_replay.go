// Replay endpoint — re-run historical messages through a pipeline.
//
// POST /api/v1/pipelines/{id}/replay
//   body: {"since": "2026-05-16T13:00:00Z", "until": "2026-05-16T13:30:00Z"}
//
// Supported on Kafka and NATS JetStream sources (anything that retains
// committed messages broker-side). RabbitMQ / MQTT / core NATS / AMQP
// 1.0 don't retain consumed messages so replay isn't meaningful there —
// the handler returns 400 with a clear reason.

package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/pipeline"
)

type replayRequest struct {
	Since time.Time `json:"since"`
	Until time.Time `json:"until"`
}

func (s *Server) handleReplayPipeline(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tenant := auth.TenantID(r.Context())
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}

	var req replayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	if req.Since.IsZero() || req.Until.IsZero() {
		writeError(w, http.StatusBadRequest, "since and until are required ISO-8601 timestamps")
		return
	}
	if !req.Until.After(req.Since) {
		writeError(w, http.StatusBadRequest, "until must be after since")
		return
	}
	// Bound the window — a 7-day replay on a busy topic would chew
	// through gigabytes and lock the API call for an arbitrary
	// duration. Operators who need bigger windows can split the call.
	if req.Until.Sub(req.Since) > 24*time.Hour {
		writeError(w, http.StatusBadRequest, "replay window cannot exceed 24h; split into multiple calls")
		return
	}

	s.logger.Info("replay requested",
		"tenant_id", tenant,
		"pipeline_id", id,
		"since", req.Since.Format(time.RFC3339),
		"until", req.Until.Format(time.RFC3339),
	)

	result, err := s.pipeline.Replay(r.Context(), id, pipeline.ReplayWindow{
		Since: req.Since,
		Until: req.Until,
	})
	if err != nil {
		if errors.Is(err, pipeline.ErrReplayNotSupported) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
