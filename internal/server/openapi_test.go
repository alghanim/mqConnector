package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAPI_PublicAndServesYAML(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/openapi.yaml", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/yaml") {
		t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	for _, want := range []string{"openapi: 3.0.3", "title: mqConnector", "/api/v1/connections"} {
		if !strings.Contains(body, want) {
			t.Errorf("spec missing %q", want)
		}
	}
}
