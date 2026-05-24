package pipeline

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// TestRunStages_PopulatesPerStageRuns confirms the new StageRun
// observation log is filled for both the success and failure cases.
// Drift detection depends on attempts being counted on both paths.
func TestRunStages_PopulatesPerStageRuns(t *testing.T) {
	ok := &okStage{}
	bad := &errStage{}

	out, err := RunStages(context.Background(), []Stage{ok, ok}, []byte(`{"a":1}`))
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(out.Runs) != 2 {
		t.Fatalf("expected 2 runs on success, got %d", len(out.Runs))
	}
	if out.Runs[0].Failed || out.Runs[1].Failed {
		t.Fatalf("no run should be marked failed: %#v", out.Runs)
	}

	out2, err2 := RunStages(context.Background(), []Stage{ok, bad, ok}, []byte(`x`))
	if err2 == nil {
		t.Fatal("expected error from bad stage")
	}
	if len(out2.Runs) != 2 {
		t.Fatalf("expected 2 runs on failure (stop at failing stage), got %d", len(out2.Runs))
	}
	if out2.Runs[1].Failed != true {
		t.Fatalf("expected last run to be marked Failed=true")
	}
}

// TestSchemaDrift_FailureRecordsValidateMetric proves that a real
// validate stage failure increments the drift counters via the
// executor → metrics path, end-to-end.
func TestSchemaDrift_FailureRecordsValidateMetric(t *testing.T) {
	store := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := &storage.Connection{Name: "src", Type: "rabbitmq", URL: "amqp://x", QueueName: "src-q"}
	dst := &storage.Connection{Name: "dst", Type: "rabbitmq", URL: "amqp://x", QueueName: "dst-q"}
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, src)
	_ = store.Connections.Create(ctx, storage.DefaultTenantID, dst)

	// Pipeline with an inline JSON-schema validate stage that requires
	// a "ssn" field. We'll send a payload missing it → validate fails.
	pipe := &storage.Pipeline{
		Name: "p", SourceID: src.ID, DestinationID: dst.ID, Enabled: true,
	}
	_ = store.Pipelines.Create(ctx, storage.DefaultTenantID, pipe)
	stage := &storage.Stage{
		PipelineID: pipe.ID,
		StageOrder: 0,
		StageType:  "validate",
		StageConfig: `{
			"schema_type":"json_schema",
			"content":"{\"type\":\"object\",\"required\":[\"ssn\"]}"
		}`,
		Enabled: true,
	}
	_ = store.Stages.ReplaceForPipeline(ctx, storage.DefaultTenantID, pipe.ID, []*storage.Stage{stage})

	reg := mq.NewMemoryRegistry(16)
	pool := mq.NewPool(mq.PoolOptions{IdleTimeout: time.Hour, HealthInterval: time.Hour})
	defer pool.Close()
	rawSrc := mq.NewMemoryConnector(reg, "src-q")
	_ = rawSrc.Connect(ctx)
	rawDst := mq.NewMemoryConnector(reg, "dst-q")
	_ = rawDst.Connect(ctx)
	pool.InjectForTest("source-"+pipe.ID, &recordingConnector{inner: rawSrc})
	pool.InjectForTest("dest-"+pipe.ID, rawDst)

	metricsStore := metrics.New()
	dlqSvc := dlqStub{}
	mgr := NewManager(ctx, store, pool, metricsStore, dlqSvc, logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// Push one passing payload (has ssn) and one failing (no ssn).
	_ = rawSrc.SendMessage(ctx, []byte(`{"ssn":"x"}`))
	_ = rawSrc.SendMessage(ctx, []byte(`{"other":"y"}`))

	if err := waitFor(t, 2*time.Second, func() bool {
		snap := metricsStore.Snapshot()[pipe.ID]
		return snap.ValidateAttempts >= 2
	}); err != nil {
		snap := metricsStore.Snapshot()[pipe.ID]
		t.Fatalf("waiting for validate attempts: attempts=%d failures=%d",
			snap.ValidateAttempts, snap.ValidateFailures)
	}
	snap := metricsStore.Snapshot()[pipe.ID]
	if snap.ValidateAttempts != 2 {
		t.Fatalf("ValidateAttempts = %d, want 2", snap.ValidateAttempts)
	}
	if snap.ValidateFailures != 1 {
		t.Fatalf("ValidateFailures = %d, want 1 (one missing-ssn payload)", snap.ValidateFailures)
	}
}

// TestPrometheusExposition_ContainsValidateCounters proves the new
// counters land in the Prometheus exposition format so the alert
// rule has data to read.
func TestPrometheusExposition_ContainsValidateCounters(t *testing.T) {
	m := metrics.New()
	m.Register("p1", "src-q", "dst-q")
	m.RecordValidateAttempt("p1", true)
	m.RecordValidateAttempt("p1", false)
	m.RecordValidateAttempt("p1", false)
	text := m.Prometheus()
	if !strings.Contains(text, "mqconnector_validate_attempts_total") {
		t.Fatalf("missing attempts metric in exposition:\n%s", text)
	}
	if !strings.Contains(text, "mqconnector_validate_failures_total") {
		t.Fatalf("missing failures metric in exposition:\n%s", text)
	}
	if !strings.Contains(text, `pipeline_id="p1"`) {
		t.Fatalf("validate metric missing pipeline_id label:\n%s", text)
	}
}

// ─── test helpers ───────────────────────────────────────────────────

type okStage struct{ count atomic.Int64 }

func (s *okStage) Name() string { return "okstage" }
func (s *okStage) Execute(_ context.Context, m []byte, f Format) ([]byte, Format, *Result, error) {
	s.count.Add(1)
	return m, f, nil, nil
}

type errStage struct{}

func (errStage) Name() string { return "errstage" }
func (errStage) Execute(_ context.Context, _ []byte, f Format) ([]byte, Format, *Result, error) {
	return nil, f, nil, errExpected
}

var errExpected = errSimulated("simulated stage failure")

type errSimulated string

func (e errSimulated) Error() string { return string(e) }

// dlqStub absorbs DLQ pushes without doing anything; lets the executor
// finalize a failed validate without needing a full DLQ service.
type dlqStub struct{}

func (dlqStub) Push(_ context.Context, _ storage.DLQEntry) error { return nil }
