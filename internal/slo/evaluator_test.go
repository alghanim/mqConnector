package slo

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMetrics is a stand-in for the live metrics.Store. The current
// snapshot and historical values are pre-loaded by the test.
type fakeMetrics struct {
	mu      sync.Mutex
	current []Sample
	// history is keyed by (metric_name + pipeline_id) and holds an
	// ordered list of (ago, value) samples. ValueAt returns the
	// newest sample at-or-before the requested `ago` offset.
	history map[string][]histEntry
}

type histEntry struct {
	ago   time.Duration
	value float64
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{
		history: map[string][]histEntry{},
	}
}

func (f *fakeMetrics) Snapshot() []Sample {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Sample, len(f.current))
	for i, s := range f.current {
		labels := map[string]string{}
		for k, v := range s.Labels {
			labels[k] = v
		}
		out[i] = Sample{Labels: labels, Value: s.Value}
	}
	return out
}

func (f *fakeMetrics) ValueAt(metric string, labels map[string]string, ago time.Duration) (float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := metric + "|" + labels["pipeline_id"]
	entries := f.history[key]
	if len(entries) == 0 {
		return 0, false
	}
	// Return the entry with the smallest |delta| to the requested ago.
	best := entries[0]
	bestDelta := abs(entries[0].ago - ago)
	for _, e := range entries[1:] {
		d := abs(e.ago - ago)
		if d < bestDelta {
			best = e
			bestDelta = d
		}
	}
	return best.value, true
}

func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func (f *fakeMetrics) set(metric, pipelineID string, value float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.current {
		s := f.current[i]
		if s.Labels["__name__"] == metric && s.Labels["pipeline_id"] == pipelineID {
			f.current[i].Value = value
			return
		}
	}
	f.current = append(f.current, Sample{
		Labels: map[string]string{
			"__name__":    metric,
			"pipeline_id": pipelineID,
		},
		Value: value,
	})
}

func (f *fakeMetrics) setHistory(metric, pipelineID string, ago time.Duration, value float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := metric + "|" + pipelineID
	f.history[key] = append(f.history[key], histEntry{ago: ago, value: value})
}

func TestEvaluator_PendingToFiring(t *testing.T) {
	dir := t.TempDir()
	rulePath := filepath.Join(dir, "rules.yaml")
	_ = os.WriteFile(rulePath, []byte(`
groups:
  - name: t
    rules:
      - alert: PipelineDown
        expr: mqconnector_pipeline_up == 0
        for: 1m
        labels: { severity: critical }
        annotations: { summary: pipeline down }
`), 0o600)
	rules, err := LoadFile(rulePath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	fm := newFakeMetrics()
	fm.set("mqconnector_pipeline_up", "p1", 0)

	clock := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	advance := func(d time.Duration) { clock = clock.Add(d) }

	e := NewEvaluator(rules, nil, fm, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	e.Clock = func() time.Time { return clock }

	// Tick 1 — first true sighting → pending, NOT firing yet.
	e.tick()
	if len(e.Snapshot()) != 0 {
		t.Fatalf("tick1: expected no firing alerts, got %v", e.Snapshot())
	}

	// Tick 2 after 30s — still pending.
	advance(30 * time.Second)
	e.tick()
	if len(e.Snapshot()) != 0 {
		t.Fatalf("tick2: still pending, got firing=%v", e.Snapshot())
	}

	// Tick 3 after 2min — should fire (For:1m elapsed).
	advance(90 * time.Second)
	e.tick()
	got := e.Snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 firing, got %d", len(got))
	}
	if got[0].Name != "PipelineDown" || got[0].Severity != "critical" {
		t.Errorf("bad alert: %+v", got[0])
	}
	// StartedAt is the first-seen time (tick1's clock).
	if !got[0].StartedAt.Equal(time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("StartedAt = %v", got[0].StartedAt)
	}

	// Stop being true → drops back to inactive.
	fm.set("mqconnector_pipeline_up", "p1", 1)
	advance(30 * time.Second)
	e.tick()
	if len(e.Snapshot()) != 0 {
		t.Fatalf("expected inactive after recovery, got %v", e.Snapshot())
	}
}

func TestEvaluator_ZeroForFiresImmediately(t *testing.T) {
	dir := t.TempDir()
	rulePath := filepath.Join(dir, "rules.yaml")
	_ = os.WriteFile(rulePath, []byte(`
groups:
  - name: t
    rules:
      - alert: Instant
        expr: mqconnector_messages_failed_total > 0
        labels: { severity: warning }
`), 0o600)
	rules, _ := LoadFile(rulePath)
	fm := newFakeMetrics()
	fm.set("mqconnector_messages_failed_total", "p1", 5)
	e := NewEvaluator(rules, nil, fm, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	e.Clock = func() time.Time { return time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC) }
	e.tick()
	got := e.Snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 firing alert, got %d", len(got))
	}
	if got[0].Value != 5 {
		t.Errorf("value = %v", got[0].Value)
	}
}

func TestEvaluator_SeveritySortOrder(t *testing.T) {
	dir := t.TempDir()
	rulePath := filepath.Join(dir, "rules.yaml")
	_ = os.WriteFile(rulePath, []byte(`
groups:
  - name: t
    rules:
      - alert: Warn
        expr: mqconnector_messages_failed_total > 0
        labels: { severity: warning }
      - alert: Crit
        expr: mqconnector_messages_failed_total > 0
        labels: { severity: critical }
      - alert: Info
        expr: mqconnector_messages_failed_total > 0
        labels: { severity: info }
`), 0o600)
	rules, _ := LoadFile(rulePath)
	fm := newFakeMetrics()
	fm.set("mqconnector_messages_failed_total", "p1", 1)
	e := NewEvaluator(rules, nil, fm, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	e.Clock = func() time.Time { return time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC) }
	e.tick()
	got := e.Snapshot()
	if len(got) != 3 {
		t.Fatalf("expected 3 firing, got %d", len(got))
	}
	if got[0].Severity != "critical" || got[1].Severity != "warning" || got[2].Severity != "info" {
		t.Errorf("unexpected severity order: %v %v %v",
			got[0].Severity, got[1].Severity, got[2].Severity)
	}
}

func TestEvaluator_RateAndComparison(t *testing.T) {
	dir := t.TempDir()
	rulePath := filepath.Join(dir, "rules.yaml")
	_ = os.WriteFile(rulePath, []byte(`
groups:
  - name: t
    rules:
      - alert: FailRate
        expr: rate(mqconnector_messages_failed_total[5m]) > 0.05
        labels: { severity: warning }
`), 0o600)
	rules, _ := LoadFile(rulePath)
	fm := newFakeMetrics()
	// Current 100 failures, 5min ago 0 → rate = 100/300 ≈ 0.333 > 0.05.
	fm.set("mqconnector_messages_failed_total", "p1", 100)
	fm.setHistory("mqconnector_messages_failed_total", "p1", 5*time.Minute, 0)
	e := NewEvaluator(rules, nil, fm, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	e.Clock = func() time.Time { return time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC) }
	e.tick()
	got := e.Snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 firing, got %d (%v)", len(got), got)
	}
	if got[0].Value < 0.3 || got[0].Value > 0.34 {
		t.Errorf("unexpected rate value %v", got[0].Value)
	}
}

func TestEvaluator_HandlesBadExprGracefully(t *testing.T) {
	rules := []Rule{
		{Name: "Broken", Expr: "this is not valid promql @@@", Labels: map[string]string{"severity": "warning"}},
		{Name: "Good", Expr: "mqconnector_pipeline_up == 0", Labels: map[string]string{"severity": "warning"}},
	}
	fm := newFakeMetrics()
	fm.set("mqconnector_pipeline_up", "p1", 0)
	e := NewEvaluator(rules, nil, fm, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	e.Clock = func() time.Time { return time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC) }
	if e.RuleCount() != 1 {
		t.Errorf("RuleCount: bad rule should be dropped, got %d", e.RuleCount())
	}
	e.tick()
	got := e.Snapshot()
	if len(got) != 1 || got[0].Name != "Good" {
		t.Errorf("expected Good alert, got %+v", got)
	}
}

func TestEvaluator_RecordingRuleReference(t *testing.T) {
	dir := t.TempDir()
	rulePath := filepath.Join(dir, "rules.yaml")
	_ = os.WriteFile(rulePath, []byte(`
groups:
  - name: t
    rules:
      - record: derived:fail_rate5m
        expr: rate(mqconnector_messages_failed_total[5m])
      - alert: UseDerived
        expr: derived:fail_rate5m > 0.05
        labels: { severity: critical }
`), 0o600)
	rules, _ := LoadFile(rulePath)
	recs, err := RecordingRulesFromFile(rulePath)
	if err != nil {
		t.Fatalf("RecordingRulesFromFile: %v", err)
	}
	fm := newFakeMetrics()
	fm.set("mqconnector_messages_failed_total", "p1", 100)
	fm.setHistory("mqconnector_messages_failed_total", "p1", 5*time.Minute, 0)
	e := NewEvaluator(rules, recs, fm, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	e.Clock = func() time.Time { return time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC) }
	e.tick()
	got := e.Snapshot()
	if len(got) != 1 || got[0].Name != "UseDerived" {
		t.Fatalf("expected UseDerived firing, got %+v", got)
	}
}

func TestEvaluator_RunCancellable(t *testing.T) {
	rules := []Rule{
		{Name: "X", Expr: "mqconnector_pipeline_up == 0", Labels: map[string]string{"severity": "warning"}},
	}
	fm := newFakeMetrics()
	e := NewEvaluator(rules, nil, fm, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	e.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.Run(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit on ctx.Done()")
	}
}

func TestEvaluator_FiringAlertJSONShape(t *testing.T) {
	// Verify the wire shape stays stable — /api/v1/alerts/active
	// consumers rely on these field names.
	a := FiringAlert{
		Name:        "X",
		Severity:    "warning",
		Value:       1.23,
		Threshold:   "> 0.05",
		StartedAt:   time.Date(2026, 5, 26, 1, 0, 0, 0, time.UTC),
		Annotations: map[string]string{"summary": "hi"},
		Labels:      map[string]string{"pipeline_id": "p1"},
	}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		`"name":"X"`,
		`"severity":"warning"`,
		`"value":1.23`,
		// encoding/json HTML-escapes `>` by default; test against
		// the escaped form so we don't make the test depend on a
		// specific encoder configuration.
		"\"threshold\":\"\\u003e 0.05\"",
		`"started_at":"2026-05-26T01:00:00Z"`,
		`"annotations":{"summary":"hi"}`,
		`"labels":{"pipeline_id":"p1"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
}

func TestRenderThreshold(t *testing.T) {
	cases := []struct{ in, want string }{
		{"rate(x[5m]) > 0.05", "> 0.05"},
		{"sum(x) <= 100", "<= 100"},
		{"x == 0", "== 0"},
		{"sum(x)", ""},
	}
	for _, c := range cases {
		if got := renderThreshold(c.in); got != c.want {
			t.Errorf("renderThreshold(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEvaluatorSnapshot_Deterministic(t *testing.T) {
	rules := []Rule{
		{Name: "A", Expr: "mqconnector_pipeline_up == 0", Labels: map[string]string{"severity": "warning"}},
	}
	fm := newFakeMetrics()
	fm.set("mqconnector_pipeline_up", "p1", 0)
	e := NewEvaluator(rules, nil, fm, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	e.Clock = func() time.Time { return time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC) }
	e.tick()
	got1 := e.Snapshot()
	got2 := e.Snapshot()
	if len(got1) != len(got2) {
		t.Fatalf("len mismatch: %d %d", len(got1), len(got2))
	}
	for i := range got1 {
		if got1[i].Name != got2[i].Name {
			t.Errorf("snapshot not stable")
		}
	}
}
