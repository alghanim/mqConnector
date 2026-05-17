package dlq

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mqConnector/internal/storage"
)

func newStore(t *testing.T) *storage.Store {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "ret.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	s, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRetention_AgePruner(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	// Insert ten rows; backdate half of them 2 days.
	for i := 0; i < 10; i++ {
		e := &storage.DLQEntry{OriginalMsg: []byte("x"), ErrorReason: "test"}
		if err := store.DLQ.Insert(ctx, storage.DefaultTenantID, e); err != nil {
			t.Fatal(err)
		}
		if i < 5 {
			_, err := store.DB.Exec(`UPDATE dlq SET created_at = ? WHERE id = ?`,
				time.Now().Add(-48*time.Hour).UTC(), e.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	r := NewRetention(store.DLQ, 24*time.Hour, 0, time.Hour, nil)
	r.sweep(ctx)

	_, total, _ := store.DLQ.List(ctx, storage.DefaultTenantID, 1, 20)
	if total != 5 {
		t.Errorf("expected 5 rows after age-prune, got %d", total)
	}
}

func TestRetention_CountPruner(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		_ = store.DLQ.Insert(ctx, storage.DefaultTenantID, &storage.DLQEntry{OriginalMsg: []byte("x"), ErrorReason: "test"})
		// Tiny sleep so created_at differs and the "newest 8" ordering is
		// deterministic. SQLite's CURRENT_TIMESTAMP has 1s resolution.
		time.Sleep(2 * time.Millisecond)
	}
	r := NewRetention(store.DLQ, 0, 8, time.Hour, nil)
	r.sweep(ctx)

	_, total, _ := store.DLQ.List(ctx, storage.DefaultTenantID, 1, 20)
	if total != 8 {
		t.Errorf("expected 8 rows after count-prune, got %d", total)
	}
}

func TestRetention_BothPolicies(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	// 5 old (>2 days) + 10 new
	for i := 0; i < 5; i++ {
		e := &storage.DLQEntry{OriginalMsg: []byte("old"), ErrorReason: "x"}
		_ = store.DLQ.Insert(ctx, storage.DefaultTenantID, e)
		_, _ = store.DB.Exec(`UPDATE dlq SET created_at = ? WHERE id = ?`,
			time.Now().Add(-72*time.Hour).UTC(), e.ID)
	}
	for i := 0; i < 10; i++ {
		_ = store.DLQ.Insert(ctx, storage.DefaultTenantID, &storage.DLQEntry{OriginalMsg: []byte("new"), ErrorReason: "x"})
		time.Sleep(2 * time.Millisecond)
	}

	r := NewRetention(store.DLQ, 24*time.Hour, 6, time.Hour, nil)
	r.sweep(ctx)

	_, total, _ := store.DLQ.List(ctx, storage.DefaultTenantID, 1, 50)
	if total != 6 {
		t.Errorf("expected 6 rows after both policies, got %d", total)
	}
}

func TestRetention_DisabledIsNoOp(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = store.DLQ.Insert(ctx, storage.DefaultTenantID, &storage.DLQEntry{OriginalMsg: []byte("x"), ErrorReason: "y"})
	}
	r := NewRetention(store.DLQ, 0, 0, time.Hour, nil)
	r.sweep(ctx) // no policies — should leave rows alone.
	_, total, _ := store.DLQ.List(ctx, storage.DefaultTenantID, 1, 50)
	if total != 5 {
		t.Errorf("disabled retention should not prune; got %d remaining", total)
	}
}

// TestRetention_SkipsWhenNotLeader — in multi-replica deploys only
// the leader sweeps. The SetLeaderCheck gate must be honoured BOTH
// for the startup sweep AND the per-tick sweep, otherwise two
// replicas compete on DELETE statements and can over-prune past the
// configured limit.
func TestRetention_SkipsWhenNotLeader(t *testing.T) {
	store := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Seed plenty of rows so a sweep would clearly do something.
	for i := 0; i < 20; i++ {
		_ = store.DLQ.Insert(ctx, storage.DefaultTenantID, &storage.DLQEntry{
			OriginalMsg: []byte("x"), ErrorReason: "test",
		})
	}

	// Configure retention that WOULD prune to 5 rows if it ran.
	r := NewRetention(store.DLQ, 0, 5, 50*time.Millisecond, nil)
	r.SetLeaderCheck(func() bool { return false })

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	// Let several ticks pass — none should run.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	// Use a fresh context for the assertion; the run context is
	// cancelled which would make any List return immediately with
	// zero rows.
	_, total, _ := store.DLQ.List(context.Background(), storage.DefaultTenantID, 1, 50)
	if total != 20 {
		t.Errorf("non-leader ran the sweep: got %d rows, want 20", total)
	}
}

// TestRetention_RunsWhenLeader — sanity check that the gate doesn't
// block the leader.
func TestRetention_RunsWhenLeader(t *testing.T) {
	store := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < 20; i++ {
		_ = store.DLQ.Insert(ctx, storage.DefaultTenantID, &storage.DLQEntry{
			OriginalMsg: []byte("x"), ErrorReason: "test",
		})
		time.Sleep(2 * time.Millisecond) // ensure distinct created_at
	}

	r := NewRetention(store.DLQ, 0, 5, time.Hour, nil)
	r.SetLeaderCheck(func() bool { return true })

	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	// The startup sweep fires immediately; give it a moment to land.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	_, total, _ := store.DLQ.List(context.Background(), storage.DefaultTenantID, 1, 50)
	if total != 5 {
		t.Errorf("leader did not prune: got %d rows, want 5", total)
	}
}

func TestRetention_RunRespectsCancel(t *testing.T) {
	store := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	r := NewRetention(store.DLQ, time.Hour, 100, 10*time.Millisecond, nil)
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after cancel")
	}
}
