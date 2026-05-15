package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"mqConnector/internal/auth"
	"mqConnector/internal/logging"
	"mqConnector/internal/storage"
)

// auditableMethods covers state-changing verbs. GETs are not audited — the
// expected query volume would drown the signal.
var auditableMethods = map[string]bool{
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// AuditAdminActions logs every state-changing request hitting /api/v1/* (and
// the /api/auth/logout housekeeping endpoint, so we can correlate session
// lifetimes). Entries are written best-effort after the response is sent —
// audit failures must not block or alter the user-visible response.
func (s *Server) AuditAdminActions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only audit auditable methods on /api/v1/* (and logout). Skip
		// /api/auth/login because the user isn't authenticated yet — the
		// rate limiter and access log already capture login attempts.
		path := r.URL.Path
		if !auditableMethods[r.Method] ||
			(!strings.HasPrefix(path, "/api/v1/") && path != "/api/auth/logout") {
			next.ServeHTTP(w, r)
			return
		}

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		entry := storage.AuditEntry{
			At:        time.Now().UTC(),
			Action:    r.Method,
			Resource:  path,
			Status:    rec.status,
			RequestID: RequestIDFromContext(r.Context()),
			RemoteIP:  clientIP(r),
		}
		if u, ok := auth.UserFromContext(r.Context()); ok && u != nil {
			entry.Actor = u.PreferredUsername
			entry.ActorSub = u.Sub
		}

		// Best-effort insert with a short timeout — never block the
		// request goroutine, never propagate an error to the client.
		go func(e storage.AuditEntry) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := s.store.Audit.Insert(ctx, &e); err != nil {
				logging.FromContext(context.Background()).Warn("audit insert failed",
					"err", err, "resource", e.Resource, "request_id", e.RequestID)
			}
		}(entry)
	})
}
