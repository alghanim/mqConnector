package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mqConnector/internal/storage"
)

// seedDLQ inserts a handful of DLQ rows directly via storage so the test
// doesn't depend on a live pipeline.
func seedDLQ(t *testing.T, srv *Server) {
	t.Helper()
	ctx := context.Background()

	for _, e := range []*storage.DLQEntry{
		{OriginalMsg: []byte("a"), ErrorReason: "Connection refused on amqp dial"},
		{OriginalMsg: []byte("b"), ErrorReason: "validation: missing field"},
		{OriginalMsg: []byte("c"), ErrorReason: "validation: bad type"},
		{OriginalMsg: []byte("d"), ErrorReason: "TLS handshake error"},
	} {
		if err := srv.store.DLQ.Insert(ctx, e); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

type dlqList struct {
	Page    int                  `json:"page"`
	PerPage int                  `json:"per_page"`
	Total   int                  `json:"total"`
	Items   []*storage.DLQEntry  `json:"items"`
}

func getDLQ(t *testing.T, h http.Handler, cookie *http.Cookie, q string) dlqList {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dlq?"+q, nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	var out dlqList
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v %s", err, rec.Body)
	}
	return out
}

func TestDLQ_HTTPFilter_Error(t *testing.T) {
	h, srv, _ := newTestServer(t)
	seedDLQ(t, srv)
	cookie := loginCookie(t, h, "alice", "wonderland")

	got := getDLQ(t, h, cookie, "error=validation")
	if got.Total != 2 {
		t.Errorf("error=validation total = %d, want 2", got.Total)
	}
}

func TestDLQ_HTTPFilter_TimeWindow(t *testing.T) {
	h, srv, _ := newTestServer(t)
	seedDLQ(t, srv)
	cookie := loginCookie(t, h, "alice", "wonderland")

	// Force one entry into the past.
	_, _ = srv.store.DB.Exec(`UPDATE dlq SET created_at = ? WHERE original_msg = ?`,
		time.Now().Add(-3*time.Hour).UTC(), []byte("a"))

	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	got := getDLQ(t, h, cookie, "since="+since)
	if got.Total != 3 {
		t.Errorf("since=1h ago total = %d, want 3", got.Total)
	}
}

func TestDLQ_HTTPFilter_NoMatchReturnsZeroTotal(t *testing.T) {
	h, srv, _ := newTestServer(t)
	seedDLQ(t, srv)
	cookie := loginCookie(t, h, "alice", "wonderland")

	got := getDLQ(t, h, cookie, "error=does-not-exist")
	if got.Total != 0 {
		t.Errorf("no-match total = %d, want 0", got.Total)
	}
	if got.Items == nil {
		t.Error("items should be a non-nil empty array")
	}
}

func TestDLQ_HTTPFilter_MalformedTimeIgnored(t *testing.T) {
	h, srv, _ := newTestServer(t)
	seedDLQ(t, srv)
	cookie := loginCookie(t, h, "alice", "wonderland")

	got := getDLQ(t, h, cookie, "since=not-a-timestamp")
	if got.Total != 4 {
		t.Errorf("malformed since should be ignored; total = %d, want 4", got.Total)
	}
}
