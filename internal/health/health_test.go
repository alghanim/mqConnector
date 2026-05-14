package health

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"mqConnector/internal/metrics"
	"mqConnector/internal/storage"
)

func openTestStore(t *testing.T) *storage.Store {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "h.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	s, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCheck_HealthyByDefault(t *testing.T) {
	store := openTestStore(t)
	ms := metrics.New()
	st := NewChecker(store, ms, "1.2.3").Check(context.Background())

	if st.Status != "healthy" {
		t.Errorf("status = %s, want healthy", st.Status)
	}
	if st.Version != "1.2.3" {
		t.Errorf("version = %s, want 1.2.3", st.Version)
	}
	if st.DBStatus != "ok" {
		t.Errorf("db_status = %s, want ok", st.DBStatus)
	}
	if st.Active != 0 {
		t.Errorf("active = %d, want 0", st.Active)
	}
}

func TestCheck_DegradedWhenAnyPipelineError(t *testing.T) {
	store := openTestStore(t)
	ms := metrics.New()
	ms.Register("p1", "src-q", "dst-q")
	ms.SetStatus("p1", "error", "broker unreachable")

	st := NewChecker(store, ms, "v").Check(context.Background())

	if st.Status != "degraded" {
		t.Errorf("status = %s, want degraded", st.Status)
	}
	if st.Active != 1 {
		t.Errorf("active = %d, want 1", st.Active)
	}
	if len(st.Connections) != 1 || st.Connections[0].PipelineID != "p1" {
		t.Errorf("connections = %+v", st.Connections)
	}
}

func TestCheck_UnhealthyWhenDBPingFails(t *testing.T) {
	store := openTestStore(t)
	_ = store.DB.Close() // close behind the checker's back

	st := NewChecker(store, metrics.New(), "v").Check(context.Background())
	if st.Status != "unhealthy" {
		t.Errorf("status = %s, want unhealthy", st.Status)
	}
	if !strings.HasPrefix(st.DBStatus, "error") {
		t.Errorf("db_status = %s, want error prefix", st.DBStatus)
	}
}

func TestCheck_NilStoreSafelyReportsNotConfigured(t *testing.T) {
	st := NewChecker(nil, metrics.New(), "v").Check(context.Background())
	if st.DBStatus != "not configured" {
		t.Errorf("db_status = %s, want \"not configured\"", st.DBStatus)
	}
	if st.Status != "unhealthy" {
		t.Errorf("status = %s, want unhealthy when DB missing", st.Status)
	}
}
