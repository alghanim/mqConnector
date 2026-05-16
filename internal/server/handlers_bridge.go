package server

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/storage"
)

func (s *Server) handleBridgePublish(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "connectionId")
	conn, err := s.store.Connections.Get(r.Context(), tenant, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "connection not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty body")
		return
	}

	cfg := pipeline.ToMQConfig(conn)
	c, release, err := s.pool.Get(r.Context(), "bridge-pub-"+id, cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer release()
	if err := c.SendMessage(r.Context(), body); err != nil {
		writeError(w, http.StatusInternalServerError, "send: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "published", "bytes": len(body)})
}

func (s *Server) handleBridgeConsume(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "connectionId")
	conn, err := s.store.Connections.Get(r.Context(), tenant, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "connection not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	cfg := pipeline.ToMQConfig(conn)
	c, release, err := s.pool.Get(r.Context(), "bridge-con-"+id, cfg)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer release()
	msg, err := c.ReceiveMessage(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "receive: "+err.Error())
		return
	}
	format := pipeline.Detect(msg)
	switch format {
	case pipeline.FormatJSON:
		w.Header().Set("Content-Type", "application/json")
	case pipeline.FormatXML:
		w.Header().Set("Content-Type", "application/xml")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	_, _ = w.Write(msg)
}
