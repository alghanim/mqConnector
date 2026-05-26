package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"mqConnector/internal/ai"
)

// AIAuditRow is one row of the ai_audit table. Mirrors ai.AuditRow but
// carries an ID (assigned by the repo on insert) so the governance
// console can deep-link to a specific call.
type AIAuditRow struct {
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
	Feature     string    `json:"feature"`
	CallerSub   string    `json:"caller_sub,omitempty"`
	TenantID    string    `json:"tenant_id,omitempty"`
	PromptHash  string    `json:"prompt_hash"`
	Model       string    `json:"model"`
	Endpoint    string    `json:"endpoint"`
	TokensIn    int       `json:"tokens_in"`
	TokensOut   int       `json:"tokens_out"`
	LatencyMs   int64     `json:"latency_ms"`
	Outcome     string    `json:"outcome"`
	ErrorMsg    string    `json:"error_msg,omitempty"`
	ResultIDRef string    `json:"result_id_ref,omitempty"`
}

// AIAuditFilter narrows AIAuditRepo.List. Empty filter returns
// everything ordered by at DESC.
type AIAuditFilter struct {
	TenantID string     // exact match; empty = no scope
	Feature  string     // exact match; empty = no scope
	Since    *time.Time // lower bound on at
	Until    *time.Time // upper bound on at
	Outcome  string     // exact match; empty = no scope
}

// AIAuditRepo persists rows in the ai_audit table and implements
// ai.AuditLogger so the AI provider can write through it directly.
//
// The Log method is best-effort: insertion failures are logged via
// slog and swallowed so a degraded database can never break the
// request path. This matches the audit-log contract elsewhere in the
// codebase (see middleware_audit.go for the same pattern).
type AIAuditRepo struct {
	db     *dbWrap
	logger *slog.Logger
}

// SetLogger installs a slog.Logger so insert failures are visible in
// the structured-log stream. Defaults to slog.Default() when unset.
func (r *AIAuditRepo) SetLogger(l *slog.Logger) {
	if l != nil {
		r.logger = l
	}
}

// Log implements ai.AuditLogger. Best-effort by contract; never
// returns or panics on error.
func (r *AIAuditRepo) Log(ctx context.Context, row ai.AuditRow) {
	if r == nil || r.db == nil {
		return
	}
	dbRow := AIAuditRow{
		Feature:     string(row.Feature),
		CallerSub:   row.CallerSub,
		TenantID:    row.TenantID,
		PromptHash:  row.PromptHash,
		Model:       row.Model,
		Endpoint:    row.Endpoint,
		TokensIn:    row.TokensIn,
		TokensOut:   row.TokensOut,
		LatencyMs:   row.LatencyMs,
		Outcome:     row.Outcome,
		ErrorMsg:    row.ErrorMsg,
		ResultIDRef: row.ResultIDRef,
		At:          row.At,
	}
	if err := r.Insert(ctx, &dbRow); err != nil {
		l := r.logger
		if l == nil {
			l = slog.Default()
		}
		l.Warn("ai audit insert failed; row dropped",
			"feature", row.Feature,
			"outcome", row.Outcome,
			"err", err)
	}
}

// Insert writes one row. Assigns ID + At when zero. Returns an error
// so callers that want the strict path (tests) can detect failure;
// the Log wrapper is the best-effort path used in production.
func (r *AIAuditRepo) Insert(ctx context.Context, row *AIAuditRow) error {
	if row == nil {
		return errors.New("storage: nil ai audit row")
	}
	if row.ID == "" {
		row.ID = uuid.NewString()
	}
	if row.At.IsZero() {
		row.At = time.Now().UTC()
	}
	if row.Outcome == "" {
		return errors.New("storage: ai audit row missing outcome")
	}
	if row.Feature == "" {
		return errors.New("storage: ai audit row missing feature")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO ai_audit (id, at, feature, caller_sub, tenant_id, prompt_hash,
		                     model, endpoint, tokens_in, tokens_out, latency_ms,
		                     outcome, error_msg, result_id_ref)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.At, row.Feature, row.CallerSub, row.TenantID, row.PromptHash,
		row.Model, row.Endpoint, row.TokensIn, row.TokensOut, row.LatencyMs,
		row.Outcome, row.ErrorMsg, row.ResultIDRef)
	if err != nil {
		return fmt.Errorf("insert ai_audit: %w", err)
	}
	return nil
}

// List returns (rows, total, error). Rows are ordered by at DESC.
// limit ≤ 0 falls back to 50; offset < 0 coerces to 0. Returns
// ErrTenantRequired when the filter scopes by tenant but the tenant
// id is empty — callers that want unscoped access must explicitly
// leave the TenantID field empty.
func (r *AIAuditRepo) List(ctx context.Context, f AIAuditFilter, limit, offset int) ([]*AIAuditRow, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where := "1=1"
	args := []any{}
	if f.TenantID != "" {
		where += " AND tenant_id = ?"
		args = append(args, f.TenantID)
	}
	if f.Feature != "" {
		where += " AND feature = ?"
		args = append(args, f.Feature)
	}
	if f.Outcome != "" {
		where += " AND outcome = ?"
		args = append(args, f.Outcome)
	}
	if f.Since != nil {
		where += " AND at >= ?"
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		where += " AND at < ?"
		args = append(args, *f.Until)
	}

	var total int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM ai_audit WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count ai_audit: %w", err)
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, at, feature, caller_sub, tenant_id, prompt_hash,
		        model, endpoint, tokens_in, tokens_out, latency_ms,
		        outcome, error_msg, result_id_ref
		 FROM ai_audit WHERE `+where+`
		 ORDER BY at DESC
		 LIMIT ? OFFSET ?`,
		append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("query ai_audit: %w", err)
	}
	defer rows.Close()
	var out []*AIAuditRow
	for rows.Next() {
		row := &AIAuditRow{}
		if err := rows.Scan(
			&row.ID, &row.At, &row.Feature, &row.CallerSub, &row.TenantID,
			&row.PromptHash, &row.Model, &row.Endpoint,
			&row.TokensIn, &row.TokensOut, &row.LatencyMs,
			&row.Outcome, &row.ErrorMsg, &row.ResultIDRef,
		); err != nil {
			return nil, 0, fmt.Errorf("scan ai_audit: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Get returns one row by id, or ErrNotFound.
func (r *AIAuditRepo) Get(ctx context.Context, id string) (*AIAuditRow, error) {
	row := &AIAuditRow{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, at, feature, caller_sub, tenant_id, prompt_hash,
		        model, endpoint, tokens_in, tokens_out, latency_ms,
		        outcome, error_msg, result_id_ref
		 FROM ai_audit WHERE id = ?`, id).Scan(
		&row.ID, &row.At, &row.Feature, &row.CallerSub, &row.TenantID,
		&row.PromptHash, &row.Model, &row.Endpoint,
		&row.TokensIn, &row.TokensOut, &row.LatencyMs,
		&row.Outcome, &row.ErrorMsg, &row.ResultIDRef,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// Compile-time check that AIAuditRepo satisfies the ai.AuditLogger
// contract. Cheap insurance against a future signature change in
// either package.
var _ ai.AuditLogger = (*AIAuditRepo)(nil)
