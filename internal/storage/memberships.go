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
	// SystemAdmin grants cross-tenant elevation: create tenants,
	// audit-verify across tenants, rotate the master key. Replaces
	// the previous implicit "owner of the default tenant" check.
	// Backfilled to 1 for default-tenant owners by migration 0013.
	SystemAdmin bool      `json:"system_admin"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// MembershipRepo manages the tenant_memberships table.
type MembershipRepo struct{ db *dbWrap }

// Upsert inserts or replaces a membership row.
func (r *MembershipRepo) Upsert(ctx context.Context, m *Membership) error {
	if !m.Role.Valid() {
		return fmt.Errorf("invalid role %q", m.Role)
	}
	m.UpdatedAt = time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = m.UpdatedAt
	}
	sysAdmin := 0
	if m.SystemAdmin {
		sysAdmin = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO tenant_memberships (tenant_id, user_sub, username, role, system_admin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, user_sub) DO UPDATE SET
			username = excluded.username,
			role = excluded.role,
			system_admin = excluded.system_admin,
			updated_at = excluded.updated_at`,
		m.TenantID, m.UserSub, m.Username, string(m.Role), sysAdmin, m.CreatedAt, m.UpdatedAt)
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

// adoptBootstrap rewrites any rows the current login should claim so
// they're keyed by the real sub. Safe to call repeatedly — once the
// rows belong to userSub the UPDATEs become no-ops.
//
// Two adoption rules, applied in order:
//
//  1. bootstrap:<username> → real sub. The historical case: a seeded
//     bootstrap row sits at user_sub='bootstrap:admin' until the first
//     real login resolves the sub claim.
//
//  2. Stale sub for the same username → current sub. The local-dev case:
//     a SimpleAuth restart hands out a fresh sub for the same user, so
//     existing membership rows keyed by the old sub become unreachable
//     by lookup. We adopt them by username so the user's tenants survive
//     SimpleAuth re-bootstraps.
//
// Rule 2 only fires when there's no row for the new sub AND every
// existing row for this username already has a *different* (old) sub —
// i.e. the username uniquely identifies the user in the
// memberships table. In a multi-user setup where the same display
// username could collide across accounts this would be unsafe, but our
// memberships are keyed by sub uniquely; the username field is a
// human-readable label.
func (r *MembershipRepo) adoptBootstrap(ctx context.Context, userSub, username string) error {
	if userSub == "" || username == "" {
		return nil
	}
	now := time.Now().UTC()
	bootstrapKey := "bootstrap:" + strings.ToLower(username)

	// First: gate the whole adoption on the bootstrap-admin signature.
	// We only salvage rows for a user that ALREADY owns (or was seeded
	// to own) the default tenant under this username. A regular tenant
	// member never matches this signature, so a username collision
	// between two regular users cannot steal anyone's rows here.
	var hasBootstrapSig int
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM tenant_memberships
		WHERE LOWER(username) = LOWER(?)
		  AND tenant_id = ?
		  AND role = ?`,
		username, DefaultTenantID, string(RoleOwner)).Scan(&hasBootstrapSig); err != nil {
		return fmt.Errorf("check bootstrap signature: %w", err)
	}
	if hasBootstrapSig == 0 {
		return nil
	}

	// Step 1: remove the seeded `bootstrap:<username>` row IFF a real
	// row for the same username already exists on the same tenant.
	// Without this, step 2's UPDATE would collide with the unique
	// (tenant_id, user_sub) constraint when both rows want to land on
	// `userSub`.
	if _, err := r.db.ExecContext(ctx, `
		DELETE FROM tenant_memberships
		WHERE user_sub = ?
		  AND EXISTS (
		    SELECT 1 FROM tenant_memberships m2
		    WHERE m2.tenant_id = tenant_memberships.tenant_id
		      AND LOWER(m2.username) = LOWER(?)
		      AND m2.user_sub <> ?
		  )`,
		bootstrapKey, username, bootstrapKey); err != nil {
		return fmt.Errorf("clean stale bootstrap rows: %w", err)
	}

	// Step 2: rename `bootstrap:<username>` rows to userSub (the
	// historical first-login path). Safe because step 1 removed any
	// rows that would collide.
	if _, err := r.db.ExecContext(ctx, `
		UPDATE tenant_memberships
		SET user_sub = ?, username = ?, updated_at = ?
		WHERE user_sub = ?`,
		userSub, username, now, bootstrapKey); err != nil {
		return fmt.Errorf("adopt bootstrap membership: %w", err)
	}

	// Step 3: salvage stale-sub rows. For every row of this username
	// whose user_sub differs from the current login, rewrite it to
	// userSub — UNLESS a row already exists at (tenant_id, userSub),
	// in which case the stale row is a duplicate and we drop it.
	if _, err := r.db.ExecContext(ctx, `
		DELETE FROM tenant_memberships
		WHERE LOWER(username) = LOWER(?)
		  AND user_sub <> ?
		  AND EXISTS (
		    SELECT 1 FROM tenant_memberships m2
		    WHERE m2.tenant_id = tenant_memberships.tenant_id
		      AND m2.user_sub = ?
		  )`,
		username, userSub, userSub); err != nil {
		return fmt.Errorf("drop duplicate stale rows: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `
		UPDATE tenant_memberships
		SET user_sub = ?, updated_at = ?
		WHERE LOWER(username) = LOWER(?)
		  AND user_sub <> ?`,
		userSub, now, username, userSub); err != nil {
		return fmt.Errorf("adopt stale-sub rows: %w", err)
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
SELECT tenant_id, user_sub, username, role, system_admin, created_at, updated_at
FROM tenant_memberships`

func scanMembership(s scanner) (*Membership, error) {
	m := &Membership{}
	var role string
	var sysAdmin int
	err := s.Scan(&m.TenantID, &m.UserSub, &m.Username, &role, &sysAdmin, &m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.Role = Role(role)
	m.SystemAdmin = sysAdmin != 0
	return m, nil
}

// SetSystemAdmin flips the system_admin bit for an existing membership
// without touching role. Used by the platform-admin endpoint to grant
// or revoke cross-tenant elevation explicitly.
func (r *MembershipRepo) SetSystemAdmin(ctx context.Context, tenantID, userSub string, on bool) error {
	v := 0
	if on {
		v = 1
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE tenant_memberships
		   SET system_admin = ?, updated_at = ?
		 WHERE tenant_id = ? AND user_sub = ?`,
		v, time.Now().UTC(), tenantID, userSub)
	if err != nil {
		return fmt.Errorf("set system_admin: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// IsSystemAdmin reports whether the named user has the system_admin
// flag set on ANY of their memberships. The flag is per-membership
// but treated as a user-level capability — once set anywhere the
// user is a platform operator across the whole deployment.
func (r *MembershipRepo) IsSystemAdmin(ctx context.Context, userSub string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tenant_memberships WHERE user_sub = ? AND system_admin = 1`,
		userSub).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("is_system_admin: %w", err)
	}
	return n > 0, nil
}
