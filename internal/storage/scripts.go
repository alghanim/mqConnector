package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ScriptRepo struct{ db *sql.DB }

func (r *ScriptRepo) Create(ctx context.Context, s *Script) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	s.CreatedAt = time.Now().UTC()
	s.UpdatedAt = s.CreatedAt
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO scripts (id, name, description, body, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.Description, s.Body, s.Enabled, s.CreatedAt, s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert script: %w", err)
	}
	return nil
}

func (r *ScriptRepo) Update(ctx context.Context, s *Script) error {
	s.UpdatedAt = time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE scripts SET name=?, description=?, body=?, enabled=?, updated_at=?
		WHERE id=?`,
		s.Name, s.Description, s.Body, s.Enabled, s.UpdatedAt, s.ID)
	if err != nil {
		return fmt.Errorf("update script: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *ScriptRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM scripts WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete script: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *ScriptRepo) Get(ctx context.Context, id string) (*Script, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, body, enabled, created_at, updated_at
		FROM scripts WHERE id=?`, id)
	s := &Script{}
	err := row.Scan(&s.ID, &s.Name, &s.Description, &s.Body, &s.Enabled, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

func (r *ScriptRepo) List(ctx context.Context) ([]*Script, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, body, enabled, created_at, updated_at
		FROM scripts ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list scripts: %w", err)
	}
	defer rows.Close()
	var out []*Script
	for rows.Next() {
		s := &Script{}
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.Body,
			&s.Enabled, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type SchemaRepo struct{ db *sql.DB }

func (r *SchemaRepo) Create(ctx context.Context, s *Schema) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	s.CreatedAt = time.Now().UTC()
	s.UpdatedAt = s.CreatedAt
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO schemas (id, name, schema_type, content, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.SchemaType, s.Content, s.CreatedAt, s.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert schema: %w", err)
	}
	return nil
}

func (r *SchemaRepo) Update(ctx context.Context, s *Schema) error {
	s.UpdatedAt = time.Now().UTC()
	res, err := r.db.ExecContext(ctx,
		`UPDATE schemas SET name=?, schema_type=?, content=?, updated_at=? WHERE id=?`,
		s.Name, s.SchemaType, s.Content, s.UpdatedAt, s.ID)
	if err != nil {
		return fmt.Errorf("update schema: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SchemaRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM schemas WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete schema: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *SchemaRepo) Get(ctx context.Context, id string) (*Schema, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, schema_type, content, created_at, updated_at
		FROM schemas WHERE id=?`, id)
	s := &Schema{}
	err := row.Scan(&s.ID, &s.Name, &s.SchemaType, &s.Content, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return s, err
}

func (r *SchemaRepo) List(ctx context.Context) ([]*Schema, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, schema_type, content, created_at, updated_at
		FROM schemas ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list schemas: %w", err)
	}
	defer rows.Close()
	var out []*Schema
	for rows.Next() {
		s := &Schema{}
		if err := rows.Scan(&s.ID, &s.Name, &s.SchemaType, &s.Content,
			&s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
