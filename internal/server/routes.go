package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/logging"
)

// routes assembles the full chi router.
func (s *Server) routes() http.Handler {
	r := chi.NewRouter()

	// Inject the logger into every request context so handlers can grab it.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := logging.IntoContext(req.Context(), s.logger)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Use(RequestID)
	r.Use(SecurityHeaders)
	r.Use(LogRequests)
	if len(s.cfg.Server.CORSOrigins) > 0 {
		r.Use(CORS(s.cfg.Server.CORSOrigins))
	}
	r.Use(MaxBodyBytes(s.cfg.Server.MaxBodyBytes))

	// Public endpoints
	r.Get("/api/health", s.handleHealth)
	r.Post("/api/auth/login", s.handleLogin)

	// Authenticated endpoints — admin only
	r.Group(func(r chi.Router) {
		r.Use(s.auth.RequireSession)

		r.Post("/api/auth/logout", s.handleLogout)
		r.Get("/api/auth/me", s.handleMe)

		r.Get("/api/metrics", s.handleMetricsJSON)
		r.Get("/api/metrics/prometheus", s.handleMetricsPrometheus)

		// Resource APIs under /api/v1/
		r.Route("/api/v1/connections", func(r chi.Router) {
			r.Get("/", s.handleListConnections)
			r.Post("/", s.handleCreateConnection)
			r.Get("/{id}", s.handleGetConnection)
			r.Put("/{id}", s.handleUpdateConnection)
			r.Delete("/{id}", s.handleDeleteConnection)
		})
		r.Route("/api/v1/pipelines", func(r chi.Router) {
			r.Get("/", s.handleListPipelines)
			r.Post("/", s.handleCreatePipeline)
			r.Get("/{id}", s.handleGetPipeline)
			r.Put("/{id}", s.handleUpdatePipeline)
			r.Delete("/{id}", s.handleDeletePipeline)
			r.Get("/{id}/stages", s.handleListStages)
			r.Put("/{id}/stages", s.handleReplaceStages)
			r.Get("/{id}/transforms", s.handleListTransforms)
			r.Put("/{id}/transforms", s.handleReplaceTransforms)
			r.Get("/{id}/routing-rules", s.handleListRoutingRules)
			r.Put("/{id}/routing-rules", s.handleReplaceRoutingRules)
		})
		r.Post("/api/v1/reload", s.handleReload)

		r.Route("/api/v1/dlq", func(r chi.Router) {
			r.Get("/", s.handleListDLQ)
			r.Post("/{id}/retry", s.handleRetryDLQ)
			r.Delete("/{id}", s.handleDeleteDLQ)
		})

		r.Route("/api/v1/scripts", func(r chi.Router) {
			r.Get("/", s.handleListScripts)
			r.Post("/", s.handleCreateScript)
			r.Get("/{id}", s.handleGetScript)
			r.Put("/{id}", s.handleUpdateScript)
			r.Delete("/{id}", s.handleDeleteScript)
		})

		r.Route("/api/v1/schemas", func(r chi.Router) {
			r.Get("/", s.handleListSchemas)
			r.Post("/", s.handleCreateSchema)
			r.Get("/{id}", s.handleGetSchema)
			r.Put("/{id}", s.handleUpdateSchema)
			r.Delete("/{id}", s.handleDeleteSchema)
		})

		r.Route("/api/v1/bridge", func(r chi.Router) {
			r.Post("/publish/{connectionId}", s.handleBridgePublish)
			r.Post("/consume/{connectionId}", s.handleBridgeConsume)
		})
	})

	// Static UI catch-all — must be last.
	s.mountStatic(r)

	return r
}
