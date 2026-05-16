// Package audit owns the append-only archival of the audit_log table to a
// rotating JSONL file. The table is the source of truth for "what
// happened recently" (queryable via /api/v1/audit); the JSONL is the
// "what happened ever" tape that the SIEM picks up.
//
// Contract:
//   - One file per UTC day, named `audit-YYYY-MM-DD.jsonl`. Newline-
//     delimited JSON, one record per line. SIEMs can `tail -F` the
//     newest file without coordinating with this process.
//   - The archive sweeper runs on a configurable interval. Each tick:
//       1. iterate rows older than MaxAge,
//       2. append each to today's file (or the file matching the row's
//          date if the row is older than today — keeps the per-day
//          layout honest under back-pressure),
//       3. delete the rows ONLY after the file has been fsynced.
//   - The whole sweep is transactional in spirit but not on disk: a
//     crash between fsync and DELETE produces duplicate records in the
//     archive, never lost ones. Downstream consumers must be idempotent
//     on `id` (every record carries one).
//
// Disabled when ArchiveDir is empty.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mqConnector/internal/storage"
)

// Archiver streams old audit rows to JSONL files and prunes the table.
// Files can optionally be uploaded to an S3-compatible object store
// once they're rotated (i.e. once we're done writing to that day's
// file) — see SetS3Uploader.
type Archiver struct {
	store        archiveStore
	archiveDir   string
	maxAge       time.Duration
	sweepEvery   time.Duration
	logger       *slog.Logger

	mu         sync.Mutex // guards open files + uploader state
	openDate   string
	openFile   *os.File
	s3         *S3Uploader
	deleteAfterUpload bool
}

// SetS3Uploader installs an S3-compatible uploader. After this is
// called, the archiver uploads each fully-written daily file to S3
// when it rotates (i.e. when a row from a newer date forces the
// previous date's file to close). Pass nil to disable.
//
// If deleteAfterUpload is true, the local file is removed after a
// successful upload. The default is to keep both copies — useful for
// re-archival under SIEM glitches.
func (a *Archiver) SetS3Uploader(u *S3Uploader, deleteAfterUpload bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.s3 = u
	a.deleteAfterUpload = deleteAfterUpload
}

// archiveStore is the slice of *storage.Store the archiver needs.
type archiveStore interface {
	IterOlderThan(ctx context.Context, cutoff time.Time, fn func(*storage.AuditEntry) error) error
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// New constructs an Archiver. ArchiveDir is created with 0750. If the
// directory is empty or maxAge <= 0 the archiver is a no-op (Run
// returns immediately).
func New(store archiveStore, archiveDir string, maxAge, sweepEvery time.Duration, logger *slog.Logger) *Archiver {
	if sweepEvery <= 0 {
		sweepEvery = time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Archiver{
		store:      store,
		archiveDir: archiveDir,
		maxAge:     maxAge,
		sweepEvery: sweepEvery,
		logger:     logger.With("component", "audit.Archiver"),
	}
}

// Run loops until ctx is done.
func (a *Archiver) Run(ctx context.Context) {
	if a.archiveDir == "" || a.maxAge <= 0 {
		a.logger.Debug("archival disabled (archive_dir or max_age unset)")
		return
	}
	if err := os.MkdirAll(a.archiveDir, 0o750); err != nil {
		a.logger.Error("create archive dir failed", "dir", a.archiveDir, "err", err)
		return
	}
	// One sweep on boot so a freshly-restarted process catches up.
	a.sweep(ctx)
	t := time.NewTicker(a.sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			a.mu.Lock()
			if a.openFile != nil {
				_ = a.openFile.Sync()
				_ = a.openFile.Close()
			}
			a.mu.Unlock()
			return
		case <-t.C:
			a.sweep(ctx)
		}
	}
}

// sweep is the per-tick body. Public via Archive() for tests that want
// to drive it synchronously.
func (a *Archiver) sweep(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-a.maxAge)
	n, err := a.Archive(ctx, cutoff)
	if err != nil {
		a.logger.Warn("archive sweep failed", "err", err, "cutoff", cutoff)
		return
	}
	if n > 0 {
		a.logger.Info("archived audit rows", "count", n, "cutoff", cutoff)
	}
}

// Archive streams all rows older than cutoff into the per-day file(s)
// and then deletes them. Returns the count moved. Exported so an
// operator can run a one-shot archive via the rotate-secrets-style
// subcommand path if we ever add one.
func (a *Archiver) Archive(ctx context.Context, cutoff time.Time) (int, error) {
	count := 0
	err := a.store.IterOlderThan(ctx, cutoff, func(e *storage.AuditEntry) error {
		if err := a.appendRow(e); err != nil {
			return err
		}
		count++
		return nil
	})
	if err != nil {
		return count, err
	}
	if count == 0 {
		return 0, nil
	}
	// fsync everything we've written before pruning. A crash between
	// here and DeleteOlderThan duplicates the archived rows — which is
	// recoverable — never loses them.
	a.mu.Lock()
	if a.openFile != nil {
		if err := a.openFile.Sync(); err != nil {
			a.mu.Unlock()
			return count, fmt.Errorf("fsync archive: %w", err)
		}
	}
	a.mu.Unlock()
	if _, err := a.store.DeleteOlderThan(ctx, cutoff); err != nil {
		return count, fmt.Errorf("delete archived rows: %w", err)
	}
	return count, nil
}

// appendRow writes one entry to the file matching its date. Files are
// opened lazily and held open across rows for the common case where a
// burst belongs to a single day.
//
// When the date changes (this row is for a different day than the one
// we're currently writing to), the previous day's file is closed and
// optionally uploaded to S3. The upload is best-effort: a failure
// logs but doesn't abort the sweep, since the row already landed on
// local disk.
func (a *Archiver) appendRow(e *storage.AuditEntry) error {
	date := e.At.UTC().Format("2006-01-02")
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.openDate != date || a.openFile == nil {
		if a.openFile != nil {
			closingPath := a.openFile.Name()
			closingDate := a.openDate
			_ = a.openFile.Sync()
			_ = a.openFile.Close()
			a.openFile = nil
			// Upload the just-closed file. The mutex is held while we
			// do this — appendRow is called from a single goroutine
			// (the sweep loop) so the lock isn't contended in practice.
			if a.s3 != nil && closingPath != "" {
				if err := a.uploadAndMaybePrune(closingPath, closingDate); err != nil {
					a.logger.Warn("s3 upload failed",
						"file", closingPath, "err", err)
				}
			}
		}
		path := filepath.Join(a.archiveDir, "audit-"+date+".jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			return fmt.Errorf("open archive %s: %w", path, err)
		}
		a.openFile = f
		a.openDate = date
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal audit row: %w", err)
	}
	if _, err := a.openFile.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write audit row: %w", err)
	}
	return nil
}

// uploadAndMaybePrune sends one JSONL file to S3. Key path:
//
//	audit/YYYY/MM/DD/audit-YYYY-MM-DD.jsonl
//
// Date-partitioned so SIEMs / Athena can prune scans by prefix. On a
// successful upload, optionally deletes the local file. Errors are
// returned to the caller (which logs but doesn't abort the sweep).
func (a *Archiver) uploadAndMaybePrune(localPath, date string) error {
	body, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", localPath, err)
	}
	if len(body) == 0 {
		return nil
	}
	// date is "YYYY-MM-DD" → partition the key.
	year, month, day := date[:4], date[5:7], date[8:10]
	key := fmt.Sprintf("audit/%s/%s/%s/audit-%s.jsonl", year, month, day, date)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	reqID, err := a.s3.PutObject(ctx, key, body)
	if err != nil {
		return err
	}
	a.logger.Info("audit file uploaded to s3",
		"file", localPath, "key", key, "size", len(body), "x_amz_request_id", reqID)
	if a.deleteAfterUpload {
		if err := os.Remove(localPath); err != nil {
			a.logger.Warn("local prune after upload failed", "file", localPath, "err", err)
		}
	}
	return nil
}
