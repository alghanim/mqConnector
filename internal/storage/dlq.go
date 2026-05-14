package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type DLQRepo struct{ db *sql.DB }

func (r *DLQRepo) Insert(ctx context.Context, e *DLQEntry) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	e.CreatedAt = time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO dlq (id, pipeline_id, source_queue, original_msg, error_reason,
		                 retry_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, nullable(e.PipelineID), e.SourceQueue, e.OriginalMsg, e.ErrorReason,
		e.RetryCount, e.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert dlq: %w", err)
	}
	return nil
}

func (r *DLQRepo) Get(ctx context.Context, id string) (*DLQEntry, error) {
	row := r.db.QueryRowContext(ctx, dlqSelect+` WHERE id=?`, id)
	return scanDLQ(row)
}

func (r *DLQRepo) List(ctx context.Context, page, perPage int) ([]*DLQEntry, int, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 || perPage > 200 {
		perPage = 20
	}

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dlq`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count dlq: %w", err)
	}

	offset := (page - 1) * perPage
	rows, err := r.db.QueryContext(ctx,
		dlqSelect+` ORDER BY created_at DESC LIMIT ? OFFSET ?`, perPage, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list dlq: %w", err)
	}
	defer rows.Close()

	var out []*DLQEntry
	for rows.Next() {
		e, err := scanDLQ(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

func (r *DLQRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM dlq WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete dlq: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *DLQRepo) IncrementRetry(ctx context.Context, id string) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx,
		`UPDATE dlq SET retry_count = retry_count + 1, last_retry_at = ? WHERE id=?`,
		now, id)
	if err != nil {
		return fmt.Errorf("increment retry: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

const dlqSelect = `
SELECT id, COALESCE(pipeline_id, ''), source_queue, original_msg, error_reason,
       retry_count, last_retry_at, created_at
FROM dlq`

func scanDLQ(s scanner) (*DLQEntry, error) {
	e := &DLQEntry{}
	var lastRetry sql.NullTime
	err := s.Scan(&e.ID, &e.PipelineID, &e.SourceQueue, &e.OriginalMsg,
		&e.ErrorReason, &e.RetryCount, &lastRetry, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if lastRetry.Valid {
		t := lastRetry.Time
		e.LastRetryAt = &t
	}
	return e, nil
}
