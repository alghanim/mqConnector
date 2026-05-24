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
	attachSession(req, cookie)
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
	attachSession(req, cookie)
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
	attachSession(pReq, cookie)
	pRec := httptest.NewRecorder()
	h.ServeHTTP(pRec, pReq)
	if pRec.Code != http.StatusCreated && pRec.Code != http.StatusOK {
		t.Fatalf("POST pipeline: %d %s", pRec.Code, pRec.Body)
	}
	var p struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(pRec.Body.Bytes(), &p)
	if p.ID == "" {
		t.Fatalf("no pipeline id in %s", pRec.Body)
	}

	// Replace stages with one filter step.
	stagesBody := `[{"stage_order":1,"stage_type":"filter","stage_config":"{\"paths\":[\"secret\"]}","enabled":true}]`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/pipelines/"+p.ID+"/stages",
		strings.NewReader(stagesBody))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT stages: %d %s", rec.Code, rec.Body)
	}

	body := `{"pipeline_id":"` + p.ID + `","sample":"{\"id\":\"x\",\"secret\":\"hush\"}"}`
	req = httptest.NewRequest(http.MethodPost, "/api/v1/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
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
	attachSession(req, cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// previewResponseEnvelope mirrors the JSON shape the handler emits, including
// the new stage_runs array the Studio dry-run dock reads. Defined locally so
// the test doesn't depend on internal struct layout.
type previewResponseEnvelope struct {
	OK        bool     `json:"ok"`
	Output    string   `json:"output"`
	Format    string   `json:"format"`
	Routes    []string `json:"routes"`
	Error     string   `json:"error"`
	StageRuns []struct {
		Name       string `json:"name"`
		DurationNs int64  `json:"duration_ns"`
		Failed     bool   `json:"failed"`
		Body       string `json:"body"`
		Format     string `json:"format"`
		Err        string `json:"err"`
	} `json:"stage_runs"`
}

func doPreview(t *testing.T, h http.Handler, cookie *http.Cookie, body string) previewResponseEnvelope {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
	var got previewResponseEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v — body=%s", err, rec.Body)
	}
	return got
}

// Successful two-stage preview returns a stage_runs strip in execution
// order with per-stage body, format, name, and a non-negative duration.
func TestPreview_StageRunsSerialised(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	body := `{
		"stages": [
			{"stage_order": 1, "stage_type": "filter",
			 "stage_config": "{\"paths\":[\"secret\"]}", "enabled": true},
			{"stage_order": 2, "stage_type": "translate",
			 "stage_config": "{\"output_format\":\"xml\"}", "enabled": true}
		],
		"sample": "{\"id\":\"x\",\"secret\":\"hush\",\"keep\":1}"
	}`
	got := doPreview(t, h, cookie, body)

	if !got.OK {
		t.Fatalf("ok=false, error=%q", got.Error)
	}
	if len(got.StageRuns) != 2 {
		t.Fatalf("len(stage_runs) = %d, want 2 (%+v)", len(got.StageRuns), got.StageRuns)
	}

	// Stage 1: filter — body should be JSON with "secret" stripped.
	s1 := got.StageRuns[0]
	if s1.Name != "filter" {
		t.Errorf("StageRuns[0].Name = %q, want filter", s1.Name)
	}
	if s1.Failed {
		t.Errorf("StageRuns[0].Failed = true")
	}
	if s1.Err != "" {
		t.Errorf("StageRuns[0].Err = %q on success", s1.Err)
	}
	if s1.DurationNs < 0 {
		t.Errorf("StageRuns[0].DurationNs = %d, want >= 0", s1.DurationNs)
	}
	if s1.Format != "json" {
		t.Errorf("StageRuns[0].Format = %q, want json", s1.Format)
	}
	if strings.Contains(s1.Body, "secret") {
		t.Errorf("StageRuns[0].Body should have stripped 'secret': %s", s1.Body)
	}
	if !strings.Contains(s1.Body, "keep") {
		t.Errorf("StageRuns[0].Body lost too much: %s", s1.Body)
	}

	// Stage 2: translate to xml. Body should look like XML, format=xml.
	s2 := got.StageRuns[1]
	if s2.Name != "translate" {
		t.Errorf("StageRuns[1].Name = %q, want translate", s2.Name)
	}
	if s2.Failed {
		t.Errorf("StageRuns[1].Failed = true")
	}
	if s2.Format != "xml" {
		t.Errorf("StageRuns[1].Format = %q, want xml", s2.Format)
	}
	if !strings.Contains(s2.Body, "<") {
		t.Errorf("StageRuns[1].Body doesn't look like XML: %s", s2.Body)
	}
	// The two stages should have produced different bodies (proves the
	// snapshot isn't a shared reference to the final state).
	if s1.Body == s2.Body {
		t.Errorf("per-stage bodies identical — snapshot likely shared")
	}
}

// When a stage in the middle of the chain fails, the response is OK=false
// and stage_runs ends at the failing stage with Failed=true and Err set.
func TestPreview_StageRunsFailedSerialised(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	// Filter (works) → validate against a required-field schema the
	// sample doesn't satisfy (fails). Translate after MUST NOT run.
	body := `{
		"stages": [
			{"stage_order": 1, "stage_type": "filter",
			 "stage_config": "{\"paths\":[\"junk\"]}", "enabled": true},
			{"stage_order": 2, "stage_type": "validate",
			 "stage_config": "{\"schema_type\":\"json_schema\",\"content\":\"{\\\"type\\\":\\\"object\\\",\\\"required\\\":[\\\"ssn\\\"]}\"}",
			 "enabled": true},
			{"stage_order": 3, "stage_type": "translate",
			 "stage_config": "{\"output_format\":\"xml\"}", "enabled": true}
		],
		"sample": "{\"id\":\"x\"}"
	}`
	got := doPreview(t, h, cookie, body)

	if got.OK {
		t.Fatalf("expected ok=false on stage failure, got %+v", got)
	}
	if got.Error == "" {
		t.Errorf("expected error string on ok=false")
	}
	if len(got.StageRuns) != 2 {
		t.Fatalf("want 2 stage_runs (filter ok, validate failed, translate skipped); got %d: %+v",
			len(got.StageRuns), got.StageRuns)
	}
	if got.StageRuns[0].Failed {
		t.Errorf("StageRuns[0] (filter) should be Failed=false")
	}
	if got.StageRuns[0].Name != "filter" {
		t.Errorf("StageRuns[0].Name = %q, want filter", got.StageRuns[0].Name)
	}
	if !got.StageRuns[1].Failed {
		t.Errorf("StageRuns[1] (validate) should be Failed=true")
	}
	if got.StageRuns[1].Err == "" {
		t.Errorf("StageRuns[1].Err empty on failed stage")
	}
	if got.StageRuns[1].Name != "validate" {
		t.Errorf("StageRuns[1].Name = %q, want validate", got.StageRuns[1].Name)
	}
	// The failed stage's body should be the INPUT it received (the
	// post-filter JSON, since filter ran first and paths:["junk"] was
	// a no-op).
	if !strings.Contains(got.StageRuns[1].Body, "\"id\"") {
		t.Errorf("StageRuns[1].Body should hold the input to the failed stage, got: %s",
			got.StageRuns[1].Body)
	}
}

// Body encoding for stage_runs matches the encoding the existing
// `output` field uses (raw string of the byte buffer). This pins the
// wire shape Task 11's DryRunDock will rely on.
func TestPreview_StageBodyEncodingMatchesOutput(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	body := `{
		"stages": [
			{"stage_order": 1, "stage_type": "filter",
			 "stage_config": "{\"paths\":[]}", "enabled": true}
		],
		"sample": "{\"id\":\"x\",\"keep\":1}"
	}`
	got := doPreview(t, h, cookie, body)

	if !got.OK {
		t.Fatalf("ok=false: %q", got.Error)
	}
	if len(got.StageRuns) != 1 {
		t.Fatalf("want 1 stage_run, got %d", len(got.StageRuns))
	}
	// The single stage's body should equal the final output verbatim
	// (encoding for both fields is `string(msg.Body)` — raw UTF-8).
	if got.StageRuns[0].Body != got.Output {
		t.Errorf("StageRuns[0].Body (%q) != Output (%q) — encodings have diverged",
			got.StageRuns[0].Body, got.Output)
	}
	// And the body should be human-readable JSON, not base64. If we
	// ever flip to base64 this assertion fires and forces an explicit
	// decision in the test rather than silent breakage.
	if !strings.HasPrefix(got.StageRuns[0].Body, "{") {
		t.Errorf("StageRuns[0].Body not raw JSON string (encoding may have changed): %s",
			got.StageRuns[0].Body)
	}
}
