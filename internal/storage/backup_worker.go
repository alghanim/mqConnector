package storage

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LeaderCheck reports whether the current process is the active
// leader. Used by BackupWorker to skip snapshots on passive replicas
// so two leaders don't fight for the same destination directory. Pass
// a func returning true to opt out (single-process deploys).
type LeaderCheck func() bool

// BackupWorker periodically writes a consistent snapshot of the store
// to a directory and rotates older files past Keep. Designed to run
// for the process lifetime — call Run with a cancellable context and
// it'll exit on context done.
type BackupWorker struct {
	Store    *Store
	Dir      string
	Interval time.Duration
	Keep     int
	IsLeader LeaderCheck
	Logger   *slog.Logger
}

// Run blocks until ctx is cancelled. Each tick (1) skips if not
// leader, (2) writes a VACUUM-INTO snapshot, (3) rotates older
// snapshots beyond Keep. Errors are logged and the next tick still
// fires — a single bad snapshot shouldn't disable scheduled backups.
func (w *BackupWorker) Run(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = 24 * time.Hour
	}
	if w.Keep <= 0 {
		w.Keep = 7
	}
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", "backup_worker", "dir", w.Dir, "interval", w.Interval)

	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		logger.Error("mkdir backup dir failed; worker exiting", "err", err)
		return
	}

	// Snapshot once at startup (assuming we're leader). This means a
	// fresh boot has a baseline rather than waiting Interval for the
	// first one. Especially valuable when Interval is daily.
	w.tick(ctx, logger)

	t := time.NewTicker(w.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("backup worker stopping")
			return
		case <-t.C:
			w.tick(ctx, logger)
		}
	}
}

func (w *BackupWorker) tick(ctx context.Context, logger *slog.Logger) {
	if w.IsLeader != nil && !w.IsLeader() {
		logger.Debug("skipping snapshot: not leader")
		return
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	dst := filepath.Join(w.Dir, "mqconnector-"+stamp+".db")

	start := time.Now()
	if err := w.Store.Backup(ctx, dst); err != nil {
		logger.Error("scheduled backup failed", "err", err, "dst", dst)
		return
	}
	info, _ := os.Stat(dst)
	var size int64
	if info != nil {
		size = info.Size()
	}
	logger.Info("scheduled backup written",
		"dst", dst,
		"bytes", size,
		"duration_ms", time.Since(start).Milliseconds())

	if err := pruneBackups(w.Dir, w.Keep); err != nil {
		logger.Warn("backup rotation failed", "err", err)
	}
}

// pruneBackups deletes the oldest mqconnector-*.db files in dir so
// only the most recent `keep` remain. Lexical sort works because the
// stamp format is fixed-width and time-sortable.
func pruneBackups(dir string, keep int) error {
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
			return err
		}
	}
	return nil
}
