package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// DefaultTenantID is the tenant every legacy row gets backfilled into.
// Operators running a single-tenant deployment never have to know it
// exists — every code path falls back to this when the request carries
// no explicit tenant.
const DefaultTenantID = "00000000-0000-0000-0000-000000000000"

// Tenant is one logical isolation boundary inside a single mqConnector
// deployment. Each customer's deployment can host many tenants; cross-
// tenant data flow only happens via explicit pipeline configuration.
type Tenant struct {
	ID               string    `json:"id"`
	Slug             string    `json:"slug"`
	Name             string    `json:"name"`
	Status           string    `json:"status"`              // active|suspended|disabled
	MaxPipelines     int       `json:"max_pipelines"`       // 0 = unlimited
	MaxMsgsPerMinute int       `json:"max_msgs_per_minute"` // 0 = unlimited
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// TenantRepo persists tenants.
type TenantRepo struct{ db *sql.DB }

func (r *TenantRepo) Create(ctx context.Context, t *Tenant) error {
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	if t.Status == "" {
		t.Status = "active"
	}
	t.CreatedAt = time.Now().UTC()
	t.UpdatedAt = t.CreatedAt
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO tenants (id, slug, name, status, max_pipelines, max_msgs_per_minute, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Slug, t.Name, t.Status, t.MaxPipelines, t.MaxMsgsPerMinute, t.CreatedAt, t.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert tenant: %w", err)
	}
	return nil
}

func (r *TenantRepo) Update(ctx context.Context, t *Tenant) error {
	t.UpdatedAt = time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE tenants SET slug=?, name=?, status=?, max_pipelines=?, max_msgs_per_minute=?, updated_at=?
		WHERE id=?`,
		t.Slug, t.Name, t.Status, t.MaxPipelines, t.MaxMsgsPerMinute, t.UpdatedAt, t.ID)
	if err != nil {
		return fmt.Errorf("update tenant: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *TenantRepo) Delete(ctx context.Context, id string) error {
	if id == DefaultTenantID {
		return errors.New("storage: refusing to delete the default tenant")
	}
	res, err := r.db.ExecContext(ctx, `DELETE FROM tenants WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Purge atomically deletes every row scoped to the named tenant —
// connections, pipelines, stages, transforms, routing rules, dlq,
// scripts, schemas, memberships, api_tokens, webhooks — and the
// tenant row itself. Audit log entries are intentionally retained
// (tamper-evident chain) so an external auditor can still verify
// what happened in the tenant before its deletion; the operator
// archives + purges audit via the separate archiver job.
//
// Use cases:
//   - GDPR right-to-erasure: a tenant has asked us to forget them.
//   - Decommissioning a customer / project / environment.
//
// The default tenant is protected; the rest of the bridge depends on
// its existence as the legacy-row backfill target. All deletions run
// inside a single transaction so a partial purge can't leave a
// half-deleted tenant if the database connection drops mid-operation.
func (r *TenantRepo) Purge(ctx context.Context, id string) error {
	if id == DefaultTenantID {
		return errors.New("storage: refusing to purge the default tenant")
	}
	// Order matters: child rows before parents, even though we don't
	// have FK CASCADE set, so a future migration that adds FKs
	// doesn't trip on left-behind references.
	tables := []string{
		"dlq",
		"routing_rules",
		"transforms",
		"stages",
		"pipelines",
		"connections",
		"schemas",
		"scripts",
		"webhooks",
		"api_tokens",
		"tenant_memberships",
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin purge: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op if Commit ran first
	for _, t := range tables {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM `+t+` WHERE tenant_id = ?`, id); err != nil {
			return fmt.Errorf("purge %s: %w", t, err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM tenants WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete tenant row: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit purge: %w", err)
	}
	return nil
}

func (r *TenantRepo) Get(ctx context.Context, id string) (*Tenant, error) {
	row := r.db.QueryRowContext(ctx, tenantSelect+` WHERE id=?`, id)
	return scanTenant(row)
}

func (r *TenantRepo) GetBySlug(ctx context.Context, slug string) (*Tenant, error) {
	row := r.db.QueryRowContext(ctx, tenantSelect+` WHERE slug=?`, slug)
	return scanTenant(row)
}

func (r *TenantRepo) List(ctx context.Context) ([]*Tenant, error) {
	rows, err := r.db.QueryContext(ctx, tenantSelect+` ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()
	var out []*Tenant
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

const tenantSelect = `
SELECT id, slug, name, status, max_pipelines, max_msgs_per_minute, created_at, updated_at
FROM tenants`

func scanTenant(s scanner) (*Tenant, error) {
	t := &Tenant{}
	err := s.Scan(&t.ID, &t.Slug, &t.Name, &t.Status,
		&t.MaxPipelines, &t.MaxMsgsPerMinute, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}
