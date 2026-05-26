package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrAINotAvailable is returned by providers and feature wrappers
// whenever the AI subsystem is disabled, the feature isn't in the
// allowlist, or the upstream endpoint is unreachable. Callers MUST
// treat this as "fall back to the deterministic path" — never as a
// fatal error.
var ErrAINotAvailable = errors.New("ai: provider not available")

// Error wraps a provider failure with structured context. Implements
// errors.Is(target, ErrAINotAvailable) for the common "feature off /
// endpoint down" path so callers can detect both with one check.
type Error struct {
	// Kind names the failure class for logs / metrics labels.
	// One of: "disabled" | "feature_off" | "timeout" |
	// "transport" | "bad_status" | "bad_body" | "rejected".
	Kind string

	// StatusCode is the HTTP status when Kind=="bad_status".
	StatusCode int

	// Err is the underlying cause; never nil.
	Err error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode != 0 {
		return fmt.Sprintf("ai: %s (status=%d): %v", e.Kind, e.StatusCode, e.Err)
	}
	return fmt.Sprintf("ai: %s: %v", e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Is reports e as ErrAINotAvailable for the off/unreachable kinds so
// the typical caller can write:
//
//	if errors.Is(err, ai.ErrAINotAvailable) { /* fallback */ }
//
// without enumerating Kind variants.
func (e *Error) Is(target error) bool {
	if target == ErrAINotAvailable {
		switch e.Kind {
		case "disabled", "feature_off", "transport", "timeout":
			return true
		}
	}
	return false
}

// LLMProvider is the abstraction every AI consumer talks to. Two
// methods cover the v1 surface: Complete for plain text, StructuredOutput
// for JSON-schema-constrained responses.
type LLMProvider interface {
	// Complete runs a single prompt and returns plain text. Idempotent
	// in the sense that the provider does not maintain conversation
	// state between calls — every Complete is a one-shot exchange.
	// ctx deadline becomes the HTTP timeout; a missing deadline falls
	// back to the provider's configured TimeoutMs.
	Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error)

	// StructuredOutput asks the LLM to emit JSON matching the supplied
	// schema. Implementations may use the OpenAI "json_schema"
	// response-format extension when available; otherwise they fall
	// back to a strict prompt that asks for JSON only.
	StructuredOutput(ctx context.Context, req StructuredRequest) (json.RawMessage, error)

	// Name is a short identifier for audit + metrics labels. Examples:
	// "openai" (for OpenAI-compatible endpoints), "fake" (for tests),
	// "noop" (for the disabled sentinel).
	Name() string
}

// CompletionRequest is the input to LLMProvider.Complete.
type CompletionRequest struct {
	// Feature names the capability this call belongs to. Used by the
	// audit logger + metrics counter; the provider also checks
	// Config.Allows(Feature) before any HTTP work.
	Feature Capability

	// System is the optional system-prompt content. Empty omits the
	// system message from the request body.
	System string

	// User is the user-content payload. Required — providers reject
	// empty user content with an Error{Kind:"rejected"}.
	User string

	// MaxTokens overrides Config.MaxTokens for this call. 0 = use
	// the provider's configured default.
	MaxTokens int

	// Temperature is the sampling temperature, 0..1. 0 leans toward
	// deterministic output; the value passes straight through to the
	// chat-completions body.
	Temperature float64
}

// CompletionResult is what Complete returns on success.
type CompletionResult struct {
	// Text is the model's generated content.
	Text string

	// TokensIn / TokensOut are pulled from the response's usage field
	// when present. Zero when the upstream omits usage.
	TokensIn  int
	TokensOut int

	// LatencyMs is the wall-clock time from request start to response
	// body fully read.
	LatencyMs int64

	// ProviderName mirrors LLMProvider.Name() so the audit row can
	// record which provider answered without re-fetching it.
	ProviderName string
}

// StructuredRequest extends CompletionRequest with a JSON-schema
// constraint. The provider sends response_format=json_schema where
// supported, and falls back to a strict prompt otherwise.
type StructuredRequest struct {
	CompletionRequest

	// SchemaName is a descriptive identifier for the schema, surfaced
	// in audit + metrics labels (e.g. "dlq_cluster_name").
	SchemaName string

	// Schema is the JSON schema body that constrains the response
	// shape. Must be valid JSON; the provider does NOT validate the
	// schema itself — that's the caller's contract.
	Schema json.RawMessage
}

// noopProvider is the sentinel returned when AI is disabled. Every
// method short-circuits to ErrAINotAvailable so the caller's fallback
// path runs without any HTTP traffic.
type noopProvider struct{}

// NewNoopProvider returns the sentinel "AI disabled" provider. Wired
// in cmd/mqconnector when cfg.AI.Enabled is false or Endpoint is empty.
func NewNoopProvider() LLMProvider { return noopProvider{} }

func (noopProvider) Complete(_ context.Context, _ CompletionRequest) (CompletionResult, error) {
	return CompletionResult{}, &Error{Kind: "disabled", Err: errors.New("ai disabled")}
}

func (noopProvider) StructuredOutput(_ context.Context, _ StructuredRequest) (json.RawMessage, error) {
	return nil, &Error{Kind: "disabled", Err: errors.New("ai disabled")}
}

func (noopProvider) Name() string { return "noop" }
