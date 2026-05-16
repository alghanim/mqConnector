package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestGenerateSecret_Shape confirms the secret follows the documented
// format and the returned prefix matches the first 8 chars of the
// suffix portion (used as the row's `prefix` for UI display).
func TestGenerateSecret_Shape(t *testing.T) {
	s, prefix, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, TokenSecretPrefix) {
		t.Errorf("secret should start with %q, got %q", TokenSecretPrefix, s)
	}
	if len(prefix) != 8 {
		t.Errorf("prefix should be 8 chars, got %d", len(prefix))
	}
	if !strings.Contains(s, prefix) {
		t.Errorf("secret %q should contain prefix %q", s, prefix)
	}
	// Two secrets should be distinct — random, not fixtures.
	other, _, _ := GenerateSecret()
	if s == other {
		t.Error("two consecutive secrets should not collide")
	}
}

// TestTokens_LookupRoundTrip exercises the issue-then-authenticate flow.
func TestTokens_LookupRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	secret, prefix, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	tok := &APIToken{
		TenantID: DefaultTenantID,
		UserSub:  "user-1",
		Name:     "ci-pipeline",
		Prefix:   prefix,
		Role:     "operator",
	}
	if err := s.APITokens.Create(ctx, tok, secret); err != nil {
		t.Fatalf("create: %v", err)
	}
	if tok.ID == "" {
		t.Fatal("token id not populated after Create")
	}

	got, err := s.APITokens.Lookup(ctx, secret)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.ID != tok.ID || got.Role != "operator" || got.TenantID != DefaultTenantID {
		t.Errorf("lookup returned %+v", got)
	}
}

// TestTokens_LookupRejectsRevoked confirms a revoked token can't
// re-enter service.
func TestTokens_LookupRejectsRevoked(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	secret, prefix, _ := GenerateSecret()
	tok := &APIToken{TenantID: DefaultTenantID, UserSub: "u", Name: "n", Prefix: prefix, Role: "viewer"}
	_ = s.APITokens.Create(ctx, tok, secret)
	if err := s.APITokens.Revoke(ctx, DefaultTenantID, tok.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := s.APITokens.Lookup(ctx, secret); err == nil {
		t.Fatal("revoked token should not authenticate")
	}
}

// TestTokens_LookupRejectsExpired covers expiry-based denial.
func TestTokens_LookupRejectsExpired(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	secret, prefix, _ := GenerateSecret()
	past := time.Now().UTC().Add(-time.Hour)
	tok := &APIToken{
		TenantID:  DefaultTenantID,
		UserSub:   "u",
		Name:     "expired",
		Prefix:    prefix,
		Role:      "viewer",
		ExpiresAt: &past,
	}
	_ = s.APITokens.Create(ctx, tok, secret)
	if _, err := s.APITokens.Lookup(ctx, secret); err == nil {
		t.Fatal("expired token should not authenticate")
	}
}

// TestTokens_LookupRejectsWrongSecret ensures a wrong-hash lookup
// returns ErrNotFound, not a different row's data.
func TestTokens_LookupRejectsWrongSecret(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	secret, prefix, _ := GenerateSecret()
	_ = s.APITokens.Create(ctx, &APIToken{
		TenantID: DefaultTenantID, UserSub: "u", Name: "n", Prefix: prefix, Role: "viewer",
	}, secret)

	if _, err := s.APITokens.Lookup(ctx, "mqct_deadbeef_completely_wrong_secret"); err == nil {
		t.Fatal("wrong secret should not authenticate")
	}
}

// TestTokens_ListByTenant scopes the listing to one tenant — a token
// minted under tenant A must never appear in tenant B's list.
func TestTokens_ListByTenant(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Create a second tenant the same way the API would.
	other := &Tenant{Slug: "other", Name: "Other"}
	if err := s.Tenants.Create(ctx, other); err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	sec1, p1, _ := GenerateSecret()
	sec2, p2, _ := GenerateSecret()
	_ = s.APITokens.Create(ctx, &APIToken{TenantID: DefaultTenantID, UserSub: "u", Name: "a", Prefix: p1, Role: "viewer"}, sec1)
	_ = s.APITokens.Create(ctx, &APIToken{TenantID: other.ID, UserSub: "u", Name: "b", Prefix: p2, Role: "viewer"}, sec2)

	a, _ := s.APITokens.List(ctx, DefaultTenantID)
	b, _ := s.APITokens.List(ctx, other.ID)
	if len(a) != 1 || a[0].Name != "a" {
		t.Errorf("tenant A list: %+v", a)
	}
	if len(b) != 1 || b[0].Name != "b" {
		t.Errorf("tenant B list: %+v", b)
	}
}
