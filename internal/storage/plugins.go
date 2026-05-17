package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Plugin is a WASM blob uploaded by an operator. Pipelines reference
// it via stage_type='wasm' with stage_config={"plugin":"<name>"}.
type Plugin struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenant_id"`
	Name       string    `json:"name"`
	SHA256     string    `json:"sha256"`
	SizeBytes  int       `json:"size_bytes"`
	UploadedBy string    `json:"uploaded_by"`
	UploadedAt time.Time `json:"uploaded_at"`
	// Blob is populated on Get; omitted by List (the blob is bytes
	// you stream as a download, not part of the metadata listing).
	Blob []byte `json:"-"`
}

type PluginRepo struct{ db *dbWrap }

// Upsert inserts the plugin or replaces it if a row already exists
// for (tenant_id, name). The sha256 + blob get refreshed on every
// upload. Repeat uploads of the same content are a no-op apart from
// the uploaded_at timestamp.
func (r *PluginRepo) Upsert(ctx context.Context, p *Plugin) error {
	if p.TenantID == "" {
		return ErrTenantRequired
	}
	if p.Name == "" {
		return errors.New("plugin: name required")
	}
	if len(p.Blob) == 0 {
		return errors.New("plugin: blob required")
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	sum := sha256.Sum256(p.Blob)
	p.SHA256 = hex.EncodeToString(sum[:])
	p.SizeBytes = len(p.Blob)
	p.UploadedAt = time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO plugins (id, tenant_id, name, sha256, blob, size_bytes, uploaded_by, uploaded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tenant_id, name) DO UPDATE SET
			sha256 = excluded.sha256,
			blob = excluded.blob,
			size_bytes = excluded.size_bytes,
			uploaded_by = excluded.uploaded_by,
			uploaded_at = excluded.uploaded_at`,
		p.ID, p.TenantID, p.Name, p.SHA256, p.Blob, p.SizeBytes, p.UploadedBy, p.UploadedAt)
	if err != nil {
		return fmt.Errorf("upsert plugin: %w", err)
	}
	return nil
}

// List returns plugin metadata for the tenant — no blobs. Use Get
// to fetch a single plugin's bytes for execution or download.
func (r *PluginRepo) List(ctx context.Context, tenantID string) ([]*Plugin, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, tenant_id, name, sha256, size_bytes, uploaded_by, uploaded_at
		FROM plugins WHERE tenant_id=? ORDER BY name`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list plugins: %w", err)
	}
	defer rows.Close()
	var out []*Plugin
	for rows.Next() {
		p := &Plugin{}
		if err := rows.Scan(&p.ID, &p.TenantID, &p.Name, &p.SHA256, &p.SizeBytes,
			&p.UploadedBy, &p.UploadedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get returns the full plugin including the blob. Used by the
// plugin loader at pipeline build time and by the download endpoint.
func (r *PluginRepo) Get(ctx context.Context, tenantID, name string) (*Plugin, error) {
	if tenantID == "" {
		return nil, ErrTenantRequired
	}
	p := &Plugin{}
	err := r.db.QueryRowContext(ctx, `
		SELECT id, tenant_id, name, sha256, blob, size_bytes, uploaded_by, uploaded_at
		FROM plugins WHERE tenant_id=? AND name=?`, tenantID, name).
		Scan(&p.ID, &p.TenantID, &p.Name, &p.SHA256, &p.Blob, &p.SizeBytes,
			&p.UploadedBy, &p.UploadedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

// Delete removes the plugin row. Stages referencing it will fail to
// build on the next reload; operators are expected to disable
// dependent pipelines before deleting.
func (r *PluginRepo) Delete(ctx context.Context, tenantID, name string) error {
	if tenantID == "" {
		return ErrTenantRequired
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM plugins WHERE tenant_id=? AND name=?`, tenantID, name)
	if err != nil {
		return fmt.Errorf("delete plugin: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
