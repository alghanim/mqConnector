// Package ai provides the LLM provider abstraction that powers
// mqConnector's AI workstream (DLQ cluster naming, transformation
// suggestions, redaction-pattern detection, "explain why" summaries).
//
// Design constraints — these are non-negotiable and the rest of the
// codebase depends on them:
//
//  1. Air-gapped first. mqConnector ships into environments with no
//     internet egress. Every AI feature MUST degrade to a deterministic
//     path when the configured endpoint is unreachable or AI is disabled.
//     Callers receive ErrAINotAvailable and pick their fallback.
//
//  2. Every call is audited. AuditLogger.Log is invoked for every
//     provider attempt — success, timeout, error, or rejection — so the
//     operator can answer "what did the LLM see and what did it return?"
//     from a single audit row. The default NoopAuditLogger is for tests
//     only; production wires the storage-backed implementation in
//     cmd/mqconnector.
//
//  3. Suggestions never auto-apply. The provider returns text or JSON;
//     consumers surface it as a "AI suggestion" chip and require an
//     operator click to apply. Auto-application is a Wave 5 governance
//     violation even when the model is perfectly correct.
//
//  4. Pure stdlib. No SDK dependencies (openai-go, anthropic-sdk, etc.)
//     allowed. The OpenAI-compatible client speaks the chat-completions
//     wire format directly via net/http + encoding/json so air-gapped
//     deploys can point at any compatible endpoint (vLLM, Ollama, TGI,
//     LiteLLM, etc.) without bringing in a vendor surface.
//
//  5. Feature gating. Capabilities are enabled per-call via
//     Config.Allows(); features not in the allowlist short-circuit to
//     ErrAINotAvailable before any HTTP work. The allowlist is the
//     governance surface — every new feature lands here first.
//
// Wire-up:
//
//	provider := ai.NewOpenAIProvider(cfg, counter, logger)
//	audit    := /* storage.AIAuditRepo, implements ai.AuditLogger */
//
//	res, err := provider.StructuredOutput(ctx, ai.StructuredRequest{
//	    CompletionRequest: ai.CompletionRequest{Feature: ai.CapDLQClusterNaming, ...},
//	    SchemaName: "dlq_cluster_name",
//	    Schema:     []byte(`{"type":"object",...}`),
//	})
//
// On any error the caller falls back to the deterministic path and
// surfaces the audit-row id so the operator can investigate why the LLM
// failed.
package ai
