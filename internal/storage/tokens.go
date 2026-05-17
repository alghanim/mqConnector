package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// APIToken is one headless-auth credential. Tokens are scoped to a
// single tenant and a single role; the role is upper-bounded by the
// creator's role at issue time. The secret is hashed at rest (sha256
// hex) and shown exactly once at creation.
type APIToken struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	UserSub    string     `json:"user_sub"`  // creator
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`    // first 8 chars of the user-visible secret
	Role       string     `json:"role"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Active reports whether the token is currently usable (not revoked,
// not past its expiry).
func (t *APIToken) Active(now time.Time) bool {
	if t.RevokedAt != nil {
		return false
	}
	if t.ExpiresAt != nil && !t.ExpiresAt.After(now) {
		return false
	}
	return true
}

// APITokenRepo persists API tokens. The store value is a hash; the
// plaintext secret is never persisted.
type APITokenRepo struct{ db *dbWrap }

// TokenSecretPrefix is the marker every issued token starts with so
// authentication code can distinguish tokens from other bearer
// formats at a glance.
const TokenSecretPrefix = "mqct_"

// GenerateSecret returns a freshly-minted token secret of the form
//
//	mqct_{8-char prefix}_{40-char suffix}
//
// 48 chars of crypto/rand → ~248 bits of entropy. The prefix is also
// returned because it's stored alongside the hash for UI display.
func GenerateSecret() (secret, prefix string, err error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", "", fmt.Errorf("token: random: %w", err)
	}
	full := hex.EncodeToString(buf[:]) // 48 hex chars
	prefix = full[:8]
	secret = TokenSecretPrefix + prefix + "_" + full[8:]
	return secret, prefix, nil
}

// HashSecret returns the sha256 hex of a token secret. The same
// function is used at issue time and at every authentication lookup.
func HashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// Create inserts a new token row from an already-generated secret.
// Caller is responsible for showing `secret` to the operator once —
// after this returns the secret is not recoverable.
func (r *APITokenRepo) Create(ctx context.Context, t *APIToken, secret string) error {
	if t.TenantID == "" {
		return ErrTenantRequired
	}
	if secret == "" {
		return errors.New("api token: secret required")
	}
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	hash := HashSecret(secret)
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO api_tokens (id, tenant_id, user_sub, name, prefix, token_hash, role, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.TenantID, t.UserSub, t.Name, t.Prefix, hash, t.Role, t.CreatedAt, nullableTime(t.ExpiresAt))
	if err != nil {
		return fmt.Errorf("insert api token: %w", err)
	}
	return nil
}

// Lookup resolves a presented secret to an active token row. Returns
// ErrNotFound when no row matches OR the matching row is revoked /
// expired — callers never get a token they can't use. A side-effect
// update to last_used_at runs best-effort after a successful lookup.
func (r *APITokenRepo) Lookup(ctx context.Context, secret string) (*APIToken, error) {
	hash := HashSecret(secret)
	t := &APIToken{}
	var expiresAt, lastUsedAt, revokedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, user_sub, name, prefix, role, created_at, expires_at, last_used_at, revoked_at
		FROM api_tokens WHERE token_hash = ?`, hash).Scan(
		&t.ID, &t.TenantID, &t.UserSub, &t.Name, &t.Prefix, &t.Role,
		&t.CreatedAt, &expiresAt, &lastUsedAt, &revokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.ExpiresAt = nullTime(expiresAt)
	t.LastUsedAt = nullTime(lastUsedAt)
	t.RevokedAt = nullTime(revokedAt)
	if !t.Active(time.Now().UTC()) {
		return nil, ErrNotFound
	}

	// Best-effort: stamp last_used_at so the UI can show "last seen 12m ago".
	_, _ = r.db.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`,
		time.Now().UTC(), t.ID)
	return t, nil
}

// List returns all tokens for a tenant (active + revoked + expired),
// newest-first. The secret hash is intentionally never returned.
func (r *APITokenRepo) List(ctx context.Context, tenantID string) ([]*APIToken, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, user_sub, name, prefix, role, created_at, expires_at, last_used_at, revoked_at
		FROM api_tokens WHERE tenant_id = ? ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list api tokens: %w", err)
	}
	defer rows.Close()
	var out []*APIToken
	for rows.Next() {
		t := &APIToken{}
		var expiresAt, lastUsedAt, revokedAt sql.NullTime
		if err := rows.Scan(&t.ID, &t.TenantID, &t.UserSub, &t.Name, &t.Prefix, &t.Role,
			&t.CreatedAt, &expiresAt, &lastUsedAt, &revokedAt); err != nil {
			return nil, err
		}
		t.ExpiresAt = nullTime(expiresAt)
		t.LastUsedAt = nullTime(lastUsedAt)
		t.RevokedAt = nullTime(revokedAt)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Revoke marks a token as revoked. A revoked token never re-enters
// service even after restart. Idempotent — calling Revoke on an
// already-revoked row is a no-op.
func (r *APITokenRepo) Revoke(ctx context.Context, tenantID, id string) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND tenant_id = ? AND revoked_at IS NULL`,
		time.Now().UTC(), id, tenantID)
	if err != nil {
		return fmt.Errorf("revoke api token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Either not found, wrong tenant, or already revoked — caller can
		// treat "already revoked" the same as success.
		return ErrNotFound
	}
	return nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
func nullTime(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}
