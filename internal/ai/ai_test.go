package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test plan (mirrors the spec in the task brief):
//
//  1. FakeProvider returns the canned response for the matched feature.
//  2. OpenAIProvider posts the right body + parses a happy-path 200.
//  3. OpenAIProvider returns ai.Error on non-2xx.
//  4. OpenAIProvider honours ctx deadline (200ms timeout, server sleeps 1s).
//  5. StructuredOutput parses the JSON content from the chat-completions
//     response.
//  6. AuditLogger.Log writes a row (asserted via a capturing logger that
//     implements AuditLogger).
//  7. CallCounter increments per call with the right labels.
//  8. Config.Allows returns false when feature isn't in the allowlist
//     (gate test).

// ─── Capturing audit logger ─────────────────────────────────────────────

type captureAudit struct {
	mu   sync.Mutex
	rows []AuditRow
}

func (c *captureAudit) Log(_ context.Context, row AuditRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append(c.rows, row)
}

func (c *captureAudit) Rows() []AuditRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]AuditRow, len(c.rows))
	copy(out, c.rows)
	return out
}

// ─── 1. FakeProvider canned response ────────────────────────────────────

func TestFakeProvider_ReturnsCannedCompletion(t *testing.T) {
	p := NewFakeProvider()
	p.SetCompletion(CapDLQClusterNaming, "Validation failures on customer.id")

	res, err := p.Complete(context.Background(), CompletionRequest{
		Feature: CapDLQClusterNaming, User: "cluster XYZ",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.Text != "Validation failures on customer.id" {
		t.Errorf("text = %q, want canned response", res.Text)
	}
	if res.ProviderName != "fake" {
		t.Errorf("provider name = %q, want fake", res.ProviderName)
	}
	calls := p.Calls()
	if len(calls) != 1 || calls[0].Feature != CapDLQClusterNaming {
		t.Errorf("calls = %+v, want one CapDLQClusterNaming call", calls)
	}
}

// ─── 2. OpenAIProvider happy-path body + parse ──────────────────────────

func TestOpenAIProvider_HappyPath(t *testing.T) {
	var got struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		MaxTokens   int     `json:"max_tokens"`
		Temperature float64 `json:"temperature"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization header = %q, want Bearer test-key", got)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"hello world"}}],
			"usage":{"prompt_tokens":7,"completion_tokens":2}
		}`))
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Enabled: true, Provider: "openai_compatible",
		Endpoint: srv.URL + "/v1", Model: "qwen2.5-7b",
		AuthHeader: "Bearer test-key",
		Features:   []Capability{CapExplainWhySummary},
		AuditEvery: true,
	}
	cap := NewCallCounter()
	aud := &captureAudit{}
	p := NewOpenAIProvider(cfg, cap, aud, nil)

	res, err := p.Complete(context.Background(), CompletionRequest{
		Feature: CapExplainWhySummary, System: "be brief",
		User: "summarise X", Temperature: 0.2,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if res.Text != "hello world" {
		t.Errorf("text = %q", res.Text)
	}
	if res.TokensIn != 7 || res.TokensOut != 2 {
		t.Errorf("tokens = in/out %d/%d", res.TokensIn, res.TokensOut)
	}
	if got.Model != "qwen2.5-7b" {
		t.Errorf("model = %q", got.Model)
	}
	if len(got.Messages) != 2 ||
		got.Messages[0].Role != "system" || got.Messages[0].Content != "be brief" ||
		got.Messages[1].Role != "user" || got.Messages[1].Content != "summarise X" {
		t.Errorf("messages mismatch: %+v", got.Messages)
	}
	if got.Temperature != 0.2 {
		t.Errorf("temperature = %v, want 0.2", got.Temperature)
	}
	if got.MaxTokens == 0 {
		t.Errorf("max_tokens should default to %d when caller passes 0, got %d", DefaultMaxTokens, got.MaxTokens)
	}
	if len(aud.Rows()) != 1 || aud.Rows()[0].Outcome != "ok" {
		t.Errorf("audit rows = %+v, want one ok row", aud.Rows())
	}
}

// ─── 3. OpenAIProvider non-2xx → ai.Error ───────────────────────────────

func TestOpenAIProvider_NonOK_ReturnsAIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Enabled: true, Endpoint: srv.URL, Model: "m",
		Features: []Capability{CapDLQClusterNaming}, AuditEvery: true,
	}
	cap := NewCallCounter()
	aud := &captureAudit{}
	p := NewOpenAIProvider(cfg, cap, aud, nil)

	_, err := p.Complete(context.Background(), CompletionRequest{
		Feature: CapDLQClusterNaming, User: "hi",
	})
	var aiErr *Error
	if !errors.As(err, &aiErr) {
		t.Fatalf("err type = %T, want *ai.Error", err)
	}
	if aiErr.Kind != "bad_status" || aiErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("Error = %+v, want bad_status 429", aiErr)
	}
	if rows := aud.Rows(); len(rows) != 1 || rows[0].Outcome != "error" {
		t.Errorf("audit row outcome = %+v, want one error row", rows)
	}
	if snap := cap.Snapshot(); snap["dlq_cluster_naming|m|error"] != 1 {
		t.Errorf("counter snapshot = %v, want error=1", snap)
	}
}

// ─── 4. ctx deadline honoured ───────────────────────────────────────────

func TestOpenAIProvider_ContextDeadlineHonoured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(1 * time.Second):
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"late"}}]}`))
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Enabled: true, Endpoint: srv.URL, Model: "m",
		Features:  []Capability{CapDLQClusterNaming},
		TimeoutMs: 10_000, // make sure the cap doesn't fire first
	}
	p := NewOpenAIProvider(cfg, nil, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := p.Complete(ctx, CompletionRequest{
		Feature: CapDLQClusterNaming, User: "hi",
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected ctx deadline error, got nil")
	}
	var aiErr *Error
	if !errors.As(err, &aiErr) || aiErr.Kind != "timeout" {
		t.Errorf("err = %+v, want ai.Error kind=timeout", err)
	}
	if elapsed > 800*time.Millisecond {
		t.Errorf("Complete blocked %v, expected <800ms", elapsed)
	}
}

// ─── 5. StructuredOutput parses JSON content ────────────────────────────

func TestOpenAIProvider_StructuredOutput_ParsesJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// The chat completion's content is a JSON-stringified payload
		// (matches OpenAI's structured-outputs wire format).
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"{\"title\":\"hi\",\"summary\":\"world\"}"}}]
		}`))
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Enabled: true, Endpoint: srv.URL, Model: "m",
		Features: []Capability{CapDLQClusterNaming},
	}
	p := NewOpenAIProvider(cfg, nil, nil, nil)

	out, err := p.StructuredOutput(context.Background(), StructuredRequest{
		CompletionRequest: CompletionRequest{Feature: CapDLQClusterNaming, User: "x"},
		SchemaName:        "thing",
		Schema:            json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatalf("StructuredOutput: %v", err)
	}
	var parsed struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("decode out: %v body=%s", err, string(out))
	}
	if parsed.Title != "hi" || parsed.Summary != "world" {
		t.Errorf("parsed = %+v, want {hi, world}", parsed)
	}
}

// Plus: the code-fence stripping happy path.
func TestOpenAIProvider_StructuredOutput_StripsCodeFence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Some endpoints wrap output even when asked not to. The fenced
		// content is the same JSON; we encode it as a JSON string for
		// the chat-completions wire envelope.
		envelope := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"content": "```json\n{\"ok\":true}\n```",
				},
			}},
		}
		raw, _ := json.Marshal(envelope)
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Enabled: true, Endpoint: srv.URL, Model: "m",
		Features: []Capability{CapDLQClusterNaming},
	}
	p := NewOpenAIProvider(cfg, nil, nil, nil)
	out, err := p.StructuredOutput(context.Background(), StructuredRequest{
		CompletionRequest: CompletionRequest{Feature: CapDLQClusterNaming, User: "x"},
		Schema:            json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatalf("StructuredOutput: %v", err)
	}
	if string(out) != `{"ok":true}` {
		t.Errorf("out = %s, want stripped JSON", string(out))
	}
}

// ─── 6. AuditLogger.Log fires ───────────────────────────────────────────

func TestAuditLogger_LogIsCalled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Enabled: true, Endpoint: srv.URL, Model: "m-1",
		Features: []Capability{CapExplainWhySummary}, AuditEvery: true,
	}
	aud := &captureAudit{}
	p := NewOpenAIProvider(cfg, nil, aud, nil)
	ctx := WithTenant(WithCaller(context.Background(), "user-sub-1"), "tenant-A")
	_, err := p.Complete(ctx, CompletionRequest{
		Feature: CapExplainWhySummary, User: "input prompt",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	rows := aud.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.Feature != CapExplainWhySummary || r.Outcome != "ok" {
		t.Errorf("row = %+v", r)
	}
	if r.CallerSub != "user-sub-1" || r.TenantID != "tenant-A" {
		t.Errorf("ctx not propagated: caller=%q tenant=%q", r.CallerSub, r.TenantID)
	}
	if r.PromptHash == "" || len(r.PromptHash) != 16 {
		t.Errorf("PromptHash = %q, want 16 hex chars", r.PromptHash)
	}
	if r.Model != "m-1" {
		t.Errorf("Model = %q, want m-1", r.Model)
	}
}

// ─── 7. CallCounter labels ──────────────────────────────────────────────

func TestCallCounter_IncWithLabels(t *testing.T) {
	c := NewCallCounter()
	c.Inc(CapDLQClusterNaming, "qwen", "ok")
	c.Inc(CapDLQClusterNaming, "qwen", "ok")
	c.Inc(CapDLQClusterNaming, "qwen", "error")
	c.Inc(CapExplainWhySummary, "qwen", "ok")
	snap := c.Snapshot()
	if snap["dlq_cluster_naming|qwen|ok"] != 2 {
		t.Errorf("ok count = %d, want 2 (snap=%v)", snap["dlq_cluster_naming|qwen|ok"], snap)
	}
	if snap["dlq_cluster_naming|qwen|error"] != 1 {
		t.Errorf("error count = %d, want 1", snap["dlq_cluster_naming|qwen|error"])
	}
	prom := c.Prometheus()
	if !strings.Contains(prom, "mqconnector_ai_calls_total") {
		t.Errorf("Prometheus output missing metric name: %s", prom)
	}
	if !strings.Contains(prom, `feature="dlq_cluster_naming"`) {
		t.Errorf("Prometheus output missing expected label: %s", prom)
	}
	if !strings.Contains(prom, `outcome="error"`) {
		t.Errorf("Prometheus output missing outcome=error: %s", prom)
	}
}

// ─── 8. Config.Allows gate ──────────────────────────────────────────────

func TestConfig_Allows(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		feature Capability
		want    bool
	}{
		{"disabled returns false even when listed",
			Config{Enabled: false, Features: []Capability{CapDLQClusterNaming}},
			CapDLQClusterNaming, false},
		{"enabled + listed returns true",
			Config{Enabled: true, Features: []Capability{CapDLQClusterNaming}},
			CapDLQClusterNaming, true},
		{"enabled + not listed returns false",
			Config{Enabled: true, Features: []Capability{CapExplainWhySummary}},
			CapDLQClusterNaming, false},
		{"empty allowlist denies everything",
			Config{Enabled: true},
			CapDLQClusterNaming, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.Allows(tc.feature); got != tc.want {
				t.Errorf("Allows = %v, want %v", got, tc.want)
			}
		})
	}
}

// ─── Extra: feature_off short-circuits before HTTP ──────────────────────

func TestOpenAIProvider_FeatureOff_RejectsBeforeHTTP(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Enabled: true, Endpoint: srv.URL, Model: "m",
		Features: []Capability{CapExplainWhySummary}, // omits CapDLQClusterNaming
	}
	p := NewOpenAIProvider(cfg, nil, nil, nil)
	_, err := p.Complete(context.Background(), CompletionRequest{
		Feature: CapDLQClusterNaming, User: "hi",
	})
	if err == nil || !errors.Is(err, ErrAINotAvailable) {
		t.Errorf("err = %v, want errors.Is ErrAINotAvailable", err)
	}
	if called {
		t.Error("HTTP server was hit; feature_off must short-circuit before HTTP")
	}
}

// ─── Extra: structured response_format fallback ─────────────────────────

func TestOpenAIProvider_StructuredOutput_FallsBackOn400(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		hits++
		// First call carries response_format → reject with 400.
		// Second call doesn't, return success.
		if strings.Contains(string(raw), "response_format") {
			http.Error(w, "response_format not supported", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"ok\":true}"}}]}`))
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		Enabled: true, Endpoint: srv.URL, Model: "m",
		Features: []Capability{CapDLQClusterNaming},
	}
	p := NewOpenAIProvider(cfg, nil, nil, nil)
	out, err := p.StructuredOutput(context.Background(), StructuredRequest{
		CompletionRequest: CompletionRequest{Feature: CapDLQClusterNaming, User: "x"},
		Schema:            json.RawMessage(`{"type":"object"}`),
	})
	if err != nil {
		t.Fatalf("StructuredOutput fallback: %v", err)
	}
	if hits != 2 {
		t.Errorf("server hit %d times, want 2 (initial + fallback)", hits)
	}
	if string(out) != `{"ok":true}` {
		t.Errorf("out = %s", out)
	}
}

// ─── Extra: NoopProvider sentinel ───────────────────────────────────────

func TestNoopProvider(t *testing.T) {
	p := NewNoopProvider()
	if p.Name() != "noop" {
		t.Errorf("Name() = %q, want noop", p.Name())
	}
	_, err := p.Complete(context.Background(), CompletionRequest{Feature: CapDLQClusterNaming})
	if !errors.Is(err, ErrAINotAvailable) {
		t.Errorf("err = %v, want ErrAINotAvailable", err)
	}
}
