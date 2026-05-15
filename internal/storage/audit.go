package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AuditEntry is one immutable record of an admin action. Records are insert-
// only; no Update or Delete repo methods exist by design.
type AuditEntry struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Actor     string    `json:"actor"`      // preferred_username
	ActorSub  string    `json:"actor_sub"`  // JWT sub
	Action    string    `json:"action"`     // HTTP verb (POST/PUT/DELETE)
	Resource  string    `json:"resource"`   // URL path including ID
	Status    int       `json:"status"`     // HTTP status of the response
	RequestID string    `json:"request_id"` // X-Request-Id for cross-reference
	RemoteIP  string    `json:"remote_ip"`
}

// AuditRepo persists and lists audit entries.
type AuditRepo struct{ db *sql.DB }

// Insert appends an entry to the log.
func (r *AuditRepo) Insert(ctx context.Context, e *AuditEntry) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO audit_log (id, at, actor, actor_sub, action, resource, status, request_id, remote_ip)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.At, e.Actor, e.ActorSub, e.Action, e.Resource, e.Status, e.RequestID, e.RemoteIP)
	if err != nil {
		return fmt.Errorf("insert audit: %w", err)
	}
	return nil
}

// AuditFilter narrows a List query. Zero values mean "any".
type AuditFilter struct {
	Actor    string
	Resource string
	Since    *time.Time
	Until    *time.Time
}

// List returns paged audit entries newest-first, plus the total count for
// the filter (without pagination).
func (r *AuditRepo) List(ctx context.Context, f AuditFilter, page, perPage int) ([]*AuditEntry, int, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 500 {
		perPage = 50
	}

	where := "1=1"
	args := []any{}
	if f.Actor != "" {
		where += " AND actor = ?"
		args = append(args, f.Actor)
	}
	if f.Resource != "" {
		where += " AND resource LIKE ?"
		args = append(args, f.Resource+"%")
	}
	if f.Since != nil {
		where += " AND at >= ?"
		args = append(args, *f.Since)
	}
	if f.Until != nil {
		where += " AND at <= ?"
		args = append(args, *f.Until)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit: %w", err)
	}

	offset := (page - 1) * perPage
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, at, actor, actor_sub, action, resource, status, request_id, remote_ip
		 FROM audit_log WHERE `+where+` ORDER BY at DESC LIMIT ? OFFSET ?`,
		append(args, perPage, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()

	var out []*AuditEntry
	for rows.Next() {
		e := &AuditEntry{}
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.ActorSub, &e.Action,
			&e.Resource, &e.Status, &e.RequestID, &e.RemoteIP); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}
