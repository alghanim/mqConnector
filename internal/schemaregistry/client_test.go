package schemaregistry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestClient_LatestBySubject_CacheHit drives one fetch then a second
// fetch against the same subject — the second call must NOT hit the
// registry (cache TTL still valid).
func TestClient_LatestBySubject_CacheHit(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"subject":    "orders-value",
			"version":    7,
			"id":         42,
			"schemaType": "PROTOBUF",
			"schema":     "syntax = \"proto3\"; message Order { string id = 1; }",
		})
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL, CacheTTL: time.Minute})
	for i := 0; i < 5; i++ {
		s, err := c.LatestBySubject(context.Background(), "orders-value")
		if err != nil {
			t.Fatalf("fetch %d: %v", i, err)
		}
		if s.ID != 42 || s.Version != 7 || s.SchemaType != "PROTOBUF" {
			t.Errorf("unexpected payload: %+v", s)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected exactly 1 backend hit; got %d", got)
	}
}

// TestClient_404 surfaces the registry's error response with a useful
// message rather than swallowing it.
func TestClient_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error_code":40401,"message":"Subject not found."}`))
	}))
	defer srv.Close()
	c := New(Config{URL: srv.URL})
	_, err := c.LatestBySubject(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestClient_DisabledWhenURLEmpty(t *testing.T) {
	if New(Config{}) != nil {
		t.Fatal("client should be nil when URL is empty")
	}
}

func TestClient_Invalidate(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"schema":"x","schemaType":"AVRO"}`))
	}))
	defer srv.Close()
	c := New(Config{URL: srv.URL, CacheTTL: time.Hour})
	_, _ = c.LatestBySubject(context.Background(), "s1")
	c.Invalidate("s1")
	_, _ = c.LatestBySubject(context.Background(), "s1")
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("invalidate should force refetch; got %d hits", got)
	}
}
