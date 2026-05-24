package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type RoutingRuleRepo struct{ db *dbWrap }

func (r *RoutingRuleRepo) ListByPipeline(ctx context.Context, tenantID, pipelineID string) ([]*RoutingRule, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	return r.listByPipeline(ctx, pipelineID, &tenantID)
}

// ListByPipelineUnsafe — internal only (pipeline manager boot).
func (r *RoutingRuleRepo) ListByPipelineUnsafe(ctx context.Context, pipelineID string) ([]*RoutingRule, error) {
	return r.listByPipeline(ctx, pipelineID, nil)
}

func (r *RoutingRuleRepo) listByPipeline(ctx context.Context, pipelineID string, tenantFilter *string) ([]*RoutingRule, error) {
	q := `
		SELECT id, tenant_id, pipeline_id, condition_path, condition_operator, condition_value,
		       destination_id, priority, enabled
		FROM routing_rules WHERE pipeline_id=?`
	args := []any{pipelineID}
	if tenantFilter != nil {
		q += ` AND tenant_id=?`
		args = append(args, *tenantFilter)
	}
	q += ` ORDER BY priority ASC`
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list routing rules: %w", err)
	}
	defer rows.Close()
	var out []*RoutingRule
	for rows.Next() {
		rr := &RoutingRule{}
		if err := rows.Scan(&rr.ID, &rr.TenantID, &rr.PipelineID, &rr.ConditionPath,
			&rr.ConditionOperator, &rr.ConditionValue, &rr.DestinationID,
			&rr.Priority, &rr.Enabled); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		out = append(out, rr)
	}
	return out, rows.Err()
}

// ReplaceForPipeline opens its own tx and delegates to ReplaceForPipelineTx
// so callers (the server-layer applyRevisionLive helper) that need to bundle
// the replace into a wider transaction can share the same statements.
func (r *RoutingRuleRepo) ReplaceForPipeline(ctx context.Context, tenantID, pipelineID string, rules []*RoutingRule) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := r.ReplaceForPipelineTx(ctx, tx, tenantID, pipelineID, rules); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplaceForPipelineTx is the tx-aware variant of ReplaceForPipeline.
// Caller owns tx lifecycle.
func (r *RoutingRuleRepo) ReplaceForPipelineTx(ctx context.Context, tx *Tx, tenantID, pipelineID string, rules []*RoutingRule) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM routing_rules WHERE pipeline_id=? AND tenant_id=?`, pipelineID, tenantID); err != nil {
		return fmt.Errorf("clear routing rules: %w", err)
	}
	for _, rr := range rules {
		if rr.ID == "" {
			rr.ID = uuid.NewString()
		}
		rr.PipelineID = pipelineID
		rr.TenantID = tenantID
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO routing_rules (id, tenant_id, pipeline_id, condition_path, condition_operator,
			                           condition_value, destination_id, priority, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rr.ID, tenantID, rr.PipelineID, rr.ConditionPath, rr.ConditionOperator,
			rr.ConditionValue, rr.DestinationID, rr.Priority, rr.Enabled); err != nil {
			return fmt.Errorf("insert routing rule: %w", err)
		}
	}
	return nil
}
