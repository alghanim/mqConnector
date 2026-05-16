package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/mq"
	"mqConnector/internal/mqcfg"
	"mqConnector/internal/storage"
)

// handleTestConnection opens a fresh connector against the named
// connection's stored config, runs a single liveness probe (Connect + Ping),
// and reports back. The probe does NOT touch the pool or persist anything —
// it's a read-only dry-run that operators can hit from the UI before saving.
//
// POST /api/v1/connections/{id}/test
//
// Optional body: {"queue":"override-queue-name"} to override the stored
// queue/topic for the duration of the probe. Useful when an operator wants
// to verify they have the right credentials before deciding what queue to
// point at.
//
// Response shape: {"ok":true/false, "elapsed_ms":..., "error":"...if failed"}
func (s *Server) handleTestConnection(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	rec, err := s.store.Connections.Get(r.Context(), tenant, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "connection not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Optional payload to override transient fields.
	var override struct {
		Queue string `json:"queue"`
		Topic string `json:"topic"`
	}
	if r.Body != nil && r.ContentLength > 0 {
		_ = decodeJSON(r, &override)
	}
	if override.Queue != "" {
		rec.QueueName = override.Queue
	}
	if override.Topic != "" {
		rec.Topic = override.Topic
	}

	cfg := mqcfg.From(rec)
	conn, err := mq.New(cfg)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "construct: " + err.Error(),
		})
		return
	}
	// Probe budget — keep short so a stuck broker doesn't pin the handler.
	probeCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	start := time.Now()
	if err := conn.Connect(probeCtx); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         false,
			"elapsed_ms": time.Since(start).Milliseconds(),
			"error":      "connect: " + err.Error(),
		})
		return
	}
	// Use Ping for liveness; tear the connection down regardless.
	pingErr := conn.Ping(probeCtx)
	_ = conn.Disconnect()

	resp := map[string]any{
		"ok":         pingErr == nil,
		"elapsed_ms": time.Since(start).Milliseconds(),
	}
	if pingErr != nil {
		resp["error"] = "ping: " + pingErr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}
