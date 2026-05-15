package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/storage"
)

func (s *Server) handleListScripts(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Scripts.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, list)
}

func (s *Server) handleGetScript(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sc, err := s.store.Scripts.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleCreateScript(w http.ResponseWriter, r *http.Request) {
	var sc storage.Script
	if err := decodeJSON(r, &sc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if sc.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.store.Scripts.Create(r.Context(), &sc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

func (s *Server) handleUpdateScript(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var sc storage.Script
	if err := decodeJSON(r, &sc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sc.ID = id
	if err := s.store.Scripts.Update(r.Context(), &sc); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleDeleteScript(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.Scripts.Delete(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleListSchemas(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Schemas.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, list)
}

func (s *Server) handleGetSchema(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sc, err := s.store.Schemas.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleCreateSchema(w http.ResponseWriter, r *http.Request) {
	var sc storage.Schema
	if err := decodeJSON(r, &sc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if sc.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.store.Schemas.Create(r.Context(), &sc); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sc)
}

func (s *Server) handleUpdateSchema(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var sc storage.Schema
	if err := decodeJSON(r, &sc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	sc.ID = id
	if err := s.store.Schemas.Update(r.Context(), &sc); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

func (s *Server) handleDeleteSchema(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.Schemas.Delete(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
