package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

type RoutingRuleRepo struct{ db *sql.DB }

func (r *RoutingRuleRepo) ListByPipeline(ctx context.Context, pipelineID string) ([]*RoutingRule, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, pipeline_id, condition_path, condition_operator, condition_value,
		       destination_id, priority, enabled
		FROM routing_rules WHERE pipeline_id=? ORDER BY priority ASC`, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("list routing rules: %w", err)
	}
	defer rows.Close()
	var out []*RoutingRule
	for rows.Next() {
		rr := &RoutingRule{}
		if err := rows.Scan(&rr.ID, &rr.PipelineID, &rr.ConditionPath,
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

func (r *RoutingRuleRepo) ReplaceForPipeline(ctx context.Context, pipelineID string, rules []*RoutingRule) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, `DELETE FROM routing_rules WHERE pipeline_id=?`, pipelineID); err != nil {
		return fmt.Errorf("clear routing rules: %w", err)
	}
	for _, rr := range rules {
		if rr.ID == "" {
			rr.ID = uuid.NewString()
		}
		rr.PipelineID = pipelineID
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO routing_rules (id, pipeline_id, condition_path, condition_operator,
			                           condition_value, destination_id, priority, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			rr.ID, rr.PipelineID, rr.ConditionPath, rr.ConditionOperator,
			rr.ConditionValue, rr.DestinationID, rr.Priority, rr.Enabled); err != nil {
			return fmt.Errorf("insert routing rule: %w", err)
		}
	}
	return tx.Commit()
}
