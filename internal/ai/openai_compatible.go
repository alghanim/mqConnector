package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// OpenAIProvider speaks the OpenAI chat-completions wire format —
// works against OpenAI itself, plus any compatible self-hosted
// endpoint (vLLM, Ollama, LiteLLM, TGI, llama.cpp server, etc.).
//
// Pure stdlib: net/http + encoding/json. No SDK dependency. The
// fall-back path for structured output handles servers that reject
// the json_schema response_format field (returning a 400 / 422) by
// re-sending with the schema folded into the system prompt.
type OpenAIProvider struct {
	cfg     Config
	client  *http.Client
	counter *CallCounter
	audit   AuditLogger
	logger  *slog.Logger
}

// OpenAIOption mutates an OpenAIProvider after construction. Used by
// tests to override the http client; production wiring uses the
// defaults.
type OpenAIOption func(*OpenAIProvider)

// WithHTTPClient overrides the default http.Client. Production code
// shouldn't need this; tests use it to inject httptest.NewServer's
// transport.
func WithHTTPClient(c *http.Client) OpenAIOption {
	return func(p *OpenAIProvider) {
		if c != nil {
			p.client = c
		}
	}
}

// NewOpenAIProvider constructs a provider. counter and audit may be
// nil — the provider will skip the corresponding emit if so, which
// is convenient for unit tests but never production.
func NewOpenAIProvider(cfg Config, counter *CallCounter, audit AuditLogger, logger *slog.Logger, opts ...OpenAIOption) *OpenAIProvider {
	if logger == nil {
		logger = slog.Default()
	}
	p := &OpenAIProvider{
		cfg:     cfg,
		counter: counter,
		audit:   audit,
		logger:  logger,
		client: &http.Client{
			// Per-call timeouts are enforced via the ctx passed to
			// Do(); this is a belt-and-braces upper bound that fires
			// when ctx has no deadline. Generous so requests close to
			// the per-call budget still complete.
			Timeout: time.Duration(cfg.effectiveTimeoutMs()*2) * time.Millisecond,
		},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Name implements LLMProvider.
func (p *OpenAIProvider) Name() string { return "openai" }

// Complete runs a single chat-completions exchange and returns the
// assistant's text content.
func (p *OpenAIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error) {
	if err := p.preflight(ctx, req); err != nil {
		return CompletionResult{}, err
	}
	body := p.buildBody(req, nil, "")
	start := time.Now()
	raw, status, err := p.do(ctx, body)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return p.failComplete(ctx, req, latency, status, err)
	}
	parsed, perr := parseChatResponse(raw)
	if perr != nil {
		return p.failComplete(ctx, req, latency, status,
			&Error{Kind: "bad_body", Err: perr})
	}
	res := CompletionResult{
		Text:         parsed.Text,
		TokensIn:     parsed.TokensIn,
		TokensOut:    parsed.TokensOut,
		LatencyMs:    latency,
		ProviderName: p.Name(),
	}
	p.emit(ctx, req, "ok", "", res, "")
	return res, nil
}

// StructuredOutput requests JSON matching req.Schema. Tries the
// response_format=json_schema path first; on a 400/422 (server
// doesn't support it) falls back to folding the schema into the
// system prompt and parsing the assistant content as JSON.
func (p *OpenAIProvider) StructuredOutput(ctx context.Context, req StructuredRequest) (json.RawMessage, error) {
	if err := p.preflight(ctx, req.CompletionRequest); err != nil {
		return nil, err
	}
	if len(req.Schema) == 0 {
		return nil, &Error{Kind: "rejected", Err: errors.New("empty schema")}
	}
	body := p.buildBody(req.CompletionRequest, req.Schema, req.SchemaName)
	start := time.Now()
	raw, status, err := p.do(ctx, body)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		// If the server rejected the response_format field (400/422),
		// retry with the schema folded into the system prompt. The
		// fall-back path is for endpoints that speak chat-completions
		// but haven't shipped the structured-outputs extension.
		var aiErr *Error
		if errors.As(err, &aiErr) && aiErr.Kind == "bad_status" &&
			(aiErr.StatusCode == http.StatusBadRequest || aiErr.StatusCode == http.StatusUnprocessableEntity) {
			fallback := p.buildFallbackBody(req)
			startFB := time.Now()
			raw, status, err = p.do(ctx, fallback)
			latency += time.Since(startFB).Milliseconds()
		}
	}
	if err != nil {
		return p.failStructured(ctx, req, latency, status, err)
	}
	parsed, perr := parseChatResponse(raw)
	if perr != nil {
		return p.failStructured(ctx, req, latency, status,
			&Error{Kind: "bad_body", Err: perr})
	}
	// The text payload may contain leading/trailing whitespace or a
	// code fence (some endpoints wrap JSON in ```json … ```). Strip
	// both before validating.
	jsonText := stripCodeFence(strings.TrimSpace(parsed.Text))
	if !json.Valid([]byte(jsonText)) {
		return p.failStructured(ctx, req, latency, status,
			&Error{Kind: "bad_body", Err: fmt.Errorf("response is not valid JSON: %q", truncate(jsonText, 200))})
	}
	res := CompletionResult{
		Text:         jsonText,
		TokensIn:     parsed.TokensIn,
		TokensOut:    parsed.TokensOut,
		LatencyMs:    latency,
		ProviderName: p.Name(),
	}
	p.emit(ctx, req.CompletionRequest, "ok", "", res, "")
	return json.RawMessage(jsonText), nil
}

// preflight runs the cheap rejection paths: master switch, feature
// allowlist, empty content. Hits before any HTTP work.
func (p *OpenAIProvider) preflight(ctx context.Context, req CompletionRequest) error {
	if !p.cfg.Enabled {
		err := &Error{Kind: "disabled", Err: errors.New("ai disabled")}
		p.emit(ctx, req, "rejected", err.Error(), CompletionResult{}, "")
		return err
	}
	if !p.cfg.Allows(req.Feature) {
		err := &Error{Kind: "feature_off",
			Err: fmt.Errorf("feature %q not in allowlist", req.Feature)}
		p.emit(ctx, req, "rejected", err.Error(), CompletionResult{}, "")
		return err
	}
	if strings.TrimSpace(req.User) == "" {
		err := &Error{Kind: "rejected", Err: errors.New("empty user content")}
		p.emit(ctx, req, "rejected", err.Error(), CompletionResult{}, "")
		return err
	}
	return nil
}

// buildBody assembles the chat-completions request body. When schema
// is non-nil the response_format=json_schema field is included.
func (p *OpenAIProvider) buildBody(req CompletionRequest, schema json.RawMessage, schemaName string) []byte {
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = p.cfg.effectiveMaxTokens()
	}

	messages := make([]map[string]string, 0, 2)
	if strings.TrimSpace(req.System) != "" {
		messages = append(messages, map[string]string{
			"role": "system", "content": req.System,
		})
	}
	messages = append(messages, map[string]string{
		"role": "user", "content": req.User,
	})

	body := map[string]any{
		"model":       p.cfg.Model,
		"messages":    messages,
		"max_tokens":  maxTok,
		"temperature": req.Temperature,
	}
	if len(schema) > 0 {
		name := schemaName
		if name == "" {
			name = "structured_output"
		}
		body["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   name,
				"strict": true,
				"schema": json.RawMessage(schema),
			},
		}
	}
	out, _ := json.Marshal(body)
	return out
}

// buildFallbackBody re-renders the request without response_format,
// folding the schema into the system prompt as a strict JSON-only
// instruction. Used when the upstream rejects the json_schema field.
func (p *OpenAIProvider) buildFallbackBody(req StructuredRequest) []byte {
	sys := req.System
	if sys == "" {
		sys = "You are a careful assistant."
	}
	sys += "\n\nYou MUST respond with valid JSON only — no prose, no markdown, no code fences. The JSON must conform to the following JSON Schema:\n"
	sys += string(req.Schema)
	mod := req.CompletionRequest
	mod.System = sys
	return p.buildBody(mod, nil, "")
}

// do posts the request body and returns the response body bytes plus
// HTTP status. Wraps every failure mode in an *Error.
func (p *OpenAIProvider) do(ctx context.Context, body []byte) ([]byte, int, error) {
	// Honour ctx.Deadline if set; otherwise apply the configured
	// TimeoutMs so a stuck endpoint can never hang the caller.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx,
			time.Duration(p.cfg.effectiveTimeoutMs())*time.Millisecond)
		defer cancel()
	}
	url := strings.TrimRight(p.cfg.Endpoint, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, &Error{Kind: "transport", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.AuthHeader != "" {
		req.Header.Set("Authorization", p.cfg.AuthHeader)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// Context-deadline-exceeded is distinct from a transport
		// failure for audit/metrics purposes.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, 0, &Error{Kind: "timeout", Err: err}
		}
		return nil, 0, &Error{Kind: "transport", Err: err}
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, resp.StatusCode, &Error{Kind: "transport", Err: readErr}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return respBody, resp.StatusCode, &Error{
			Kind:       "bad_status",
			StatusCode: resp.StatusCode,
			Err:        fmt.Errorf("upstream %d: %s", resp.StatusCode, truncate(string(respBody), 200)),
		}
	}
	return respBody, resp.StatusCode, nil
}

// failComplete records a failed Complete call and returns the error.
func (p *OpenAIProvider) failComplete(ctx context.Context, req CompletionRequest, latency int64, _ int, err error) (CompletionResult, error) {
	outcome := outcomeFor(err)
	p.emit(ctx, req, outcome, err.Error(), CompletionResult{LatencyMs: latency, ProviderName: p.Name()}, "")
	return CompletionResult{}, err
}

// failStructured records a failed StructuredOutput call and returns
// the error.
func (p *OpenAIProvider) failStructured(ctx context.Context, req StructuredRequest, latency int64, _ int, err error) (json.RawMessage, error) {
	outcome := outcomeFor(err)
	p.emit(ctx, req.CompletionRequest, outcome, err.Error(), CompletionResult{LatencyMs: latency, ProviderName: p.Name()}, "")
	return nil, err
}

// emit fires both the audit logger and the metrics counter for one
// call. Best-effort — never blocks the caller.
func (p *OpenAIProvider) emit(ctx context.Context, req CompletionRequest, outcome, errMsg string, res CompletionResult, resultRef string) {
	if p.counter != nil {
		p.counter.Inc(req.Feature, p.cfg.Model, outcome)
	}
	if p.audit != nil && p.cfg.AuditEvery {
		row := AuditRow{
			Feature:     req.Feature,
			TenantID:    tenantFromContext(ctx),
			CallerSub:   callerFromContext(ctx),
			PromptHash:  PromptHash(req.User),
			Model:       p.cfg.Model,
			Endpoint:    p.cfg.Endpoint,
			TokensIn:    res.TokensIn,
			TokensOut:   res.TokensOut,
			LatencyMs:   res.LatencyMs,
			Outcome:     outcome,
			ErrorMsg:    errMsg,
			ResultIDRef: resultRef,
			At:          time.Now().UTC(),
		}
		p.audit.Log(ctx, row)
	}
}

// outcomeFor maps an error to a metrics outcome label.
func outcomeFor(err error) string {
	var aiErr *Error
	if errors.As(err, &aiErr) {
		switch aiErr.Kind {
		case "timeout":
			return "timeout"
		case "disabled", "feature_off", "rejected":
			return "rejected"
		}
	}
	return "error"
}

// chatResponse is the subset of the chat-completions response we
// actually read. Defensive: every field is optional so a slightly-
// different upstream wire shape doesn't tank the parse.
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type parsedResponse struct {
	Text      string
	TokensIn  int
	TokensOut int
}

// parseChatResponse decodes the JSON body of a chat-completions
// response and pulls out the content + usage. Returns an error when
// the body is unparseable OR when no choices are present.
func parseChatResponse(body []byte) (parsedResponse, error) {
	var r chatResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return parsedResponse{}, fmt.Errorf("decode response: %w", err)
	}
	if len(r.Choices) == 0 {
		return parsedResponse{}, errors.New("response has no choices")
	}
	return parsedResponse{
		Text:      r.Choices[0].Message.Content,
		TokensIn:  r.Usage.PromptTokens,
		TokensOut: r.Usage.CompletionTokens,
	}, nil
}

// stripCodeFence removes ```json … ``` or ``` … ``` wrappers some
// providers emit even when asked for raw JSON. Leaves un-fenced
// content untouched.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the opening fence + optional language tag, then the
	// trailing fence. Conservative: only strip when both ends match.
	rest := strings.TrimPrefix(s, "```")
	// Skip an optional language identifier on the same line.
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 && !strings.ContainsAny(rest[:nl], " \t{[") {
		rest = rest[nl+1:]
	}
	rest = strings.TrimSuffix(strings.TrimRight(rest, "\n "), "```")
	return strings.TrimSpace(rest)
}

// truncate caps the length of an error message for log lines.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Context keys for caller / tenant. The server package writes both
// into the request context; this provider reads them so the audit
// row records who triggered the call. We don't import internal/auth
// here to avoid a cycle (the auth package is allowed to consume
// internal/ai in the future, not the other way around) — instead we
// define our own keys and the server wires both via WithValue.

type contextKey string

const (
	tenantContextKey contextKey = "ai.tenant_id"
	callerContextKey contextKey = "ai.caller_sub"
)

// WithCaller stamps the caller's sub onto the context so any
// downstream provider call records it. Returns the input ctx
// unchanged when sub is empty.
func WithCaller(ctx context.Context, sub string) context.Context {
	if sub == "" {
		return ctx
	}
	return context.WithValue(ctx, callerContextKey, sub)
}

// WithTenant stamps the tenant id onto the context for the same
// reason. Returns the input ctx unchanged when id is empty.
func WithTenant(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, tenantContextKey, id)
}

func tenantFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(tenantContextKey).(string); ok {
		return v
	}
	return ""
}

func callerFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(callerContextKey).(string); ok {
		return v
	}
	return ""
}
