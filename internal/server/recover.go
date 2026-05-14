package server

import (
	"net/http"
	"runtime/debug"

	"mqConnector/internal/logging"
)

// Recover wraps the next handler so a panic in any downstream code returns a
// 500 to the client and produces a structured log entry — rather than tearing
// the whole process down. This is critical for long-running services: a
// single buggy handler must not be able to kill the binary.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger := logging.FromContext(r.Context())
				logger.Error("panic in handler",
					"panic", rec,
					"method", r.Method,
					"path", r.URL.Path,
					"request_id", RequestIDFromContext(r.Context()),
					"stack", string(debug.Stack()),
				)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"internal server error"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
