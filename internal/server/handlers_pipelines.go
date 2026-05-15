package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/storage"
)

func (s *Server) handleListPipelines(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Pipelines.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, list)
}

func (s *Server) handleGetPipeline(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := s.store.Pipelines.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleCreatePipeline(w http.ResponseWriter, r *http.Request) {
	var p storage.Pipeline
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if p.Name == "" || p.SourceID == "" || p.DestinationID == "" {
		writeError(w, http.StatusBadRequest, "name, source_id and destination_id are required")
		return
	}
	if err := s.store.Pipelines.Create(r.Context(), &p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go s.reloadPipelines() // hot reload after structural change
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdatePipeline(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var p storage.Pipeline
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p.ID = id
	if err := s.store.Pipelines.Update(r.Context(), &p); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go s.reloadPipelines()
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeletePipeline(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.Pipelines.Delete(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go s.reloadPipelines()
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleListStages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stages, err := s.store.Stages.ListByPipeline(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, stages)
}

func (s *Server) handleReplaceStages(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var stages []*storage.Stage
	if err := decodeJSON(r, &stages); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.store.Stages.ReplaceForPipeline(r.Context(), id, stages); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go s.reloadPipelines()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": len(stages)})
}

func (s *Server) handleListTransforms(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	list, err := s.store.Transforms.ListByPipeline(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, list)
}

func (s *Server) handleReplaceTransforms(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var rules []*storage.Transform
	if err := decodeJSON(r, &rules); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.store.Transforms.ReplaceForPipeline(r.Context(), id, rules); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go s.reloadPipelines()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": len(rules)})
}

func (s *Server) handleListRoutingRules(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	list, err := s.store.RoutingRules.ListByPipeline(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, list)
}

func (s *Server) handleReplaceRoutingRules(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var rules []*storage.RoutingRule
	if err := decodeJSON(r, &rules); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.store.RoutingRules.ReplaceForPipeline(r.Context(), id, rules); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go s.reloadPipelines()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": len(rules)})
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	started, err := s.pipeline.Reload(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "started": started})
}

func (s *Server) reloadPipelines() {
	if s.pipeline == nil {
		return
	}
	if _, err := s.pipeline.Reload(contextBackground()); err != nil {
		s.logger.Warn("hot-reload failed", "err", err)
	}
}
