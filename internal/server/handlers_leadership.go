package server

import (
	"net/http"
)

// handleLeadership reports this replica's view of the leadership lease:
// who's holding it, when it expires, and whether *this* process is the
// leader (so operators staring at the admin UI of a passive standby can
// tell at a glance).
//
// Endpoint: GET /api/v1/leadership
//
// When leadership is disabled (single-process deploy), responds with
// `{"enabled":false}` so the UI can hide the panel cleanly.
func (s *Server) handleLeadership(w http.ResponseWriter, r *http.Request) {
	if s.leadership == nil {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	snap := s.leadership.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    true,
		"self":       snap.Self,
		"holder":     snap.Holder,
		"is_leader":  snap.IsLeader,
		"expires_at": snap.Expires,
	})
}
