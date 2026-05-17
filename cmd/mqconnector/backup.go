package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"mqConnector/internal/config"
	"mqConnector/internal/storage"
)

// backupCmd snapshots the live SQLite database to a destination file
// using SQLite's online VACUUM INTO. The snapshot is consistent without
// stopping the running binary — readers and writers see no
// interruption. The produced file is itself a valid SQLite database,
// openable with the same driver for restore or for offline analysis.
//
// Invocation:
//
//	mqconnector backup --to=/var/backups/mqc-2026-05-17.db
//	mqconnector backup --to-dir=/var/backups --keep=7
//
// --to and --to-dir are mutually exclusive. --to-dir adds a timestamp
// to the filename and rotates older backups so the directory keeps the
// most recent N entries (default 7).
//
// Exit status:
//
//	0 — backup completed and integrity check passed
//	1 — backup failed (write error, disk full, etc.)
//	2 — backup completed but integrity check on the snapshot failed —
//	    treat the snapshot as suspect and investigate the source DB
func backupCmd() error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to config file")
	to := fs.String("to", "", "destination file (absolute path)")
	toDir := fs.String("to-dir", "", "destination directory; filename auto-generated with timestamp")
	keep := fs.Int("keep", 7, "with --to-dir: number of recent snapshots to retain")
	skipCheck := fs.Bool("skip-integrity-check", false, "skip the PRAGMA integrity_check on the snapshot (faster but unsafe)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if (*to == "" && *toDir == "") || (*to != "" && *toDir != "") {
		fs.Usage()
		return errors.New("exactly one of --to or --to-dir is required")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	dest := *to
	if *toDir != "" {
		if err := os.MkdirAll(*toDir, 0o755); err != nil {
			return fmt.Errorf("mkdir backup dir: %w", err)
		}
		stamp := time.Now().UTC().Format("20060102T150405Z")
		dest = filepath.Join(*toDir, "mqconnector-"+stamp+".db")
	}

	store, err := storage.Open(cfg.Storage.DSN, cfg.Storage.MaxOpenConns, cfg.Storage.MaxOpenConns/2)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	start := time.Now()
	if err := store.Backup(ctx, dest); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	info, _ := os.Stat(dest)
	var size int64
	if info != nil {
		size = info.Size()
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes) in %s\n", dest, size, time.Since(start).Round(time.Millisecond))

	if !*skipCheck {
		if err := verifySnapshotIntegrity(ctx, dest); err != nil {
			// Don't delete the snapshot — operator may want to inspect.
			fmt.Fprintf(os.Stderr, "WARN: integrity check on snapshot failed: %v\n", err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "integrity check: ok")
	}

	if *toDir != "" && *keep > 0 {
		if err := rotateBackups(*toDir, *keep); err != nil {
			fmt.Fprintf(os.Stderr, "WARN: backup rotation: %v\n", err)
		}
	}
	return nil
}

// verifySnapshotIntegrity opens the snapshot in read-only mode and runs
// PRAGMA integrity_check. A snapshot that passes here is safe to
// restore from; one that fails should be discarded.
func verifySnapshotIntegrity(ctx context.Context, path string) error {
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)"
	store, err := storage.Open(dsn, 2, 1)
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer store.Close()
	rows, err := store.IntegrityCheck(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 1 && rows[0] == "ok" {
		return nil
	}
	return fmt.Errorf("integrity_check returned %d rows; first: %s", len(rows), firstOrEmpty(rows))
}

func firstOrEmpty(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return s[0]
}

// rotateBackups walks dir, sorts mqconnector-*.db by name (timestamps
// are lexically sortable in the chosen format), and deletes the
// oldest entries beyond keep.
func rotateBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, "mqconnector-") && strings.HasSuffix(n, ".db") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if len(names) <= keep {
		return nil
	}
	for _, n := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			return fmt.Errorf("remove %s: %w", n, err)
		}
	}
	return nil
}
