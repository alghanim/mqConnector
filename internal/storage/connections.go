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

// Sealer is the minimal interface ConnectionRepo needs to optionally
// encrypt/decrypt the Password field at rest. internal/secrets.Service
// satisfies it. A nil Sealer is treated as identity (no encryption).
type Sealer interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(value string) (string, error)
}

type ConnectionRepo struct {
	db     *sql.DB
	sealer Sealer // optional; nil means store plaintext
}

// WithSealer returns a copy of the repo that transparently encrypts the
// Password column on writes and decrypts it on reads. Plaintext rows
// already in the database are returned unchanged (the sealer recognises
// non-prefixed values).
func (r *ConnectionRepo) WithSealer(s Sealer) *ConnectionRepo {
	return &ConnectionRepo{db: r.db, sealer: s}
}

func (r *ConnectionRepo) seal(v string) (string, error) {
	if r.sealer == nil {
		return v, nil
	}
	return r.sealer.Encrypt(v)
}
func (r *ConnectionRepo) unseal(v string) (string, error) {
	if r.sealer == nil {
		return v, nil
	}
	return r.sealer.Decrypt(v)
}

func (r *ConnectionRepo) Create(ctx context.Context, c *Connection) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	c.CreatedAt = time.Now().UTC()
	c.UpdatedAt = c.CreatedAt
	pw, err := r.seal(c.Password)
	if err != nil {
		return fmt.Errorf("seal password: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO connections (id, name, type, queue_manager, conn_name, channel,
		                         username, password, queue_name, url, brokers, topic,
		                         created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Type, c.QueueManager, c.ConnName, c.Channel,
		c.Username, pw, c.QueueName, c.URL, c.Brokers, c.Topic,
		c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert connection: %w", err)
	}
	return nil
}

func (r *ConnectionRepo) Update(ctx context.Context, c *Connection) error {
	c.UpdatedAt = time.Now().UTC()
	pw, err := r.seal(c.Password)
	if err != nil {
		return fmt.Errorf("seal password: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE connections SET name=?, type=?, queue_manager=?, conn_name=?, channel=?,
		                       username=?, password=?, queue_name=?, url=?, brokers=?,
		                       topic=?, updated_at=?
		WHERE id=?`,
		c.Name, c.Type, c.QueueManager, c.ConnName, c.Channel,
		c.Username, pw, c.QueueName, c.URL, c.Brokers, c.Topic,
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
	return r.scanConnection(row)
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
		c, err := r.scanConnection(rows)
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

func (r *ConnectionRepo) scanConnection(s scanner) (*Connection, error) {
	c := &Connection{}
	err := s.Scan(&c.ID, &c.Name, &c.Type, &c.QueueManager, &c.ConnName, &c.Channel,
		&c.Username, &c.Password, &c.QueueName, &c.URL, &c.Brokers, &c.Topic,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	pw, derr := r.unseal(c.Password)
	if derr != nil {
		return nil, fmt.Errorf("unseal password: %w", derr)
	}
	c.Password = pw
	return c, nil
}
