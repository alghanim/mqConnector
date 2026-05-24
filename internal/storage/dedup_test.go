package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func openDedupTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "dedup-test.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	s, err := Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedPipelineForDedup(t *testing.T, store *Store) string {
	t.Helper()
	ctx := context.Background()
	src := &Connection{Name: "s", Type: "rabbitmq", URL: "amqp://localhost"}
	if err := store.Connections.Create(ctx, DefaultTenantID, src); err != nil {
		t.Fatalf("create src: %v", err)
	}
	dst := &Connection{Name: "d", Type: "rabbitmq", URL: "amqp://localhost"}
	if err := store.Connections.Create(ctx, DefaultTenantID, dst); err != nil {
		t.Fatalf("create dst: %v", err)
	}
	p := &Pipeline{
		ID: uuid.NewString(), Name: "p",
		SourceID: src.ID, DestinationID: dst.ID, Enabled: true,
		DedupWindowSeconds: 60,
	}
	if err := store.Pipelines.Create(ctx, DefaultTenantID, p); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	return p.ID
}

func TestDedup_FreshSighting_NotADupe(t *testing.T) {
	store := openDedupTestStore(t)
	pid := seedPipelineForDedup(t, store)
	dupe, err := store.Dedup.CheckAndRecord(context.Background(), pid, "abc123", 60)
	if err != nil {
		t.Fatalf("CheckAndRecord: %v", err)
	}
	if dupe {
		t.Fatalf("first sighting should NOT be a dupe")
	}
}

func TestDedup_SecondSighting_IsADupe(t *testing.T) {
	store := openDedupTestStore(t)
	pid := seedPipelineForDedup(t, store)
	ctx := context.Background()
	if _, err := store.Dedup.CheckAndRecord(ctx, pid, "h", 60); err != nil {
		t.Fatal(err)
	}
	dupe, err := store.Dedup.CheckAndRecord(ctx, pid, "h", 60)
	if err != nil {
		t.Fatalf("CheckAndRecord: %v", err)
	}
	if !dupe {
		t.Fatalf("second in-window sighting should be a dupe")
	}
	n, _ := store.Dedup.CountForPipeline(ctx, pid)
	if n != 1 {
		t.Fatalf("expected one row, got %d", n)
	}
}

func TestDedup_DifferentHash_NotADupe(t *testing.T) {
	store := openDedupTestStore(t)
	pid := seedPipelineForDedup(t, store)
	ctx := context.Background()
	_, _ = store.Dedup.CheckAndRecord(ctx, pid, "h1", 60)
	dupe, _ := store.Dedup.CheckAndRecord(ctx, pid, "h2", 60)
	if dupe {
		t.Fatalf("different hash must not be a dupe")
	}
}

func TestDedup_DifferentPipeline_IndependentSpaces(t *testing.T) {
	store := openDedupTestStore(t)
	p1 := seedPipelineForDedup(t, store)
	p2 := seedPipelineForDedup(t, store)
	ctx := context.Background()
	_, _ = store.Dedup.CheckAndRecord(ctx, p1, "shared", 60)
	dupe, _ := store.Dedup.CheckAndRecord(ctx, p2, "shared", 60)
	if dupe {
		t.Fatalf("same hash on different pipeline must not be a dupe")
	}
}

func TestDedup_WindowDisabled_NeverDupe(t *testing.T) {
	store := openDedupTestStore(t)
	pid := seedPipelineForDedup(t, store)
	ctx := context.Background()
	_, _ = store.Dedup.CheckAndRecord(ctx, pid, "h", 0)
	dupe, err := store.Dedup.CheckAndRecord(ctx, pid, "h", 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if dupe {
		t.Fatalf("window=0 must never report a dupe")
	}
	n, _ := store.Dedup.CountForPipeline(ctx, pid)
	if n != 0 {
		t.Fatalf("window=0 must not persist any rows; got %d", n)
	}
}

func TestDedup_ExpiredEntry_TreatedAsFresh(t *testing.T) {
	// Manually backdate the entry to simulate the row outliving its
	// window. CheckAndRecord with a small window must see it as fresh.
	store := openDedupTestStore(t)
	pid := seedPipelineForDedup(t, store)
	ctx := context.Background()
	if _, err := store.Dedup.CheckAndRecord(ctx, pid, "h", 3600); err != nil {
		t.Fatal(err)
	}
	// Force the row to look 5s old.
	if _, err := store.DB.ExecContext(ctx,
		`UPDATE pipeline_dedup SET last_seen_at = ? WHERE pipeline_id = ? AND payload_hash = ?`,
		time.Now().UTC().Add(-5*time.Second), pid, "h"); err != nil {
		t.Fatal(err)
	}
	// Window of 1s should treat the 5s-old row as expired → fresh insert.
	dupe, err := store.Dedup.CheckAndRecord(ctx, pid, "h", 1)
	if err != nil {
		t.Fatalf("CheckAndRecord: %v", err)
	}
	if dupe {
		t.Fatalf("entry past its window must be treated as fresh, got dupe=true")
	}
}

func TestDedup_Prune_RemovesOldRows(t *testing.T) {
	store := openDedupTestStore(t)
	pid := seedPipelineForDedup(t, store)
	ctx := context.Background()
	_, _ = store.Dedup.CheckAndRecord(ctx, pid, "old", 3600)
	_, _ = store.Dedup.CheckAndRecord(ctx, pid, "new", 3600)
	// Backdate one row.
	_, _ = store.DB.ExecContext(ctx,
		`UPDATE pipeline_dedup SET last_seen_at = ? WHERE payload_hash = ?`,
		time.Now().UTC().Add(-time.Hour), "old")

	n, err := store.Dedup.Prune(ctx, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected to prune 1 row, got %d", n)
	}
	total, _ := store.Dedup.CountForPipeline(ctx, pid)
	if total != 1 {
		t.Fatalf("expected 1 row remaining, got %d", total)
	}
}

func TestDedup_DeletePipeline_CascadeOnPipelineDrop(t *testing.T) {
	// FK cascade: dropping the pipeline must drop its dedup rows.
	store := openDedupTestStore(t)
	pid := seedPipelineForDedup(t, store)
	ctx := context.Background()
	_, _ = store.Dedup.CheckAndRecord(ctx, pid, "h", 60)

	if err := store.Pipelines.Delete(ctx, DefaultTenantID, pid); err != nil {
		t.Fatalf("delete pipeline: %v", err)
	}
	n, _ := store.Dedup.CountForPipeline(ctx, pid)
	if n != 0 {
		t.Fatalf("expected cascade to delete dedup rows, %d remain", n)
	}
}
