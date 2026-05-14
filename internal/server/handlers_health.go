package server

import "net/http"

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := s.health.Check(r.Context())
	code := http.StatusOK
	if status.Status == "unhealthy" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, status)
}

func (s *Server) handleMetricsJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"uptime":    s.metrics.Uptime().String(),
		"pipelines": s.metrics.Snapshot(),
	})
}

func (s *Server) handleMetricsPrometheus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(s.metrics.Prometheus()))
}
