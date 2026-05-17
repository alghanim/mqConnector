package storage

import (
	"context"
	"strings"
	"testing"
)

// TestPlugins_UpsertAndGet — the canonical round-trip. Upsert
// computes the sha256 + size from the blob; Get returns the same
// bytes plus the metadata.
func TestPlugins_UpsertAndGet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	blob := []byte("\x00asm\x01\x00\x00\x00") // WASM magic header
	p := &Plugin{
		TenantID:   DefaultTenantID,
		Name:       "redact-ssn",
		Blob:       blob,
		UploadedBy: "alice",
	}
	if err := s.Plugins.Upsert(ctx, p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if p.SHA256 == "" {
		t.Error("expected sha256 to be populated")
	}
	if p.SizeBytes != len(blob) {
		t.Errorf("size_bytes = %d, want %d", p.SizeBytes, len(blob))
	}

	got, err := s.Plugins.Get(ctx, DefaultTenantID, "redact-ssn")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got.Blob) != string(blob) {
		t.Errorf("blob round-trip mismatch")
	}
	if got.UploadedBy != "alice" {
		t.Errorf("uploaded_by = %q", got.UploadedBy)
	}
}

// TestPlugins_UpsertReplaces — second upload with the same name in
// the same tenant replaces the blob and updates sha256. Idempotent
// re-uploads are the gitops use case.
func TestPlugins_UpsertReplaces(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_ = s.Plugins.Upsert(ctx, &Plugin{
		TenantID: DefaultTenantID, Name: "f", Blob: []byte("v1"),
	})
	v1, _ := s.Plugins.Get(ctx, DefaultTenantID, "f")

	_ = s.Plugins.Upsert(ctx, &Plugin{
		TenantID: DefaultTenantID, Name: "f", Blob: []byte("v2-much-longer"),
	})
	v2, _ := s.Plugins.Get(ctx, DefaultTenantID, "f")

	if v1.SHA256 == v2.SHA256 {
		t.Error("sha256 should change with blob content")
	}
	if string(v2.Blob) != "v2-much-longer" {
		t.Errorf("blob = %q, want v2-much-longer", v2.Blob)
	}
}

// TestPlugins_ListOmitsBlob — the listing endpoint exposes metadata
// only. Blobs can be multi-MiB; List returning them would explode
// admin UI requests.
func TestPlugins_ListOmitsBlob(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.Plugins.Upsert(ctx, &Plugin{
		TenantID: DefaultTenantID, Name: "p", Blob: []byte("xxxxxxxxxx"),
	})
	rows, err := s.Plugins.List(ctx, DefaultTenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if len(rows[0].Blob) != 0 {
		t.Error("List should not include blob bytes")
	}
	if rows[0].SizeBytes == 0 {
		t.Error("size_bytes should be present even when blob is omitted")
	}
}

// TestPlugins_TenantIsolation — two tenants can ship plugins with
// the same name without collision.
func TestPlugins_TenantIsolation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	tA := "11111111-1111-1111-1111-111111111111"
	tB := "22222222-2222-2222-2222-222222222222"
	_ = s.Tenants.Create(ctx, &Tenant{ID: tA, Slug: "a", Name: "A"})
	_ = s.Tenants.Create(ctx, &Tenant{ID: tB, Slug: "b", Name: "B"})

	if err := s.Plugins.Upsert(ctx, &Plugin{TenantID: tA, Name: "common", Blob: []byte("A")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Plugins.Upsert(ctx, &Plugin{TenantID: tB, Name: "common", Blob: []byte("BB")}); err != nil {
		t.Fatal(err)
	}

	gotA, _ := s.Plugins.Get(ctx, tA, "common")
	gotB, _ := s.Plugins.Get(ctx, tB, "common")
	if string(gotA.Blob) != "A" || string(gotB.Blob) != "BB" {
		t.Errorf("tenant isolation broken: A=%q B=%q", gotA.Blob, gotB.Blob)
	}
}

// TestPlugins_DeleteAndNotFound
func TestPlugins_DeleteAndNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_ = s.Plugins.Upsert(ctx, &Plugin{TenantID: DefaultTenantID, Name: "z", Blob: []byte("x")})
	if err := s.Plugins.Delete(ctx, DefaultTenantID, "z"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Plugins.Get(ctx, DefaultTenantID, "z"); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if err := s.Plugins.Delete(ctx, DefaultTenantID, "z"); err != ErrNotFound {
		t.Errorf("delete on already-gone should ErrNotFound, got %v", err)
	}
}

// TestPlugins_RejectsEmpty — uploads must carry a name + a blob.
// Catches a misformed multipart at the storage layer even if the
// handler missed the validation.
func TestPlugins_RejectsEmpty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	cases := []struct {
		p    *Plugin
		want string
	}{
		{&Plugin{TenantID: DefaultTenantID, Name: "", Blob: []byte("x")}, "name"},
		{&Plugin{TenantID: DefaultTenantID, Name: "n", Blob: nil}, "blob"},
	}
	for _, c := range cases {
		err := s.Plugins.Upsert(ctx, c.p)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("expected error containing %q, got %v", c.want, err)
		}
	}
}
