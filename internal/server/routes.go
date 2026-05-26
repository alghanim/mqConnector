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
		// CSRF gate sits AFTER session validation (so 401 is preferred
		// over 403 for missing-cookie cases) and BEFORE audit
		// recording (a CSRF reject shouldn't generate an audit row).
		// Bearer-token (API token) callers pass straight through.
		r.Use(s.requireCSRF)
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
		r.With(s.rateLimitSensitive("/api/v1/secrets/rotate")).
			Post("/api/v1/secrets/rotate", s.handleRotateSecrets)

		// Disaster-recovery hooks. /admin/backup snapshots the SQLite
		// file (VACUUM INTO) and streams it back to the caller;
		// /admin/integrity runs PRAGMA integrity_check on the live
		// database. Both are system-admin only — the snapshot is the
		// entire state of the bridge including encrypted secrets.
		r.Get("/api/v1/admin/backup", s.handleAdminBackup)
		r.Get("/api/v1/admin/integrity", s.handleAdminIntegrity)

		// Runtime profiling for production debugging. net/http/pprof
		// registers handlers like /debug/pprof/heap, /goroutine,
		// /profile, /trace, etc. We mount them under
		// /api/v1/admin/pprof/* so the regular auth + system-admin
		// gate applies — pprof endpoints leak goroutine stacks and
		// CPU samples and absolutely must not be public.
		s.mountPprof(r)

		// WASM plugin lifecycle. System-admin only; uploads run
		// through the sensitive-route limiter (6/min/tenant) because
		// a malicious upload can DoS the server even if it can't
		// escape the sandbox. The CompileWasm validation at upload
		// time blocks bad blobs from reaching the storage layer.
		r.Route("/api/v1/plugins", func(r chi.Router) {
			r.Get("/", s.handleListPlugins)
			r.With(s.rateLimitSensitive("/api/v1/plugins")).
				Post("/", s.handleUploadPlugin)
			r.Get("/{name}", s.handleGetPlugin)
			r.Delete("/{name}", s.handleDeletePlugin)
		})

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
		r.With(s.rateLimitSensitive("/api/v1/config/import")).
			Post("/api/v1/config/import", s.handleImportConfig)

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
			// Revision history (Pipeline Studio Wave 1). Read-only;
			// authenticated viewers can browse. Rollback /
			// save-draft / deploy land in later waves.
			r.Get("/{id}/revisions", s.handleListRevisions)
			r.Get("/{id}/revisions/current", s.handleGetCurrentRevision)
			r.Get("/{id}/revisions/{rev}", s.handleGetRevision)
			// Structured diff between two revisions. Direction is
			// from {rev} → to against, so the Studio diff viewer
			// can render both rollback previews and forward-deploy
			// previews with the same wire shape. Viewer-readable.
			r.Get("/{id}/revisions/{rev}/diff", s.handleDiffRevisions)
			// Pipeline Studio Wave 1 write-through endpoints.
			// /revisions/{rev}/rollback creates a NEW revision
			// holding the target's snapshot and promotes it to
			// live. /deploy promotes an EXISTING revision to
			// live without creating a new row. Both gate on
			// operator role per-pipeline.
			r.Post("/{id}/revisions/{rev}/rollback", s.handleRollbackRevision)
			r.Post("/{id}/deploy", s.handleDeployRevision)
			// Per-pipeline RBAC grants. Mounted under the pipeline
			// so the chi router carries the pipeline id through the
			// handler chain via {id}; the grant subject is named
			// "userSub" to mirror auth.UserSub / SimpleAuth's sub
			// claim.
			r.Get("/{id}/grants", s.handleListPipelineGrants)
			r.Put("/{id}/grants/{userSub}", s.handleSetPipelineGrant)
			r.Delete("/{id}/grants/{userSub}", s.handleDeletePipelineGrant)
			// Per-pipeline DLQ redaction rules. PUT is admin-only
			// because malformed rules can mask all DLQ output (visible
			// regression) and badly-scoped rules can leak PII (silent
			// regression); both deserve audit-log coverage that the
			// admin path already gets via AuditAdminActions.
			r.Get("/{id}/dlq-redaction-rules", s.handleListDLQRedactionRules)
			r.With(s.auth.RequireRole("admin")).
				Put("/{id}/dlq-redaction-rules", s.handleReplaceDLQRedactionRules)
		})
		r.Post("/api/v1/reload", s.handleReload)
		r.Post("/api/v1/pipelines/{id}/replay", s.handleReplayPipeline)

		// Wave 2 — Live Topology aggregator. One read returns every
		// broker connection in the tenant, every pipeline that flows
		// between them, and the live health/throughput/depth signals
		// for the topology page. Viewer-readable; tenant-scoped.
		r.Get("/api/v1/topology", s.handleTopology)

		// Wave 4 — Explain endpoint. Composable telemetry → a
		// structured "why" answer for one of the known subjects
		// (circuit, drift, latency, dlq_cluster, dlq_entry).
		// Viewer-readable; tenant-scoped. Optional ?ai=summary
		// gates on the CapExplainWhySummary capability.
		r.Get("/api/v1/explain/{subject}/{id}", s.handleExplain)

		r.Route("/api/v1/dlq", func(r chi.Router) {
			r.Get("/", s.handleListDLQ)
			r.Get("/groups", s.handleGroupDLQ)
			// DLQ Intelligence Console (Wave 3 Task 3). Clusters +
			// payload-diff are viewer-readable; replay-sim is
			// operator-gated per-pipeline inside the handler.
			r.Get("/clusters", s.handleListDLQClusters)
			r.Post("/{id}/retry", s.handleRetryDLQ)
			r.Post("/{id}/replay-sim", s.handleReplaySimDLQ)
			r.Get("/{id}/diff", s.handleDiffDLQ)
			r.Delete("/{id}", s.handleDeleteDLQ)
			// Raw payload view is admin-only; every successful read
			// is audited as action=dlq_raw_view (see handler).
			r.With(s.auth.RequireRole("admin")).Get("/{id}/raw", s.handleGetDLQRaw)
			// Bulk triage. Operator role gates write — bulk replay
			// can wake up an entire downstream consumer set; bulk
			// delete is irreversible at the row level. The
			// sensitive-route limiter also fires.
			r.With(s.auth.RequireRole("operator"), s.rateLimitSensitive("/api/v1/dlq/bulk")).
				Post("/bulk/retry", s.handleBulkRetryDLQ)
			r.With(s.auth.RequireRole("operator"), s.rateLimitSensitive("/api/v1/dlq/bulk")).
				Post("/bulk/delete", s.handleBulkDeleteDLQ)
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
		r.With(s.rateLimitSensitive("/api/v1/samples/extract")).
			Post("/api/v1/samples/extract", s.handleExtractSample)

		// Pipeline preview — drive a sample message through a saved
		// pipeline or an inline draft, return what would be sent
		// downstream. No brokers are touched but the stage chain may
		// run arbitrary JS scripts, so a tight rate limit is warranted.
		r.With(s.rateLimitSensitive("/api/v1/preview")).
			Post("/api/v1/preview", s.handlePreview)
	})

	// Static UI catch-all — must be last.
	s.mountStatic(r)

	return r
}
