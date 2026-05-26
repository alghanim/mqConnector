package ai

// Capability names a single AI-driven feature. Features must be
// explicitly listed in Config.Features to be usable — an unrecognised
// or unlisted capability short-circuits to ErrAINotAvailable before any
// HTTP work, which is the governance surface that keeps off-by-default
// features genuinely off in air-gapped deploys.
type Capability string

// Known capabilities. Adding a new feature here is a deliberate
// decision: the capability MUST be:
//   - documented in MQCONNECTOR_PLATFORM_EVOLUTION.md
//   - opt-in via Config.Features (defaults exclude it)
//   - surfaced in the UI behind an "AI suggestion" chip
//   - audit-logged on every call
const (
	// CapDLQClusterNaming asks the LLM to name + summarise + suggest
	// next-action for a single recurring DLQ failure cluster. See
	// internal/dlq/cluster/naming.go.
	CapDLQClusterNaming Capability = "dlq_cluster_naming"

	// CapTransformationFromExample reads a before/after JSON pair and
	// proposes a list of rename/mask/move/set/delete transforms that
	// would turn the source into the target. Operator review required.
	CapTransformationFromExample Capability = "transformation_from_example"

	// CapExplainWhySummary takes a "what happened" event chain and
	// returns a 2-sentence summary suitable for the dashboard timeline.
	CapExplainWhySummary Capability = "explain_why_summary"

	// CapRedactionPatternDetect scans a sample payload and proposes
	// regex / jsonpath candidates that look like PII or secrets.
	// Operator must accept each suggestion individually.
	CapRedactionPatternDetect Capability = "redaction_pattern_detect"
)

// Config is the AI subsystem's YAML/env surface. Mirrored from the
// docs/ai-ops.md sketch. Defaults are conservative — Enabled=false,
// Features empty, audit on. An operator who pastes the example block
// from config.example.yaml into a fresh deploy gets a usable, gated
// setup that does nothing until they opt features in.
type Config struct {
	// Enabled is the master switch. When false, every provider call
	// short-circuits to ErrAINotAvailable without touching the network.
	Enabled bool `yaml:"enabled"`

	// Provider names the implementation. Only "openai_compatible" is
	// shipped in v1 — additional providers add new switch arms in
	// cmd/mqconnector's wiring.
	Provider string `yaml:"provider"`

	// Endpoint is the base URL of the OpenAI-compatible API, e.g.
	// "http://llm.internal/v1" or "https://api.openai.com/v1". The
	// client posts to <Endpoint>/chat/completions.
	Endpoint string `yaml:"endpoint"`

	// Model is the model name passed in the chat-completions body.
	// Examples: "qwen2.5-14b-instruct", "gpt-4o-mini",
	// "llama-3.1-8b-instruct".
	Model string `yaml:"model"`

	// AuthHeader is the raw value sent in the Authorization header
	// (typically "Bearer <api-key>"). Empty means no Authorization
	// header is sent — useful for self-hosted endpoints that gate
	// access via mTLS or network policy instead.
	AuthHeader string `yaml:"auth_header"`

	// TimeoutMs caps a single Complete/StructuredOutput call.
	// Defaults to 8000 (8 seconds) when zero. The deadline is honoured
	// via the request's context so callers that pass a shorter
	// ctx.Deadline win.
	TimeoutMs int `yaml:"timeout_ms"`

	// MaxTokens defaults to 1024 when zero. Per-feature overrides on
	// CompletionRequest.MaxTokens win.
	MaxTokens int `yaml:"max_tokens"`

	// Features is the explicit allowlist of enabled capabilities.
	// Nothing is enabled by default — the operator must opt each
	// feature in.
	Features []Capability `yaml:"features"`

	// AuditEvery controls whether every provider attempt is audited.
	// Defaults to true; set false only in load-test or local dev
	// scenarios where audit volume matters more than completeness.
	AuditEvery bool `yaml:"audit_every_call"`
}

// Allows reports whether the given capability is enabled. Returns
// false when Enabled=false, regardless of the Features allowlist —
// callers don't have to double-check the master switch.
func (c Config) Allows(cap Capability) bool {
	if !c.Enabled {
		return false
	}
	for _, f := range c.Features {
		if f == cap {
			return true
		}
	}
	return false
}

// DefaultTimeoutMs is the fallback when Config.TimeoutMs is zero.
const DefaultTimeoutMs = 8000

// DefaultMaxTokens is the fallback when Config.MaxTokens is zero.
const DefaultMaxTokens = 1024

// effectiveTimeoutMs returns TimeoutMs if set, else DefaultTimeoutMs.
func (c Config) effectiveTimeoutMs() int {
	if c.TimeoutMs > 0 {
		return c.TimeoutMs
	}
	return DefaultTimeoutMs
}

// effectiveMaxTokens returns MaxTokens if set, else DefaultMaxTokens.
func (c Config) effectiveMaxTokens() int {
	if c.MaxTokens > 0 {
		return c.MaxTokens
	}
	return DefaultMaxTokens
}
