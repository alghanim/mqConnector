// Package server wires the HTTP layer: chi router, middleware stack, route
// registration, embedded SvelteKit UI, and graceful TLS startup/shutdown.
package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/tetratelabs/wazero"

	"mqConnector/internal/ai"
	"mqConnector/internal/auth"
	"mqConnector/internal/config"
	"mqConnector/internal/dlq"
	"mqConnector/internal/explain"
	"mqConnector/internal/health"
	"mqConnector/internal/leadership"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/secrets"
	"mqConnector/internal/slo"
	"mqConnector/internal/storage"
	"mqConnector/internal/web"
)

// Server is the wired HTTP layer.
type Server struct {
	cfg        config.Config
	logger     *slog.Logger
	httpSrv    *http.Server
	auth       *auth.Service
	store      *storage.Store
	pool       *mq.Pool
	metrics    *metrics.Store
	dlq        *dlq.Service
	pipeline   *pipeline.Manager
	health     *health.Checker
	leadership *leadership.Lease // nil when not enabled
	sealer     *secrets.Service  // nil when MQC_MASTER_KEY is unset
	// AI provider (Wave 3 Task 4). When AI is disabled / endpoint
	// empty, aiProvider is the no-op sentinel that returns
	// ErrAINotAvailable; aiAudit is the noop logger; aiCfg.Enabled
	// is false. Handlers branch on aiCfg.Allows(...) before any
	// provider call.
	aiProvider ai.LLMProvider
	aiAudit    ai.AuditLogger
	aiCounter  *ai.CallCounter
	aiCfg      ai.Config
	// aiClusterNameCache memoises DLQ-cluster naming results by
	// fingerprint to bound LLM cost on UI re-renders. Entry TTL is
	// 60s; eviction is per-read (no background goroutine).
	aiClusterNameMu    sync.Mutex
	aiClusterNameCache map[string]aiClusterNameCacheEntry
	loginLimiter       *loginLimiter
	tenantLimiter      *tenantLimiter
	sensitiveLimiter   *sensitiveLimiter
	accountLockout     *accountLockout
	tlsReloader        *certReloader
	wasmRuntime        wazero.Runtime
	stopGC             chan struct{}
	stopGCOnce         sync.Once
	// pendingBackgroundOps tracks fire-and-forget goroutines spawned
	// off the request path (currently the snapshot helper) so tests
	// can deterministically wait for them to drain and so graceful
	// shutdown can give in-flight work a bounded chance to finish
	// before the process exits. Increment with Add(1) immediately
	// before `go` and Done() in the goroutine's defer.
	pendingBackgroundOps sync.WaitGroup

	// explainEngine composes existing telemetry into structured
	// "why is this state happening" explanations. Lazy-built in
	// New from the live deps so handler code can assume a non-nil
	// engine. The engine's own Source fields fan out to the
	// underlying repos / metrics / manager.
	explainEngine *explain.Engine

	// sloEvaluator periodically evaluates Prometheus alerting
	// rules against the in-process metrics store. Nil when SLO
	// is disabled (RulesFile empty / loader failed); handlers
	// that depend on it return an empty alert list rather than
	// 500-ing.
	sloEvaluator *slo.Evaluator

	// topologyRates is the per-pipeline rate sampler used by the
	// /api/v1/topology aggregator to derive msg_per_min from
	// successive snapshots of the cumulative messages_processed
	// counter. Lazy-initialised on first call so tests that never
	// hit the topology endpoint don't pay for it.
	topologyRatesMu sync.Mutex
	topologyRates   *topologyRateSampler
}

// WaitForBackgroundOps blocks until every fire-and-forget goroutine
// tracked by pendingBackgroundOps (today, the async pipeline-revision
// snapshot dispatcher) has returned. It is intended for tests that
// need to read state mutated by those goroutines without polling, and
// for the graceful-shutdown path which calls it under a bounded
// context. Production request handlers must not depend on it for
// correctness — the underlying ops are best-effort by design.
func (s *Server) WaitForBackgroundOps() {
	if s == nil {
		return
	}
	s.pendingBackgroundOps.Wait()
}

// shutdownGC signals the rate-limiter's GC loop to exit. Idempotent — both
// branches of Run's select call it, so a race between server-closed and
// context-cancelled doesn't double-close the channel.
func (s *Server) shutdownGC() {
	s.stopGCOnce.Do(func() {
		if s.stopGC != nil {
			close(s.stopGC)
		}
	})
}

// Deps bundles the dependencies New requires.
type Deps struct {
	Auth       *auth.Service
	Store      *storage.Store
	Pool       *mq.Pool
	Metrics    *metrics.Store
	DLQ        *dlq.Service
	Pipeline   *pipeline.Manager
	Health     *health.Checker
	Leadership *leadership.Lease // optional — nil when leadership is disabled
	Sealer     *secrets.Service  // optional — nil disables /secrets/rotate
	Logger     *slog.Logger
	// AI integration (Wave 3 Task 4). All three are optional: a nil
	// AIProvider falls back to ai.NewNoopProvider() and a nil
	// AIAudit falls back to ai.NoopAuditLogger{}, so handlers can
	// assume non-nil fields without a guard. AIConfig.Enabled=false
	// keeps everything off regardless of provider type.
	AIProvider ai.LLMProvider
	AIAudit    ai.AuditLogger
	AICounter  *ai.CallCounter
	AIConfig   ai.Config
	// SLOEvaluator is the in-process SLO rule evaluator. Optional
	// — when nil the /api/v1/alerts/active endpoint still serves,
	// returning an empty alert list with total=0 so the frontend
	// AlertRibbon stays silent rather than erroring.
	SLOEvaluator *slo.Evaluator
}

// New constructs a Server. Run blocks until Shutdown is called.
func New(cfg config.Config, deps Deps) (*Server, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	// AI defaults: nil provider / audit fall back to noop sentinels
	// so handlers never have to nil-check. The Allows() gate on
	// AIConfig is the source of truth — handlers MUST consult it
	// before invoking the provider; the noop sentinel is a defence
	// in depth.
	if deps.AIProvider == nil {
		deps.AIProvider = ai.NewNoopProvider()
	}
	if deps.AIAudit == nil {
		deps.AIAudit = ai.NoopAuditLogger{}
	}
	if deps.AICounter == nil {
		deps.AICounter = ai.NewCallCounter()
	}
	s := &Server{
		cfg:              cfg,
		logger:           deps.Logger.With("component", "server"),
		auth:             deps.Auth,
		store:            deps.Store,
		pool:             deps.Pool,
		metrics:          deps.Metrics,
		dlq:              deps.DLQ,
		pipeline:         deps.Pipeline,
		health:           deps.Health,
		leadership:       deps.Leadership,
		sealer:           deps.Sealer,
		aiProvider:       deps.AIProvider,
		aiAudit:          deps.AIAudit,
		aiCounter:        deps.AICounter,
		aiCfg:            deps.AIConfig,
		sloEvaluator:     deps.SLOEvaluator,
		loginLimiter:     newLoginLimiter(10, time.Minute),
		tenantLimiter:    newTenantLimiter(120, time.Minute, tenantOverrideFromStore(deps.Store)),
		sensitiveLimiter: newSensitiveLimiter(6, time.Minute),
		// Per-username lockout: 5 consecutive failures within 5 min
		// → 15 min lockout. Complements the per-IP loginLimiter so
		// distributed credential stuffing also gets stopped.
		accountLockout: newAccountLockout(5, 5*time.Minute, 15*time.Minute),
		// One wazero runtime per Server. Modules are instantiated
		// per-message (cheap) but share this runtime. Configured with
		// the default 32-MiB memory cap; per-stage limits override
		// downward but not upward.
		wasmRuntime: pipeline.NewWasmRuntime(context.Background(), pipeline.DefaultWasmLimits),
		stopGC:      make(chan struct{}),
	}
	// The pipeline manager compiles plugins on Reload; lend it the
	// runtime so the lifecycle is owned by the Server (one runtime
	// per process).
	if deps.Pipeline != nil {
		deps.Pipeline.SetWasmRuntime(s.wasmRuntime)
	}
	// Wire the explain engine — composable telemetry → structured
	// "why" explanations. Sub-Source nils are fine; the engine
	// degrades gracefully when a source isn't wired.
	s.explainEngine = buildExplainEngine(deps.Store, deps.Metrics, deps.Pipeline)
	go s.loginLimiter.gc(s.stopGC)
	go s.tenantLimiter.gc(s.stopGC)
	go s.sensitiveLimiter.gc(s.stopGC)
	go s.accountLockout.gc(s.stopGC)

	// Install the memberships-backed tenant resolver onto the auth
	// service. Without this, RequireSession falls back to the legacy
	// "everyone is owner of the default tenant" model, which keeps
	// single-tenant deployments working unchanged but doesn't isolate
	// new tenants — so we always install it when storage is wired.
	if deps.Auth != nil && deps.Store != nil {
		deps.Auth.SetTenantResolver(newTenantResolver(deps.Store, deps.Logger))
		// Bearer-token authentication. The adapter keeps the auth
		// package free of a storage dependency.
		if deps.Store.APITokens != nil {
			deps.Auth.SetAPITokenLookup(apiTokenAdapter{repo: deps.Store.APITokens})
		}
	}

	router := s.routes()

	s.httpSrv = &http.Server{
		Addr:         cfg.Server.Listen,
		Handler:      router,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}
	if cfg.Server.TLS.Enabled {
		minVer := uint16(tls.VersionTLS12)
		if strings.HasPrefix(cfg.Server.TLS.MinVersion, "1.3") {
			minVer = tls.VersionTLS13
		}
		// Hot-reload of the cert + key so an external rotator
		// (cert-manager, certbot, ACM) can swap files in without us
		// restarting. GetCertificate is consulted on every handshake;
		// the reloader's cached pointer means no lock contention on
		// the hot path.
		reloader, err := newCertReloader(
			cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile, s.logger)
		if err != nil {
			return nil, fmt.Errorf("tls cert load: %w", err)
		}
		s.tlsReloader = reloader
		s.httpSrv.TLSConfig = &tls.Config{
			MinVersion:     minVer,
			GetCertificate: reloader.GetCertificate,
		}
		go reloader.Watch(30*time.Second, s.stopGC)
	}
	return s, nil
}

// Run starts the HTTP server and blocks until ctx is cancelled or an error
// occurs.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("server listening",
			"addr", s.cfg.Server.Listen,
			"tls", s.cfg.Server.TLS.Enabled,
		)
		if s.cfg.Server.TLS.Enabled {
			// Empty cert/key args here mean "use TLSConfig.GetCertificate"
			// which the reloader installed. That's what enables hot
			// rotation: the loader pulls a fresh cert per handshake.
			errCh <- s.httpSrv.ListenAndServeTLS("", "")
		} else {
			errCh <- s.httpSrv.ListenAndServe()
		}
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			s.logger.Warn("server shutdown error", "err", err)
		}
		s.shutdownGC()
		// Give in-flight best-effort background ops (snapshot writes)
		// a bounded chance to land before the process exits, so a
		// SIGTERM mid-PUT doesn't routinely drop the revision row
		// the live tables just committed to. Bounded so a stuck DB
		// can't hang shutdown indefinitely.
		s.waitBackgroundOpsBounded(5 * time.Second)
		return nil
	case err := <-errCh:
		s.shutdownGC()
		s.waitBackgroundOpsBounded(5 * time.Second)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("server: %w", err)
	}
}

// waitBackgroundOpsBounded waits for pendingBackgroundOps to drain or
// for the timeout to elapse, whichever comes first. Used from Run's
// shutdown path so a stuck DB can't pin the process open past the
// configured grace period.
func (s *Server) waitBackgroundOpsBounded(timeout time.Duration) {
	if s == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		s.pendingBackgroundOps.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		s.logger.Warn("background ops did not drain within shutdown budget",
			"timeout", timeout)
	}
}

// staticFS mounts the embedded SvelteKit build at the chi router, serving
// /assets etc. and falling back to index.html for client-side routes.
//
// index.html gets special handling: the SvelteKit shell has two inline
// <script> blocks (FOUC theme reader + hydration bootstrap) which would
// otherwise be blocked by the strict CSP. The static handler reads the
// per-request nonce off the context (set by SecurityHeaders), injects it
// into every inline <script> tag, and serves the rewritten body. All
// other files pass through http.FileServer untouched.
func (s *Server) mountStatic(r chi.Router) {
	uiFS := web.DistFS()
	fileServer := http.FileServer(http.FS(uiFS))

	// Cache the index.html body at boot so the per-request hot path is just
	// a single byte-substitution rather than an FS read.
	indexHTML, _ := fs.ReadFile(uiFS, "index.html")

	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		// Try the exact path; if it doesn't exist, fall back to index.html
		// so client-side routing handles deep links.
		_, err := fs.Stat(uiFS, p)
		isIndex := err != nil || p == "index.html"

		if isIndex && len(indexHTML) > 0 {
			body := injectCSPNonce(indexHTML, CSPNonceFromContext(r.Context()))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// SPA shell mutates on deploy (new bundle hashes) so don't allow
			// long caches. The hashed asset files under /_app/ are
			// content-addressed and the FileServer below will set its own
			// far-future caching for those.
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
			_, _ = w.Write(body)
			return
		}

		// SvelteKit emits hashed filenames under /_app/immutable/* — same
		// bytes always map to the same URL, so they're safe to cache for
		// a year and skip revalidation. Without this header browsers fall
		// back to heuristic caching, which can leak stale CSS across
		// deploys (we hit this in dev when the metrics page CSS chunk
		// wouldn't refresh until a hard reload).
		if strings.HasPrefix(p, "_app/immutable/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		// Embedded font files are also content-addressed enough — they
		// never change for a given binary version — and benefit from
		// long caching.
		if strings.HasPrefix(p, "fonts/") {
			w.Header().Set("Cache-Control", "public, max-age=2592000")
		}

		fileServer.ServeHTTP(w, r)
	})
}

// inlineScriptOpen matches an opening <script> tag with no src= attribute —
// SvelteKit's app.html shell uses these for the FOUC theme reader and the
// hydration bootstrap, both of which need the per-request nonce. We
// deliberately leave <script src=...> alone: external scripts are vetted
// by 'self' in the script-src directive and don't need (or want) a nonce.
var inlineScriptOpen = regexp.MustCompile(`<script(\s+(?:[^>]*[^/])?)?>`)

// injectCSPNonce rewrites every inline <script> opener to carry the given
// nonce attribute. <script src="..."> tags are skipped because the regex
// only matches openers without a `src=` attribute would be too coarse —
// we instead check defensively in the replacer.
func injectCSPNonce(body []byte, nonce string) []byte {
	if nonce == "" {
		return body
	}
	attr := []byte(` nonce="` + nonce + `"`)
	return inlineScriptOpen.ReplaceAllFunc(body, func(m []byte) []byte {
		// Skip <script src=...> — those load external code and don't need
		// (or want) a nonce attribute; the strict 'self' directive already
		// gates which origins they can load from.
		if bytes.Contains(m, []byte(" src=")) {
			return m
		}
		// Insert the nonce attribute right after `<script`.
		return append(append([]byte("<script"), attr...), m[len("<script"):]...)
	})
}
