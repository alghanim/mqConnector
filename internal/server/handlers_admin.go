package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// handleAdminBackup snapshots the SQLite database and streams it as a
// download. System-admin only — the bundle contains every tenant's
// encrypted-but-recoverable credentials and the audit chain, so giving
// it out lightly defeats multi-tenant isolation.
//
// Mechanics: write via the Store's online backup (VACUUM INTO) to a
// temp file, stat, stream the bytes, then unlink. We don't hold the
// file open across the stream — the OS retains the inode until our
// Open handle closes, which is fine for the request lifetime.
//
// Filename: mqconnector-backup-<RFC3339 timestamp>.db, content-type
// application/octet-stream. The caller is expected to redirect into
// curl --output or save via the browser dialog.
func (s *Server) handleAdminBackup(w http.ResponseWriter, r *http.Request) {
	if !s.isSystemAdmin(r) {
		writeError(w, http.StatusForbidden, "system-admin required")
		return
	}
	// 10-minute budget — generous so a multi-GB database has time to
	// snapshot under load. Bounded so a hung backup eventually gives
	// the caller back the connection.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "mqc-backup-")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tmp dir: "+err.Error())
		return
	}
	defer os.RemoveAll(tmpDir)
	dst := filepath.Join(tmpDir, "snapshot.db")

	if err := s.store.Backup(ctx, dst); err != nil {
		writeError(w, http.StatusInternalServerError, "backup: "+err.Error())
		return
	}

	info, err := os.Stat(dst)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stat snapshot: "+err.Error())
		return
	}
	f, err := os.Open(dst)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "open snapshot: "+err.Error())
		return
	}
	defer f.Close()

	stamp := time.Now().UTC().Format("20060102T150405Z")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="mqconnector-backup-%s.db"`, stamp))
	if _, err := io.Copy(w, f); err != nil {
		// Headers already sent — the best we can do is log via the
		// request body (will be discarded but not 500ed mid-stream).
		_ = err
	}
}

// handleAdminIntegrity runs PRAGMA integrity_check against the live
// database. Returns 200 with {"ok": true, ...} on clean, 200 with
// {"ok": false, "errors": [...]} on detected corruption (HTTP success
// signals "the check ran"; the body discriminates the result so a
// dashboard can render either case without flipping on status codes).
//
// System-admin only. The check is read-only but not free — a multi-GB
// database can take minutes. We don't expose this on the regular
// /api/health probe.
func (s *Server) handleAdminIntegrity(w http.ResponseWriter, r *http.Request) {
	if !s.isSystemAdmin(r) {
		writeError(w, http.StatusForbidden, "system-admin required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	start := time.Now()
	rows, err := s.store.IntegrityCheck(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "integrity check: "+err.Error())
		return
	}
	resp := map[string]any{
		"duration_ms": time.Since(start).Milliseconds(),
	}
	if len(rows) == 1 && strings.TrimSpace(rows[0]) == "ok" {
		resp["ok"] = true
	} else {
		resp["ok"] = false
		resp["errors"] = rows
	}
	writeJSON(w, http.StatusOK, resp)
}
