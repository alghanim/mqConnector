package server

import "net/http"

// rateLimitLogin is the chi-compatible middleware bound to /api/auth/login.
// It enforces the per-IP cap configured on the server's loginLimiter.
func (s *Server) rateLimitLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.loginLimiter.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "too many login attempts, try again later")
			return
		}
		next.ServeHTTP(w, r)
	})
}
