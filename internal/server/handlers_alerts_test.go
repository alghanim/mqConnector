package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"mqConnector/internal/slo"
)

// stubMetricsSrc lets the test drive slo.Evaluator without a live
// metrics.Store.
type stubMetricsSrc struct {
	samples []slo.Sample
}

func (s *stubMetricsSrc) Snapshot() []slo.Sample { return s.samples }
func (s *stubMetricsSrc) ValueAt(_ string, _ map[string]string, _ time.Duration) (float64, bool) {
	return 0, false
}

func newEvaluatorForTest(t *testing.T) *slo.Evaluator {
	t.Helper()
	rules := []slo.Rule{
		{
			Name:        "PipelineDown",
			Expr:        "mqconnector_pipeline_up == 0",
			Labels:      map[string]string{"severity": "critical"},
			Annotations: map[string]string{"summary": "pipeline down"},
			Group:       "test",
		},
		{
			Name:   "FailRateHigh",
			Expr:   "mqconnector_messages_failed_total > 5",
			Labels: map[string]string{"severity": "warning"},
			Group:  "test",
		},
	}
	src := &stubMetricsSrc{
		samples: []slo.Sample{
			{Labels: map[string]string{"__name__": "mqconnector_pipeline_up", "pipeline_id": "p1"}, Value: 0},
			{Labels: map[string]string{"__name__": "mqconnector_messages_failed_total", "pipeline_id": "p2"}, Value: 10},
		},
	}
	e := slo.NewEvaluator(rules, nil, src, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	e.Clock = func() time.Time { return time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC) }
	// One synthetic tick to populate firing alerts (zero For:).
	// The Run loop is not started — we call the unexported tick via
	// the public Snapshot path: Snapshot reads from firing, which is
	// populated by tick. NewEvaluator doesn't tick automatically;
	// hijack via Run(ctx) and cancel quickly, OR provide a deterministic
	// hook. Use the public testable path: invoke Run briefly.
	e.Interval = 5 * time.Millisecond
	// Forcing a single tick: spin Run for 50ms then drop the
	// goroutine reference (we don't cancel — Snapshot is immutable
	// after the first tick).
	go e.Run(testContextWithCancel(t, 50*time.Millisecond))
	time.Sleep(30 * time.Millisecond)
	return e
}

func TestHandleListActiveAlerts(t *testing.T) {
	h, srv, _ := newTestServer(t)
	srv.sloEvaluator = newEvaluatorForTest(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/active", nil)
	attachSession(req, cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got alertsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.EvaluatorEnabled {
		t.Errorf("evaluator_enabled = false (want true)")
	}
	if got.Total < 2 {
		t.Errorf("total = %d, want ≥ 2 (got=%+v)", got.Total, got.Alerts)
	}
	// Severity sort: critical first.
	if got.Alerts[0].Severity != "critical" {
		t.Errorf("first severity = %q, want critical", got.Alerts[0].Severity)
	}
}

func TestHandleListActiveAlerts_SeverityFilter(t *testing.T) {
	h, srv, _ := newTestServer(t)
	srv.sloEvaluator = newEvaluatorForTest(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/active?severity=warning", nil)
	attachSession(req, cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got alertsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, a := range got.Alerts {
		if a.Severity != "warning" {
			t.Errorf("got severity %q after warning filter", a.Severity)
		}
	}
}

func TestHandleListActiveAlerts_PipelineFilter(t *testing.T) {
	h, srv, _ := newTestServer(t)
	srv.sloEvaluator = newEvaluatorForTest(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/active?pipeline=p1", nil)
	attachSession(req, cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got alertsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, a := range got.Alerts {
		if a.Labels["pipeline_id"] != "p1" {
			t.Errorf("got pipeline_id %q after p1 filter", a.Labels["pipeline_id"])
		}
	}
}

func TestHandleListActiveAlerts_NoEvaluator(t *testing.T) {
	h, srv, _ := newTestServer(t)
	srv.sloEvaluator = nil // simulate slo disabled at boot
	cookie := loginCookie(t, h, "alice", "wonderland")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/alerts/active", nil)
	attachSession(req, cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got alertsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.EvaluatorEnabled {
		t.Errorf("evaluator_enabled = true with nil evaluator")
	}
	if got.Total != 0 {
		t.Errorf("total = %d (want 0)", got.Total)
	}
}

// testContextWithCancel returns a context that cancels after d. Used
// by the alerts-handler test to drive a brief Run loop on the SLO
// evaluator without spinning forever.
func testContextWithCancel(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
