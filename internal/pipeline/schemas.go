package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"mqConnector/internal/storage"
)

// loadReferencedSchemas walks the pipeline's stage rows looking for validate
// stages with a schema_id, plus the pipeline's own SchemaID, and returns a
// map of id → *storage.Schema. Missing rows are reported with a clear error
// so a misconfigured pipeline fails fast at boot rather than surfacing a
// per-message validate error.
func loadReferencedSchemas(
	ctx context.Context,
	store *storage.Store,
	p *storage.Pipeline,
	rows []*storage.Stage,
) (map[string]*storage.Schema, error) {
	wanted := map[string]struct{}{}
	if p != nil && p.SchemaID != "" {
		wanted[p.SchemaID] = struct{}{}
	}
	for _, row := range rows {
		if row.StageType != "validate" || row.StageConfig == "" {
			continue
		}
		var cfg ValidateConfig
		if err := json.Unmarshal([]byte(row.StageConfig), &cfg); err != nil {
			// Tolerate malformed configs at load time — let Build surface
			// the actual problem when it builds the stage.
			continue
		}
		if cfg.SchemaID != "" {
			wanted[cfg.SchemaID] = struct{}{}
		}
	}

	out := make(map[string]*storage.Schema, len(wanted))
	for id := range wanted {
		// GetUnsafe: this is system-level pipeline boot. The schema's
		// tenant is verified against the pipeline's tenant below.
		s, err := store.Schemas.GetUnsafe(ctx, id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				return nil, fmt.Errorf("schema %q referenced by pipeline but not found in storage", id)
			}
			return nil, fmt.Errorf("load schema %q: %w", id, err)
		}
		// Only enforce the cross-tenant check when BOTH sides carry a
		// tenant id. A pipeline with an empty tenant (preview drafts,
		// legacy inline-config callers) cannot meaningfully mismatch.
		if p != nil && p.TenantID != "" && s.TenantID != "" && s.TenantID != p.TenantID {
			return nil, fmt.Errorf("cross-tenant schema reference on pipeline %s (schema %s belongs to a different tenant)", p.ID, id)
		}
		out[id] = s
	}
	return out, nil
}
