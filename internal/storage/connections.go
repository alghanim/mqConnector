package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by repo lookups when the requested row does not exist.
var ErrNotFound = errors.New("storage: not found")

type ConnectionRepo struct{ db *sql.DB }

func (r *ConnectionRepo) Create(ctx context.Context, c *Connection) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	c.CreatedAt = time.Now().UTC()
	c.UpdatedAt = c.CreatedAt
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO connections (id, name, type, queue_manager, conn_name, channel,
		                         username, password, queue_name, url, brokers, topic,
		                         created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Type, c.QueueManager, c.ConnName, c.Channel,
		c.Username, c.Password, c.QueueName, c.URL, c.Brokers, c.Topic,
		c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert connection: %w", err)
	}
	return nil
}

func (r *ConnectionRepo) Update(ctx context.Context, c *Connection) error {
	c.UpdatedAt = time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
		UPDATE connections SET name=?, type=?, queue_manager=?, conn_name=?, channel=?,
		                       username=?, password=?, queue_name=?, url=?, brokers=?,
		                       topic=?, updated_at=?
		WHERE id=?`,
		c.Name, c.Type, c.QueueManager, c.ConnName, c.Channel,
		c.Username, c.Password, c.QueueName, c.URL, c.Brokers, c.Topic,
		c.UpdatedAt, c.ID)
	if err != nil {
		return fmt.Errorf("update connection: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *ConnectionRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM connections WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete connection: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *ConnectionRepo) Get(ctx context.Context, id string) (*Connection, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, type, queue_manager, conn_name, channel, username, password,
		       queue_name, url, brokers, topic, created_at, updated_at
		FROM connections WHERE id=?`, id)
	return scanConnection(row)
}

func (r *ConnectionRepo) List(ctx context.Context) ([]*Connection, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, type, queue_manager, conn_name, channel, username, password,
		       queue_name, url, brokers, topic, created_at, updated_at
		FROM connections ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()
	var out []*Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanConnection(s scanner) (*Connection, error) {
	c := &Connection{}
	err := s.Scan(&c.ID, &c.Name, &c.Type, &c.QueueManager, &c.ConnName, &c.Channel,
		&c.Username, &c.Password, &c.QueueName, &c.URL, &c.Brokers, &c.Topic,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}
