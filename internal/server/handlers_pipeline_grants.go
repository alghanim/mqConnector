package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/storage"
)

// Pipeline grants management surface. The shape mirrors the
// connections / pipelines CRUD:
//
//   GET    /api/v1/pipelines/{id}/grants                  → list grants
//   PUT    /api/v1/pipelines/{id}/grants/{userSub}        → set role
//   DELETE /api/v1/pipelines/{id}/grants/{userSub}        → revoke
//
// Every handler requires effective role ≥ admin on the pipeline. A
// pipeline operator can change configuration but not who can access
// it — that decision belongs to admins so a compromised operator
// account can't escalate itself.

type setGrantRequest struct {
	Role storage.Role `json:"role"`
}

func (s *Server) handleListPipelineGrants(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	if !s.gatePipeline(w, r, id, storage.RoleAdmin) {
		return
	}
	grants, err := s.store.PipelineGrants.ListForPipeline(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, grants)
}

func (s *Server) handleSetPipelineGrant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userSub := chi.URLParam(r, "userSub")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	if !s.gatePipeline(w, r, id, storage.RoleAdmin) {
		return
	}
	if userSub == "" {
		writeError(w, http.StatusBadRequest, "user_sub required")
		return
	}
	var req setGrantRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !req.Role.Valid() {
		writeError(w, http.StatusBadRequest, "role must be one of viewer|operator|admin|owner")
		return
	}
	if err := s.store.PipelineGrants.Set(r.Context(), id, userSub, req.Role); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, storage.PipelineGrant{
		PipelineID: id,
		UserSub:    userSub,
		Role:       req.Role,
	})
}

func (s *Server) handleDeletePipelineGrant(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	userSub := chi.URLParam(r, "userSub")
	if err := s.ensurePipelineInTenant(w, r, id); err != nil {
		return
	}
	if !s.gatePipeline(w, r, id, storage.RoleAdmin) {
		return
	}
	if err := s.store.PipelineGrants.Delete(r.Context(), id, userSub); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
