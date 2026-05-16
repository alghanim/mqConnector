package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// handleListWebhooks returns every webhook in the caller's tenant.
// The signing secret is included so the operator can recover it (the
// receiver needs it to verify HMAC). Treat the response as sensitive.
func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	list, err := s.store.Webhooks.List(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []*storage.Webhook{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	var h storage.Webhook
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		writeError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	if h.Name == "" || h.URL == "" || h.Secret == "" {
		writeError(w, http.StatusBadRequest, "name, url, and secret are required")
		return
	}
	if err := s.store.Webhooks.Create(r.Context(), tenant, &h); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}
	var h storage.Webhook
	if err := json.NewDecoder(r.Body).Decode(&h); err != nil {
		writeError(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	h.ID = id
	if err := s.store.Webhooks.Update(r.Context(), tenant, &h); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}
	if err := s.store.Webhooks.Delete(r.Context(), tenant, id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
