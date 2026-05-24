package storage

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// seedPipelineForRevisions creates a tenant-scoped pipeline plus the two
// connections it needs as source/destination. Returns the pipeline ID so
// tests can attach revisions to a known pipeline_id without re-deriving
// the FK each call.
func seedPipelineForRevisions(t *testing.T, s *Store, tenantID, name string) string {
	t.Helper()
	ctx := context.Background()
	src := &Connection{Name: name + "-src", Type: "rabbitmq"}
	if err := s.Connections.Create(ctx, tenantID, src); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	dst := &Connection{Name: name + "-dst", Type: "kafka"}
	if err := s.Connections.Create(ctx, tenantID, dst); err != nil {
		t.Fatalf("seed dst: %v", err)
	}
	p := &Pipeline{
		Name:          name,
		SourceID:      src.ID,
		DestinationID: dst.ID,
		Enabled:       true,
	}
	if err := s.Pipelines.Create(ctx, tenantID, p); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	return p.ID
}

// seedTenant inserts a tenant row and returns its id. Tests that need a
// distinct second tenant for isolation checks call this.
func seedTenant(t *testing.T, s *Store, slug string) string {
	t.Helper()
	ctx := context.Background()
	tn := &Tenant{Slug: slug, Name: slug}
	if err := s.Tenants.Create(ctx, tn); err != nil {
		t.Fatalf("seed tenant %s: %v", slug, err)
	}
	return tn.ID
}

func TestPipelineRevisions_CreateAssignsRevisionOne(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pid := seedPipelineForRevisions(t, s, DefaultTenantID, "rev-create")

	rev := &PipelineRevision{
		PipelineID:     pid,
		Snapshot:       `{"snapshot_schema_version":1}`,
		SnapshotHash:   "hash-1",
		AuthorSub:      "alice-sub",
		AuthorUsername: "alice",
		ChangeSummary:  "initial",
	}
	if err := s.PipelineRevisions.Create(ctx, DefaultTenantID, rev); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rev.ID == "" {
		t.Error("Create must assign an ID")
	}
	if rev.RevisionNumber != 1 {
		t.Errorf("expected revision_number=1, got %d", rev.RevisionNumber)
	}
	if rev.CreatedAt.IsZero() {
		t.Error("CreatedAt should be populated by Create")
	}
	if rev.TenantID != DefaultTenantID {
		t.Errorf("Create must stamp TenantID, got %q", rev.TenantID)
	}
}

func TestPipelineRevisions_HashDedup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pid := seedPipelineForRevisions(t, s, DefaultTenantID, "rev-dedup")

	first := &PipelineRevision{
		PipelineID:   pid,
		Snapshot:     `{"v":1}`,
		SnapshotHash: "same-hash",
		AuthorSub:    "alice",
	}
	if err := s.PipelineRevisions.Create(ctx, DefaultTenantID, first); err != nil {
		t.Fatalf("Create first: %v", err)
	}
	firstID := first.ID
	if first.RevisionNumber != 1 {
		t.Fatalf("first should be rev 1, got %d", first.RevisionNumber)
	}

	second := &PipelineRevision{
		PipelineID:   pid,
		Snapshot:     `{"v":1}`,
		SnapshotHash: "same-hash",
		AuthorSub:    "bob",
	}
	if err := s.PipelineRevisions.Create(ctx, DefaultTenantID, second); err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if second.ID != firstID {
		t.Errorf("dedup should return existing ID %q, got %q", firstID, second.ID)
	}
	if second.RevisionNumber != 1 {
		t.Errorf("dedup should NOT increment revision_number, got %d", second.RevisionNumber)
	}

	// Confirm exactly one row in the DB.
	list, total, err := s.PipelineRevisions.List(ctx, DefaultTenantID, pid, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(list) != 1 {
		t.Errorf("expected exactly 1 row after dedup, got total=%d len=%d", total, len(list))
	}
}

func TestPipelineRevisions_ConcurrentCreateMonotonic(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pid := seedPipelineForRevisions(t, s, DefaultTenantID, "rev-concurrent")

	const N = 16
	var wg sync.WaitGroup
	errs := make([]error, N)
	revNums := make([]int, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rev := &PipelineRevision{
				PipelineID:   pid,
				Snapshot:     `{"i":` + uuid.NewString() + `}`,
				SnapshotHash: uuid.NewString(), // distinct hash → real insert
				AuthorSub:    "writer",
			}
			if err := s.PipelineRevisions.Create(ctx, DefaultTenantID, rev); err != nil {
				errs[idx] = err
				return
			}
			revNums[idx] = rev.RevisionNumber
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d Create: %v", i, err)
		}
	}
	got := append([]int(nil), revNums...)
	sort.Ints(got)
	for i := 0; i < N; i++ {
		if got[i] != i+1 {
			t.Fatalf("expected revision_numbers {1..%d} with no gaps, got %v", N, got)
		}
	}
}

func TestPipelineRevisions_ListPaginationAndOrder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pid := seedPipelineForRevisions(t, s, DefaultTenantID, "rev-list")

	for i := 0; i < 5; i++ {
		rev := &PipelineRevision{
			PipelineID:   pid,
			Snapshot:     `{}`,
			SnapshotHash: uuid.NewString(),
			AuthorSub:    "x",
		}
		if err := s.PipelineRevisions.Create(ctx, DefaultTenantID, rev); err != nil {
			t.Fatalf("seed rev %d: %v", i, err)
		}
	}

	page1, total, err := s.PipelineRevisions.List(ctx, DefaultTenantID, pid, 2, 0)
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if total != 5 {
		t.Errorf("expected total=5, got %d", total)
	}
	if len(page1) != 2 {
		t.Fatalf("expected 2 rows on page1, got %d", len(page1))
	}
	if page1[0].RevisionNumber != 5 || page1[1].RevisionNumber != 4 {
		t.Errorf("expected newest-first 5,4 — got %d,%d", page1[0].RevisionNumber, page1[1].RevisionNumber)
	}

	page3, _, err := s.PipelineRevisions.List(ctx, DefaultTenantID, pid, 2, 4)
	if err != nil {
		t.Fatalf("List page3: %v", err)
	}
	if len(page3) != 1 || page3[0].RevisionNumber != 1 {
		t.Errorf("expected final page = [1], got %#v", page3)
	}
}

func TestPipelineRevisions_GetAndNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pid := seedPipelineForRevisions(t, s, DefaultTenantID, "rev-get")

	rev := &PipelineRevision{
		PipelineID:   pid,
		Snapshot:     `{"x":1}`,
		SnapshotHash: "h1",
		AuthorSub:    "alice",
	}
	if err := s.PipelineRevisions.Create(ctx, DefaultTenantID, rev); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := s.PipelineRevisions.Get(ctx, DefaultTenantID, pid, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != rev.ID || got.Snapshot != `{"x":1}` {
		t.Errorf("Get returned wrong row: %#v", got)
	}

	_, err = s.PipelineRevisions.Get(ctx, DefaultTenantID, pid, 99)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for unknown revision, got %v", err)
	}
}

func TestPipelineRevisions_LatestIgnoresDeployState(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pid := seedPipelineForRevisions(t, s, DefaultTenantID, "rev-latest")

	for i := 0; i < 3; i++ {
		rev := &PipelineRevision{
			PipelineID:   pid,
			Snapshot:     `{}`,
			SnapshotHash: uuid.NewString(),
			AuthorSub:    "a",
		}
		if err := s.PipelineRevisions.Create(ctx, DefaultTenantID, rev); err != nil {
			t.Fatalf("seed rev %d: %v", i, err)
		}
	}

	// Deploy rev 2 — Latest should still report rev 3.
	if err := s.PipelineRevisions.MarkDeployed(ctx, DefaultTenantID, pid, 2, "req-mid"); err != nil {
		t.Fatalf("MarkDeployed: %v", err)
	}

	latest, err := s.PipelineRevisions.Latest(ctx, DefaultTenantID, pid)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.RevisionNumber != 3 {
		t.Errorf("expected Latest=3 (regardless of deploy state), got %d", latest.RevisionNumber)
	}
	if latest.DeployedAt != nil {
		t.Errorf("rev 3 was never deployed, deployed_at should be nil")
	}
}

func TestPipelineRevisions_LatestDeployedIgnoresUndeployed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pid := seedPipelineForRevisions(t, s, DefaultTenantID, "rev-latestdep")

	for i := 0; i < 3; i++ {
		rev := &PipelineRevision{
			PipelineID:   pid,
			Snapshot:     `{}`,
			SnapshotHash: uuid.NewString(),
			AuthorSub:    "a",
		}
		if err := s.PipelineRevisions.Create(ctx, DefaultTenantID, rev); err != nil {
			t.Fatalf("seed rev %d: %v", i, err)
		}
	}

	// Nothing deployed → ErrNotFound.
	if _, err := s.PipelineRevisions.LatestDeployed(ctx, DefaultTenantID, pid); err != ErrNotFound {
		t.Errorf("expected ErrNotFound when nothing deployed, got %v", err)
	}

	if err := s.PipelineRevisions.MarkDeployed(ctx, DefaultTenantID, pid, 2, "req-2"); err != nil {
		t.Fatalf("MarkDeployed rev2: %v", err)
	}

	latest, err := s.PipelineRevisions.LatestDeployed(ctx, DefaultTenantID, pid)
	if err != nil {
		t.Fatalf("LatestDeployed: %v", err)
	}
	if latest.RevisionNumber != 2 {
		t.Errorf("expected LatestDeployed=2, got %d", latest.RevisionNumber)
	}
	if latest.DeployedAt == nil {
		t.Errorf("LatestDeployed must have DeployedAt set")
	}
}

func TestPipelineRevisions_MarkDeployedIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pid := seedPipelineForRevisions(t, s, DefaultTenantID, "rev-markdep")

	rev := &PipelineRevision{
		PipelineID:   pid,
		Snapshot:     `{}`,
		SnapshotHash: "h1",
		AuthorSub:    "a",
	}
	if err := s.PipelineRevisions.Create(ctx, DefaultTenantID, rev); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := s.PipelineRevisions.MarkDeployed(ctx, DefaultTenantID, pid, 1, "req-first"); err != nil {
		t.Fatalf("MarkDeployed first: %v", err)
	}
	firstGot, err := s.PipelineRevisions.Get(ctx, DefaultTenantID, pid, 1)
	if err != nil {
		t.Fatalf("Get after first deploy: %v", err)
	}
	if firstGot.DeployedAt == nil {
		t.Fatal("DeployedAt should be set after first MarkDeployed")
	}
	if firstGot.DeployRequestID != "req-first" {
		t.Errorf("expected deploy_request_id=req-first, got %q", firstGot.DeployRequestID)
	}
	firstDeployedAt := *firstGot.DeployedAt

	// Second call must not shift deployed_at; with non-empty existing
	// request id, the new one must NOT overwrite it.
	if err := s.PipelineRevisions.MarkDeployed(ctx, DefaultTenantID, pid, 1, "req-second"); err != nil {
		t.Fatalf("MarkDeployed second: %v", err)
	}
	secondGot, err := s.PipelineRevisions.Get(ctx, DefaultTenantID, pid, 1)
	if err != nil {
		t.Fatalf("Get after second deploy: %v", err)
	}
	if secondGot.DeployedAt == nil || !secondGot.DeployedAt.Equal(firstDeployedAt) {
		t.Errorf("deployed_at must not shift on idempotent re-mark (was %v, now %v)",
			firstDeployedAt, secondGot.DeployedAt)
	}
	if secondGot.DeployRequestID != "req-first" {
		t.Errorf("non-empty deploy_request_id must not be overwritten, got %q", secondGot.DeployRequestID)
	}

	// MarkDeployed on unknown revision → error.
	if err := s.PipelineRevisions.MarkDeployed(ctx, DefaultTenantID, pid, 999, "x"); err == nil {
		t.Error("expected error when marking nonexistent revision deployed")
	}
}

func TestPipelineRevisions_MarkDeployedFillsEmptyRequestID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pid := seedPipelineForRevisions(t, s, DefaultTenantID, "rev-fillreq")

	rev := &PipelineRevision{
		PipelineID:   pid,
		Snapshot:     `{}`,
		SnapshotHash: "h1",
		AuthorSub:    "a",
	}
	if err := s.PipelineRevisions.Create(ctx, DefaultTenantID, rev); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// First deploy with empty request id.
	if err := s.PipelineRevisions.MarkDeployed(ctx, DefaultTenantID, pid, 1, ""); err != nil {
		t.Fatalf("MarkDeployed empty: %v", err)
	}
	g1, _ := s.PipelineRevisions.Get(ctx, DefaultTenantID, pid, 1)
	d1 := *g1.DeployedAt

	// Second call: deploy_request_id should now be filled in even though
	// deployed_at is preserved (the idempotency contract).
	if err := s.PipelineRevisions.MarkDeployed(ctx, DefaultTenantID, pid, 1, "req-late"); err != nil {
		t.Fatalf("MarkDeployed fill: %v", err)
	}
	g2, _ := s.PipelineRevisions.Get(ctx, DefaultTenantID, pid, 1)
	if g2.DeployRequestID != "req-late" {
		t.Errorf("empty deploy_request_id should be filled, got %q", g2.DeployRequestID)
	}
	if !g2.DeployedAt.Equal(d1) {
		t.Errorf("deployed_at must be preserved on fill, was %v now %v", d1, g2.DeployedAt)
	}
}

func TestPipelineRevisions_TenantIsolation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	otherTenant := seedTenant(t, s, "tenant-b")

	pidA := seedPipelineForRevisions(t, s, DefaultTenantID, "rev-iso-a")
	pidB := seedPipelineForRevisions(t, s, otherTenant, "rev-iso-b")

	revA := &PipelineRevision{
		PipelineID:   pidA,
		Snapshot:     `{"a":1}`,
		SnapshotHash: "ha",
		AuthorSub:    "alice",
	}
	if err := s.PipelineRevisions.Create(ctx, DefaultTenantID, revA); err != nil {
		t.Fatalf("Create A: %v", err)
	}

	// Tenant B must not see tenant A's revision via any read path.
	if _, err := s.PipelineRevisions.Get(ctx, otherTenant, pidA, 1); err != ErrNotFound {
		t.Errorf("cross-tenant Get must return ErrNotFound, got %v", err)
	}
	if _, err := s.PipelineRevisions.Latest(ctx, otherTenant, pidA); err != ErrNotFound {
		t.Errorf("cross-tenant Latest must return ErrNotFound, got %v", err)
	}
	list, total, err := s.PipelineRevisions.List(ctx, otherTenant, pidA, 10, 0)
	if err != nil {
		t.Fatalf("cross-tenant List: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Errorf("cross-tenant List must be empty, got total=%d len=%d", total, len(list))
	}

	// Cross-tenant MarkDeployed must fail.
	if err := s.PipelineRevisions.MarkDeployed(ctx, otherTenant, pidA, 1, "x"); err == nil {
		t.Error("cross-tenant MarkDeployed must error")
	}

	// Tenant B's own pipeline keeps revision numbers independent.
	revB := &PipelineRevision{
		PipelineID:   pidB,
		Snapshot:     `{"b":1}`,
		SnapshotHash: "hb",
		AuthorSub:    "bob",
	}
	if err := s.PipelineRevisions.Create(ctx, otherTenant, revB); err != nil {
		t.Fatalf("Create B: %v", err)
	}
	if revB.RevisionNumber != 1 {
		t.Errorf("tenant B's first rev should be 1, got %d", revB.RevisionNumber)
	}
}
