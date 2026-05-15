package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mqConnector/internal/storage"
)

// TestAudit_MutationsRecordedWithActor walks the full mutation lifecycle and
// asserts the audit log captures it with the right actor + status.
func TestAudit_MutationsRecordedWithActor(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	// Create + delete a connection (two mutations).
	conn := postConn(t, h, cookie, `{"name":"audit-test","type":"rabbitmq","url":"amqp://x","queue_name":"q"}`)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/connections/"+conn.ID, nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}

	// Audit insert is async (best-effort goroutine) — poll briefly.
	deadline := time.Now().Add(2 * time.Second)
	var list []*storage.AuditEntry
	var total int
	for time.Now().Before(deadline) {
		list, total, _ = srv.store.Audit.List(context.Background(),
			storage.AuditFilter{Actor: "alice"}, 1, 50)
		if total >= 2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if total < 2 {
		t.Fatalf("expected at least 2 audit entries for alice, got %d", total)
	}
	verbs := map[string]bool{}
	for _, e := range list {
		if e.Actor != "alice" {
			t.Errorf("unexpected actor %q", e.Actor)
		}
		verbs[e.Action] = true
	}
	if !verbs["POST"] || !verbs["DELETE"] {
		t.Errorf("missing verbs in audit log: %v", verbs)
	}
}

// TestAudit_ReadOnlyRequestsNotLogged makes sure GETs don't pollute the trail.
func TestAudit_ReadOnlyRequestsNotLogged(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil)
		req.AddCookie(cookie)
		h.ServeHTTP(rec, req)
	}
	// Brief settle — the audit insert is async.
	time.Sleep(200 * time.Millisecond)

	_, total, _ := srv.store.Audit.List(context.Background(), storage.AuditFilter{}, 1, 50)
	if total != 0 {
		t.Errorf("GETs leaked into audit log: %d entries", total)
	}
}

// TestAudit_HTTPEndpoint returns entries via the REST API.
func TestAudit_HTTPEndpoint(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	_ = postConn(t, h, cookie, `{"name":"x","type":"rabbitmq","url":"amqp://x","queue_name":"q"}`)
	time.Sleep(200 * time.Millisecond)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit?per_page=10", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit list: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"actor":"alice"`) {
		t.Errorf("audit body missing actor: %s", rec.Body)
	}
}
