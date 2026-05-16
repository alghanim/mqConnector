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

	"mqConnector/internal/auth"
	"mqConnector/internal/config"
	"mqConnector/internal/dlq"
	"mqConnector/internal/health"
	"mqConnector/internal/leadership"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/secrets"
	"mqConnector/internal/storage"
	"mqConnector/internal/web"
)

// Server is the wired HTTP layer.
type Server struct {
	cfg          config.Config
	logger       *slog.Logger
	httpSrv      *http.Server
	auth         *auth.Service
	store        *storage.Store
	pool         *mq.Pool
	metrics      *metrics.Store
	dlq          *dlq.Service
	pipeline     *pipeline.Manager
	health       *health.Checker
	leadership   *leadership.Lease // nil when not enabled
	sealer       *secrets.Service  // nil when MQC_MASTER_KEY is unset
	loginLimiter *loginLimiter
	stopGC       chan struct{}
	stopGCOnce   sync.Once
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
}

// New constructs a Server. Run blocks until Shutdown is called.
func New(cfg config.Config, deps Deps) (*Server, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	s := &Server{
		cfg:          cfg,
		logger:       deps.Logger.With("component", "server"),
		auth:         deps.Auth,
		store:        deps.Store,
		pool:         deps.Pool,
		metrics:      deps.Metrics,
		dlq:          deps.DLQ,
		pipeline:     deps.Pipeline,
		health:       deps.Health,
		leadership:   deps.Leadership,
		sealer:       deps.Sealer,
		loginLimiter: newLoginLimiter(10, time.Minute),
		stopGC:       make(chan struct{}),
	}
	go s.loginLimiter.gc(s.stopGC)

	// Install the memberships-backed tenant resolver onto the auth
	// service. Without this, RequireSession falls back to the legacy
	// "everyone is owner of the default tenant" model, which keeps
	// single-tenant deployments working unchanged but doesn't isolate
	// new tenants — so we always install it when storage is wired.
	if deps.Auth != nil && deps.Store != nil {
		deps.Auth.SetTenantResolver(newTenantResolver(deps.Store, deps.Logger))
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
		s.httpSrv.TLSConfig = &tls.Config{MinVersion: minVer}
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
			errCh <- s.httpSrv.ListenAndServeTLS(s.cfg.Server.TLS.CertFile, s.cfg.Server.TLS.KeyFile)
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
		return nil
	case err := <-errCh:
		s.shutdownGC()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("server: %w", err)
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
