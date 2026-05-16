package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/logging"
)

// routes assembles the full chi router.
func (s *Server) routes() http.Handler {
	r := chi.NewRouter()

	// Order matters: Recover wraps everything so a panic in any other
	// middleware or handler still produces a structured log + 500 instead
	// of tearing the process down.
	r.Use(Recover)

	// Inject the logger into every request context so handlers can grab it.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := logging.IntoContext(req.Context(), s.logger)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Use(RequestID)
	r.Use(TraceContext)
	r.Use(SecurityHeaders)
	r.Use(LogRequests)
	if len(s.cfg.Server.CORSOrigins) > 0 {
		r.Use(CORS(s.cfg.Server.CORSOrigins))
	}
	r.Use(MaxBodyBytes(s.cfg.Server.MaxBodyBytes))
	// Per-request hard cap so a stuck downstream can't hold a goroutine past
	// the server's Write timeout.
	if s.cfg.Server.WriteTimeout > 0 {
		r.Use(RequestContextTimeout(s.cfg.Server.WriteTimeout))
	}

	// Public endpoints
	r.Get("/api/health", s.handleHealth)
	r.Get("/api/openapi.yaml", s.handleOpenAPI)
	// Login is rate-limited per source IP to slow credential stuffing.
	r.With(s.rateLimitLogin).Post("/api/auth/login", s.handleLogin)
	// Refresh is public — the access cookie may already have expired by the
	// time the UI's silent refresh fires, so requiring it would make this
	// endpoint useless. Same per-IP cap as login to slow refresh-token
	// brute-forcing.
	r.With(s.rateLimitLogin).Post("/api/auth/refresh", s.handleRefresh)

	// Authenticated endpoints — admin only. AuditAdminActions records every
	// mutation after RequireSession populates the user context. Per-tenant
	// rate limiting sits after RequireSession (so the tenant id is in the
	// request context) but before the audit middleware (so a 429 doesn't
	// produce a spurious audit row).
	r.Group(func(r chi.Router) {
		r.Use(s.auth.RequireSession)
		r.Use(s.rateLimitTenant)
		r.Use(s.AuditAdminActions)

		r.Post("/api/auth/logout", s.handleLogout)
		r.Get("/api/auth/me", s.handleMe)

		r.Get("/api/metrics", s.handleMetricsJSON)
		r.Get("/api/metrics/prometheus", s.handleMetricsPrometheus)
		r.Get("/api/v1/audit", s.handleListAudit)
		// Tamper-evident chain verifier. Returns one ChainStatus per
		// tenant the caller can see; `?scope=all` widens to every
		// tenant for default-tenant owners only.
		r.Get("/api/v1/audit/verify", s.handleVerifyAudit)
		// Before/after JSON for one audit row (PUT mutations only).
		r.Get("/api/v1/audit/{id}/diff", s.handleGetAuditDiff)

		// Envelope-encryption key rotation. Status is read-only; rotate
		// is system-admin only and additionally rewraps every stored
		// connection password under the new key.
		r.Get("/api/v1/secrets/status", s.handleSecretsStatus)
		r.Post("/api/v1/secrets/rotate", s.handleRotateSecrets)

		// API tokens (headless / CI auth). Scoped to the caller's
		// tenant; the secret is shown exactly once at creation.
		r.Get("/api/v1/tokens", s.handleListTokens)
		r.Post("/api/v1/tokens", s.handleCreateToken)
		r.Delete("/api/v1/tokens/{id}", s.handleRevokeToken)

		// Webhooks — outbound HTTP delivery of internal events with
		// HMAC-SHA256 signing.
		r.Route("/api/v1/webhooks", func(r chi.Router) {
			r.Get("/", s.handleListWebhooks)
			r.Post("/", s.handleCreateWebhook)
			r.Put("/{id}", s.handleUpdateWebhook)
			r.Delete("/{id}", s.handleDeleteWebhook)
		})

		// Tenant-scoped configuration import / export. YAML by default;
		// JSON on Accept: application/json or ?format=json. Import
		// requires admin (enforced inside the handler).
		r.Get("/api/v1/config/export", s.handleExportConfig)
		r.Post("/api/v1/config/import", s.handleImportConfig)

		// Server-Sent Events — long-lived stream. The RequestContextTimeout
		// middleware detects "Accept: text/event-stream" and skips the
		// per-request deadline; the SSE handler clears the per-connection
		// write deadline so neither timeout severs the stream.
		r.Get("/api/v1/events", s.handleEvents)

		// Tenant management. Read-self is open to any authenticated
		// user; everything else is enforced inside the handler.
		r.Route("/api/v1/tenants", func(r chi.Router) {
			r.Get("/", s.handleListMyTenants)
			r.Post("/", s.handleCreateTenant)
			r.Get("/{id}", s.handleGetTenant)
			r.Put("/{id}", s.handleUpdateTenant)
			r.Delete("/{id}", s.handleDeleteTenant)
			r.Post("/{id}/switch", s.handleSwitchTenant)
			r.Get("/{id}/members", s.handleListMembers)
			r.Post("/{id}/members", s.handleUpsertMember)
			r.Delete("/{id}/members/{user_sub}", s.handleDeleteMember)
		})
		r.Get("/api/v1/leadership", s.handleLeadership)

		// Resource APIs under /api/v1/
		r.Route("/api/v1/connections", func(r chi.Router) {
			r.Get("/", s.handleListConnections)
			r.Post("/", s.handleCreateConnection)
			r.Get("/{id}", s.handleGetConnection)
			r.Put("/{id}", s.handleUpdateConnection)
			r.Delete("/{id}", s.handleDeleteConnection)
			r.Post("/{id}/test", s.handleTestConnection)
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
		r.Post("/api/v1/pipelines/{id}/replay", s.handleReplayPipeline)

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

		// Sample upload + path extraction. Lets the pipeline editor's
		// path-picker show what's actually in the operator's data without
		// hand-typing every field name.
		r.Post("/api/v1/samples/extract", s.handleExtractSample)

		// Pipeline preview — drive a sample message through a saved
		// pipeline or an inline draft, return what would be sent
		// downstream. No brokers are touched.
		r.Post("/api/v1/preview", s.handlePreview)
	})

	// Static UI catch-all — must be last.
	s.mountStatic(r)

	return r
}
