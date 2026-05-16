package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Webhook is one operator-registered outbound HTTP endpoint that
// receives event notifications. The `Secret` column is used as the
// HMAC-SHA256 signing key for delivery; consumers verify it from the
// `X-MQC-Signature` header.
type Webhook struct {
	ID            string     `json:"id"`
	TenantID      string     `json:"tenant_id"`
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	Secret        string     `json:"secret,omitempty"` // omitted on list
	Events        string     `json:"events"`           // "*" or csv of types
	Enabled       bool       `json:"enabled"`
	LastStatus    int        `json:"last_status"`
	LastError     string     `json:"last_error,omitempty"`
	LastAttemptAt *time.Time `json:"last_attempt_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Matches reports whether this webhook should fire for an event of
// the given type. "*" matches everything; otherwise the type must
// appear in the comma-separated Events list (trim-tolerant).
func (w *Webhook) Matches(eventType string) bool {
	if !w.Enabled || w.Events == "" {
		return false
	}
	if w.Events == "*" {
		return true
	}
	for _, e := range splitCSV(w.Events) {
		if e == eventType {
			return true
		}
	}
	return false
}

// WebhookRepo persists webhook configurations.
type WebhookRepo struct{ db *sql.DB }

func (r *WebhookRepo) Create(ctx context.Context, tenantID string, w *Webhook) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	if w.ID == "" {
		w.ID = uuid.NewString()
	}
	if w.Events == "" {
		w.Events = "*"
	}
	w.TenantID = tenantID
	now := time.Now().UTC()
	w.CreatedAt = now
	w.UpdatedAt = now
	enabled := 0
	if w.Enabled {
		enabled = 1
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO webhooks (id, tenant_id, name, url, secret, events, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ID, tenantID, w.Name, w.URL, w.Secret, w.Events, enabled, w.CreatedAt, w.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert webhook: %w", err)
	}
	return nil
}

func (r *WebhookRepo) Update(ctx context.Context, tenantID string, w *Webhook) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	w.UpdatedAt = time.Now().UTC()
	enabled := 0
	if w.Enabled {
		enabled = 1
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE webhooks SET name=?, url=?, secret=?, events=?, enabled=?, updated_at=?
		WHERE id=? AND tenant_id=?`,
		w.Name, w.URL, w.Secret, w.Events, enabled, w.UpdatedAt, w.ID, tenantID)
	if err != nil {
		return fmt.Errorf("update webhook: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *WebhookRepo) Delete(ctx context.Context, tenantID, id string) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM webhooks WHERE id=? AND tenant_id=?`, id, tenantID)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *WebhookRepo) Get(ctx context.Context, tenantID, id string) (*Webhook, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	return r.scan(r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, name, url, secret, events, enabled,
		       last_status, last_error, last_attempt_at, created_at, updated_at
		FROM webhooks WHERE id=? AND tenant_id=?`, id, tenantID))
}

func (r *WebhookRepo) List(ctx context.Context, tenantID string) ([]*Webhook, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, name, url, secret, events, enabled,
		       last_status, last_error, last_attempt_at, created_at, updated_at
		FROM webhooks WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer rows.Close()
	var out []*Webhook
	for rows.Next() {
		w, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ListAll is the dispatcher's view — every webhook across every
// tenant. Used by the in-process delivery loop to subscribe at boot.
func (r *WebhookRepo) ListAll(ctx context.Context) ([]*Webhook, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, name, url, secret, events, enabled,
		       last_status, last_error, last_attempt_at, created_at, updated_at
		FROM webhooks ORDER BY tenant_id, name`)
	if err != nil {
		return nil, fmt.Errorf("list all webhooks: %w", err)
	}
	defer rows.Close()
	var out []*Webhook
	for rows.Next() {
		w, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// RecordAttempt stamps the last_status / last_error / last_attempt_at
// columns after a delivery attempt. Errors are clamped to 500 chars
// so a verbose downstream response can't blow up the row.
func (r *WebhookRepo) RecordAttempt(ctx context.Context, id string, status int, errText string) error {
	if len(errText) > 500 {
		errText = errText[:500]
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE webhooks SET last_status=?, last_error=?, last_attempt_at=? WHERE id=?`,
		status, errText, time.Now().UTC(), id)
	return err
}

func (r *WebhookRepo) scan(s scanner) (*Webhook, error) {
	w := &Webhook{}
	var enabled int
	var lastAttemptAt sql.NullTime
	err := s.Scan(&w.ID, &w.TenantID, &w.Name, &w.URL, &w.Secret, &w.Events, &enabled,
		&w.LastStatus, &w.LastError, &lastAttemptAt, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	w.Enabled = enabled != 0
	w.LastAttemptAt = nullTime(lastAttemptAt)
	return w, nil
}

// splitCSV is a small helper for parsing the `events` column. Kept
// here rather than importing the standard "strings" CSV machinery —
// the inputs are tiny.
func splitCSV(s string) []string {
	out := make([]string, 0, 4)
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			// Trim spaces around the slice.
			lo, hi := start, i
			for lo < hi && (s[lo] == ' ' || s[lo] == '\t') {
				lo++
			}
			for hi > lo && (s[hi-1] == ' ' || s[hi-1] == '\t') {
				hi--
			}
			if hi > lo {
				out = append(out, s[lo:hi])
			}
			start = i + 1
		}
	}
	return out
}
