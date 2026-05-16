package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"mqConnector/internal/auth"
	"mqConnector/internal/logging"
	"mqConnector/internal/storage"
)

// maxAuditDiffBytes caps the captured request body size. Larger PUT
// payloads (e.g. a stages-replacement with hundreds of stages) are
// still applied — we just don't keep the bytes. Keeps the audit table
// from being weaponised as a backup blob store.
const maxAuditDiffBytes = 64 * 1024

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
//
// For PUT mutations on successful (2xx) responses, the captured request
// body is also stored as the "after" half of an audit diff (capped at
// maxAuditDiffBytes). The "before" half is left empty here — populating
// it requires a per-resource pre-read, which a future pass can wire by
// path inspection.
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

		// For PUTs, peek at the request body so we can store the
		// intended new state alongside the audit row. We replace r.Body
		// with a fresh reader so the downstream handler sees the bytes.
		var afterBody []byte
		if r.Method == http.MethodPut && r.Body != nil {
			capped := io.LimitReader(r.Body, maxAuditDiffBytes+1)
			b, err := io.ReadAll(capped)
			if err == nil {
				afterBody = b
				r.Body = io.NopCloser(bytes.NewReader(b))
			}
		}

		// Generate the audit row id up-front so the diff can reference
		// it without a second round-trip.
		entryID := uuid.NewString()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		entry := storage.AuditEntry{
			ID:        entryID,
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
		entry.TenantID = auth.TenantID(r.Context())

		// Best-effort insert with a short timeout — never block the
		// request goroutine, never propagate an error to the client.
		// Diff is saved only on success and only when we actually
		// captured a body small enough to keep.
		saveDiff := r.Method == http.MethodPut &&
			rec.status >= 200 && rec.status < 300 &&
			len(afterBody) > 0 && len(afterBody) <= maxAuditDiffBytes

		go func(e storage.AuditEntry, after []byte, wantDiff bool) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := s.store.Audit.Insert(ctx, &e); err != nil {
				logging.FromContext(context.Background()).Warn("audit insert failed",
					"err", err, "resource", e.Resource, "request_id", e.RequestID)
				return
			}
			if wantDiff {
				if err := s.store.Audit.SaveDiff(ctx, e.ID, "", string(after)); err != nil {
					logging.FromContext(context.Background()).Warn("audit diff save failed",
						"err", err, "audit_id", e.ID)
				}
			}
		}(entry, afterBody, saveDiff)
	})
}
