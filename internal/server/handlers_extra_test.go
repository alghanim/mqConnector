package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// withAuth returns a request prepared with a fresh login cookie.
func withAuth(t *testing.T, h http.Handler) (*http.Cookie, http.Handler) {
	t.Helper()
	return loginCookie(t, h, "alice", "wonderland"), h
}

// ----------------------------------------------------------------------------
// Pipelines
// ----------------------------------------------------------------------------

func TestPipelines_FullCRUDFlow(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)

	// seed two connections via the API
	src := postConn(t, h, cookie, `{"name":"src","type":"rabbitmq","url":"amqp://x","queue_name":"src-q"}`)
	dst := postConn(t, h, cookie, `{"name":"dst","type":"rabbitmq","url":"amqp://x","queue_name":"dst-q"}`)

	// CREATE
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"name":"p1","source_id":"` + src.ID + `","destination_id":"` + dst.ID +
		`","output_format":"same","filter_paths":["a.b"],"enabled":true}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines", body)
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create pipeline: %d %s", rec.Code, rec.Body)
	}
	var pipe storage.Pipeline
	_ = json.Unmarshal(rec.Body.Bytes(), &pipe)

	// LIST
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/pipelines", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	var list []storage.Pipeline
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Errorf("expected 1 pipeline, got %d", len(list))
	}

	// REPLACE STAGES
	rec = httptest.NewRecorder()
	body = strings.NewReader(`[{"stage_order":1,"stage_type":"filter","stage_config":"{}","enabled":true}]`)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/pipelines/"+pipe.ID+"/stages", body)
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace stages: %d %s", rec.Code, rec.Body)
	}

	// REPLACE TRANSFORMS
	rec = httptest.NewRecorder()
	body = strings.NewReader(`[{"transform_type":"rename","source_path":"a","target_path":"b","order":1}]`)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/pipelines/"+pipe.ID+"/transforms", body)
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("replace transforms: %d %s", rec.Code, rec.Body)
	}

	// LIST STAGES
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/pipelines/"+pipe.ID+"/stages", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list stages: %d", rec.Code)
	}

	// RELOAD
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/reload", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("reload: %d %s", rec.Code, rec.Body)
	}

	// DELETE
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/pipelines/"+pipe.ID, nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("delete: %d", rec.Code)
	}
}

func TestPipelines_CreateRejectsMissingFields(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/pipelines",
		strings.NewReader(`{"name":""}`))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: %d, want 400", rec.Code)
	}
}

func TestConnections_CreateRejectsBadType(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connections",
		strings.NewReader(`{"name":"bad","type":"gibberish"}`))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status: %d, want 400", rec.Code)
	}
}

// ----------------------------------------------------------------------------
// DLQ
// ----------------------------------------------------------------------------

func TestDLQ_ListEmptyOK(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/dlq", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dlq list: %d", rec.Code)
	}
}

func TestDLQ_RetryUnknownIs404(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/dlq/nope/retry", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound && rec.Code != http.StatusInternalServerError {
		t.Errorf("unexpected status: %d %s", rec.Code, rec.Body)
	}
}

// ----------------------------------------------------------------------------
// Scripts + Schemas
// ----------------------------------------------------------------------------

func TestScripts_CRUDViaHTTP(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/scripts",
		strings.NewReader(`{"name":"noop","body":"msg.x = 1","enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var sc storage.Script
	_ = json.Unmarshal(rec.Body.Bytes(), &sc)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/scripts", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/scripts/"+sc.ID, nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("delete: %d", rec.Code)
	}
}

// ----------------------------------------------------------------------------
// Bridge (REST → MQ)
// ----------------------------------------------------------------------------

func TestBridgePublish_PublishesToConnection(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)

	// Create a connection in storage.
	conn := postConn(t, h, cookie, `{"name":"local","type":"rabbitmq","url":"amqp://x","queue_name":"events"}`)

	// Pre-seat a memory connector in the pool so the bridge doesn't dial AMQP.
	reg := mq.NewMemoryRegistry(8)
	mem := mq.NewMemoryConnector(reg, "events")
	_ = mem.Connect(context.Background())
	srv.pool.InjectForTest("bridge-pub-"+conn.ID, mem)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bridge/publish/"+conn.ID,
		strings.NewReader(`{"event":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	if msgs := reg.Drain("events"); len(msgs) != 1 || string(msgs[0]) != `{"event":"hello"}` {
		t.Errorf("payload mismatch: %v", msgs)
	}
}

func TestBridgeConsume_ReceivesFromConnection(t *testing.T) {
	h, srv, _ := newTestServer(t)
	cookie, _ := withAuth(t, h)

	conn := postConn(t, h, cookie, `{"name":"local","type":"rabbitmq","url":"amqp://x","queue_name":"events2"}`)

	reg := mq.NewMemoryRegistry(8)
	mem := mq.NewMemoryConnector(reg, "events2")
	_ = mem.Connect(context.Background())
	srv.pool.InjectForTest("bridge-con-"+conn.ID, mem)
	_ = mem.SendMessage(context.Background(), []byte(`<doc/>`))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/bridge/consume/"+conn.ID, nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("consume: %d", rec.Code)
	}
	if rec.Header().Get("Content-Type") != "application/xml" {
		t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != `<doc/>` {
		t.Errorf("body = %q", rec.Body.String())
	}
}

// helper: create a connection via API and return the decoded record.
func postConn(t *testing.T, h http.Handler, cookie *http.Cookie, body string) storage.Connection {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connections", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed conn: %d %s", rec.Code, rec.Body)
	}
	var c storage.Connection
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	return c
}
