package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// raw body path — easiest call site for an SDK or a curl one-liner.
func TestExtractSample_RawJSONBody(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	body := strings.NewReader(`{"id":"x","nested":{"a":1,"b":2}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/samples/extract", body)
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
	var got struct {
		Format string   `json:"format"`
		Paths  []string `json:"paths"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Format != "json" {
		t.Errorf("format = %q", got.Format)
	}
	want := map[string]bool{"id": false, "nested": false, "nested.a": false, "nested.b": false}
	for _, p := range got.Paths {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("expected path %q in %v", k, got.Paths)
		}
	}
}

// multipart upload path — what the UI's file-picker triggers.
func TestExtractSample_MultipartXML(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("file", "sample.xml")
	fw.Write([]byte(`<order><id>1</id><items><item><sku>A</sku></item></items></order>`))
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/samples/extract", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	attachSession(req, cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
	var got struct {
		Format  string   `json:"format"`
		RootTag string   `json:"root_tag"`
		Paths   []string `json:"paths"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Format != "xml" || got.RootTag != "order" {
		t.Errorf("got format=%q root=%q", got.Format, got.RootTag)
	}
	if len(got.Paths) == 0 {
		t.Error("expected paths from XML body")
	}
}

func TestExtractSample_EmptyBody_400(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/samples/extract", strings.NewReader(""))
	attachSession(req, cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestExtractSample_RequiresAuth(t *testing.T) {
	h, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/samples/extract", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
