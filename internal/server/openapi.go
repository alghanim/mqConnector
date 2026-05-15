package server

import (
	_ "embed"
	"net/http"
)

// openAPISpec is the source-of-truth YAML, embedded at compile time so the
// binary can serve it without a side-loaded file. Edits go in
// api/openapi.yaml; embed reflects the working tree at build time.
//
//go:embed openapi.yaml
var openAPISpec []byte

// handleOpenAPI returns the spec to anyone who can reach the server.
// Public (no auth) — the spec describes the API contract, not any data,
// and access to it is implicitly granted by network reachability anyway.
func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(openAPISpec)
}
