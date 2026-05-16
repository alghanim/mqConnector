package audit

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestS3_PutObjectShape confirms the uploader sends a SigV4-signed
// PUT to the expected path. We don't validate the signature
// cryptographically here (the receiver would need the same key
// material), but we DO check that all the required SigV4 headers are
// present and well-formed.
func TestS3_PutObjectShape(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotDate, gotPayloadHash, gotCT string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotDate = r.Header.Get("X-Amz-Date")
		gotPayloadHash = r.Header.Get("X-Amz-Content-Sha256")
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("x-amz-request-id", "test-request-id")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Strip the http:// off the test server URL — the uploader builds
	// it back depending on UseTLS.
	endpoint := strings.TrimPrefix(srv.URL, "http://")

	up := NewS3(S3Config{
		Endpoint:  endpoint,
		Region:    "us-east-1",
		Bucket:    "test-bucket",
		AccessKey: "AKIAEXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		UseTLS:    false,
	})
	if up == nil {
		t.Fatal("NewS3 returned nil with full config")
	}

	reqID, err := up.PutObject(context.Background(), "audit/2026/05/16/audit-2026-05-16.jsonl",
		[]byte(`{"id":"r1"}`+"\n"))
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if reqID != "test-request-id" {
		t.Errorf("returned request id: %q", reqID)
	}
	if gotMethod != "PUT" {
		t.Errorf("method: %q", gotMethod)
	}
	if gotPath != "/test-bucket/audit/2026/05/16/audit-2026-05-16.jsonl" {
		t.Errorf("path: %q", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") {
		t.Errorf("authorization: %q", gotAuth)
	}
	if !strings.Contains(gotAuth, "SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date") {
		t.Errorf("authorization SignedHeaders missing required entries: %q", gotAuth)
	}
	if len(gotDate) != 16 || !strings.HasSuffix(gotDate, "Z") {
		t.Errorf("x-amz-date wrong shape: %q", gotDate)
	}
	if len(gotPayloadHash) != 64 {
		t.Errorf("x-amz-content-sha256 must be 64 hex chars: %q", gotPayloadHash)
	}
	if gotCT != "application/x-ndjson" {
		t.Errorf("content-type: %q (expected application/x-ndjson for .jsonl)", gotCT)
	}
	if string(gotBody) != `{"id":"r1"}`+"\n" {
		t.Errorf("body: %q", gotBody)
	}
}

// TestS3_DisabledOnMissingCreds returns nil rather than constructing a
// broken uploader. The archiver treats nil as "S3 disabled".
func TestS3_DisabledOnMissingCreds(t *testing.T) {
	cases := []S3Config{
		{},
		{Endpoint: "s3.example.com"},
		{Endpoint: "s3.example.com", Bucket: "b"},
		{Endpoint: "s3.example.com", Bucket: "b", AccessKey: "a"},
	}
	for i, c := range cases {
		if NewS3(c) != nil {
			t.Errorf("case %d: expected nil for incomplete config %+v", i, c)
		}
	}
}

// TestS3_NonSuccessSurfaces the response body in the error so the
// operator can see what their bucket policy or signing went wrong.
func TestS3_NonSuccessSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
	}))
	defer srv.Close()
	up := NewS3(S3Config{
		Endpoint:  strings.TrimPrefix(srv.URL, "http://"),
		Region:    "us-east-1",
		Bucket:    "b",
		AccessKey: "a",
		SecretKey: "s",
	})
	_, err := up.PutObject(context.Background(), "key", []byte("x"))
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if !strings.Contains(err.Error(), "AccessDenied") {
		t.Errorf("error should surface response body: %v", err)
	}
}
