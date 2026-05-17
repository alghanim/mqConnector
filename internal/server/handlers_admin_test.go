package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mqConnector/internal/storage"
)

// grantSystemAdmin makes the test user "alice" a system admin by
// upserting a default-tenant owner membership with the flag set. Used
// by every test that hits a system-admin-only endpoint.
func grantSystemAdmin(t *testing.T, srv *Server) {
	t.Helper()
	if err := srv.store.Memberships.Upsert(context.Background(), &storage.Membership{
		TenantID:    storage.DefaultTenantID,
		UserSub:     "alice",
		Username:    "alice",
		Role:        storage.RoleOwner,
		SystemAdmin: true,
	}); err != nil {
		t.Fatalf("grant system_admin: %v", err)
	}
}

// TestAdminBackup_RequiresSystemAdmin — a regular session must NOT be
// able to download a full database snapshot. The endpoint streams
// every tenant's encrypted-but-recoverable secrets and the audit
// chain; tenant-scoped access wouldn't be enough.
func TestAdminBackup_RequiresSystemAdmin(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	// alice has no system_admin yet.

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/backup", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin should get 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAdminBackup_HappyPath — a system admin can download a snapshot.
// The response is a binary stream with a download disposition; we
// verify the headers + non-empty body. The integrity of the snapshot
// itself is covered by storage's BackupAndRestore test.
func TestAdminBackup_HappyPath(t *testing.T) {
	h, srv, _ := newTestServer(t)
	grantSystemAdmin(t, srv)
	cookie := loginCookie(t, h, "alice", "wonderland")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/backup", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("content-type = %q, want application/octet-stream", ct)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "attachment") || !strings.Contains(cd, ".db") {
		t.Errorf("content-disposition = %q, want attachment + .db", cd)
	}
	if rec.Body.Len() == 0 {
		t.Error("snapshot body is empty")
	}
}

// TestAdminIntegrity_ReportsOK — fresh database passes the integrity
// check. The endpoint returns 200 in both clean and corrupted cases;
// the body's "ok" field discriminates.
func TestAdminIntegrity_ReportsOK(t *testing.T) {
	h, srv, _ := newTestServer(t)
	grantSystemAdmin(t, srv)
	cookie := loginCookie(t, h, "alice", "wonderland")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/integrity", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	var resp struct {
		OK         bool     `json:"ok"`
		DurationMs int64    `json:"duration_ms"`
		Errors     []string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Errorf("expected ok=true, got errors: %v", resp.Errors)
	}
}

// TestAdminIntegrity_RequiresSystemAdmin — same gate as backup.
// PRAGMA integrity_check is read-only but not free; multi-GB
// databases can pin the writer thread for minutes. System-admin only.
func TestAdminIntegrity_RequiresSystemAdmin(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/integrity", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin should get 403, got %d", rec.Code)
	}
}
