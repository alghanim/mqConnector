package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// We can't reach a real broker from inside the test process, but we CAN
// assert the handler reports `ok:false` with a clear error when the dial
// fails — and that's exactly what "Test connection" needs to communicate.
func TestTestConnection_ReachableHandler_FailureShape(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	// Create a bogus RabbitMQ connection — Test must report ok:false.
	conn := postConn(t, h, cookie,
		`{"name":"unreachable","type":"rabbitmq","url":"amqp://127.0.0.1:1","queue_name":"q"}`)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connections/"+conn.ID+"/test", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (handler answers OK even on failure)", rec.Code)
	}
	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	if body.OK {
		t.Errorf("expected ok=false against an unreachable broker, got body=%s", rec.Body)
	}
	if body.Error == "" {
		t.Errorf("expected an error string when ok=false")
	}
}

func TestTestConnection_UnknownIDReturns404(t *testing.T) {
	h, _, _ := newTestServer(t)
	cookie := loginCookie(t, h, "alice", "wonderland")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connections/nope/test", nil)
	attachSession(req, cookie)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestTestConnection_Unauthenticated_401(t *testing.T) {
	h, _, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/connections/any/test", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
