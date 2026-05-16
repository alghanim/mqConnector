package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestConfig_ExportThenImport_RoundTrips creates two connections and
// a pipeline, exports the tenant's config, deletes everything, then
// imports the captured YAML back. The resulting tenant should look
// identical to the original (same names, same wiring).
func TestConfig_ExportThenImport_RoundTrips(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	// 1. Seed two connections + a pipeline.
	src := postConn(t, h, cookie, `{"name":"src","type":"rabbitmq","url":"amqp://x","queue_name":"q-src"}`)
	dst := postConn(t, h, cookie, `{"name":"dst","type":"rabbitmq","url":"amqp://x","queue_name":"q-dst"}`)

	rec := httptest.NewRecorder()
	body := `{"name":"main","source_id":"` + src.ID + `","destination_id":"` + dst.ID + `","output_format":"same","filter_paths":[],"enabled":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("create pipeline: status=%d body=%s", rec.Code, rec.Body)
	}

	// 2. Export. Both YAML (default) and JSON should work.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/config/export?format=json", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export json: status=%d body=%s", rec.Code, rec.Body)
	}
	exportedJSON := rec.Body.String()
	if !strings.Contains(exportedJSON, `"name":"main"`) ||
		!strings.Contains(exportedJSON, `"source_connection":"src"`) {
		t.Fatalf("exported JSON missing expected fields: %s", exportedJSON)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/config/export", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export yaml: status=%d body=%s", rec.Code, rec.Body)
	}
	exportedYAML, _ := io.ReadAll(rec.Body)
	var bundle configBundle
	if err := yaml.Unmarshal(exportedYAML, &bundle); err != nil {
		t.Fatalf("YAML round-trip parse failed: %v\nbody:\n%s", err, exportedYAML)
	}
	if bundle.Version != 1 || len(bundle.Connections) != 2 || len(bundle.Pipelines) != 1 {
		t.Fatalf("exported bundle wrong shape: %+v", bundle)
	}
	if bundle.Pipelines[0].SourceConnectionName != "src" ||
		bundle.Pipelines[0].DestConnectionName != "dst" {
		t.Fatalf("exported pipeline references wrong connection names: %+v", bundle.Pipelines[0])
	}

	// 3. Delete everything (pipeline first, then connections).
	pipeID := getFirstPipelineID(t, h, cookie)
	deleteEntity(t, h, cookie, "/api/v1/pipelines/"+pipeID)
	deleteEntity(t, h, cookie, "/api/v1/connections/"+src.ID)
	deleteEntity(t, h, cookie, "/api/v1/connections/"+dst.ID)

	// 4. Import the captured YAML.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/config/import", strings.NewReader(string(exportedYAML)))
	req.Header.Set("Content-Type", "application/yaml")
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("import: status=%d body=%s", rec.Code, rec.Body)
	}

	// 5. Verify the names came back.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	var conns []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &conns)
	gotNames := map[string]bool{}
	for _, c := range conns {
		if n, _ := c["name"].(string); n != "" {
			gotNames[n] = true
		}
	}
	if !gotNames["src"] || !gotNames["dst"] {
		t.Errorf("imported connections missing: got %v", gotNames)
	}
	_ = srv
}

// TestConfig_ImportRejectsConflicts catches duplicate names before
// any rows are written — no partial-import surprises.
func TestConfig_ImportRejectsConflicts(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	// Seed one connection.
	postConn(t, h, cookie, `{"name":"existing","type":"rabbitmq","url":"amqp://x","queue_name":"q"}`)

	// Try to import a bundle that contains the same connection name.
	body := `{
		"version": 1,
		"connections": [{"name":"existing","type":"rabbitmq","url":"amqp://y","queue_name":"q2"}],
		"pipelines": []
	}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got status=%d body=%s", rec.Code, rec.Body)
	}
}

// TestConfig_ImportDryRunDoesntWrite confirms ?dry_run=true validates
// the bundle but leaves the tenant unchanged.
func TestConfig_ImportDryRunDoesntWrite(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	body := `{
		"version": 1,
		"connections": [{"name":"new-conn","type":"rabbitmq","url":"amqp://z","queue_name":"q"}],
		"pipelines": []
	}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/import?dry_run=true", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run: status=%d body=%s", rec.Code, rec.Body)
	}

	// Confirm no rows landed.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/connections", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `"name":"new-conn"`) {
		t.Fatalf("dry run wrote rows: %s", rec.Body)
	}
}

// ─── small helpers ─────────────────────────────────────────────────

func getFirstPipelineID(t *testing.T, h http.Handler, cookie *http.Cookie) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/pipelines", nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) == 0 {
		t.Fatal("no pipelines found")
	}
	id, _ := list[0]["id"].(string)
	if id == "" {
		t.Fatal("pipeline id missing in response")
	}
	return id
}

func deleteEntity(t *testing.T, h http.Handler, cookie *http.Cookie, path string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req.AddCookie(cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		t.Fatalf("delete %s: status=%d body=%s", path, rec.Code, rec.Body)
	}
}
