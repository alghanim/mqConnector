package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// FakeProvider is a test double for LLMProvider. Per-feature canned
// responses + per-feature canned errors let tests drive both the
// happy path and every failure mode without standing up an HTTP
// server.
//
// Concurrency-safe: every method takes the mutex before reading the
// maps. Use SetCompletion / SetStructured / SetError to program the
// double and Calls() to inspect the captured request log.
type FakeProvider struct {
	mu sync.Mutex

	// completions maps feature → text response. Unmatched features
	// return an empty result with no error (so a test that forgets to
	// program a feature gets a visible "" rather than a panic).
	completions map[Capability]string

	// structured maps feature → JSON response.
	structured map[Capability]json.RawMessage

	// errs maps feature → error to return. Wins over completions/
	// structured when set.
	errs map[Capability]error

	// calls is the captured log of every Complete / StructuredOutput
	// invocation, in order. Tests inspect this to assert prompt
	// content, feature labels, etc.
	calls []FakeCall

	// audit is the optional audit logger. When set, every captured
	// call also fires through to it (mirroring what production wiring
	// does).
	audit AuditLogger

	// counter is the optional metrics counter. Same lifecycle as audit.
	counter *CallCounter

	// name overrides the provider's reported Name(). Defaults to "fake".
	name string

	// model overrides the model label on audit/metrics rows.
	model string

	// allowAll, when true, bypasses the AllowedFeatures gate and
	// answers every request. When false (default) the provider checks
	// AllowedFeatures and short-circuits with feature_off otherwise.
	allowAll bool

	// allowed is the per-call allowlist. Empty + allowAll=false means
	// "everything is allowed" — the fake's gate is opt-in.
	allowed map[Capability]bool
}

// FakeCall captures one invocation for later assertion.
type FakeCall struct {
	Feature      Capability
	System       string
	User         string
	Schema       string
	SchemaName   string
	IsStructured bool
}

// NewFakeProvider returns a fresh fake. All maps are lazy-allocated.
func NewFakeProvider() *FakeProvider {
	return &FakeProvider{
		completions: map[Capability]string{},
		structured:  map[Capability]json.RawMessage{},
		errs:        map[Capability]error{},
		name:        "fake",
		model:       "fake-model",
		allowAll:    true,
	}
}

// Name implements LLMProvider.
func (f *FakeProvider) Name() string { return f.name }

// SetName overrides the reported provider name.
func (f *FakeProvider) SetName(n string) { f.mu.Lock(); defer f.mu.Unlock(); f.name = n }

// SetCompletion programs the response for one feature's Complete call.
func (f *FakeProvider) SetCompletion(feat Capability, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completions[feat] = text
}

// SetStructured programs the response for one feature's StructuredOutput call.
func (f *FakeProvider) SetStructured(feat Capability, body json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.structured[feat] = body
}

// SetError programs the error returned for one feature, overriding
// any completion/structured response. Pass nil to clear.
func (f *FakeProvider) SetError(feat Capability, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.errs, feat)
		return
	}
	f.errs[feat] = err
}

// SetAudit installs an optional audit logger that fires after every
// call. Used in tests that want to assert audit rows are emitted.
func (f *FakeProvider) SetAudit(a AuditLogger) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audit = a
}

// SetCounter installs an optional metrics counter that fires after every call.
func (f *FakeProvider) SetCounter(c *CallCounter) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counter = c
}

// AllowOnly restricts the fake to the given features. Calls for
// other features short-circuit with an *Error{Kind:"feature_off"}.
func (f *FakeProvider) AllowOnly(features ...Capability) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.allowAll = false
	f.allowed = make(map[Capability]bool, len(features))
	for _, feat := range features {
		f.allowed[feat] = true
	}
}

// Calls returns a snapshot of the captured call log.
func (f *FakeProvider) Calls() []FakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FakeCall, len(f.calls))
	copy(out, f.calls)
	return out
}

// Reset clears the captured call log. Useful in table-driven tests
// that share a provider across sub-tests.
func (f *FakeProvider) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

// Complete implements LLMProvider.
func (f *FakeProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error) {
	f.mu.Lock()
	if !f.allowAll && !f.allowed[req.Feature] {
		f.mu.Unlock()
		err := &Error{Kind: "feature_off",
			Err: fmt.Errorf("feature %q not allowed by fake", req.Feature)}
		f.emit(ctx, req, "rejected", err.Error(), 0)
		return CompletionResult{}, err
	}
	f.calls = append(f.calls, FakeCall{
		Feature: req.Feature, System: req.System, User: req.User,
	})
	if err, ok := f.errs[req.Feature]; ok {
		f.mu.Unlock()
		f.emit(ctx, req, outcomeFor(err), err.Error(), 0)
		return CompletionResult{}, err
	}
	text := f.completions[req.Feature]
	model := f.model
	name := f.name
	f.mu.Unlock()
	res := CompletionResult{
		Text:         text,
		TokensIn:     len(req.User) / 4,
		TokensOut:    len(text) / 4,
		LatencyMs:    1,
		ProviderName: name,
	}
	_ = model
	f.emit(ctx, req, "ok", "", res.LatencyMs)
	return res, nil
}

// StructuredOutput implements LLMProvider.
func (f *FakeProvider) StructuredOutput(ctx context.Context, req StructuredRequest) (json.RawMessage, error) {
	f.mu.Lock()
	if !f.allowAll && !f.allowed[req.Feature] {
		f.mu.Unlock()
		err := &Error{Kind: "feature_off",
			Err: fmt.Errorf("feature %q not allowed by fake", req.Feature)}
		f.emit(ctx, req.CompletionRequest, "rejected", err.Error(), 0)
		return nil, err
	}
	f.calls = append(f.calls, FakeCall{
		Feature: req.Feature, System: req.System, User: req.User,
		Schema: string(req.Schema), SchemaName: req.SchemaName, IsStructured: true,
	})
	if err, ok := f.errs[req.Feature]; ok {
		f.mu.Unlock()
		f.emit(ctx, req.CompletionRequest, outcomeFor(err), err.Error(), 0)
		return nil, err
	}
	body, ok := f.structured[req.Feature]
	if !ok || len(body) == 0 {
		f.mu.Unlock()
		err := errors.New("fake: no structured response programmed")
		f.emit(ctx, req.CompletionRequest, "error", err.Error(), 0)
		return nil, err
	}
	name := f.name
	f.mu.Unlock()
	res := CompletionResult{
		Text:         string(body),
		TokensIn:     len(req.User) / 4,
		TokensOut:    len(body) / 4,
		LatencyMs:    1,
		ProviderName: name,
	}
	f.emit(ctx, req.CompletionRequest, "ok", "", res.LatencyMs)
	return body, nil
}

// emit mirrors the production provider's audit + counter wiring so
// tests that exercise the AI surface end-to-end see the same side
// effects. Best-effort; nil sinks are no-ops.
func (f *FakeProvider) emit(ctx context.Context, req CompletionRequest, outcome, errMsg string, latency int64) {
	f.mu.Lock()
	audit := f.audit
	counter := f.counter
	model := f.model
	f.mu.Unlock()
	if counter != nil {
		counter.Inc(req.Feature, model, outcome)
	}
	if audit != nil {
		audit.Log(ctx, AuditRow{
			Feature:    req.Feature,
			TenantID:   tenantFromContext(ctx),
			CallerSub:  callerFromContext(ctx),
			PromptHash: PromptHash(req.User),
			Model:      model,
			LatencyMs:  latency,
			Outcome:    outcome,
			ErrorMsg:   errMsg,
			At:         time.Now().UTC(),
		})
	}
}
