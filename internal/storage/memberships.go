package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Role enumerates tenant-scoped roles, weakest → strongest. Compare via
// the Rank method instead of string-comparing.
type Role string

const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
	RoleOwner    Role = "owner"
)

// Rank returns 0..3 for ordering. -1 for an unknown role (treat as
// "no access").
func (r Role) Rank() int {
	switch r {
	case RoleViewer:
		return 0
	case RoleOperator:
		return 1
	case RoleAdmin:
		return 2
	case RoleOwner:
		return 3
	default:
		return -1
	}
}

// AtLeast reports whether r meets the minimum requirement.
func (r Role) AtLeast(min Role) bool {
	return r.Rank() >= min.Rank() && r.Rank() >= 0
}

// Valid reports whether r is one of the named constants.
func (r Role) Valid() bool { return r.Rank() >= 0 }

// Membership is the row that says "user X has role Y in tenant Z."
type Membership struct {
	TenantID  string    `json:"tenant_id"`
	UserSub   string    `json:"user_sub"`
	Username  string    `json:"username"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MembershipRepo manages the tenant_memberships table.
type MembershipRepo struct{ db *sql.DB }

// Upsert inserts or replaces a membership row.
func (r *MembershipRepo) Upsert(ctx context.Context, m *Membership) error {
	if !m.Role.Valid() {
		return fmt.Errorf("invalid role %q", m.Role)
	}
	m.UpdatedAt = time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = m.UpdatedAt
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO tenant_memberships (tenant_id, user_sub, username, role, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, user_sub) DO UPDATE SET
			username = excluded.username,
			role = excluded.role,
			updated_at = excluded.updated_at`,
		m.TenantID, m.UserSub, m.Username, string(m.Role), m.CreatedAt, m.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert membership: %w", err)
	}
	return nil
}

// Delete removes a membership.
func (r *MembershipRepo) Delete(ctx context.Context, tenantID, userSub string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM tenant_memberships WHERE tenant_id=? AND user_sub=?`, tenantID, userSub)
	if err != nil {
		return fmt.Errorf("delete membership: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns the membership row for a (tenant, user) pair, or
// ErrNotFound if the user has no membership in that tenant.
func (r *MembershipRepo) Get(ctx context.Context, tenantID, userSub string) (*Membership, error) {
	row := r.db.QueryRowContext(ctx, membershipSelect+` WHERE tenant_id=? AND user_sub=?`,
		tenantID, userSub)
	return scanMembership(row)
}

// ListByUser returns every membership for a given user across tenants.
// Used at login to populate the tenant switcher.
//
// userSub is the JWT sub claim. The bootstrap row (`bootstrap:admin`)
// is auto-upgraded to the real sub on the first lookup that names the
// admin username, so the second login onwards skips the migration.
func (r *MembershipRepo) ListByUser(ctx context.Context, userSub, username string) ([]*Membership, error) {
	out, err := r.queryByUser(ctx, userSub)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 && username != "" {
		// First login for this sub — check if a bootstrap row exists for
		// this username and promote it.
		if err := r.adoptBootstrap(ctx, userSub, username); err != nil {
			return nil, err
		}
		out, err = r.queryByUser(ctx, userSub)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *MembershipRepo) queryByUser(ctx context.Context, userSub string) ([]*Membership, error) {
	rows, err := r.db.QueryContext(ctx,
		membershipSelect+` WHERE user_sub=? ORDER BY tenant_id`, userSub)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	defer rows.Close()
	var out []*Membership
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// adoptBootstrap rewrites any bootstrap:<username> rows so they're keyed
// by the real sub. Safe to call repeatedly — it just becomes a no-op
// once the bootstrap row is gone.
func (r *MembershipRepo) adoptBootstrap(ctx context.Context, userSub, username string) error {
	if userSub == "" || username == "" {
		return nil
	}
	bootstrapKey := "bootstrap:" + strings.ToLower(username)
	_, err := r.db.ExecContext(ctx, `
		UPDATE tenant_memberships
		SET user_sub = ?, username = ?, updated_at = ?
		WHERE user_sub = ?`,
		userSub, username, time.Now().UTC(), bootstrapKey)
	if err != nil {
		return fmt.Errorf("adopt bootstrap membership: %w", err)
	}
	return nil
}

// ListByTenant lists every member of a tenant. Used by the members UI.
func (r *MembershipRepo) ListByTenant(ctx context.Context, tenantID string) ([]*Membership, error) {
	rows, err := r.db.QueryContext(ctx,
		membershipSelect+` WHERE tenant_id=? ORDER BY username, user_sub`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list tenant members: %w", err)
	}
	defer rows.Close()
	var out []*Membership
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

const membershipSelect = `
SELECT tenant_id, user_sub, username, role, created_at, updated_at
FROM tenant_memberships`

func scanMembership(s scanner) (*Membership, error) {
	m := &Membership{}
	var role string
	err := s.Scan(&m.TenantID, &m.UserSub, &m.Username, &role, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Role = Role(role)
	return m, nil
}
