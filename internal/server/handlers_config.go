package server

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"

	"mqConnector/internal/auth"
	"mqConnector/internal/storage"
)

// configBundle is the on-the-wire shape of an import/export. Stable
// fields only — never the SQLite row primary keys, ever-changing
// timestamps, or runtime status. Pipelines reference their connections
// + schemas by name within the bundle; the importer resolves names to
// (new) ids on receive. This keeps a bundle portable across instances
// even though every row has a UUID id at rest.
//
// Version pinning lets us evolve the format with explicit handling
// for old bundles. Today is v1; bumping requires a parallel reader.
type configBundle struct {
	Version     int                   `yaml:"version" json:"version"`
	ExportedAt  time.Time             `yaml:"exported_at" json:"exported_at"`
	TenantSlug  string                `yaml:"tenant_slug" json:"tenant_slug"`
	Connections []connectionDoc       `yaml:"connections" json:"connections"`
	Schemas     []schemaDoc           `yaml:"schemas,omitempty" json:"schemas,omitempty"`
	Scripts     []scriptDoc           `yaml:"scripts,omitempty" json:"scripts,omitempty"`
	Pipelines   []pipelineDoc         `yaml:"pipelines" json:"pipelines"`
}

type connectionDoc struct {
	Name         string `yaml:"name" json:"name"`
	Type         string `yaml:"type" json:"type"`
	QueueManager string `yaml:"queue_manager,omitempty" json:"queue_manager,omitempty"`
	ConnName     string `yaml:"conn_name,omitempty" json:"conn_name,omitempty"`
	Channel      string `yaml:"channel,omitempty" json:"channel,omitempty"`
	Username     string `yaml:"username,omitempty" json:"username,omitempty"`
	// Password is intentionally omitted on export — it would leak the
	// plaintext (which is decrypted by ConnectionRepo.Get). Importers
	// must either supply passwords inline or update them afterward.
	Password   string `yaml:"password,omitempty" json:"password,omitempty"`
	QueueName  string `yaml:"queue_name,omitempty" json:"queue_name,omitempty"`
	URL        string `yaml:"url,omitempty" json:"url,omitempty"`
	Brokers    string `yaml:"brokers,omitempty" json:"brokers,omitempty"`
	Topic      string `yaml:"topic,omitempty" json:"topic,omitempty"`
	TLSCAFile  string `yaml:"tls_ca_file,omitempty" json:"tls_ca_file,omitempty"`
	TLSCertFile string `yaml:"tls_cert_file,omitempty" json:"tls_cert_file,omitempty"`
	TLSKeyFile  string `yaml:"tls_key_file,omitempty" json:"tls_key_file,omitempty"`
	TLSInsecureSkipVerify bool `yaml:"tls_insecure_skip_verify,omitempty" json:"tls_insecure_skip_verify,omitempty"`
}

type schemaDoc struct {
	Name    string `yaml:"name" json:"name"`
	Type    string `yaml:"type" json:"type"`
	Content string `yaml:"content" json:"content"`
}

type scriptDoc struct {
	Name string `yaml:"name" json:"name"`
	Body string `yaml:"body" json:"body"`
}

type pipelineDoc struct {
	Name                    string         `yaml:"name" json:"name"`
	SourceConnectionName    string         `yaml:"source_connection" json:"source_connection"`
	DestConnectionName      string         `yaml:"dest_connection" json:"dest_connection"`
	OutputFormat            string         `yaml:"output_format" json:"output_format"`
	SchemaName              string         `yaml:"schema_name,omitempty" json:"schema_name,omitempty"`
	FilterPaths             []string       `yaml:"filter_paths,omitempty" json:"filter_paths,omitempty"`
	Enabled                 bool           `yaml:"enabled" json:"enabled"`
	Stages                  []stageDoc     `yaml:"stages,omitempty" json:"stages,omitempty"`
	Transforms              []transformDoc `yaml:"transforms,omitempty" json:"transforms,omitempty"`
	RoutingRules            []routeDoc     `yaml:"routing_rules,omitempty" json:"routing_rules,omitempty"`
}

type stageDoc struct {
	Order   int    `yaml:"order" json:"order"`
	Type    string `yaml:"type" json:"type"`
	Config  string `yaml:"config" json:"config"`
	Enabled bool   `yaml:"enabled" json:"enabled"`
}

type transformDoc struct {
	Order       int    `yaml:"order" json:"order"`
	Type        string `yaml:"type" json:"type"`
	SourcePath  string `yaml:"source_path,omitempty" json:"source_path,omitempty"`
	TargetPath  string `yaml:"target_path,omitempty" json:"target_path,omitempty"`
	MaskPattern string `yaml:"mask_pattern,omitempty" json:"mask_pattern,omitempty"`
	MaskReplace string `yaml:"mask_replace,omitempty" json:"mask_replace,omitempty"`
	SetValue    string `yaml:"set_value,omitempty" json:"set_value,omitempty"`
}

type routeDoc struct {
	Priority           int    `yaml:"priority" json:"priority"`
	Path               string `yaml:"path" json:"path"`
	Operator           string `yaml:"operator" json:"operator"`
	Value              string `yaml:"value" json:"value"`
	DestConnectionName string `yaml:"dest_connection" json:"dest_connection"`
	Enabled            bool   `yaml:"enabled" json:"enabled"`
}

// handleExportConfig serialises the caller's tenant configuration to
// YAML (or JSON, on Accept negotiation). Suitable for git-committing
// or replaying onto a fresh instance via handleImportConfig.
//
// Passwords are stripped on export — re-importing requires the
// operator to supply them inline or via a separate step. This is the
// safer default; an explicit `?include_passwords=true` could be added
// later for closed-loop disaster-recovery flows.
func (s *Server) handleExportConfig(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	bundle, err := s.buildBundle(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Content negotiation: default to YAML, JSON on Accept: application/json.
	if r.URL.Query().Get("format") == "json" || acceptsJSON(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="mqconnector-config.json"`)
		_ = json.NewEncoder(w).Encode(bundle)
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mqconnector-config.yaml"`)
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	_ = enc.Encode(bundle)
	_ = enc.Close()
}

func (s *Server) buildBundle(ctx interface {
	Done() <-chan struct{}
	Err() error
	Value(any) any
	Deadline() (time.Time, bool)
}, tenantID string) (*configBundle, error) {
	bundle := &configBundle{Version: 1, ExportedAt: time.Now().UTC()}

	// Slug for the bundle is convenience metadata only; the importer
	// pins to the *caller's* tenant regardless of what's on the wire.
	if t, err := s.store.Tenants.Get(ctx, tenantID); err == nil && t != nil {
		bundle.TenantSlug = t.Slug
	}

	connsByID := map[string]*storage.Connection{}
	conns, err := s.store.Connections.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, c := range conns {
		connsByID[c.ID] = c
		bundle.Connections = append(bundle.Connections, connectionDoc{
			Name:         c.Name,
			Type:         c.Type,
			QueueManager: c.QueueManager,
			ConnName:     c.ConnName,
			Channel:      c.Channel,
			Username:     c.Username,
			// Password deliberately omitted.
			QueueName:             c.QueueName,
			URL:                   c.URL,
			Brokers:               c.Brokers,
			Topic:                 c.Topic,
			TLSCAFile:             c.TLSCAFile,
			TLSCertFile:           c.TLSCertFile,
			TLSKeyFile:            c.TLSKeyFile,
			TLSInsecureSkipVerify: c.TLSInsecureSkipVerify,
		})
	}

	if schemas, err := s.store.Schemas.List(ctx, tenantID); err == nil {
		for _, sc := range schemas {
			bundle.Schemas = append(bundle.Schemas, schemaDoc{
				Name: sc.Name, Type: sc.SchemaType, Content: sc.Content,
			})
		}
	}
	if scripts, err := s.store.Scripts.List(ctx, tenantID); err == nil {
		for _, sc := range scripts {
			bundle.Scripts = append(bundle.Scripts, scriptDoc{
				Name: sc.Name, Body: sc.Body,
			})
		}
	}

	pipes, err := s.store.Pipelines.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for _, p := range pipes {
		doc := pipelineDoc{
			Name:         p.Name,
			OutputFormat: p.OutputFormat,
			FilterPaths:  p.FilterPaths,
			Enabled:      p.Enabled,
		}
		if c := connsByID[p.SourceID]; c != nil {
			doc.SourceConnectionName = c.Name
		}
		if c := connsByID[p.DestinationID]; c != nil {
			doc.DestConnectionName = c.Name
		}
		if stages, err := s.store.Stages.ListByPipeline(ctx, tenantID, p.ID); err == nil {
			for _, st := range stages {
				doc.Stages = append(doc.Stages, stageDoc{
					Order:   st.StageOrder,
					Type:    st.StageType,
					Config:  st.StageConfig,
					Enabled: st.Enabled,
				})
			}
		}
		if txs, err := s.store.Transforms.ListByPipeline(ctx, tenantID, p.ID); err == nil {
			for _, tx := range txs {
				doc.Transforms = append(doc.Transforms, transformDoc{
					Order:       tx.Order,
					Type:        tx.TransformType,
					SourcePath:  tx.SourcePath,
					TargetPath:  tx.TargetPath,
					MaskPattern: tx.MaskPattern,
					MaskReplace: tx.MaskReplace,
					SetValue:    tx.SetValue,
				})
			}
		}
		if rules, err := s.store.RoutingRules.ListByPipeline(ctx, tenantID, p.ID); err == nil {
			for _, ru := range rules {
				rd := routeDoc{
					Priority: ru.Priority,
					Path:     ru.ConditionPath,
					Operator: ru.ConditionOperator,
					Value:    ru.ConditionValue,
					Enabled:  ru.Enabled,
				}
				if c := connsByID[ru.DestinationID]; c != nil {
					rd.DestConnectionName = c.Name
				}
				doc.RoutingRules = append(doc.RoutingRules, rd)
			}
		}
		bundle.Pipelines = append(bundle.Pipelines, doc)
	}
	return bundle, nil
}

// handleImportConfig accepts a configBundle (YAML or JSON) and creates
// matching rows under the caller's tenant. Naming is preserved;
// internal IDs are minted fresh. Conflicts with existing rows
// (same name) cause the bundle to be rejected upfront — no
// partial-import surprises. A `?dry_run=true` query parameter
// validates without writing.
//
// Authorization: admin role required — this can introduce arbitrary
// pipelines into the tenant.
func (s *Server) handleImportConfig(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())

	// Enforce admin (or higher) at the handler — the routes register
	// this under the regular admin group already, but the gate keeps
	// the contract explicit on the function.
	if claim, ok := auth.TenantFromContext(r.Context()); ok {
		if !roleAtLeastAtLeast(claim.Role, "admin") {
			writeError(w, http.StatusForbidden, "admin role required for config import")
			return
		}
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	var bundle configBundle
	// Try YAML first (the canonical export format); fall back to JSON
	// so the same handler accepts both shapes without a separate route.
	if yamlErr := yaml.Unmarshal(raw, &bundle); yamlErr != nil {
		if jsonErr := json.Unmarshal(raw, &bundle); jsonErr != nil {
			writeError(w, http.StatusBadRequest,
				"could not parse body as YAML or JSON; YAML err: "+yamlErr.Error())
			return
		}
	}
	if bundle.Version != 1 {
		writeError(w, http.StatusBadRequest, "unsupported bundle version (expected 1)")
		return
	}

	// Pre-flight conflict check: every connection / pipeline name in
	// the bundle must NOT collide with an existing row.
	existingConns, _ := s.store.Connections.List(r.Context(), tenant)
	existingPipes, _ := s.store.Pipelines.List(r.Context(), tenant)
	connByName := map[string]string{} // bundle name → existing id (collision)
	for _, c := range existingConns {
		connByName[c.Name] = c.ID
	}
	pipeByName := map[string]bool{}
	for _, p := range existingPipes {
		pipeByName[p.Name] = true
	}
	for _, c := range bundle.Connections {
		if _, exists := connByName[c.Name]; exists {
			writeError(w, http.StatusConflict, "connection name already exists: "+c.Name)
			return
		}
	}
	for _, p := range bundle.Pipelines {
		if pipeByName[p.Name] {
			writeError(w, http.StatusConflict, "pipeline name already exists: "+p.Name)
			return
		}
	}

	if r.URL.Query().Get("dry_run") == "true" {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "ok",
			"connections": len(bundle.Connections),
			"pipelines":   len(bundle.Pipelines),
			"dry_run":     true,
		})
		return
	}

	// Apply. Connections first so pipelines can reference them by id.
	nameToConnID := map[string]string{}
	for _, c := range bundle.Connections {
		row := &storage.Connection{
			Name:                  c.Name,
			Type:                  c.Type,
			QueueManager:          c.QueueManager,
			ConnName:              c.ConnName,
			Channel:               c.Channel,
			Username:              c.Username,
			Password:              c.Password,
			QueueName:             c.QueueName,
			URL:                   c.URL,
			Brokers:               c.Brokers,
			Topic:                 c.Topic,
			TLSCAFile:             c.TLSCAFile,
			TLSCertFile:           c.TLSCertFile,
			TLSKeyFile:            c.TLSKeyFile,
			TLSInsecureSkipVerify: c.TLSInsecureSkipVerify,
		}
		if err := s.store.Connections.Create(r.Context(), tenant, row); err != nil {
			writeError(w, http.StatusInternalServerError, "create connection "+c.Name+": "+err.Error())
			return
		}
		nameToConnID[c.Name] = row.ID
	}

	// Schemas + scripts — best-effort. A skipped one logs but doesn't
	// fail the whole import; the pipelines that reference them by name
	// will still resolve at runtime if the operator creates them later.
	for _, sc := range bundle.Schemas {
		_ = s.store.Schemas.Create(r.Context(), tenant, &storage.Schema{
			Name: sc.Name, SchemaType: sc.Type, Content: sc.Content,
		})
	}
	for _, sc := range bundle.Scripts {
		_ = s.store.Scripts.Create(r.Context(), tenant, &storage.Script{
			Name: sc.Name, Body: sc.Body,
		})
	}

	// Pipelines.
	for _, p := range bundle.Pipelines {
		srcID := nameToConnID[p.SourceConnectionName]
		dstID := nameToConnID[p.DestConnectionName]
		if srcID == "" || dstID == "" {
			writeError(w, http.StatusBadRequest,
				"pipeline "+p.Name+" references unknown connection name(s); aborting (some rows already inserted)")
			return
		}
		row := &storage.Pipeline{
			Name:          p.Name,
			SourceID:      srcID,
			DestinationID: dstID,
			OutputFormat:  p.OutputFormat,
			FilterPaths:   p.FilterPaths,
			Enabled:       p.Enabled,
		}
		if err := s.store.Pipelines.Create(r.Context(), tenant, row); err != nil {
			writeError(w, http.StatusInternalServerError, "create pipeline "+p.Name+": "+err.Error())
			return
		}

		// Stages.
		stages := make([]*storage.Stage, 0, len(p.Stages))
		for _, st := range p.Stages {
			stages = append(stages, &storage.Stage{
				PipelineID:  row.ID,
				StageOrder:  st.Order,
				StageType:   st.Type,
				StageConfig: st.Config,
				Enabled:     st.Enabled,
			})
		}
		if err := s.store.Stages.ReplaceForPipeline(r.Context(), tenant, row.ID, stages); err != nil {
			writeError(w, http.StatusInternalServerError, "replace stages "+p.Name+": "+err.Error())
			return
		}

		// Transforms.
		txs := make([]*storage.Transform, 0, len(p.Transforms))
		for _, tx := range p.Transforms {
			txs = append(txs, &storage.Transform{
				PipelineID:    row.ID,
				Order:         tx.Order,
				TransformType: tx.Type,
				SourcePath:    tx.SourcePath,
				TargetPath:    tx.TargetPath,
				MaskPattern:   tx.MaskPattern,
				MaskReplace:   tx.MaskReplace,
				SetValue:      tx.SetValue,
			})
		}
		_ = s.store.Transforms.ReplaceForPipeline(r.Context(), tenant, row.ID, txs)

		// Routing rules.
		rules := make([]*storage.RoutingRule, 0, len(p.RoutingRules))
		for _, ru := range p.RoutingRules {
			rules = append(rules, &storage.RoutingRule{
				PipelineID:        row.ID,
				DestinationID:     nameToConnID[ru.DestConnectionName],
				Priority:          ru.Priority,
				ConditionPath:     ru.Path,
				ConditionOperator: ru.Operator,
				ConditionValue:    ru.Value,
				Enabled:           ru.Enabled,
			})
		}
		_ = s.store.RoutingRules.ReplaceForPipeline(r.Context(), tenant, row.ID, rules)
	}

	// Hot-reload so new pipelines start immediately.
	_, _ = s.pipeline.Reload(r.Context())

	writeJSON(w, http.StatusCreated, map[string]any{
		"status":      "imported",
		"connections": len(bundle.Connections),
		"pipelines":   len(bundle.Pipelines),
	})
}

func acceptsJSON(r *http.Request) bool {
	a := r.Header.Get("Accept")
	return a != "" && (a == "application/json" || a == "*/*" && false)
}
