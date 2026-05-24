package pipeline

import (
	"context"
	"testing"
	"time"
)

// TestTenantBudget_SharedAcrossPipelines proves the manager hands out
// the SAME budget instance for two pipelines belonging to the same
// tenant — so the tenant cap is genuinely aggregate, not duplicated
// per pipeline.
func TestTenantBudget_SharedAcrossPipelines(t *testing.T) {
	m := &Manager{}
	tenantA := "tenant-a"
	tenantB := "tenant-b"

	a1 := m.tenantBudgetFor(tenantA, 100)
	a2 := m.tenantBudgetFor(tenantA, 100)
	if a1 == nil || a2 == nil {
		t.Fatalf("budget should not be nil for non-zero cap")
	}
	if a1 != a2 {
		t.Fatalf("manager must return the same budget instance for the same tenant")
	}
	b1 := m.tenantBudgetFor(tenantB, 100)
	if b1 == a1 {
		t.Fatalf("different tenants must get different budget instances")
	}
}

func TestTenantBudget_ZeroIsNil(t *testing.T) {
	m := &Manager{}
	if m.tenantBudgetFor("t", 0) != nil {
		t.Fatalf("zero cap must return nil (no budget)")
	}
}

// TestTenantBudget_BlocksWhenExhausted proves the budget actually
// throttles. With limit=1/window=200ms, two consecutive takes from
// the SAME budget take ≥ 200ms (the second must wait for the window
// to roll).
func TestTenantBudget_BlocksWhenExhausted(t *testing.T) {
	b := newBudget(1, 200*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	if err := b.take(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.take(ctx); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 150*time.Millisecond {
		t.Fatalf("second take should have blocked; elapsed=%v", elapsed)
	}
}
