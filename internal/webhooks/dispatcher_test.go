package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mqConnector/internal/events"
	"mqConnector/internal/storage"
)

// fakeStore is a minimal Store stub — keeps the test free of SQLite.
type fakeStore struct {
	mu       sync.Mutex
	hooks    []*storage.Webhook
	attempts []recordedAttempt
}
type recordedAttempt struct {
	id     string
	status int
	err    string
}

func (s *fakeStore) ListAll(_ context.Context) ([]*storage.Webhook, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*storage.Webhook, len(s.hooks))
	copy(out, s.hooks)
	return out, nil
}
func (s *fakeStore) RecordAttempt(_ context.Context, id string, status int, errText string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, recordedAttempt{id, status, errText})
	return nil
}

// TestDispatcher_DeliversWithHMAC confirms an event is POSTed to the
// webhook URL with an X-MQC-Signature header that the receiver can
// verify against the shared secret.
func TestDispatcher_DeliversWithHMAC(t *testing.T) {
	const secret = "shared-secret-1234"

	var gotBody []byte
	var gotSig string
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-MQC-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &fakeStore{
		hooks: []*storage.Webhook{{
			ID: "h1", TenantID: "tenant-a",
			Name: "test", URL: srv.URL, Secret: secret,
			Events: "*", Enabled: true,
		}},
	}
	pub := events.NewPublisher(8, nil)
	d := New(store, pub, Options{MaxRetries: 1, HTTPTimeout: 2 * time.Second}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	defer func() { cancel(); <-d.Done() }()

	pub.Publish(ctx, events.Event{
		Type:     events.TypePipelineStarted,
		TenantID: "tenant-a",
		Data:     map[string]any{"pipeline_id": "abc"},
	})

	// Wait briefly for delivery — runs in a goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&hits) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected 1 webhook hit, got %d", hits)
	}
	if !strings.HasPrefix(gotSig, "sha256=") {
		t.Errorf("signature header malformed: %q", gotSig)
	}
	expectedMAC := hmac.New(sha256.New, []byte(secret))
	expectedMAC.Write(gotBody)
	expectedSig := "sha256=" + hex.EncodeToString(expectedMAC.Sum(nil))
	if gotSig != expectedSig {
		t.Errorf("signature mismatch:\n  got %q\n want %q", gotSig, expectedSig)
	}
	var ev map[string]any
	if err := json.Unmarshal(gotBody, &ev); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if ev["type"] != events.TypePipelineStarted {
		t.Errorf("body event type: %v", ev["type"])
	}
}

// TestDispatcher_TenantIsolation makes sure an event in tenant A
// doesn't fire a webhook registered in tenant B.
func TestDispatcher_TenantIsolation(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &fakeStore{
		hooks: []*storage.Webhook{{
			ID: "h1", TenantID: "tenant-b", // wrong tenant
			Name: "test", URL: srv.URL, Secret: "s",
			Events: "*", Enabled: true,
		}},
	}
	pub := events.NewPublisher(8, nil)
	d := New(store, pub, Options{MaxRetries: 1, HTTPTimeout: 2 * time.Second}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	defer func() { cancel(); <-d.Done() }()

	pub.Publish(ctx, events.Event{Type: events.TypePipelineStarted, TenantID: "tenant-a"})
	time.Sleep(150 * time.Millisecond)
	if atomic.LoadInt32(&hits) != 0 {
		t.Errorf("cross-tenant fire: hits = %d", hits)
	}
}

// TestDispatcher_EventFilter only fires when the event type matches.
func TestDispatcher_EventFilter(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := &fakeStore{
		hooks: []*storage.Webhook{{
			ID: "h1", TenantID: "t",
			Name: "only-dlq", URL: srv.URL, Secret: "s",
			Events: "dlq.pushed", Enabled: true,
		}},
	}
	pub := events.NewPublisher(8, nil)
	d := New(store, pub, Options{MaxRetries: 1, HTTPTimeout: 2 * time.Second}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go d.Run(ctx)
	defer func() { cancel(); <-d.Done() }()

	// Non-matching event.
	pub.Publish(ctx, events.Event{Type: events.TypePipelineStarted, TenantID: "t"})
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("non-matching event fired: %d", hits)
	}
	// Matching event.
	pub.Publish(ctx, events.Event{Type: events.TypeDLQPushed, TenantID: "t"})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&hits) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("matching event missed: %d", hits)
	}
}
