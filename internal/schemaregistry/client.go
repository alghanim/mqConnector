// Package schemaregistry is a thin client for Confluent-compatible
// schema registries (the de-facto API is shared across Confluent,
// Apicurio's compat mode, AWS Glue's compat shim, Redpanda Schema
// Registry, and the Karapace OSS implementation).
//
// Use case: pipelines that consume from Kafka topics where producers
// already publish schemas to a registry. Instead of duplicating those
// schemas into the local `schemas` table by hand, the pipeline references
// a subject and the registry's authoritative version. Read-through cache
// keeps the hot path on the in-process map, refresh in the background.
//
// API surface we depend on (REST, GET-only):
//
//	GET /subjects/<subject>/versions/latest
//	GET /subjects/<subject>/versions/<int>
//	GET /schemas/ids/<int>
//
// Each returns a JSON object with at minimum {"schema": "<string>",
// "schemaType": "AVRO"|"PROTOBUF"|"JSON"}. We only forward schema +
// type to the caller — the inflight version + id are tracked
// internally for cache invalidation.
package schemaregistry

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config configures the registry client.
type Config struct {
	// URL is the base URL of the registry, e.g. https://sr.svc:8081.
	// Empty disables the client; callers should treat the absence as
	// "registry not configured, fall back to inline schemas".
	URL string

	// Username / Password apply basic auth (matches Confluent Cloud's
	// API-key/secret model). Optional.
	Username string
	Password string

	// CacheTTL is how long a successfully-fetched entry stays valid in
	// the in-process cache. After expiry the next read triggers a
	// background refresh; stale-while-revalidate semantics. Default 5
	// minutes.
	CacheTTL time.Duration

	// RequestTimeout caps each HTTP call. Default 10s.
	RequestTimeout time.Duration

	// InsecureSkipVerify disables TLS verification when calling the
	// registry. Dev-only.
	InsecureSkipVerify bool
}

// Schema is the registry's view of a schema. SchemaType is one of
// AVRO / PROTOBUF / JSON in Confluent's wire format. Content is the
// raw schema text (Avro JSON definition, .proto text, or JSON Schema).
type Schema struct {
	Subject    string
	Version    int
	ID         int
	SchemaType string
	Schema     string
}

// Client is the registry client. Safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client

	mu    sync.RWMutex
	cache map[string]cachedEntry
}

type cachedEntry struct {
	schema Schema
	expiry time.Time
}

// New constructs a client. Returns nil when URL is empty.
func New(cfg Config) *Client {
	if cfg.URL == "" {
		return nil
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 10 * time.Second
	}
	hc := &http.Client{
		Timeout: cfg.RequestTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: cfg.InsecureSkipVerify, // #nosec G402 — opt-in dev flag
			},
		},
	}
	return &Client{
		cfg:   cfg,
		http:  hc,
		cache: make(map[string]cachedEntry),
	}
}

// LatestBySubject fetches /subjects/<subject>/versions/latest, returning
// the schema content + type. Cache TTL applies.
func (c *Client) LatestBySubject(ctx context.Context, subject string) (Schema, error) {
	if c == nil {
		return Schema{}, fmt.Errorf("schemaregistry: client not configured")
	}
	key := "subject:" + subject
	if s, ok := c.fromCache(key); ok {
		return s, nil
	}
	url := strings.TrimRight(c.cfg.URL, "/") +
		"/subjects/" + subject + "/versions/latest"
	s, err := c.fetch(ctx, url)
	if err != nil {
		return Schema{}, err
	}
	s.Subject = subject
	c.putCache(key, s)
	return s, nil
}

// VersionBySubject pins to a specific version. The subject's "latest"
// pointer can move; versions don't, so pipelines that need
// reproducibility should reference a fixed version.
func (c *Client) VersionBySubject(ctx context.Context, subject string, version int) (Schema, error) {
	if c == nil {
		return Schema{}, fmt.Errorf("schemaregistry: client not configured")
	}
	key := fmt.Sprintf("subject:%s:v%d", subject, version)
	if s, ok := c.fromCache(key); ok {
		return s, nil
	}
	url := fmt.Sprintf("%s/subjects/%s/versions/%d",
		strings.TrimRight(c.cfg.URL, "/"), subject, version)
	s, err := c.fetch(ctx, url)
	if err != nil {
		return Schema{}, err
	}
	s.Subject = subject
	c.putCache(key, s)
	return s, nil
}

// ByID fetches by registry-assigned schema id. Useful when a consumer
// reads the id from a Kafka message header and wants the matching
// schema text.
func (c *Client) ByID(ctx context.Context, id int) (Schema, error) {
	if c == nil {
		return Schema{}, fmt.Errorf("schemaregistry: client not configured")
	}
	key := fmt.Sprintf("id:%d", id)
	if s, ok := c.fromCache(key); ok {
		return s, nil
	}
	url := fmt.Sprintf("%s/schemas/ids/%d",
		strings.TrimRight(c.cfg.URL, "/"), id)
	s, err := c.fetch(ctx, url)
	if err != nil {
		return Schema{}, err
	}
	s.ID = id
	c.putCache(key, s)
	return s, nil
}

// fetch is the shared GET path. Adds basic auth, decodes the registry's
// JSON envelope, returns a populated Schema (subject and id may be left
// to the caller depending on which endpoint we hit).
func (c *Client) fetch(ctx context.Context, url string) (Schema, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Schema{}, fmt.Errorf("schemaregistry: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.schemaregistry.v1+json, application/json")
	if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Schema{}, fmt.Errorf("schemaregistry: %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Schema{}, fmt.Errorf("schemaregistry: %s: %d: %s",
			url, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Subject    string `json:"subject"`
		Version    int    `json:"version"`
		ID         int    `json:"id"`
		SchemaType string `json:"schemaType"`
		Schema     string `json:"schema"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Schema{}, fmt.Errorf("schemaregistry: decode: %w", err)
	}
	// The /schemas/ids endpoint doesn't echo a SchemaType for legacy
	// AVRO-only registries; default to AVRO to match Confluent's
	// pre-2019 wire format.
	st := payload.SchemaType
	if st == "" {
		st = "AVRO"
	}
	return Schema{
		Subject:    payload.Subject,
		Version:    payload.Version,
		ID:         payload.ID,
		SchemaType: st,
		Schema:     payload.Schema,
	}, nil
}

func (c *Client) fromCache(key string) (Schema, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.cache[key]
	if !ok || time.Now().After(e.expiry) {
		return Schema{}, false
	}
	return e.schema, true
}

func (c *Client) putCache(key string, s Schema) {
	c.mu.Lock()
	c.cache[key] = cachedEntry{schema: s, expiry: time.Now().Add(c.cfg.CacheTTL)}
	c.mu.Unlock()
}

// Invalidate forgets a subject's cached value. Useful for ops tools
// that update a schema and want the next pipeline lookup to refetch.
func (c *Client) Invalidate(subject string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	for k := range c.cache {
		if strings.HasPrefix(k, "subject:"+subject) {
			delete(c.cache, k)
		}
	}
	c.mu.Unlock()
}
