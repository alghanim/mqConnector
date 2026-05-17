package server

import (
	"net/http"
	"net/http/pprof"

	"github.com/go-chi/chi/v5"
)

// mountPprof attaches Go runtime profiling endpoints under
// /api/v1/admin/pprof/*. Wrapped in a system-admin gate because the
// profiles leak goroutine stacks, heap layouts, CPU samples, and
// program text — all useful for debugging, all dangerous to expose
// publicly.
//
// Endpoints (each takes optional query params; see pprof package docs):
//
//	GET /api/v1/admin/pprof/             index page
//	GET /api/v1/admin/pprof/cmdline      process command line
//	GET /api/v1/admin/pprof/profile      30s CPU profile (binary)
//	GET /api/v1/admin/pprof/symbol       symbol lookup
//	GET /api/v1/admin/pprof/trace        execution trace (binary)
//	GET /api/v1/admin/pprof/{name}       named profile (heap, goroutine, allocs, mutex, block, threadcreate)
//
// Usage from a developer laptop with port-forward:
//
//	go tool pprof -http=:9999 \
//	  "https://mqc.svc:8443/api/v1/admin/pprof/heap?session=<cookie>"
//
// Important: pprof's CPU profile and trace endpoints block for their
// duration (default 30s for /profile, configurable via ?seconds=N).
// Server-side request timeouts must be high enough to accommodate
// expected profile durations — the default 30s WriteTimeout barely
// fits a 30s profile and a 60s profile will time out.
func (s *Server) mountPprof(r chi.Router) {
	r.Route("/api/v1/admin/pprof", func(r chi.Router) {
		r.Use(s.requirePprofAdmin)
		r.Get("/", pprof.Index)
		r.Get("/cmdline", pprof.Cmdline)
		r.Get("/profile", pprof.Profile)
		r.Get("/symbol", pprof.Symbol)
		r.Post("/symbol", pprof.Symbol)
		r.Get("/trace", pprof.Trace)
		// Named profiles — heap, goroutine, allocs, mutex, block,
		// threadcreate. pprof.Handler returns an http.Handler for
		// the named profile so we route any unmatched name through it.
		r.Get("/{name}", func(w http.ResponseWriter, req *http.Request) {
			name := chi.URLParam(req, "name")
			pprof.Handler(name).ServeHTTP(w, req)
		})
	})
}

// requirePprofAdmin is a small wrapper that returns 403 for non-
// system-admin callers. Mirrors the gate used by /admin/backup and
// /admin/integrity, kept inline because pprof needs http.HandlerFunc
// composition rather than the writeError pattern.
func (s *Server) requirePprofAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isSystemAdmin(r) {
			writeError(w, http.StatusForbidden, "system-admin required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
