// Package server wires the HTTP layer: chi router, middleware stack, route
// registration, embedded SvelteKit UI, and graceful TLS startup/shutdown.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/config"
	"mqConnector/internal/dlq"
	"mqConnector/internal/health"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/storage"
	"mqConnector/internal/web"
)

// Server is the wired HTTP layer.
type Server struct {
	cfg      config.Config
	logger   *slog.Logger
	httpSrv  *http.Server
	auth     *auth.Service
	store    *storage.Store
	pool     *mq.Pool
	metrics  *metrics.Store
	dlq      *dlq.Service
	pipeline *pipeline.Manager
	health   *health.Checker
}

// Deps bundles the dependencies New requires.
type Deps struct {
	Auth     *auth.Service
	Store    *storage.Store
	Pool     *mq.Pool
	Metrics  *metrics.Store
	DLQ      *dlq.Service
	Pipeline *pipeline.Manager
	Health   *health.Checker
	Logger   *slog.Logger
}

// New constructs a Server. Run blocks until Shutdown is called.
func New(cfg config.Config, deps Deps) (*Server, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	s := &Server{
		cfg:      cfg,
		logger:   deps.Logger.With("component", "server"),
		auth:     deps.Auth,
		store:    deps.Store,
		pool:     deps.Pool,
		metrics:  deps.Metrics,
		dlq:      deps.DLQ,
		pipeline: deps.Pipeline,
		health:   deps.Health,
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
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("server: %w", err)
	}
}

// staticFS mounts the embedded SvelteKit build at the chi router, serving
// /assets etc. and falling back to index.html for client-side routes.
func (s *Server) mountStatic(r chi.Router) {
	uiFS := web.DistFS()
	fileServer := http.FileServer(http.FS(uiFS))

	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		// Try the exact path; if it doesn't exist, fall back to index.html
		// so client-side routing handles deep links.
		if _, err := fs.Stat(uiFS, p); err != nil {
			r.URL.Path = "/"
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/index.html"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
