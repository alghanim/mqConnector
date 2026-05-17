//go:build integration

package schemaregistry

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestIntegration_LiveRegistry drives the real Apicurio (Confluent-
// compat) container brought up by docker-compose. Skipped by default;
// run via:
//
//	docker compose up -d schemaregistry
//	# register a schema first (see docs/test scenarios)
//	SR_URL=http://localhost:8181/apis/ccompat/v7 \
//	  go test -tags integration -run TestIntegration_LiveRegistry \
//	  ./internal/schemaregistry/...
func TestIntegration_LiveRegistry(t *testing.T) {
	url := os.Getenv("SR_URL")
	if url == "" {
		t.Skip("set SR_URL to run; e.g. SR_URL=http://localhost:8181/apis/ccompat/v7")
	}
	subject := os.Getenv("SR_SUBJECT")
	if subject == "" {
		subject = "orders-value"
	}
	c := New(Config{URL: url, CacheTTL: 5 * time.Second})
	if c == nil {
		t.Fatal("client unexpectedly nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s, err := c.LatestBySubject(ctx, subject)
	if err != nil {
		t.Fatalf("LatestBySubject: %v", err)
	}
	if s.Subject != subject {
		t.Errorf("subject roundtrip: got %q want %q", s.Subject, subject)
	}
	if s.Version < 1 {
		t.Errorf("version: got %d want >=1", s.Version)
	}
	if s.ID < 1 {
		t.Errorf("id: got %d want >=1", s.ID)
	}
	if s.Schema == "" {
		t.Error("schema body empty")
	}
	t.Logf("LIVE: subject=%s version=%d id=%d type=%q", s.Subject, s.Version, s.ID, s.SchemaType)

	// Second fetch — cache hit. Confirmed by the unit tests already; here
	// we just exercise it doesn't blow up on real network state.
	if _, err := c.LatestBySubject(ctx, subject); err != nil {
		t.Errorf("cached fetch: %v", err)
	}
}
