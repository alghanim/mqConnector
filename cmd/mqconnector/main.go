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
	"time"

	"mqConnector/internal/audit"
	"mqConnector/internal/auth"
	"mqConnector/internal/config"
	"mqConnector/internal/dlq"
	"mqConnector/internal/events"
	"mqConnector/internal/health"
	"mqConnector/internal/leadership"
	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/secrets"
	"mqConnector/internal/server"
	"mqConnector/internal/storage"
	"mqConnector/internal/tracing"
	"mqConnector/internal/webhooks"
)

// version is stamped at build time via -ldflags. Falls back to "dev" otherwise.
var version = "dev"

func main() {
	// Subcommand routing: anything before flags is treated as a verb.
	// Kept trivial — two verbs today, no need for cobra.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "rotate-secrets":
			os.Args = append(os.Args[:1], os.Args[2:]...) // drop verb for flag.Parse
			if err := rotateSecrets(); err != nil {
				fmt.Fprintf(os.Stderr, "rotate-secrets: %v\n", err)
				os.Exit(1)
			}
			return
		case "gitops":
			if err := gitops(); err != nil {
				fmt.Fprintf(os.Stderr, "gitops: %v\n", err)
				os.Exit(1)
			}
			return
		case "backup":
			if err := backupCmd(); err != nil {
				fmt.Fprintf(os.Stderr, "backup: %v\n", err)
				os.Exit(1)
			}
			return
		case "healthcheck":
			if err := healthcheck(); err != nil {
				fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
				os.Exit(1)
			}
			return
		case "kafka-offsets":
			os.Args = append(os.Args[:1], os.Args[2:]...)
			if err := kafkaOffsets(); err != nil {
				fmt.Fprintf(os.Stderr, "kafka-offsets: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

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

	// Optional OpenTelemetry export. Empty endpoint = no-op, tracing
	// stays as structured-log spans. When configured, every Span.End()
	// throughout the binary mirrors into an OTLP/HTTP exporter.
	otelShutdown, err := tracing.EnableOTLP(rootCtx, tracing.OTLPConfig{
		Endpoint:    cfg.Tracing.OTLPEndpoint,
		ServiceName: cfg.Tracing.ServiceName,
		Version:     version,
		Insecure:    cfg.Tracing.Insecure,
		SampleRatio: cfg.Tracing.SampleRatio,
	})
	if err != nil {
		return fmt.Errorf("enable OTLP: %w", err)
	}
	if cfg.Tracing.OTLPEndpoint != "" {
		logger.Info("otlp export enabled",
			"endpoint", cfg.Tracing.OTLPEndpoint,
			"sample_ratio", cfg.Tracing.SampleRatio,
		)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = otelShutdown(shutdownCtx)
	}()

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

	// Connection-password encryption. In prod mode the master key is
	// mandatory — an enterprise deployment must not store broker
	// credentials at rest in plaintext. Dev mode tolerates an unset key
	// so a developer can poke at a local SQLite file without ceremony.
	sealer, err := secrets.FromEnv()
	if err != nil {
		return fmt.Errorf("init secrets: %w", err)
	}
	if sealer.Enabled() {
		store.Connections = store.Connections.WithSealer(sealer)
		logger.Info("connection-password encryption enabled (AES-GCM)",
			"current_key_version", sealer.Current())
	} else if cfg.IsDev() {
		logger.Warn("connection passwords stored in plaintext — set MQC_MASTER_KEY to encrypt at rest")
	} else {
		return fmt.Errorf("MQC_MASTER_KEY (or MQC_MASTER_KEYS) is required in prod mode; " +
			"set a 32-byte hex/base64 key so broker passwords are encrypted at rest, " +
			"or run with server.mode=dev for local testing")
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
		IdleTimeout:        cfg.Auth.IdleTimeout,
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

	// DLQ retry reaper. Walks dlq rows whose next_retry_at <= now and
	// re-publishes through the pipeline's destination with exponential
	// backoff. Pipelines opt in by setting retry_max > 0 on their row.
	stopReaper := dlqSvc.StartReaper(rootCtx, dlq.ReaperOptions{})
	defer stopReaper()

	// DLQ retention sweeper — bounded so a long broker outage can't
	// fill the disk. Disabled cleanly when both knobs are zero.
	// Leader gating wired below once leaseRunner is constructed.
	retention := dlq.NewRetention(
		store.DLQ,
		cfg.Pipeline.DLQ.MaxAge,
		cfg.Pipeline.DLQ.MaxRows,
		cfg.Pipeline.DLQ.SweepInterval,
		logger,
	)

	// Audit-log archival — streams rows older than cfg.Audit.MaxAge to
	// per-day JSONL files in cfg.Audit.ArchiveDir, then prunes the
	// live table. No-op when ArchiveDir is empty.
	archiver := audit.New(
		store.Audit,
		cfg.Audit.ArchiveDir,
		cfg.Audit.MaxAge,
		cfg.Audit.SweepInterval,
		logger,
	)
	// Optional S3 upload of rotated daily files. Opt-in via
	// cfg.Audit.S3 — empty access key / bucket leaves archival on
	// local disk only. Air-gapped deployments leave the block empty.
	if up := audit.NewS3(audit.S3Config{
		Endpoint:  cfg.Audit.S3.Endpoint,
		Region:    cfg.Audit.S3.Region,
		Bucket:    cfg.Audit.S3.Bucket,
		Prefix:    cfg.Audit.S3.Prefix,
		AccessKey: cfg.Audit.S3.AccessKey,
		SecretKey: cfg.Audit.S3.SecretKey,
	}); up != nil {
		archiver.SetS3Uploader(up, cfg.Audit.S3.DeleteAfterUpload)
		logger.Info("audit S3 upload enabled",
			"endpoint", cfg.Audit.S3.Endpoint,
			"bucket", cfg.Audit.S3.Bucket,
			"prefix", cfg.Audit.S3.Prefix,
			"delete_after_upload", cfg.Audit.S3.DeleteAfterUpload)
	}
	if cfg.Audit.ArchiveDir != "" {
		logger.Info("audit archival enabled",
			"archive_dir", cfg.Audit.ArchiveDir,
			"max_age", cfg.Audit.MaxAge,
			"sweep_interval", cfg.Audit.SweepInterval)
	}
	// Optional real-time syslog fan-out. Independent of archival —
	// SIEMs that want low-latency feed register a URL here; the
	// archiver still rolls daily files for compliance retention.
	if cfg.Audit.SyslogURL != "" {
		host, _ := os.Hostname()
		sf, err := audit.NewSyslogForwarder(cfg.Audit.SyslogURL, host, "mqconnector", logger)
		if err != nil {
			logger.Error("syslog forwarder init failed; continuing without it",
				"err", err, "url", cfg.Audit.SyslogURL)
		} else {
			store.Audit.AddSink(sf)
			sf.Start(rootCtx)
			logger.Info("audit syslog forwarder enabled", "url", cfg.Audit.SyslogURL)
		}
	}

	// Event bus + webhook dispatcher. Started before the pipeline
	// manager so lifecycle events emitted during Reload land on a
	// running subscriber rather than getting dropped by a not-yet-
	// subscribed publisher.
	eventBus := events.NewPublisher(128, logger)
	dispatcher := webhooks.New(store.Webhooks, eventBus, webhooks.Options{}, logger)
	go dispatcher.Run(rootCtx)
	defer dispatcher.Stop()

	// Pipeline manager
	mgr := pipeline.NewManager(rootCtx, store, pool, metricsStore, dlqSvc, logger)
	mgr.SetEventSink(eventBus)
	dlqSvc.SetEventSink(eventBus)
	// Graceful drain: give in-flight messages 30s to finish their
	// receive → stages → send round trip before the process exits.
	// Fire-and-forget Stop() runs anyway as a backstop if the deferred
	// call below somehow doesn't fire (panic-during-panic).
	defer func() {
		if ok := mgr.StopAndWait(30 * time.Second); !ok {
			logger.Warn("pipeline manager drain timed out — some messages may be in-flight at exit")
		}
	}()

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
		leaseDialect := leadership.DialectSQLite
		if store.Dialect() == storage.DialectPostgres {
			leaseDialect = leadership.DialectPostgres
		}
		leaseRunner = leadership.NewWithDialect(store.DB, id, cfg.Leadership.TTL, leaseDialect, logger)
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

	// Wire leader-only gating into retention + archiver now that
	// leaseRunner is known, then start their sweep loops. In multi-
	// replica deploys only the leader prunes so two replicas don't
	// fight over the same DELETE statements.
	if leaseRunner != nil {
		isLeader := func() bool { return leaseRunner.Snapshot().IsLeader }
		retention.SetLeaderCheck(isLeader)
		archiver.SetLeaderCheck(isLeader)
	}
	go retention.Run(rootCtx)
	go archiver.Run(rootCtx)

	// Scheduled backups. Off by default; the operator opts in by
	// setting storage.backup.dir. On multi-replica deploys only the
	// leader actually snapshots so the destination directory doesn't
	// get duplicated entries. See OPERATIONS.md for the restore
	// procedure.
	if cfg.Storage.Backup.Dir != "" {
		isLeader := func() bool { return true }
		if leaseRunner != nil {
			isLeader = func() bool { return leaseRunner.Snapshot().IsLeader }
		}
		bw := &storage.BackupWorker{
			Store:    store,
			Dir:      cfg.Storage.Backup.Dir,
			Interval: cfg.Storage.Backup.Interval,
			Keep:     cfg.Storage.Backup.Keep,
			IsLeader: isLeader,
			Logger:   logger,
		}
		go bw.Run(rootCtx)
		logger.Info("scheduled backups enabled",
			"dir", cfg.Storage.Backup.Dir,
			"interval", cfg.Storage.Backup.Interval,
			"keep", cfg.Storage.Backup.Keep)
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
		Sealer:     sealer,      // nil when MQC_MASTER_KEY is unset
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
