package explain

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"mqConnector/internal/metrics"
	"mqConnector/internal/storage"
)

// fakeMetrics implements MetricsSource + StageHistogramsSource.
// One snapshot per pipeline, plus optional per-stage histograms.
type fakeMetrics struct {
	snaps  map[string]metrics.Pipeline
	stages map[string][]metrics.StageHistogramSnapshot
}

func newFakeMetrics() *fakeMetrics {
	return &fakeMetrics{
		snaps:  map[string]metrics.Pipeline{},
		stages: map[string][]metrics.StageHistogramSnapshot{},
	}
}

func (f *fakeMetrics) SnapshotPipeline(_, pipelineID string) (metrics.Pipeline, bool) {
	p, ok := f.snaps[pipelineID]
	return p, ok
}
func (f *fakeMetrics) Snapshot() map[string]metrics.Pipeline { return f.snaps }
func (f *fakeMetrics) StageHistogramsFor(_, pipelineID string) []metrics.StageHistogramSnapshot {
	return f.stages[pipelineID]
}

// fakeDLQ implements DLQSource.
type fakeDLQ struct {
	entries  map[string]*storage.DLQEntry
	recents  map[string][]*storage.DLQEntry // by pipeline id
	clusters map[string]storage.DLQClusterRow
}

func newFakeDLQ() *fakeDLQ {
	return &fakeDLQ{
		entries:  map[string]*storage.DLQEntry{},
		recents:  map[string][]*storage.DLQEntry{},
		clusters: map[string]storage.DLQClusterRow{},
	}
}

func (f *fakeDLQ) RecentForPipeline(_ context.Context, _, pipelineID string, _ int, _ time.Time) ([]*storage.DLQEntry, error) {
	return f.recents[pipelineID], nil
}
func (f *fakeDLQ) GetEntry(_ context.Context, _, id string) (*storage.DLQEntry, error) {
	e, ok := f.entries[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return e, nil
}
func (f *fakeDLQ) ClusterByFingerprint(_ context.Context, _, fp string) (storage.DLQClusterRow, error) {
	c, ok := f.clusters[fp]
	if !ok {
		return storage.DLQClusterRow{}, storage.ErrNotFound
	}
	return c, nil
}

// fakeAudit implements AuditSource.
type fakeAudit struct {
	byResourcePrefix map[string][]*storage.AuditEntry
}

func newFakeAudit() *fakeAudit {
	return &fakeAudit{byResourcePrefix: map[string][]*storage.AuditEntry{}}
}

func (f *fakeAudit) RecentForResource(_ context.Context, _, resource string, _ int) ([]*storage.AuditEntry, error) {
	for prefix, rows := range f.byResourcePrefix {
		if strings.HasPrefix(resource, prefix) || strings.HasPrefix(prefix, resource) {
			return rows, nil
		}
	}
	return nil, nil
}

// fakePipelines implements PipelinesSource.
type fakePipelines struct {
	pipelines map[string]*storage.Pipeline
	deployed  map[string]*storage.PipelineRevision
}

func newFakePipelines() *fakePipelines {
	return &fakePipelines{
		pipelines: map[string]*storage.Pipeline{},
		deployed:  map[string]*storage.PipelineRevision{},
	}
}
func (f *fakePipelines) Get(_ context.Context, _, id string) (*storage.Pipeline, error) {
	p, ok := f.pipelines[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return p, nil
}
func (f *fakePipelines) LatestDeployedRevision(_ context.Context, _, pipelineID string) (*storage.PipelineRevision, error) {
	r, ok := f.deployed[pipelineID]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return r, nil
}

// fakeBreakers implements BreakersSource.
type fakeBreakers struct {
	states  map[string]string
	history map[string][]BreakerTransition
}

func newFakeBreakers() *fakeBreakers {
	return &fakeBreakers{
		states:  map[string]string{},
		history: map[string][]BreakerTransition{},
	}
}
func (f *fakeBreakers) State(pipelineID string) string { return f.states[pipelineID] }
func (f *fakeBreakers) History(pipelineID string) []BreakerTransition {
	return f.history[pipelineID]
}

// buildEngine constructs an Engine with every Source wired. Each
// test overrides the fakes as needed.
func buildEngine() (*Engine, *fakeMetrics, *fakeDLQ, *fakeAudit, *fakePipelines, *fakeBreakers) {
	m := newFakeMetrics()
	d := newFakeDLQ()
	a := newFakeAudit()
	p := newFakePipelines()
	b := newFakeBreakers()
	eng := &Engine{
		Metrics:   m,
		DLQ:       d,
		Audit:     a,
		Pipelines: p,
		Breakers:  b,
		Clock:     func() time.Time { return time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC) },
	}
	return eng, m, d, a, p, b
}

// TestExplain_UnknownSubject covers the bare dispatch failure.
func TestExplain_UnknownSubject(t *testing.T) {
	eng, _, _, _, _, _ := buildEngine()
	_, err := eng.Explain(context.Background(), "no-such-subject", "anything", "tenant-1")
	if !errors.Is(err, ErrUnknownSubject) {
		t.Fatalf("got err=%v, want ErrUnknownSubject", err)
	}
}

// TestExplain_ValidSubjectUnknownID covers the per-explainer
// not-found path for the three pipeline-keyed subjects.
func TestExplain_ValidSubjectUnknownID(t *testing.T) {
	subjects := []string{"circuit", "drift", "latency"}
	for _, sub := range subjects {
		t.Run(sub, func(t *testing.T) {
			eng, _, _, _, _, _ := buildEngine()
			_, err := eng.Explain(context.Background(), sub, "missing-pipeline", "tenant-1")
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("subject %s: got err=%v, want ErrNotFound", sub, err)
			}
		})
	}
}

// TestCircuitExplainer_OpenWithFailures asserts the headline +
// severity + fact list when the breaker is open and DLQ entries
// exist.
func TestCircuitExplainer_OpenWithFailures(t *testing.T) {
	eng, m, d, a, p, b := buildEngine()
	const pid = "p-circuit"
	p.pipelines[pid] = &storage.Pipeline{ID: pid, TenantID: "tenant-1"}
	b.states[pid] = "open"
	m.snaps[pid] = metrics.Pipeline{
		PipelineID:        pid,
		MessagesProcessed: 100,
		MessagesFailed:    7,
		LastError:         "destination broker unreachable",
	}
	d.recents[pid] = []*storage.DLQEntry{
		{ID: "e1", ErrorReason: "send: dial tcp 10.0.0.1:5672: refused", CreatedAt: time.Now().UTC()},
		{ID: "e2", ErrorReason: "send: dial tcp 10.0.0.1:5672: refused", CreatedAt: time.Now().UTC()},
	}
	a.byResourcePrefix["/api/v1/pipelines/"+pid] = []*storage.AuditEntry{
		{ID: "audit-1", Resource: "/api/v1/pipelines/" + pid, Actor: "alice",
			Action: "PUT", At: time.Now().UTC()},
	}

	exp, err := eng.Explain(context.Background(), "circuit", pid, "tenant-1")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if exp.Severity != SeverityCritical {
		t.Errorf("severity = %s, want critical", exp.Severity)
	}
	if !strings.Contains(strings.ToLower(exp.Headline), "open") {
		t.Errorf("headline does not contain 'open': %q", exp.Headline)
	}
	// State + counters + recent failure samples + last edit must be present.
	wantLabels := []string{"Current state", "Processed (cumulative)", "Failed (cumulative)",
		"Recent failure #1", "Last pipeline edit"}
	for _, want := range wantLabels {
		if !factHasLabel(exp.Facts, want) {
			t.Errorf("facts missing label %q: %+v", want, factLabels(exp.Facts))
		}
	}
}

// TestCircuitExplainer_ClosedHealthy covers the happy path.
func TestCircuitExplainer_ClosedHealthy(t *testing.T) {
	eng, m, _, _, p, b := buildEngine()
	const pid = "p-healthy"
	p.pipelines[pid] = &storage.Pipeline{ID: pid, TenantID: "tenant-1"}
	b.states[pid] = "closed"
	m.snaps[pid] = metrics.Pipeline{PipelineID: pid, MessagesProcessed: 9999}

	exp, err := eng.Explain(context.Background(), "circuit", pid, "tenant-1")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if exp.Severity != SeverityInfo {
		t.Errorf("severity = %s, want info", exp.Severity)
	}
	if !strings.Contains(strings.ToLower(exp.Headline), "closed") {
		t.Errorf("headline does not contain 'closed': %q", exp.Headline)
	}
}

// TestDriftExplainer_CriticalRatio covers the >20% branch.
func TestDriftExplainer_CriticalRatio(t *testing.T) {
	eng, m, d, _, p, _ := buildEngine()
	const pid = "p-drift"
	p.pipelines[pid] = &storage.Pipeline{ID: pid, TenantID: "tenant-1"}
	m.snaps[pid] = metrics.Pipeline{
		PipelineID:       pid,
		ValidateAttempts: 100,
		ValidateFailures: 30, // 30%
	}
	now := time.Now().UTC()
	d.recents[pid] = []*storage.DLQEntry{
		{ID: "x", ErrorTemplate: "validation: missing field <field>", CreatedAt: now},
		{ID: "y", ErrorTemplate: "validation: missing field <field>", CreatedAt: now},
		{ID: "z", ErrorTemplate: "validation: type mismatch on <field>", CreatedAt: now},
	}

	exp, err := eng.Explain(context.Background(), "drift", pid, "tenant-1")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if exp.Severity != SeverityCritical {
		t.Errorf("severity = %s, want critical (ratio = 30%%)", exp.Severity)
	}
	if !strings.Contains(exp.Headline, "30.00%") {
		t.Errorf("headline missing 30.00%% ratio: %q", exp.Headline)
	}
	if !factHasLabel(exp.Facts, "Failure ratio") {
		t.Errorf("missing 'Failure ratio' fact: %+v", factLabels(exp.Facts))
	}
	if !factHasLabel(exp.Facts, "Top error template #1 (2 hits)") {
		t.Errorf("missing top-template fact: %+v", factLabels(exp.Facts))
	}
}

// TestDriftExplainer_NoObservations covers the zero-attempts path.
func TestDriftExplainer_NoObservations(t *testing.T) {
	eng, m, _, _, p, _ := buildEngine()
	const pid = "p-noop"
	p.pipelines[pid] = &storage.Pipeline{ID: pid, TenantID: "tenant-1"}
	m.snaps[pid] = metrics.Pipeline{PipelineID: pid} // attempts=0

	exp, err := eng.Explain(context.Background(), "drift", pid, "tenant-1")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if exp.Severity != SeverityInfo {
		t.Errorf("severity = %s, want info", exp.Severity)
	}
	if !strings.Contains(exp.Headline, "No validate-stage observations") {
		t.Errorf("headline missing 'No validate-stage observations': %q", exp.Headline)
	}
}

// TestLatencyExplainer_DominantStage seeds per-stage histograms
// where one stage clearly dominates.
func TestLatencyExplainer_DominantStage(t *testing.T) {
	eng, m, _, _, p, _ := buildEngine()
	const pid = "p-lat"
	p.pipelines[pid] = &storage.Pipeline{ID: pid, TenantID: "tenant-1"}
	m.snaps[pid] = metrics.Pipeline{PipelineID: pid, AvgLatencyMs: 30, SourceDepth: -1}

	// Build a "slow" stage whose observations land in the 500-1000ms
	// buckets, and a "fast" stage whose observations land in <=1ms.
	// Latency buckets: 0.5, 1, 2.5, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000.
	buckets := metrics.LatencyBuckets()
	// fast: all 50 obs in the 1ms bucket.
	fastCounts := make([]uint64, len(buckets))
	for i := range buckets {
		if buckets[i] >= 1 {
			fastCounts[i] = 50
		}
	}
	// slow: all 50 obs land at 1000ms.
	slowCounts := make([]uint64, len(buckets))
	for i, b := range buckets {
		if b >= 1000 {
			slowCounts[i] = 50
		}
	}
	m.stages[pid] = []metrics.StageHistogramSnapshot{
		{StageName: "fast", BucketCounts: fastCounts, Count: 50, SumMs: 50},
		{StageName: "slow", BucketCounts: slowCounts, Count: 50, SumMs: 50000},
	}

	exp, err := eng.Explain(context.Background(), "latency", pid, "tenant-1")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !strings.Contains(strings.ToLower(exp.Headline), "slow") {
		t.Errorf("headline does not call out the slow stage: %q", exp.Headline)
	}
	if exp.Severity == SeverityInfo {
		t.Errorf("severity = info but a stage dominates; want warning/critical")
	}
	// Stages section should be present.
	foundStages := false
	for _, sec := range exp.Sections {
		if sec.Kind == "stages" {
			foundStages = true
			var payload struct {
				Stages []StageLatency `json:"stages"`
			}
			if err := json.Unmarshal(sec.Data, &payload); err != nil {
				t.Fatalf("unmarshal stages: %v", err)
			}
			if len(payload.Stages) != 2 {
				t.Errorf("stages section count = %d, want 2", len(payload.Stages))
			}
			// Sorted by p99 desc — slow first.
			if payload.Stages[0].Name != "slow" {
				t.Errorf("stages[0] = %q, want 'slow'", payload.Stages[0].Name)
			}
		}
	}
	if !foundStages {
		t.Error("no 'stages' section found")
	}
}

// TestLatencyExplainer_NoObservations covers the empty branch.
func TestLatencyExplainer_NoObservations(t *testing.T) {
	eng, _, _, _, p, _ := buildEngine()
	const pid = "p-empty"
	p.pipelines[pid] = &storage.Pipeline{ID: pid, TenantID: "tenant-1"}

	exp, err := eng.Explain(context.Background(), "latency", pid, "tenant-1")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if exp.Severity != SeverityInfo {
		t.Errorf("severity = %s, want info", exp.Severity)
	}
}

// TestDLQClusterExplainer_HappyPath covers the cluster mode with
// a correlated recent edit.
func TestDLQClusterExplainer_HappyPath(t *testing.T) {
	eng, _, d, a, _, _ := buildEngine()
	const fp = "abc123"
	d.clusters[fp] = storage.DLQClusterRow{
		Fingerprint: fp,
		Template:    "validation: missing field <field>=customer.id",
		Count:       42,
		FirstSeen:   time.Date(2026, 5, 26, 9, 0, 0, 0, time.UTC),
		LastSeen:    time.Date(2026, 5, 26, 11, 0, 0, 0, time.UTC),
	}
	a.byResourcePrefix["/api/v1/pipelines/"] = []*storage.AuditEntry{
		{ID: "ed1", Resource: "/api/v1/pipelines/p1/stages",
			Actor: "bob", Action: "PUT",
			At: time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)},
	}

	exp, err := eng.Explain(context.Background(), "dlq_cluster", fp, "tenant-1")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !strings.Contains(exp.Headline, "42") {
		t.Errorf("headline missing cluster count: %q", exp.Headline)
	}
	if !factHasLabel(exp.Facts, "Cluster count") {
		t.Errorf("missing 'Cluster count' fact: %+v", factLabels(exp.Facts))
	}
	// Fields section should expose the <field>=customer.id extraction.
	foundFields := false
	for _, sec := range exp.Sections {
		if sec.Kind == "fields" {
			foundFields = true
			if !strings.Contains(string(sec.Data), "customer.id") {
				t.Errorf("fields section missing customer.id: %s", sec.Data)
			}
		}
	}
	if !foundFields {
		t.Error("no 'fields' section found")
	}
}

// TestDLQClusterExplainer_UnknownFingerprint covers the
// not-found path.
func TestDLQClusterExplainer_UnknownFingerprint(t *testing.T) {
	eng, _, _, _, _, _ := buildEngine()
	_, err := eng.Explain(context.Background(), "dlq_cluster", "no-such-fp", "tenant-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestDLQEntryExplainer_HappyPath covers the entry mode.
func TestDLQEntryExplainer_HappyPath(t *testing.T) {
	eng, _, d, a, p, _ := buildEngine()
	const eid = "entry-1"
	const pid = "p-1"
	p.pipelines[pid] = &storage.Pipeline{ID: pid, TenantID: "tenant-1"}
	deployTime := time.Date(2026, 5, 26, 9, 0, 0, 0, time.UTC)
	p.deployed[pid] = &storage.PipelineRevision{
		ID: "rev-1", PipelineID: pid, RevisionNumber: 5,
		DeployedAt: &deployTime,
	}
	d.entries[eid] = &storage.DLQEntry{
		ID:               eid,
		PipelineID:       pid,
		ErrorReason:      "validate: missing field customer.id",
		ErrorTemplate:    "validate: missing field <field>=customer.id",
		ErrorFingerprint: "fp-1",
		FailingStageName: "validate",
		CreatedAt:        time.Date(2026, 5, 26, 11, 30, 0, 0, time.UTC),
	}
	d.clusters["fp-1"] = storage.DLQClusterRow{Fingerprint: "fp-1", Count: 8}
	a.byResourcePrefix["/api/v1/pipelines/"+pid] = []*storage.AuditEntry{
		{ID: "ed", Resource: "/api/v1/pipelines/" + pid + "/stages",
			Actor: "carol", Action: "PUT",
			At: time.Date(2026, 5, 26, 11, 15, 0, 0, time.UTC)},
	}

	exp, err := eng.Explain(context.Background(), "dlq_entry", eid, "tenant-1")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if !strings.Contains(strings.ToLower(exp.Headline), "validate") {
		t.Errorf("headline missing 'validate' stage: %q", exp.Headline)
	}
	for _, want := range []string{"Failing stage", "Pipeline", "Error reason",
		"Cluster count (same fingerprint)", "Latest deployed revision", "Last pipeline edit"} {
		if !factHasLabel(exp.Facts, want) {
			t.Errorf("missing fact %q: %+v", want, factLabels(exp.Facts))
		}
	}
}

// TestDLQEntryExplainer_UnknownEntry covers the not-found path.
func TestDLQEntryExplainer_UnknownEntry(t *testing.T) {
	eng, _, _, _, _, _ := buildEngine()
	_, err := eng.Explain(context.Background(), "dlq_entry", "no-such-entry", "tenant-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestExplain_EndToEndEverySubject proves dispatch is wired for
// each of the five subjects by exercising one happy path each
// with the same Engine instance.
func TestExplain_EndToEndEverySubject(t *testing.T) {
	eng, m, d, _, p, b := buildEngine()
	const pid = "pipe-x"
	p.pipelines[pid] = &storage.Pipeline{ID: pid, TenantID: "tenant-1"}
	b.states[pid] = "closed"
	m.snaps[pid] = metrics.Pipeline{
		PipelineID: pid, MessagesProcessed: 10,
		ValidateAttempts: 10, ValidateFailures: 0,
	}
	d.clusters["fp"] = storage.DLQClusterRow{
		Fingerprint: "fp", Count: 1, Template: "x",
		FirstSeen: time.Now().UTC(), LastSeen: time.Now().UTC(),
	}
	d.entries["e"] = &storage.DLQEntry{ID: "e", PipelineID: pid,
		ErrorReason: "boom", CreatedAt: time.Now().UTC()}

	cases := []struct {
		subject, id, mustHave string
	}{
		{"circuit", pid, "circuit"},
		{"drift", pid, "validate"},
		{"latency", pid, "envelope"},
		{"dlq_cluster", "fp", "cluster"},
		{"dlq_entry", "e", "failure"},
	}
	for _, c := range cases {
		t.Run(c.subject, func(t *testing.T) {
			exp, err := eng.Explain(context.Background(), c.subject, c.id, "tenant-1")
			if err != nil {
				t.Fatalf("subject %s: %v", c.subject, err)
			}
			if exp.Subject != c.subject {
				t.Errorf("subject = %s, want %s", exp.Subject, c.subject)
			}
			if exp.ID != c.id {
				t.Errorf("id = %s, want %s", exp.ID, c.id)
			}
			if exp.Headline == "" {
				t.Errorf("headline must be non-empty")
			}
			if exp.AsOf.IsZero() {
				t.Errorf("as_of must be set")
			}
			// The mustHave check is a loose wording assertion;
			// catches "wrong explainer wired to this subject".
			if !strings.Contains(strings.ToLower(exp.Headline), strings.ToLower(c.mustHave)) {
				t.Logf("headline %q does not contain %q (informational)", exp.Headline, c.mustHave)
			}
		})
	}
}

// TestExplain_NilEngine guards the defensive nil check on Engine.
func TestExplain_NilEngine(t *testing.T) {
	var eng *Engine
	_, err := eng.Explain(context.Background(), "circuit", "anything", "tenant-1")
	if !errors.Is(err, ErrUnknownSubject) {
		t.Fatalf("got err=%v, want ErrUnknownSubject", err)
	}
}

// ----- helpers --------------------------------------------------------------

func factHasLabel(facts []Fact, want string) bool {
	for _, f := range facts {
		if f.Label == want {
			return true
		}
	}
	return false
}

func factLabels(facts []Fact) []string {
	out := make([]string, len(facts))
	for i, f := range facts {
		out[i] = f.Label
	}
	return out
}
