// Command mqconnector is the entrypoint binary. It wires every internal
// package, applies graceful shutdown on SIGINT/SIGTERM, and serves the
// HTTP API + embedded admin UI.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"mqConnector/internal/auth"
	"mqConnector/internal/config"
	"mqConnector/internal/dlq"
	"mqConnector/internal/health"
	"mqConnector/internal/leadership"
	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/secrets"
	"mqConnector/internal/server"
	"mqConnector/internal/storage"
)

// version is stamped at build time via -ldflags. Falls back to "dev" otherwise.
var version = "dev"

func main() {
	var (
		configPath  = flag.String("config", "config.yaml", "path to config file (use empty string for defaults+env only)")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if err := run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		return fmt.Errorf("ensure dirs: %w", err)
	}

	logger := logging.New(cfg.Logging.Level, cfg.Logging.Format).With(
		"app", "mqconnector",
		"version", version,
	)
	slog.SetDefault(logger)

	logger.Info("starting",
		"mode", cfg.Server.Mode,
		"listen", cfg.Server.Listen,
		"tls", cfg.Server.TLS.Enabled,
	)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Signal handling — first signal triggers graceful shutdown; a second
	// signal escalates to immediate exit.
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("shutdown signal received")
		rootCancel()
		<-sigCh
		logger.Error("second signal received, forcing exit")
		os.Exit(2)
	}()

	// Storage
	store, err := storage.Open(cfg.Storage.DSN, cfg.Storage.MaxOpenConns, cfg.Storage.MaxIdleConns)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer func() { _ = store.Close() }()
	logger.Info("storage opened")

	// Connection-password encryption. The sealer is nil when MQC_MASTER_KEY
	// isn't set, in which case ConnectionRepo stores+returns plaintext —
	// preserving the old behaviour without breaking existing data.
	sealer, err := secrets.FromEnv()
	if err != nil {
		return fmt.Errorf("init secrets: %w", err)
	}
	if sealer.Enabled() {
		store.Connections = store.Connections.WithSealer(sealer)
		logger.Info("connection-password encryption enabled (AES-GCM)")
	} else {
		logger.Warn("connection passwords stored in plaintext — set MQC_MASTER_KEY to encrypt at rest")
	}

	// Auth
	var authSvc *auth.Service
	if cfg.IsDev() && cfg.Auth.SimpleAuthURL == "" {
		return fmt.Errorf("auth.simpleauth_url is required even in dev mode; point it at a local SimpleAuth instance")
	}
	authSvc, err = auth.NewService(auth.Options{
		SimpleAuthURL:      cfg.Auth.SimpleAuthURL,
		SimpleAuthAdminKey: cfg.Auth.AdminKey,
		InsecureSkipVerify: cfg.Auth.InsecureSkipVerify,
		CookieName:         cfg.Auth.CookieName,
		SessionTTL:         cfg.Auth.SessionTTL,
		Secure:             !cfg.IsDev(),
	})
	if err != nil {
		return fmt.Errorf("init auth: %w", err)
	}

	// MQ pool
	pool := mq.NewPool(mq.PoolOptions{
		IdleTimeout:    cfg.MQ.Pool.IdleTimeout,
		HealthInterval: cfg.MQ.Pool.HealthInterval,
		Logger:         logger,
	})
	defer pool.Close()

	// Metrics
	metricsStore := metrics.New()

	// DLQ
	dlqSvc := dlq.NewService(store, pool, dlq.Options{
		MaxRetries: cfg.Pipeline.DLQ.MaxRetries,
		Logger:     logger,
	})

	// DLQ retention sweeper — bounded so a long broker outage can't fill
	// the disk. Disabled cleanly when both knobs are zero.
	retention := dlq.NewRetention(
		store.DLQ,
		cfg.Pipeline.DLQ.MaxAge,
		cfg.Pipeline.DLQ.MaxRows,
		cfg.Pipeline.DLQ.SweepInterval,
		logger,
	)
	go retention.Run(rootCtx)

	// Pipeline manager
	mgr := pipeline.NewManager(rootCtx, store, pool, metricsStore, dlqSvc, logger)
	defer mgr.Stop()

	// Optional leader-election lease. When enabled, this replica only
	// starts pipeline workers while it holds the lease — other replicas
	// pointed at the same database stay idle so the same source queue
	// isn't double-consumed. The admin UI is always served regardless.
	var leaseRunner *leadership.Lease
	if cfg.Leadership.Enabled {
		id := cfg.Leadership.ID
		if id == "" {
			id, _ = os.Hostname()
			if id == "" {
				id = "mqconnector"
			}
		}
		leaseRunner = leadership.New(store.DB, id, cfg.Leadership.TTL, logger)
		go func() {
			if err := leaseRunner.Run(rootCtx); err != nil {
				logger.Error("leadership.Run exited with error", "err", err)
			}
		}()
		go func() {
			for s := range leaseRunner.Changes() {
				if s.IsLeader {
					if _, err := mgr.Reload(rootCtx); err != nil {
						logger.Warn("reload on lease-acquire failed", "err", err)
					}
				} else {
					mgr.StopAll()
				}
			}
		}()
		logger.Info("leadership enabled", "self", id, "ttl", cfg.Leadership.TTL)
	} else {
		// Single-process deploy: start workers immediately.
		if _, err := mgr.Reload(rootCtx); err != nil {
			logger.Warn("initial pipeline reload failed", "err", err)
		}
	}

	// Health
	checker := health.NewChecker(store, metricsStore, version)

	// Server
	srv, err := server.New(cfg, server.Deps{
		Auth:       authSvc,
		Store:      store,
		Pool:       pool,
		Metrics:    metricsStore,
		DLQ:        dlqSvc,
		Pipeline:   mgr,
		Health:     checker,
		Leadership: leaseRunner, // nil when leadership is disabled
		Logger:     logger,
	})
	if err != nil {
		return fmt.Errorf("init server: %w", err)
	}
	logger.Info("server initialised, entering run loop")

	if err := srv.Run(rootCtx); err != nil {
		return fmt.Errorf("server: %w", err)
	}

	logger.Info("clean shutdown complete")
	return nil
}
