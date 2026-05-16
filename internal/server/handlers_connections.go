package server

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// connectionDTO is the wire-format the HTTP handler accepts /
// emits. Field set matches storage.Connection 1:1; new broker
// support is added here AND in dtoToConnection below.
//
// decodeJSON sets DisallowUnknownFields so any field not listed
// here causes a 400. That's deliberate: typos in the API client
// (wrong field name, stale schema) surface loudly instead of
// silently dropping data. The cost is that every new column needs
// a parallel DTO field — a real but acceptable tax.
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
	// Phase 17 — broker TLS / mTLS (paths to PEM files).
	TLSCAFile             string `json:"tls_ca_file,omitempty"`
	TLSCertFile           string `json:"tls_cert_file,omitempty"`
	TLSKeyFile            string `json:"tls_key_file,omitempty"`
	TLSInsecureSkipVerify bool   `json:"tls_insecure_skip_verify,omitempty"`
	// Phase 22 — MQTT / NATS / AMQP 1.0 specific fields.
	ClientID     string `json:"client_id,omitempty"`
	StreamName   string `json:"stream_name,omitempty"`
	ConsumerName string `json:"consumer_name,omitempty"`
	QoS          int    `json:"qos,omitempty"`
	// Read-only echoes — the handler returns these from storage; the
	// DTO includes them so a round-trip GET → edit → PUT doesn't drop
	// timestamps that callers might pin to.
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func (s *Server) handleListConnections(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	list, err := s.store.Connections.List(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSONList(w, http.StatusOK, list)
}

func (s *Server) handleGetConnection(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	c, err := s.store.Connections.Get(r.Context(), tenant, id)
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
	tenant := auth.TenantID(r.Context())
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
	if err := s.store.Connections.Create(r.Context(), tenant, conn); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, conn)
}

func (s *Server) handleUpdateConnection(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
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
	if err := s.store.Connections.Update(r.Context(), tenant, conn); err != nil {
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
	tenant := auth.TenantID(r.Context())
	id := chi.URLParam(r, "id")
	if err := s.store.Connections.Delete(r.Context(), tenant, id); err != nil {
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
		TLSCAFile:             d.TLSCAFile,
		TLSCertFile:           d.TLSCertFile,
		TLSKeyFile:            d.TLSKeyFile,
		TLSInsecureSkipVerify: d.TLSInsecureSkipVerify,
		ClientID:     d.ClientID,
		StreamName:   d.StreamName,
		ConsumerName: d.ConsumerName,
		QoS:          d.QoS,
	}
}
