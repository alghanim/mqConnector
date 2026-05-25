package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// AuditLogger records every AI call. The contract is best-effort —
// implementations MUST swallow errors (logging them is fine) so a
// failing audit sink can never break the request path.
//
// The storage-backed implementation lives in
// internal/storage/ai_audit.go; NoopAuditLogger here exists for tests
// and for the bootstrap window before storage is wired.
type AuditLogger interface {
	Log(ctx context.Context, row AuditRow)
}

// AuditRow is one row of the ai_audit table. Fields map 1:1 to the
// columns in migration 0024.
type AuditRow struct {
	// Feature names the capability that invoked the call.
	Feature Capability

	// CallerSub is the user sub from the auth context, or "" when the
	// call is system-driven (background job, reaper, etc.).
	CallerSub string

	// TenantID is the tenant the result belongs to. Required for the
	// future /governance/audit?source=ai filter.
	TenantID string

	// PromptHash is the first 16 hex chars of sha256(user content).
	// Stored instead of the prompt itself so the audit row stays
	// PII-safe — operators can still group rows that share a prompt
	// without the prompt sitting in plaintext at rest.
	PromptHash string

	// Model is the model name passed to the provider for this call.
	Model string

	// Endpoint is the provider's base URL. Stored so a swap to a new
	// endpoint is visible in the audit history.
	Endpoint string

	// TokensIn / TokensOut are pulled from the provider's usage
	// field. Zero when the upstream omits usage.
	TokensIn  int
	TokensOut int

	// LatencyMs is the wall-clock time from request start to response
	// body fully read.
	LatencyMs int64

	// Outcome is one of: "ok" | "timeout" | "error" | "rejected".
	// "rejected" is for pre-flight failures (feature off, empty
	// prompt) where no HTTP call was made.
	Outcome string

	// ErrorMsg carries the error string when Outcome != "ok".
	ErrorMsg string

	// ResultIDRef is an optional caller-supplied correlation id. For
	// DLQ cluster naming this is the fingerprint; for transformation-
	// from-example it's the pipeline id; etc.
	ResultIDRef string

	// At is when the call started (UTC). The storage layer fills this
	// in from time.Now().UTC() when zero.
	At time.Time
}

// PromptHash returns the first 16 hex characters of sha256(content).
// Exported as a helper so callers can pre-compute it for their own
// logging without depending on the AuditRow shape.
func PromptHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:16]
}

// NoopAuditLogger discards every row. Useful in unit tests and during
// the bootstrap window before storage is wired. Production wires
// internal/storage/ai_audit.go.
type NoopAuditLogger struct{}

// Log implements AuditLogger by discarding the row.
func (NoopAuditLogger) Log(_ context.Context, _ AuditRow) {}
