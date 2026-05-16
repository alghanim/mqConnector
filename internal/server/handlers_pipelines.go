package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// Every handler in this file is tenant-scoped through auth.TenantID. The
// child resources (stages, transforms, routing rules) must additionally
// confirm the pipeline lives in the caller's tenant — otherwise a stage
// edit could mutate stages of a pipeline the caller doesn't own. The
// PipelineRepo.Get check upstream of every child mutation handles that.

func (s *Server) handleListPipelines(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	list, err := s.store.Pipelines.List(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, list)
}

func (s *Server) handleGetPipeline(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	p, err := s.store.Pipelines.Get(r.Context(), tenant, id)
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
	tenant := auth.TenantID(r.Context())
	var p storage.Pipeline
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if p.Name == "" || p.SourceID == "" || p.DestinationID == "" {
		writeError(w, http.StatusBadRequest, "name, source_id and destination_id are required")
		return
	}
	if err := validatePipelineTuning(&p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Pre-validate that source and destination belong to the caller's
	// tenant — silently rejects attempts to wire a pipeline through
	// another tenant's connections.
	if _, err := s.store.Connections.Get(r.Context(), tenant, p.SourceID); err != nil {
		writeError(w, http.StatusBadRequest, "source_id not found in this tenant")
		return
	}
	if _, err := s.store.Connections.Get(r.Context(), tenant, p.DestinationID); err != nil {
		writeError(w, http.StatusBadRequest, "destination_id not found in this tenant")
		return
	}
	if err := s.store.Pipelines.Create(r.Context(), tenant, &p); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go s.reloadPipelines() // hot reload after structural change
	writeJSON(w, http.StatusCreated, p)
}

func (s *Server) handleUpdatePipeline(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	var p storage.Pipeline
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p.ID = id
	if err := validatePipelineTuning(&p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.Pipelines.Update(r.Context(), tenant, &p); err != nil {
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
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.Pipelines.Delete(r.Context(), tenant, id); err != nil {
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

// ensurePipelineInTenant returns nil if pipelineID lives in the caller's
// tenant, otherwise writes 404 and returns the error. Every child-row
// handler calls this so it can't mutate a pipeline it doesn't own.
func (s *Server) ensurePipelineInTenant(w http.ResponseWriter, r *http.Request, pipelineID string) error {
	tenant := auth.TenantID(r.Context())
	_, err := s.store.Pipelines.Get(r.Context(), tenant, pipelineID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return err
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return err
	}
	return nil
}

func (s *Server) handleListStages(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	stages, err := s.store.Stages.ListByPipeline(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, stages)
}

func (s *Server) handleReplaceStages(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	var stages []*storage.Stage
	if err := decodeJSON(r, &stages); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.store.Stages.ReplaceForPipeline(r.Context(), tenant, id, stages); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go s.reloadPipelines()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": len(stages)})
}

func (s *Server) handleListTransforms(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	list, err := s.store.Transforms.ListByPipeline(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, list)
}

func (s *Server) handleReplaceTransforms(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	var rules []*storage.Transform
	if err := decodeJSON(r, &rules); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.store.Transforms.ReplaceForPipeline(r.Context(), tenant, id, rules); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	go s.reloadPipelines()
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": len(rules)})
}

func (s *Server) handleListRoutingRules(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	list, err := s.store.RoutingRules.ListByPipeline(r.Context(), tenant, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, list)
}

func (s *Server) handleReplaceRoutingRules(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	var rules []*storage.RoutingRule
	if err := decodeJSON(r, &rules); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.store.RoutingRules.ReplaceForPipeline(r.Context(), tenant, id, rules); err != nil {
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

// validatePipelineTuning clamps the per-pipeline performance / reliability
// knobs to safe bounds. workers > 16 is almost never useful (the broker
// becomes the bottleneck long before that); retry_max > 100 is operator
// error 99% of the time (a poison message would cycle for days).
func validatePipelineTuning(p *storage.Pipeline) error {
	if p.Workers < 0 || p.Workers > 16 {
		return fmt.Errorf("workers must be between 0 and 16 (got %d)", p.Workers)
	}
	if p.RetryMax < 0 || p.RetryMax > 100 {
		return fmt.Errorf("retry_max must be between 0 and 100 (got %d)", p.RetryMax)
	}
	if p.RetryBackoffMs < 0 || p.RetryBackoffMs > 10*60*1000 {
		return fmt.Errorf("retry_backoff_ms must be between 0 and 600000 (got %d)", p.RetryBackoffMs)
	}
	return nil
}
