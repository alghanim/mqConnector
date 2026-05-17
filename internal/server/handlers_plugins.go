package server

import (
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"mqConnector/internal/auth"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/storage"
)

// Plugin endpoints — system-admin only, rate-limited as a sensitive
// route. Plugins execute arbitrary WASM in-process; gating uploads
// behind the strongest admin check is the only safe posture.

const maxPluginUploadBytes = 16 * 1024 * 1024 // 16 MiB

// handleListPlugins returns plugin metadata for the caller's tenant.
// Blobs are omitted — the list view is for the operator UI.
func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	tenant := auth.TenantID(r.Context())
	rows, err := s.store.Plugins.List(r.Context(), tenant)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list plugins: "+err.Error())
		return
	}
	if rows == nil {
		rows = []*storage.Plugin{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleUploadPlugin accepts a multipart "blob" field carrying the
// .wasm bytes plus a "name" form value. Compiles the blob through
// the configured wazero runtime to validate it before storing — a
// bad upload fails here rather than at pipeline reload time.
func (s *Server) handleUploadPlugin(w http.ResponseWriter, r *http.Request) {
	if !s.isSystemAdmin(r) {
		writeError(w, http.StatusForbidden, "system-admin required")
		return
	}
	if err := r.ParseMultipartForm(maxPluginUploadBytes); err != nil {
		writeError(w, http.StatusBadRequest, "parse multipart: "+err.Error())
		return
	}
	name := r.FormValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	f, _, err := r.FormFile("blob")
	if err != nil {
		writeError(w, http.StatusBadRequest, "blob file required: "+err.Error())
		return
	}
	defer f.Close()
	// Cap the read at maxPluginUploadBytes — a malicious client
	// claiming a small multipart-form-size but streaming gigabytes
	// would otherwise pin memory.
	blob, err := io.ReadAll(io.LimitReader(f, maxPluginUploadBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read blob: "+err.Error())
		return
	}
	if len(blob) > maxPluginUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			"plugin blob exceeds 16 MiB cap")
		return
	}
	// Validate via the runtime BEFORE persisting. CompileWasm checks
	// memory limits, refuses host imports, and confirms the required
	// exports exist.
	if s.wasmRuntime == nil {
		writeError(w, http.StatusServiceUnavailable, "wasm runtime not initialised")
		return
	}
	mod, err := pipeline.CompileWasm(r.Context(), s.wasmRuntime, blob, pipeline.DefaultWasmLimits)
	if err != nil {
		writeError(w, http.StatusBadRequest, "plugin validation failed: "+err.Error())
		return
	}
	_ = mod.Close(r.Context())

	tenant := auth.TenantID(r.Context())
	user, _ := auth.UserFromContext(r.Context())
	uploadedBy := ""
	if user != nil {
		uploadedBy = user.Sub
	}
	p := &storage.Plugin{
		TenantID:   tenant,
		Name:       name,
		Blob:       blob,
		UploadedBy: uploadedBy,
	}
	if err := s.store.Plugins.Upsert(r.Context(), p); err != nil {
		writeError(w, http.StatusInternalServerError, "store plugin: "+err.Error())
		return
	}
	// Don't echo the blob back; the response is metadata only.
	p.Blob = nil
	writeJSON(w, http.StatusCreated, p)
}

// handleGetPlugin streams the plugin blob for download. Useful for
// operators verifying what's loaded vs what they built.
func (s *Server) handleGetPlugin(w http.ResponseWriter, r *http.Request) {
	if !s.isSystemAdmin(r) {
		writeError(w, http.StatusForbidden, "system-admin required")
		return
	}
	name := chi.URLParam(r, "name")
	tenant := auth.TenantID(r.Context())
	p, err := s.store.Plugins.Get(r.Context(), tenant, name)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "plugin not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/wasm")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+p.Name+`.wasm"`)
	_, _ = w.Write(p.Blob)
}

// handleDeletePlugin removes a plugin row. Pipelines still
// referencing the plugin will fail to build on the next reload;
// operators are expected to disable affected pipelines first.
func (s *Server) handleDeletePlugin(w http.ResponseWriter, r *http.Request) {
	if !s.isSystemAdmin(r) {
		writeError(w, http.StatusForbidden, "system-admin required")
		return
	}
	name := chi.URLParam(r, "name")
	tenant := auth.TenantID(r.Context())
	if err := s.store.Plugins.Delete(r.Context(), tenant, name); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeError(w, http.StatusNotFound, "plugin not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
