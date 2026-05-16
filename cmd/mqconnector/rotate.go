package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"mqConnector/internal/config"
	"mqConnector/internal/logging"
	"mqConnector/internal/secrets"
	"mqConnector/internal/storage"
)

// rotateSecrets re-encrypts every stored MQ connection password from the
// OLD master key to the NEW master key. Designed to be run while the
// bridge is stopped (the SQLite file is locked by the running binary).
//
// Invocation:
//
//	mqconnector rotate-secrets \
//	    --old-key=$(openssl rand -hex 32) \
//	    --new-key=$(openssl rand -hex 32)
//
// If --old-key is omitted, the existing rows are assumed plaintext (the
// first-time-encryption case). If --new-key is omitted, MQC_MASTER_KEY
// is used so an operator can pre-set the new key in env and just run
// the rotate command bare.
//
// Idempotent: rows that are already encrypted under the new key (or
// plaintext when --new-key="") pass through unchanged.
//
// The subcommand is intentionally noisy on stderr — every rotated row
// is logged so an audit reviewer can confirm coverage. The SQLite file
// is updated in place; back it up first via `scripts/backup.sh`.
func rotateSecrets() error {
	fs := flag.NewFlagSet("rotate-secrets", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	oldKey := fs.String("old-key", "", "current master key (hex or base64). Empty means rows are plaintext.")
	newKey := fs.String("new-key", "", "new master key. Empty falls back to $MQC_MASTER_KEY.")
	dryRun := fs.Bool("dry-run", false, "print what would change without writing")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	if *newKey == "" {
		*newKey = strings.TrimSpace(os.Getenv("MQC_MASTER_KEY"))
	}

	logger := logging.New("info", "text").With("subcommand", "rotate-secrets")

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		return fmt.Errorf("ensure dirs: %w", err)
	}

	// Build the two sealers. Either may be nil; ConnectionRepo handles
	// nil as "no encryption."
	var oldSealer, newSealer *secrets.Service
	if *oldKey != "" {
		s, err := secrets.New(*oldKey)
		if err != nil {
			return fmt.Errorf("parse old key: %w", err)
		}
		oldSealer = s
	}
	if *newKey != "" {
		s, err := secrets.New(*newKey)
		if err != nil {
			return fmt.Errorf("parse new key: %w", err)
		}
		newSealer = s
	}
	if oldSealer == nil && newSealer == nil {
		return errors.New("nothing to rotate: provide --old-key, --new-key, or both")
	}

	// Open storage with the OLD sealer so Get returns plaintext.
	store, err := storage.Open(cfg.Storage.DSN, cfg.Storage.MaxOpenConns, cfg.Storage.MaxIdleConns)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer func() { _ = store.Close() }()
	store.Connections = store.Connections.WithSealer(oldSealer)

	ctx := context.Background()
	// System-level walk across every tenant — rotation is a privileged
	// op, run only by an operator with direct access to the SQLite file.
	rows, err := store.Connections.ListAll(ctx)
	if err != nil {
		return fmt.Errorf("list connections: %w", err)
	}
	logger.Info("loaded connections", "count", len(rows))

	if *dryRun {
		for _, c := range rows {
			logger.Info("would rotate", "id", c.ID, "name", c.Name, "tenant_id", c.TenantID, "type", c.Type, "has_password", c.Password != "")
		}
		fmt.Fprintf(os.Stderr, "dry-run: %d connection(s) would be rotated\n", len(rows))
		return nil
	}

	// Swap the sealer for writes. Update each row in place, scoped to
	// its own tenant.
	store.Connections = store.Connections.WithSealer(newSealer)
	for _, c := range rows {
		if err := store.Connections.Update(ctx, c.TenantID, c); err != nil {
			return fmt.Errorf("rewrite connection %s (%s): %w", c.ID, c.Name, err)
		}
		logger.Info("rotated", "id", c.ID, "name", c.Name, "type", c.Type)
	}
	fmt.Fprintf(os.Stderr, "✓ rotated %d connection(s)\n", len(rows))
	return nil
}
