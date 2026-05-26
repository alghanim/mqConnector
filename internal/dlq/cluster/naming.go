package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mqConnector/internal/ai"
)

// ErrAINotAvailable is re-exported from internal/ai so callers of
// Name() can branch on a single sentinel without taking a second
// import. The fallback path (deterministic naming from the template)
// is owned by the caller.
var ErrAINotAvailable = ai.ErrAINotAvailable

// NameRequest is a single cluster the operator wants named/explained.
// All fields come from the cluster rollup (see
// internal/server/handlers_dlq_intel.go dlqCluster).
type NameRequest struct {
	// Fingerprint is the cluster's SimHash. Passed straight through to
	// the audit row's ResultIDRef so a Wave 5 audit drill-down can
	// answer "what did the LLM say about cluster XYZ?".
	Fingerprint string

	// Template is the tokenised error template the cluster rolls up.
	// Becomes the deterministic fallback title if the LLM call fails.
	Template string

	// Count is the number of DLQ rows in this cluster.
	Count int

	// PipelinesAffected is the distinct list of pipeline ids whose
	// rows landed in this cluster. May be empty for bridge-endpoint
	// failures.
	PipelinesAffected []string

	// FailingStages is the distinct list of stage names that emitted
	// the failure. May be empty for send-side failures.
	FailingStages []string

	// SampleErrors is 1-3 representative full error strings. Bounded
	// to keep the prompt size predictable; the caller is responsible
	// for selecting representative samples.
	SampleErrors []string
}

// NameResult is the LLM's output: a short human title + 2-sentence
// summary + 1-sentence "what to do next" suggestion. The shape is
// constrained by the JSON schema below; the LLM is asked to enforce
// the length budgets but we re-clamp them on parse to defend against
// a model that ignores instructions.
type NameResult struct {
	Title      string `json:"title"`      // ≤ 80 chars
	Summary    string `json:"summary"`    // ≤ 240 chars
	Suggestion string `json:"suggestion"` // ≤ 200 chars
}

// nameSystemPrompt establishes the model's role + the output
// constraints. Versioned with the code rather than the database so a
// rollback of the binary rolls back the prompt — debugging a "the
// LLM changed its tone" report becomes a git blame on this constant.
const nameSystemPrompt = `You are a senior site-reliability engineer naming a recurring failure pattern in a message-queue pipeline. You will be given:
- A failure template (with variable parts collapsed to placeholders)
- The count of failures in the cluster
- The affected pipeline IDs
- The failing stage names
- One or more representative error strings

Your task: produce a short title, a two-sentence summary, and a one-sentence next-action suggestion that helps an operator triage this failure quickly.

Rules:
- Return ONLY valid JSON matching the provided schema. No prose, no markdown, no code fences.
- title:      <= 8 words, no trailing punctuation.
- summary:    <= 2 sentences explaining the likely root cause in operator terms.
- suggestion: <= 1 sentence with a concrete next action ("check X", "ask owner of Y to redeploy", "increase Z timeout").
- Do NOT speculate beyond what the inputs support.
- Do NOT include the cluster fingerprint, raw stack traces, or any payload values in the output.`

// nameJSONSchema constrains the LLM's structured output. The
// json_schema response_format makes this strict on compatible
// endpoints; the prompt fallback (when an endpoint rejects the field)
// re-includes the schema text inside the system prompt.
//
// additionalProperties=false locks the shape down — any extra field
// is a schema violation, so a model that wants to be chatty hits the
// validator before it reaches the operator.
var nameJSONSchema = json.RawMessage(`{
  "type": "object",
  "additionalProperties": false,
  "required": ["title", "summary", "suggestion"],
  "properties": {
    "title":      { "type": "string", "maxLength": 80 },
    "summary":    { "type": "string", "maxLength": 240 },
    "suggestion": { "type": "string", "maxLength": 200 }
  }
}`)

// Name takes a cluster request and returns an AI-generated name.
// On any provider failure or when the AI subsystem doesn't allow
// CapDLQClusterNaming, returns (NameResult{}, ErrAINotAvailable) so
// the caller falls back to a deterministic naming path (e.g. use the
// first line of req.Template as the title).
//
// Caching is NOT a concern of this function — the HTTP handler that
// consumes Name owns the cache because the cache key (fingerprint)
// also gates the audit row's ResultIDRef. A request-level cache miss
// produces a fresh audit row; a cache hit doesn't.
func Name(
	ctx context.Context,
	provider ai.LLMProvider,
	auditLogger ai.AuditLogger,
	cfg ai.Config,
	req NameRequest,
) (NameResult, error) {
	if !cfg.Allows(ai.CapDLQClusterNaming) {
		return NameResult{}, ErrAINotAvailable
	}
	if provider == nil {
		return NameResult{}, ErrAINotAvailable
	}
	prompt := renderNamePrompt(req)
	out, err := provider.StructuredOutput(ctx, ai.StructuredRequest{
		CompletionRequest: ai.CompletionRequest{
			Feature:     ai.CapDLQClusterNaming,
			System:      nameSystemPrompt,
			User:        prompt,
			MaxTokens:   400, // generous: title+summary+suggestion + JSON overhead
			Temperature: 0.2, // lean deterministic; this is operations text
		},
		SchemaName: "dlq_cluster_name",
		Schema:     nameJSONSchema,
	})
	if err != nil {
		// The provider already wrote the audit row + counter. The
		// caller's fallback path handles the surface so we return
		// the wrapped error and let errors.Is(err, ErrAINotAvailable)
		// branch the caller toward the deterministic path.
		_ = auditLogger // documented receiver; the provider owns the emit
		return NameResult{}, err
	}
	var nr NameResult
	if jerr := json.Unmarshal(out, &nr); jerr != nil {
		return NameResult{}, &ai.Error{Kind: "bad_body",
			Err: fmt.Errorf("name result: %w", jerr)}
	}
	nr.Title = clamp(strings.TrimSpace(nr.Title), 80)
	nr.Summary = clamp(strings.TrimSpace(nr.Summary), 240)
	nr.Suggestion = clamp(strings.TrimSpace(nr.Suggestion), 200)
	if nr.Title == "" {
		return NameResult{}, &ai.Error{Kind: "bad_body",
			Err: errors.New("name result missing title")}
	}
	return nr, nil
}

// renderNamePrompt produces the user-side content for the LLM call.
// Kept as a separate function so tests can inspect the exact bytes
// without having to fish them out of an http.Request body.
func renderNamePrompt(req NameRequest) string {
	var b strings.Builder
	b.WriteString("Cluster fingerprint: ")
	b.WriteString(req.Fingerprint)
	b.WriteString("\n\nFailure template:\n")
	b.WriteString(req.Template)
	fmt.Fprintf(&b, "\n\nFailure count: %d", req.Count)
	if len(req.PipelinesAffected) > 0 {
		b.WriteString("\nAffected pipelines: ")
		b.WriteString(strings.Join(req.PipelinesAffected, ", "))
	}
	if len(req.FailingStages) > 0 {
		b.WriteString("\nFailing stages: ")
		b.WriteString(strings.Join(req.FailingStages, ", "))
	}
	if len(req.SampleErrors) > 0 {
		b.WriteString("\n\nRepresentative errors:")
		for i, s := range req.SampleErrors {
			fmt.Fprintf(&b, "\n%d. %s", i+1, s)
		}
	}
	return b.String()
}

// clamp truncates s to a max byte length while trying to honour
// rune boundaries. Conservative: if the cut point lands inside a
// multi-byte rune, walks back to the nearest boundary so we never
// emit a malformed UTF-8 sequence. Adds an ellipsis when truncation
// happened so the operator sees the boundary visually.
func clamp(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Walk back to a safe rune boundary.
	cut := max
	for cut > 0 && (s[cut-1]&0xC0) == 0x80 {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return s[:cut] + "…"
}
