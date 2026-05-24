package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// DLQRedactionRepo persists per-pipeline redaction rules. The DLQ Push
// path consults this repo on every failure to decide whether (and how)
// to redact a payload before it's written to the dlq table.
//
// Tenant boundary: rules belong to a pipeline, and the pipeline owns
// the tenant binding. Write methods require an explicit tenantID
// argument and refuse to update rows that aren't owned by that
// tenant — preventing cross-tenant rule mutations via a guessed
// pipeline_id.
type DLQRedactionRepo struct{ db *dbWrap }

// Replace deletes every existing rule for pipelineID inside tenantID
// and inserts the supplied list as the new ordered set. Mirrors the
// Stages / Transforms / RoutingRules replace-all pattern so the editor
// UI can send the full ruleset and not worry about diffing.
//
// The pipeline_id is validated against tenantID via a sub-select on
// the pipelines table so a caller can't move someone else's rules by
// supplying a stale pipeline id.
func (r *DLQRedactionRepo) Replace(ctx context.Context, tenantID, pipelineID string, rules []DLQRedactionRule) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	if pipelineID == "" {
		return fmt.Errorf("dlq redaction: pipeline_id required")
	}
	// Confirm the pipeline belongs to this tenant before mutating.
	var owned int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM pipelines WHERE id=? AND tenant_id=?`,
		pipelineID, tenantID).Scan(&owned); err != nil {
		return fmt.Errorf("dlq redaction: pipeline lookup: %w", err)
	}
	if owned == 0 {
		return ErrNotFound
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("dlq redaction: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM dlq_redaction_rules WHERE pipeline_id=? AND tenant_id=?`,
		pipelineID, tenantID); err != nil {
		return fmt.Errorf("dlq redaction: clear: %w", err)
	}

	for i, rule := range rules {
		id := rule.ID
		if id == "" {
			id = uuid.NewString()
		}
		kind := rule.RuleKind
		if kind != "jsonpath" && kind != "regex" {
			return fmt.Errorf("dlq redaction: rule %d: rule_kind must be 'jsonpath' or 'regex'", i)
		}
		if rule.Pattern == "" {
			return fmt.Errorf("dlq redaction: rule %d: pattern required", i)
		}
		mask := rule.MaskReplace
		if mask == "" {
			mask = "[REDACTED]"
		}
		ord := rule.Order
		if ord == 0 {
			ord = i
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dlq_redaction_rules
			  (id, pipeline_id, tenant_id, rule_kind, pattern, mask_replace, ord, enabled, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, pipelineID, tenantID, kind, rule.Pattern, mask, ord, rule.Enabled,
			time.Now().UTC()); err != nil {
			return fmt.Errorf("dlq redaction: insert rule %d: %w", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("dlq redaction: commit: %w", err)
	}
	return nil
}

// List returns the ordered rule set for one pipeline within tenantID.
func (r *DLQRedactionRepo) List(ctx context.Context, tenantID, pipelineID string) ([]DLQRedactionRule, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, pipeline_id, rule_kind, pattern, mask_replace, ord, enabled, created_at
		FROM dlq_redaction_rules
		WHERE pipeline_id=? AND tenant_id=?
		ORDER BY ord, id`, pipelineID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("dlq redaction: list: %w", err)
	}
	defer rows.Close()
	var out []DLQRedactionRule
	for rows.Next() {
		var rule DLQRedactionRule
		if err := rows.Scan(&rule.ID, &rule.TenantID, &rule.PipelineID,
			&rule.RuleKind, &rule.Pattern, &rule.MaskReplace, &rule.Order,
			&rule.Enabled, &rule.CreatedAt); err != nil {
			return nil, fmt.Errorf("dlq redaction: scan: %w", err)
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// ListForPipelineUnsafe returns the rule set keyed only by pipeline id,
// bypassing tenant scoping. The DLQ Push path is the intended caller —
// the executor already carries the pipeline-to-tenant binding it
// trusts and the alternative (two-step lookup via Pipelines.GetUnsafe
// then List) does the same work at twice the round-trip cost on a
// failure-path that's already on the slow side.
func (r *DLQRedactionRepo) ListForPipelineUnsafe(ctx context.Context, pipelineID string) ([]DLQRedactionRule, error) {
	if pipelineID == "" {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, pipeline_id, rule_kind, pattern, mask_replace, ord, enabled, created_at
		FROM dlq_redaction_rules
		WHERE pipeline_id=?
		ORDER BY ord, id`, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("dlq redaction: list unsafe: %w", err)
	}
	defer rows.Close()
	var out []DLQRedactionRule
	for rows.Next() {
		var rule DLQRedactionRule
		if err := rows.Scan(&rule.ID, &rule.TenantID, &rule.PipelineID,
			&rule.RuleKind, &rule.Pattern, &rule.MaskReplace, &rule.Order,
			&rule.Enabled, &rule.CreatedAt); err != nil {
			return nil, fmt.Errorf("dlq redaction: scan: %w", err)
		}
		out = append(out, rule)
	}
	return out, rows.Err()
}

// Get returns one rule by id within tenantID. Returned for editor UIs
// that want to render a single rule's full body.
func (r *DLQRedactionRepo) Get(ctx context.Context, tenantID, id string) (*DLQRedactionRule, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, pipeline_id, rule_kind, pattern, mask_replace, ord, enabled, created_at
		FROM dlq_redaction_rules
		WHERE id=? AND tenant_id=?`, id, tenantID)
	var rule DLQRedactionRule
	if err := row.Scan(&rule.ID, &rule.TenantID, &rule.PipelineID,
		&rule.RuleKind, &rule.Pattern, &rule.MaskReplace, &rule.Order,
		&rule.Enabled, &rule.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("dlq redaction: get: %w", err)
	}
	return &rule, nil
}
