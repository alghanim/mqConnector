package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PipelineGrant is one row of the per-pipeline RBAC override table.
// A grant says: user X has role Y on pipeline P specifically,
// regardless of (or on top of) their tenant role.
//
// The semantics are ESCALATION-ONLY: a viewer-on-tenant who has an
// admin grant on pipeline P is admin on P; a viewer-on-tenant with
// no grant is still viewer on P. Grants never demote — a user
// can't be "viewer on this one pipeline" if they're admin on the
// tenant. That decision keeps the surface auditable: any pipeline
// access ≥ tenant role is reachable via the tenant role audit log
// alone; only escalations need the grants audit trail.
type PipelineGrant struct {
	PipelineID string    `json:"pipeline_id"`
	UserSub    string    `json:"user_sub"`
	Role       Role      `json:"role"`
	CreatedAt  time.Time `json:"created_at"`
}

// PipelineGrantsRepo manages the pipeline_grants table.
//
// This repository is the storage half of Phase 5 of the enterprise
// hardening pass. The handler-side integration (filter list endpoints,
// gate update endpoints on EffectiveRole) lands in a follow-on commit
// so the database surface and the access-control logic can be reviewed
// independently.
type PipelineGrantsRepo struct {
	db *dbWrap
}

// Set creates or updates the grant for (pipelineID, userSub). Roles
// are validated against the enum; an empty pipelineID or userSub is
// rejected so callers don't accidentally insert "grant for everyone".
func (r *PipelineGrantsRepo) Set(ctx context.Context, pipelineID, userSub string, role Role) error {
	if pipelineID == "" {
		return errors.New("pipeline grants: pipeline_id required")
	}
	if userSub == "" {
		return errors.New("pipeline grants: user_sub required")
	}
	if !role.Valid() {
		return fmt.Errorf("pipeline grants: invalid role %q", role)
	}
	now := time.Now().UTC()
	// Both SQLite and Postgres accept this ON CONFLICT form; placeholder
	// rewriting happens in dbWrap.
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO pipeline_grants (pipeline_id, user_sub, role, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(pipeline_id, user_sub) DO UPDATE SET role = excluded.role`,
		pipelineID, userSub, string(role), now)
	if err != nil {
		return fmt.Errorf("pipeline_grants upsert: %w", err)
	}
	return nil
}

// Get returns the grant for one (pipeline, user) pair, or nil if no
// row exists. The absence of a row is the common case — most users
// have only their tenant role.
func (r *PipelineGrantsRepo) Get(ctx context.Context, pipelineID, userSub string) (*PipelineGrant, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT pipeline_id, user_sub, role, created_at
		   FROM pipeline_grants
		  WHERE pipeline_id = ? AND user_sub = ?`,
		pipelineID, userSub)
	var g PipelineGrant
	var role string
	err := row.Scan(&g.PipelineID, &g.UserSub, &role, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pipeline_grants get: %w", err)
	}
	g.Role = Role(role)
	return &g, nil
}

// ListForPipeline returns every grant on a pipeline. Used by the
// pipeline-detail UI to render the access list and by the audit
// review path to enumerate non-default access.
func (r *PipelineGrantsRepo) ListForPipeline(ctx context.Context, pipelineID string) ([]PipelineGrant, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT pipeline_id, user_sub, role, created_at
		   FROM pipeline_grants
		  WHERE pipeline_id = ?
		  ORDER BY user_sub`,
		pipelineID)
	if err != nil {
		return nil, fmt.Errorf("pipeline_grants list: %w", err)
	}
	defer rows.Close()
	var out []PipelineGrant
	for rows.Next() {
		var g PipelineGrant
		var role string
		if err := rows.Scan(&g.PipelineID, &g.UserSub, &role, &g.CreatedAt); err != nil {
			return nil, fmt.Errorf("pipeline_grants scan: %w", err)
		}
		g.Role = Role(role)
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListPipelinesForUser returns the IDs of every pipeline this user
// has an explicit grant on. Combined with the user's tenant
// memberships, this is the full set of pipelines they can see at
// "operator or better" (after EffectiveRole filtering). Used by the
// list-pipelines handler to enrich the query when the user's tenant
// role alone wouldn't admit them.
func (r *PipelineGrantsRepo) ListPipelinesForUser(ctx context.Context, userSub string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT pipeline_id FROM pipeline_grants WHERE user_sub = ?`, userSub)
	if err != nil {
		return nil, fmt.Errorf("pipeline_grants list-by-user: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("pipeline_grants scan id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Delete removes a single grant. Returns ErrNotFound if the (pipeline,
// user) pair has no grant — the caller distinguishes "already absent"
// from "deleted" if it needs to.
func (r *PipelineGrantsRepo) Delete(ctx context.Context, pipelineID, userSub string) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM pipeline_grants WHERE pipeline_id = ? AND user_sub = ?`,
		pipelineID, userSub)
	if err != nil {
		return fmt.Errorf("pipeline_grants delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// EffectiveRole returns the user's effective role on a specific
// pipeline. The rule:
//
//	effective = max(tenantRole, pipelineGrant)
//
// tenantRole is what the caller already resolved from the tenant
// memberships. If no grant exists, returns tenantRole unchanged.
// If a grant exists with a HIGHER role, returns the grant. A grant
// with a LOWER role is ignored — grants can only escalate (see
// PipelineGrant doc comment).
//
// This is the call site every authorisation gate in handlers_pipelines.go
// will eventually route through. Today it's used by the bootstrap
// tests that lock in the resolver semantics ahead of the handler
// integration.
func (r *PipelineGrantsRepo) EffectiveRole(ctx context.Context, pipelineID, userSub string, tenantRole Role) (Role, error) {
	if !tenantRole.Valid() {
		// A user with no tenant membership still gets their grant role,
		// if any — that's the "external collaborator who only has
		// access to this one pipeline" case.
		tenantRole = ""
	}
	g, err := r.Get(ctx, pipelineID, userSub)
	if err != nil {
		return tenantRole, err
	}
	if g == nil {
		return tenantRole, nil
	}
	if !g.Role.Valid() {
		return tenantRole, nil
	}
	if g.Role.Rank() > tenantRole.Rank() {
		return g.Role, nil
	}
	return tenantRole, nil
}
