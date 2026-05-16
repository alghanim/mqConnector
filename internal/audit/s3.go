package audit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// S3Uploader uploads a local file to an S3-compatible object store
// using AWS Signature v4. Implemented with only the standard library so
// we don't pull in aws-sdk-go for what is, at its core, a single PUT
// request per archived day.
//
// Compatible with: AWS S3, MinIO, Cloudflare R2 (via the S3 endpoint),
// Wasabi, Backblaze B2 (S3 interface). Anything else that speaks
// SigV4-signed PUT will work too.
//
// Concurrency: safe across goroutines. The credentials live in
// immutable fields after construction.
type S3Uploader struct {
	endpoint  string // e.g. "s3.amazonaws.com" or "minio.internal:9000"
	region    string // e.g. "us-east-1"
	bucket    string
	accessKey string
	secretKey string
	useTLS    bool
	prefix    string // optional path prefix inside the bucket
	client    *http.Client
}

// S3Config carries the constructor arguments. Empty AccessKey /
// SecretKey produces a nil uploader — the archiver treats that as
// "S3 disabled".
type S3Config struct {
	Endpoint  string
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string
	UseTLS    bool
	Prefix    string

	// HTTPClient is optional. Defaults to a 30-second-timeout client.
	HTTPClient *http.Client
}

// NewS3 constructs an S3Uploader. Returns nil when credentials or
// bucket are missing — the operator's signal that S3 is intentionally
// off. Callers check for nil and skip upload accordingly.
func NewS3(cfg S3Config) *S3Uploader {
	if cfg.AccessKey == "" || cfg.SecretKey == "" || cfg.Bucket == "" || cfg.Endpoint == "" {
		return nil
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &S3Uploader{
		endpoint:  cfg.Endpoint,
		region:    cfg.Region,
		bucket:    cfg.Bucket,
		accessKey: cfg.AccessKey,
		secretKey: cfg.SecretKey,
		useTLS:    cfg.UseTLS,
		prefix:    cfg.Prefix,
		client:    client,
	}
}

// PutObject uploads `body` to s3://<bucket>/<prefix><key>. Content
// type is set to application/x-ndjson when the key ends in .jsonl,
// application/octet-stream otherwise. Returns the request id from the
// response header on success so an operator can correlate uploads in
// the bucket's access log.
func (s *S3Uploader) PutObject(ctx context.Context, key string, body []byte) (string, error) {
	if s == nil {
		return "", fmt.Errorf("audit: s3 uploader is nil")
	}
	fullKey := strings.TrimLeft(s.prefix+key, "/")
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	scheme := "http"
	if s.useTLS {
		scheme = "https"
	}
	// Path-style S3 — works for MinIO out of the box; works for AWS too
	// (deprecated but still supported as of 2024-09 for existing
	// buckets). Virtual-hosted style would require a different
	// canonical-host header; path-style keeps the request body simpler.
	u := &url.URL{
		Scheme: scheme,
		Host:   s.endpoint,
		Path:   "/" + s.bucket + "/" + fullKey,
	}

	payloadHash := sha256Hex(body)
	contentType := "application/octet-stream"
	if strings.HasSuffix(fullKey, ".jsonl") {
		contentType = "application/x-ndjson"
	}

	// Canonical headers — sorted, lowercased, trimmed. SigV4 requires
	// host + x-amz-content-sha256 + x-amz-date at minimum.
	headers := map[string]string{
		"host":                 u.Host,
		"content-type":         contentType,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	signedHeaders, canonicalHeaders := canonicalize(headers)
	canonicalRequest := strings.Join([]string{
		"PUT",
		u.EscapedPath(),
		"",
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, s.region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(s.secretKey, dateStamp, s.region, "s3")
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authorization := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.accessKey, credentialScope, signedHeaders, signature,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("audit s3: build request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", authorization)

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("audit s3: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("audit s3: PUT %s → %d: %s",
			fullKey, resp.StatusCode, string(errBody))
	}
	return resp.Header.Get("x-amz-request-id"), nil
}

// ─── SigV4 primitives ────────────────────────────────────────────────

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func deriveSigningKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

// canonicalize returns the SignedHeaders (semicolon-joined names) and
// CanonicalHeaders (\n-joined "name:value") blocks for SigV4.
func canonicalize(h map[string]string) (signed, canonical string) {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, strings.ToLower(k))
	}
	// insertion sort — len is < 10
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte(':')
		sb.WriteString(strings.TrimSpace(h[k]))
		sb.WriteByte('\n')
	}
	return strings.Join(keys, ";"), sb.String()
}
