package storage

import (
	"context"
	"testing"
	"time"
)

func TestDLQ_ListFiltered_PipelineExactMatch(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Need real pipeline rows so the FK is happy.
	src := &Connection{Name: "src", Type: "rabbitmq"}
	dst := &Connection{Name: "dst", Type: "rabbitmq"}
	_ = s.Connections.Create(ctx, DefaultTenantID, src)
	_ = s.Connections.Create(ctx, DefaultTenantID, dst)
	p1 := &Pipeline{Name: "p1", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	p2 := &Pipeline{Name: "p2", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	_ = s.Pipelines.Create(ctx, DefaultTenantID, p1)
	_ = s.Pipelines.Create(ctx, DefaultTenantID, p2)

	_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{PipelineID: p1.ID, OriginalMsg: []byte("a"), ErrorReason: "x"})
	_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{PipelineID: p1.ID, OriginalMsg: []byte("b"), ErrorReason: "y"})
	_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{PipelineID: p2.ID, OriginalMsg: []byte("c"), ErrorReason: "z"})

	list, total, err := s.DLQ.ListFiltered(ctx, DefaultTenantID, DLQFilter{PipelineID: p1.ID}, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(list) != 2 {
		t.Errorf("expected 2 entries for p1, got total=%d len=%d", total, len(list))
	}
	for _, e := range list {
		if e.PipelineID != p1.ID {
			t.Errorf("unexpected pipeline %s", e.PipelineID)
		}
	}
}

func TestDLQ_ListFiltered_ErrorContainsCaseInsensitive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{OriginalMsg: []byte("a"), ErrorReason: "Connection refused"})
	_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{OriginalMsg: []byte("b"), ErrorReason: "validation failed"})
	_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{OriginalMsg: []byte("c"), ErrorReason: "TLS handshake"})

	list, total, _ := s.DLQ.ListFiltered(ctx, DefaultTenantID, DLQFilter{Error: "connection"}, 1, 10)
	if total != 1 || list[0].ErrorReason != "Connection refused" {
		t.Errorf("case-insensitive contains failed: total=%d items=%+v", total, list)
	}

	list, total, _ = s.DLQ.ListFiltered(ctx, DefaultTenantID, DLQFilter{Error: "FAILED"}, 1, 10)
	if total != 1 {
		t.Errorf("expected 1 match for FAILED, got %d", total)
	}
}

func TestDLQ_ListFiltered_TimeRange(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	old := &DLQEntry{OriginalMsg: []byte("old"), ErrorReason: "x"}
	_ = s.DLQ.Insert(ctx, DefaultTenantID, old)
	_, _ = s.DB.ExecContext(ctx, `UPDATE dlq SET created_at = ? WHERE id = ?`,
		time.Now().Add(-2*time.Hour).UTC(), old.ID)

	_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{OriginalMsg: []byte("recent"), ErrorReason: "y"})

	cutoff := time.Now().Add(-time.Hour).UTC()
	list, total, _ := s.DLQ.ListFiltered(ctx, DefaultTenantID, DLQFilter{Since: &cutoff}, 1, 10)
	if total != 1 {
		t.Errorf("Since cutoff: expected 1, got %d", total)
	}
	if len(list) == 1 && string(list[0].OriginalMsg) != "recent" {
		t.Errorf("wrong entry: %s", list[0].OriginalMsg)
	}
}

func TestDLQ_ListFiltered_CombinedFilters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	src := &Connection{Name: "src", Type: "rabbitmq"}
	dst := &Connection{Name: "dst", Type: "rabbitmq"}
	_ = s.Connections.Create(ctx, DefaultTenantID, src)
	_ = s.Connections.Create(ctx, DefaultTenantID, dst)
	p := &Pipeline{Name: "p", SourceID: src.ID, DestinationID: dst.ID, Enabled: true}
	_ = s.Pipelines.Create(ctx, DefaultTenantID, p)

	_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{PipelineID: p.ID, OriginalMsg: []byte("a"), ErrorReason: "validation: bad type"})
	_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{PipelineID: p.ID, OriginalMsg: []byte("b"), ErrorReason: "connection reset"})
	_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{OriginalMsg: []byte("c"), ErrorReason: "validation: missing field"}) // no pipeline

	list, total, _ := s.DLQ.ListFiltered(ctx, DefaultTenantID, DLQFilter{
		PipelineID: p.ID,
		Error:      "validation",
	}, 1, 10)
	if total != 1 || string(list[0].OriginalMsg) != "a" {
		t.Errorf("combined filter failed: total=%d items=%+v", total, list)
	}
}

func TestDLQ_ListFiltered_PaginationCountReflectsFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{OriginalMsg: []byte("matching"), ErrorReason: "boom"})
	}
	for i := 0; i < 7; i++ {
		_ = s.DLQ.Insert(ctx, DefaultTenantID, &DLQEntry{OriginalMsg: []byte("other"), ErrorReason: "quiet"})
	}

	list, total, _ := s.DLQ.ListFiltered(ctx, DefaultTenantID, DLQFilter{Error: "boom"}, 1, 3)
	if total != 5 {
		t.Errorf("filtered total should be 5, got %d", total)
	}
	if len(list) != 3 {
		t.Errorf("per_page=3 should return 3 rows, got %d", len(list))
	}
}
