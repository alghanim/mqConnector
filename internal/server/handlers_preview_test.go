package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Inline-draft mode: the editor sends a fresh stage list + sample, gets the
// transformed output back. Asserts the filter stage actually strips a field.
func TestPreview_InlineDraft_FilterStrips(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	body := `{
		"stages": [
			{"stage_order": 1, "stage_type": "filter",
			 "stage_config": "{\"paths\":[\"secret\"]}", "enabled": true}
		],
		"sample": "{\"id\":\"x\",\"secret\":\"hush\",\"keep\":1}"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
	var got struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
		Format string `json:"format"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.OK {
		t.Fatalf("ok=false, error=%q", got.Error)
	}
	if strings.Contains(got.Output, "secret") {
		t.Errorf("filter didn't strip 'secret': %s", got.Output)
	}
	if !strings.Contains(got.Output, "keep") {
		t.Errorf("filter stripped too much: %s", got.Output)
	}
	if got.Format != "json" {
		t.Errorf("format = %q", got.Format)
	}
}

// Translate stage swaps JSON -> XML; we can spot-check the XML opener.
func TestPreview_InlineDraft_TranslateJSONtoXML(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	body := `{
		"stages": [
			{"stage_order": 1, "stage_type": "translate",
			 "stage_config": "{\"output_format\":\"xml\"}", "enabled": true}
		],
		"sample": "{\"order\":{\"id\":1}}"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var got struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
		Format string `json:"format"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.OK || got.Format != "xml" {
		t.Fatalf("got=%+v", got)
	}
	if !strings.Contains(got.Output, "<order>") {
		t.Errorf("expected XML, got %s", got.Output)
	}
}

// Saved-pipeline mode: persist a pipeline, then preview it by ID.
func TestPreview_FromSavedPipelineID(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	src := postConn(t, h, cookie, `{"name":"prev-src","type":"rabbitmq","url":"amqp://x","queue_name":"a"}`)
	dst := postConn(t, h, cookie, `{"name":"prev-dst","type":"rabbitmq","url":"amqp://x","queue_name":"b"}`)

	// Create the pipeline via the same API the editor uses.
	pipeBody := `{"name":"prev","source_id":"` + src.ID + `","destination_id":"` + dst.ID + `","output_format":"same","filter_paths":[],"enabled":true}`
	pReq := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines", strings.NewReader(pipeBody))
	pReq.Header.Set("Content-Type", "application/json")
	pReq.AddCookie(cookie)
	pRec := httptest.NewRecorder()
	h.ServeHTTP(pRec, pReq)
	if pRec.Code != http.StatusCreated && pRec.Code != http.StatusOK {
		t.Fatalf("POST pipeline: %d %s", pRec.Code, pRec.Body)
	}
	var p struct{ ID string `json:"id"` }
	_ = json.Unmarshal(pRec.Body.Bytes(), &p)
	if p.ID == "" {
		t.Fatalf("no pipeline id in %s", pRec.Body)
	}

	// Replace stages with one filter step.
	stagesBody := `[{"stage_order":1,"stage_type":"filter","stage_config":"{\"paths\":[\"secret\"]}","enabled":true}]`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/pipelines/"+p.ID+"/stages",
		strings.NewReader(stagesBody))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT stages: %d %s", rec.Code, rec.Body)
	}

	body := `{"pipeline_id":"` + p.ID + `","sample":"{\"id\":\"x\",\"secret\":\"hush\"}"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	var got struct {
		OK     bool   `json:"ok"`
		Output string `json:"output"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.OK || strings.Contains(got.Output, "secret") {
		t.Errorf("expected filter to strip secret, got %s", got.Output)
	}
	_ = srv
}

func TestPreview_MissingSample_400(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/preview",
		strings.NewReader(`{"stages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
