package dlq

import (
	"context"
	"testing"
	"time"

	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

func TestBulkDelete_FilterByError(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()
	svc := NewService(store, pool, Options{MaxRetries: 3})
	ctx := context.Background()

	// Push 5 "validate:" entries and 3 "send:" entries.
	for i := 0; i < 5; i++ {
		_ = svc.Push(ctx, storage.DLQEntry{
			SourceQueue: "src", OriginalMsg: []byte("x"),
			ErrorReason: "validate: required field missing",
		})
	}
	for i := 0; i < 3; i++ {
		_ = svc.Push(ctx, storage.DLQEntry{
			SourceQueue: "src", OriginalMsg: []byte("x"),
			ErrorReason: "send: broker down",
		})
	}

	res, err := svc.BulkDelete(ctx, storage.DefaultTenantID,
		storage.DLQFilter{Error: "validate"}, 100)
	if err != nil {
		t.Fatalf("BulkDelete: %v", err)
	}
	if res.Succeeded != 5 {
		t.Fatalf("expected 5 deleted, got %d", res.Succeeded)
	}
	// Remaining should be the 3 send: rows.
	_, total, _ := svc.List(ctx, storage.DefaultTenantID, 1, 50)
	if total != 3 {
		t.Fatalf("expected 3 rows remaining after bulk delete, got %d", total)
	}
}

func TestBulkDelete_RespectsTenantBoundary(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()
	svc := NewService(store, pool, Options{MaxRetries: 3})
	ctx := context.Background()

	other := "other-tenant"
	if err := store.Tenants.Create(ctx, &storage.Tenant{
		ID: other, Slug: "other", Name: "Other", Status: "active",
	}); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	for i := 0; i < 3; i++ {
		_ = svc.Push(ctx, storage.DLQEntry{
			TenantID: storage.DefaultTenantID,
			OriginalMsg: []byte("x"), ErrorReason: "boom",
		})
	}
	for i := 0; i < 2; i++ {
		_ = svc.Push(ctx, storage.DLQEntry{
			TenantID: other,
			OriginalMsg: []byte("y"), ErrorReason: "boom",
		})
	}
	res, err := svc.BulkDelete(ctx, storage.DefaultTenantID,
		storage.DLQFilter{Error: "boom"}, 100)
	if err != nil {
		t.Fatalf("BulkDelete: %v", err)
	}
	if res.Succeeded != 3 {
		t.Fatalf("expected only the default-tenant's 3 rows to delete, got %d", res.Succeeded)
	}
	_, otherTotal, _ := svc.List(ctx, other, 1, 50)
	if otherTotal != 2 {
		t.Fatalf("other tenant's rows must be untouched; got %d", otherTotal)
	}
}

func TestGroupByError_TopBuckets(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()
	svc := NewService(store, pool, Options{MaxRetries: 3})
	ctx := context.Background()

	for i := 0; i < 7; i++ {
		_ = svc.Push(ctx, storage.DLQEntry{
			OriginalMsg: []byte("x"),
			ErrorReason: "validate: required field 'ssn' missing",
		})
	}
	for i := 0; i < 3; i++ {
		_ = svc.Push(ctx, storage.DLQEntry{
			OriginalMsg: []byte("y"),
			ErrorReason: "send: dial tcp 10.0.0.1:5672: connect: connection refused",
		})
	}

	groups, err := svc.GroupByError(ctx, storage.DefaultTenantID, 5)
	if err != nil {
		t.Fatalf("GroupByError: %v", err)
	}
	if len(groups) < 2 {
		t.Fatalf("expected ≥2 buckets, got %d", len(groups))
	}
	// Sorted desc by count; first bucket should be the validate one.
	if groups[0].Count != 7 {
		t.Fatalf("expected first bucket count=7, got %d", groups[0].Count)
	}
	if groups[1].Count != 3 {
		t.Fatalf("expected second bucket count=3, got %d", groups[1].Count)
	}
}

func TestBulkDelete_CapHonoured(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()
	svc := NewService(store, pool, Options{MaxRetries: 3})
	ctx := context.Background()

	for i := 0; i < 25; i++ {
		_ = svc.Push(ctx, storage.DLQEntry{
			OriginalMsg: []byte("x"), ErrorReason: "noisy",
		})
	}
	res, err := svc.BulkDelete(ctx, storage.DefaultTenantID,
		storage.DLQFilter{Error: "noisy"}, 10)
	if err != nil {
		t.Fatalf("BulkDelete: %v", err)
	}
	if res.Succeeded != 10 {
		t.Fatalf("expected cap=10 to bound the delete; got %d", res.Succeeded)
	}
	_, remaining, _ := svc.List(ctx, storage.DefaultTenantID, 1, 50)
	if remaining != 15 {
		t.Fatalf("expected 15 rows still in DLQ; got %d", remaining)
	}
}

func TestIDsByFilter_RespectsTimeWindow(t *testing.T) {
	store := openTestStore(t)
	pool := mq.NewPool(mq.PoolOptions{})
	defer pool.Close()
	svc := NewService(store, pool, Options{MaxRetries: 3})
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_ = svc.Push(ctx, storage.DLQEntry{
			OriginalMsg: []byte("x"), ErrorReason: "boom",
		})
	}

	// "since" in the future should match nothing.
	future := time.Now().UTC().Add(time.Hour)
	ids, err := store.DLQ.IDsByFilter(ctx, storage.DefaultTenantID,
		storage.DLQFilter{Since: &future}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("expected 0 ids for since=future, got %d", len(ids))
	}
}
