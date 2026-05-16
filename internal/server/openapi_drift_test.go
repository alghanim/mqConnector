package server

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
)

// TestOpenAPIDrift asserts the chi router and internal/server/openapi.yaml
// describe the same set of (path, method) pairs.
//
// The check is bidirectional:
//   - Every (path, method) the chi router serves under /api/* must be
//     documented in openapi.yaml (otherwise SDK consumers miss it).
//   - Every (path, method) in openapi.yaml must be served by the chi
//     router (otherwise the spec lies about what's available).
//
// Why a test instead of CI lint: the chi router only exists once it's
// wired up by Server.routes(). The cheapest way to introspect it is to
// build a Server with mocked dependencies and walk the live mux. Any
// drift fails the test with a precise diff so the fix is obvious.
func TestOpenAPIDrift(t *testing.T) {
	// Use the existing test-server helper so all dependencies (cfg,
	// auth, store, pool, manager) wire up exactly the way prod does.
	// We never execute a handler — just walk the chi mux.
	_, srv, _ := newTestServer(t)

	chiRoutes := collectChiRoutes(t, srv)
	specRoutes := collectSpecRoutes(t, "openapi.yaml")

	// Routes intentionally outside the public REST API spec:
	//   - "GET /*" is the embedded SvelteKit static handler.
	// Everything below is *known tech debt* — phase-22-and-later
	// endpoints that were added before openapi.yaml caught up. The
	// allowlist makes the gap explicit and shrinks as routes are
	// documented; any NEW undocumented route trips this test.
	allowChiOnly := map[string]bool{
		"GET /*": true,
		// TODO(openapi): document the following endpoints. Once a route
		// is added to internal/server/openapi.yaml, delete it from this
		// list — the drift test will then guard it against regression.
		"DELETE /api/v1/tokens/{id}":          true,
		"DELETE /api/v1/webhooks/{id}":        true,
		"GET /api/openapi.yaml":               true,
		"GET /api/v1/audit/verify":            true,
		"GET /api/v1/audit/{id}/diff":         true,
		"GET /api/v1/config/export":           true,
		"GET /api/v1/events":                  true,
		"GET /api/v1/leadership":              true,
		"GET /api/v1/secrets/status":          true,
		"GET /api/v1/tokens":                  true,
		"GET /api/v1/webhooks":                true,
		"POST /api/v1/config/import":          true,
		"POST /api/v1/pipelines/{id}/replay":  true,
		"POST /api/v1/preview":                true,
		"POST /api/v1/samples/extract":        true,
		"POST /api/v1/secrets/rotate":         true,
		"POST /api/v1/tokens":                 true,
		"POST /api/v1/webhooks":               true,
		"PUT /api/v1/webhooks/{id}":           true,
	}

	var missingFromSpec, missingFromChi []string
	for r := range chiRoutes {
		if allowChiOnly[r] {
			continue
		}
		if !specRoutes[r] {
			missingFromSpec = append(missingFromSpec, r)
		}
	}
	for r := range specRoutes {
		if !chiRoutes[r] {
			missingFromChi = append(missingFromChi, r)
		}
	}
	sort.Strings(missingFromSpec)
	sort.Strings(missingFromChi)

	if len(missingFromSpec) > 0 {
		t.Errorf("routes served by chi but missing from openapi.yaml:\n  %s\n\nAdd them to internal/server/openapi.yaml or remove them from the router.",
			strings.Join(missingFromSpec, "\n  "))
	}
	if len(missingFromChi) > 0 {
		t.Errorf("paths in openapi.yaml not served by chi:\n  %s\n\nRemove them from the spec or implement them in routes.go.",
			strings.Join(missingFromChi, "\n  "))
	}
}

// collectChiRoutes builds the router, walks every registered pattern,
// and normalises method+path into the same shape the OpenAPI spec
// uses.
func collectChiRoutes(t *testing.T, s *Server) map[string]bool {
	t.Helper()
	router := s.routes().(chi.Router)
	out := map[string]bool{}
	walker := func(method string, route string, handler http.Handler, middlewares ...func(http.Handler) http.Handler) error {
		// chi reports routes with a trailing slash on every nested Route
		// (e.g. "/api/v1/connections/"). The OpenAPI spec uses the
		// no-trailing-slash form; normalise to match.
		p := strings.TrimSuffix(route, "/")
		if p == "" {
			p = "/"
		}
		out[fmt.Sprintf("%s %s", method, p)] = true
		return nil
	}
	if err := chi.Walk(router, walker); err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	return out
}

// collectSpecRoutes parses openapi.yaml and emits one entry per
// (path, method) pair. Multiple methods on the same path get one
// entry each.
func collectSpecRoutes(t *testing.T, path string) map[string]bool {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(body, &spec); err != nil {
		t.Fatalf("unmarshal openapi: %v", err)
	}
	out := map[string]bool{}
	known := map[string]bool{
		"get": true, "post": true, "put": true, "patch": true,
		"delete": true, "head": true, "options": true,
	}
	for p, methods := range spec.Paths {
		for m := range methods {
			if !known[strings.ToLower(m)] {
				continue // skip "parameters", "summary", "description" etc.
			}
			out[fmt.Sprintf("%s %s", strings.ToUpper(m), p)] = true
		}
	}
	return out
}
