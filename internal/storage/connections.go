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

// PasswordRewrapper rewraps a stored ciphertext under whatever the
// current key happens to be. Plaintext / empty / already-current
// values pass through unchanged. secrets.Service satisfies this.
type PasswordRewrapper interface {
	Rewrap(value string) (string, error)
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

// ErrTenantRequired is returned when a repo method is called without a
// tenantID. Multi-tenant safety net — never return data unscoped.
var ErrTenantRequired = errors.New("storage: tenant_id is required")

// Create inserts a connection scoped to the given tenant. The tenant_id
// on the Connection struct is overwritten with the argument so callers
// can't accidentally bypass scoping by setting the field directly.
func (r *ConnectionRepo) Create(ctx context.Context, tenantID string, c *Connection) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	c.TenantID = tenantID
	c.CreatedAt = time.Now().UTC()
	c.UpdatedAt = c.CreatedAt
	pw, err := r.seal(c.Password)
	if err != nil {
		return fmt.Errorf("seal password: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO connections (id, tenant_id, name, type, queue_manager, conn_name, channel,
		                         username, password, queue_name, url, brokers, topic,
		                         tls_ca_file, tls_cert_file, tls_key_file, tls_insecure_skip_verify,
		                         client_id, stream_name, consumer_name, qos, group_id,
		                         created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, tenantID, c.Name, c.Type, c.QueueManager, c.ConnName, c.Channel,
		c.Username, pw, c.QueueName, c.URL, c.Brokers, c.Topic,
		c.TLSCAFile, c.TLSCertFile, c.TLSKeyFile, boolToInt(c.TLSInsecureSkipVerify),
		c.ClientID, c.StreamName, c.ConsumerName, c.QoS, c.GroupID,
		c.CreatedAt, c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert connection: %w", err)
	}
	return nil
}

// Update modifies a connection if it exists in the named tenant. Cross-
// tenant updates return ErrNotFound (deliberately indistinguishable
// from "wrong id" — no information leak about other tenants).
func (r *ConnectionRepo) Update(ctx context.Context, tenantID string, c *Connection) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	c.UpdatedAt = time.Now().UTC()
	pw, err := r.seal(c.Password)
	if err != nil {
		return fmt.Errorf("seal password: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE connections SET name=?, type=?, queue_manager=?, conn_name=?, channel=?,
		                       username=?, password=?, queue_name=?, url=?, brokers=?,
		                       topic=?, tls_ca_file=?, tls_cert_file=?, tls_key_file=?,
		                       tls_insecure_skip_verify=?,
		                       client_id=?, stream_name=?, consumer_name=?, qos=?, group_id=?,
		                       updated_at=?
		WHERE id=? AND tenant_id=?`,
		c.Name, c.Type, c.QueueManager, c.ConnName, c.Channel,
		c.Username, pw, c.QueueName, c.URL, c.Brokers, c.Topic,
		c.TLSCAFile, c.TLSCertFile, c.TLSKeyFile, boolToInt(c.TLSInsecureSkipVerify),
		c.ClientID, c.StreamName, c.ConsumerName, c.QoS, c.GroupID,
		c.UpdatedAt, c.ID, tenantID)
	if err != nil {
		return fmt.Errorf("update connection: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *ConnectionRepo) Delete(ctx context.Context, tenantID, id string) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM connections WHERE id=? AND tenant_id=?`, id, tenantID)
	if err != nil {
		return fmt.Errorf("delete connection: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *ConnectionRepo) Get(ctx context.Context, tenantID, id string) (*Connection, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	row := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, name, type, queue_manager, conn_name, channel, username, password,
		       queue_name, url, brokers, topic,
		       tls_ca_file, tls_cert_file, tls_key_file, tls_insecure_skip_verify,
		       client_id, stream_name, consumer_name, qos, group_id,
		       created_at, updated_at
		FROM connections WHERE id=? AND tenant_id=?`, id, tenantID)
	return r.scanConnection(row)
}

// GetUnsafe reads a connection by id only, bypassing tenant scoping.
// Used by internal subsystems (pipeline executor, DLQ retry) that
// already trust the id they hold because it came from a tenant-scoped
// query. NOT exposed via HTTP.
func (r *ConnectionRepo) GetUnsafe(ctx context.Context, id string) (*Connection, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, name, type, queue_manager, conn_name, channel, username, password,
		       queue_name, url, brokers, topic,
		       tls_ca_file, tls_cert_file, tls_key_file, tls_insecure_skip_verify,
		       client_id, stream_name, consumer_name, qos, group_id,
		       created_at, updated_at
		FROM connections WHERE id=?`, id)
	return r.scanConnection(row)
}

func (r *ConnectionRepo) List(ctx context.Context, tenantID string) ([]*Connection, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, name, type, queue_manager, conn_name, channel, username, password,
		       queue_name, url, brokers, topic,
		       tls_ca_file, tls_cert_file, tls_key_file, tls_insecure_skip_verify,
		       client_id, stream_name, consumer_name, qos, group_id,
		       created_at, updated_at
		FROM connections WHERE tenant_id=? ORDER BY name`, tenantID)
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

// ListAll walks every connection across every tenant. System-level
// only — used by the rotate-secrets subcommand. NOT exposed via HTTP.
func (r *ConnectionRepo) ListAll(ctx context.Context) ([]*Connection, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, name, type, queue_manager, conn_name, channel, username, password,
		       queue_name, url, brokers, topic,
		       tls_ca_file, tls_cert_file, tls_key_file, tls_insecure_skip_verify,
		       client_id, stream_name, consumer_name, qos, group_id,
		       created_at, updated_at
		FROM connections ORDER BY tenant_id, name`)
	if err != nil {
		return nil, fmt.Errorf("list all connections: %w", err)
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

// RewrapPasswords runs through every stored connection password and
// re-encrypts it under whatever ciphertext the rewrapper returns. Used
// by the secrets rotation endpoint: after a Rotate() the operator
// calls this once and every old-version row is upgraded in place.
//
// Reads the raw password column directly (no Decrypt round-trip) so
// rows that are *already* at the current version pass through
// unchanged — Rewrap is the no-op fast path there. Plaintext rows
// (legacy, pre-encryption) get encrypted under the current key on
// first call.
//
// Returns (rewrapped, skipped, error). `skipped` is non-zero when
// some rows produced an error; the rest still got their update.
func (r *ConnectionRepo) RewrapPasswords(ctx context.Context, rw PasswordRewrapper) (int, int, error) {
	if rw == nil {
		return 0, 0, errors.New("rewrap: nil rewrapper")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, password FROM connections`)
	if err != nil {
		return 0, 0, fmt.Errorf("list passwords: %w", err)
	}
	type row struct{ id, pw string }
	var items []row
	for rows.Next() {
		var it row
		if err := rows.Scan(&it.id, &it.pw); err != nil {
			rows.Close()
			return 0, 0, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	// For plaintext rows we wrap with Encrypt-equivalent semantics —
	// Rewrap already does that: a value without the prefix is treated
	// as plaintext and encrypted under the current key when the
	// wrapper's Rewrap implementation calls Encrypt. secrets.Service
	// does exactly that.
	rewrapped, skipped := 0, 0
	for _, it := range items {
		newPW, err := rw.Rewrap(it.pw)
		if err != nil {
			skipped++
			continue
		}
		if newPW == it.pw {
			continue
		}
		if _, err := r.db.ExecContext(ctx,
			`UPDATE connections SET password = ?, updated_at = ? WHERE id = ?`,
			newPW, time.Now().UTC(), it.id); err != nil {
			skipped++
			continue
		}
		rewrapped++
	}
	return rewrapped, skipped, nil
}

type scanner interface {
	Scan(dest ...any) error
}

// boolToInt maps Go bool to SQLite's 0/1 INTEGER convention. Use only
// for columns declared INTEGER, not for BOOLEAN typed columns elsewhere.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (r *ConnectionRepo) scanConnection(s scanner) (*Connection, error) {
	c := &Connection{}
	var skip int
	err := s.Scan(&c.ID, &c.TenantID, &c.Name, &c.Type, &c.QueueManager, &c.ConnName, &c.Channel,
		&c.Username, &c.Password, &c.QueueName, &c.URL, &c.Brokers, &c.Topic,
		&c.TLSCAFile, &c.TLSCertFile, &c.TLSKeyFile, &skip,
		&c.ClientID, &c.StreamName, &c.ConsumerName, &c.QoS, &c.GroupID,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.TLSInsecureSkipVerify = skip != 0
	pw, derr := r.unseal(c.Password)
	if derr != nil {
		return nil, fmt.Errorf("unseal password: %w", derr)
	}
	c.Password = pw
	return c, nil
}
