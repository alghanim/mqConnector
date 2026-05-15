package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

type connectionDTO struct {
	ID           string `json:"id,omitempty"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	QueueManager string `json:"queue_manager,omitempty"`
	ConnName     string `json:"conn_name,omitempty"`
	Channel      string `json:"channel,omitempty"`
	Username     string `json:"username,omitempty"`
	Password     string `json:"password,omitempty"`
	QueueName    string `json:"queue_name,omitempty"`
	URL          string `json:"url,omitempty"`
	Brokers      string `json:"brokers,omitempty"`
	Topic        string `json:"topic,omitempty"`
}

func (s *Server) handleListConnections(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Connections.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, list)
}

func (s *Server) handleGetConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	c, err := s.store.Connections.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleCreateConnection(w http.ResponseWriter, r *http.Request) {
	var dto connectionDTO
	if err := decodeJSON(r, &dto); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if dto.Name == "" || dto.Type == "" {
		writeError(w, http.StatusBadRequest, "name and type are required")
		return
	}
	if _, err := mq.ParseType(dto.Type); err != nil {
		writeError(w, http.StatusBadRequest, "invalid type")
		return
	}
	conn := dtoToConnection(dto)
	if err := s.store.Connections.Create(r.Context(), conn); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, conn)
}

func (s *Server) handleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var dto connectionDTO
	if err := decodeJSON(r, &dto); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	dto.ID = id
	if dto.Type != "" {
		if _, err := mq.ParseType(dto.Type); err != nil {
			writeError(w, http.StatusBadRequest, "invalid type")
			return
		}
	}
	conn := dtoToConnection(dto)
	if err := s.store.Connections.Update(r.Context(), conn); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, conn)
}

func (s *Server) handleDeleteConnection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.Connections.Delete(r.Context(), id); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func dtoToConnection(d connectionDTO) *storage.Connection {
	return &storage.Connection{
		ID:           d.ID,
		Name:         d.Name,
		Type:         d.Type,
		QueueManager: d.QueueManager,
		ConnName:     d.ConnName,
		Channel:      d.Channel,
		Username:     d.Username,
		Password:     d.Password,
		QueueName:    d.QueueName,
		URL:          d.URL,
		Brokers:      d.Brokers,
		Topic:        d.Topic,
	}
}
