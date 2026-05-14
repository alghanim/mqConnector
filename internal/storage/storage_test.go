package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dsn := "file:" + filepath.Join(dir, "test.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	s, err := Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestStore_OpenMigrates(t *testing.T) {
	s := openTestStore(t)
	// schema_migrations should exist
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("schema_migrations not present: %v", err)
	}
	if n < 1 {
		t.Error("expected at least one migration applied")
	}
}

func TestConnections_CRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	c := &Connection{Name: "ibm-prod", Type: "ibm", QueueManager: "QM1"}
	if err := s.Connections.Create(ctx, c); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.ID == "" {
		t.Fatal("Create should assign ID")
	}

	got, err := s.Connections.Get(ctx, c.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "ibm-prod" {
		t.Errorf("Name mismatch: %s", got.Name)
	}

	got.Name = "renamed"
	if err := s.Connections.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}

	all, err := s.Connections.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].Name != "renamed" {
		t.Errorf("expected one renamed connection, got %#v", all)
	}

	if err := s.Connections.Delete(ctx, c.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Connections.Get(ctx, c.ID); err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestPipelines_CRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	src := &Connection{Name: "src", Type: "rabbitmq"}
	dst := &Connection{Name: "dst", Type: "kafka"}
	_ = s.Connections.Create(ctx, src)
	_ = s.Connections.Create(ctx, dst)

	p := &Pipeline{
		Name:          "p1",
		SourceID:      src.ID,
		DestinationID: dst.ID,
		FilterPaths:   []string{"a.b", "c"},
		Enabled:       true,
	}
	if err := s.Pipelines.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.Pipelines.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.FilterPaths) != 2 || got.FilterPaths[0] != "a.b" {
		t.Errorf("FilterPaths roundtrip failed: %v", got.FilterPaths)
	}

	// Replace stages
	stages := []*Stage{
		{StageOrder: 1, StageType: "filter", StageConfig: `{}`, Enabled: true},
		{StageOrder: 2, StageType: "transform", StageConfig: `{}`, Enabled: true},
	}
	if err := s.Stages.ReplaceForPipeline(ctx, p.ID, stages); err != nil {
		t.Fatalf("ReplaceStages: %v", err)
	}
	listed, err := s.Stages.ListByPipeline(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListByPipeline: %v", err)
	}
	if len(listed) != 2 {
		t.Errorf("expected 2 stages, got %d", len(listed))
	}
}

func TestDLQ_InsertAndIncrementRetry(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	e := &DLQEntry{
		SourceQueue: "QUEUE.1",
		OriginalMsg: []byte("payload"),
		ErrorReason: "validation failed",
	}
	if err := s.DLQ.Insert(ctx, e); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := s.DLQ.IncrementRetry(ctx, e.ID); err != nil {
		t.Fatalf("IncrementRetry: %v", err)
	}
	got, err := s.DLQ.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RetryCount != 1 {
		t.Errorf("expected retry count 1, got %d", got.RetryCount)
	}
	if got.LastRetryAt == nil {
		t.Error("LastRetryAt should be set")
	}
}
