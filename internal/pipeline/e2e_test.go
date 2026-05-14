package pipeline

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mqConnector/internal/dlq"
	"mqConnector/internal/logging"
	"mqConnector/internal/metrics"
	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
)

// TestE2E_UploadSchema_FilterAndRoute walks the full "real-case" scenario:
//
//  1. Seed two MQ connections in storage (one source, one destination).
//  2. Upload a JSON Schema and attach it to a pipeline.
//  3. Configure pipeline stages: validate → filter (remove customer.phone,
//     items[*].internal_cost) → transform (mask customer.email).
//  4. Wire the connector pool to the in-memory MemoryConnector so the
//     pipeline runs without any external broker.
//  5. Push several messages onto the source queue, including one that fails
//     validation (proves the DLQ pathway).
//  6. Assert: the valid ones land on the destination queue with the
//     filter+transform applied, the invalid one lands in storage.DLQ, and
//     pipeline metrics reflect both successes and the failure.
//
// This is the closest thing to a production smoke test that can run in CI
// without Docker — every piece of code in the message path executes
// (storage → manager → executor → stages → pool → MemoryConnector).
func TestE2E_UploadSchema_FilterAndRoute(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ----- 1. Storage ----------------------------------------------------
	dsn := "file:" + filepath.Join(t.TempDir(), "e2e.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	store, err := storage.Open(dsn, 4, 2)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer store.Close()

	// Connection records — type "rabbitmq" because the storage CHECK
	// constraint requires one of the supported broker types. The actual
	// transport will be the in-memory MemoryConnector, swapped in via the
	// pool's InjectForTest seam.
	src := &storage.Connection{
		Name: "orders-source", Type: "rabbitmq",
		URL: "amqp://x", QueueName: "orders.src",
	}
	dst := &storage.Connection{
		Name: "orders-dest", Type: "rabbitmq",
		URL: "amqp://x", QueueName: "orders.dst",
	}
	if err := store.Connections.Create(ctx, src); err != nil {
		t.Fatal(err)
	}
	if err := store.Connections.Create(ctx, dst); err != nil {
		t.Fatal(err)
	}

	// ----- 2. Upload a schema -------------------------------------------
	schemaContent := `{
	  "type": "object",
	  "required": ["id", "customer", "items"],
	  "properties": {
	    "id":       {"type": "string"},
	    "customer": {"type": "object", "required": ["name", "email"]},
	    "items":    {"type": "array"}
	  }
	}`
	schema := &storage.Schema{
		Name:       "orders.v1",
		SchemaType: "json_schema",
		Content:    schemaContent,
	}
	if err := store.Schemas.Create(ctx, schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	// ----- 3. Pipeline + stages + transforms ----------------------------
	pipe := &storage.Pipeline{
		Name:          "orders-bridge",
		SourceID:      src.ID,
		DestinationID: dst.ID,
		OutputFormat:  "same",
		FilterPaths:   []string{}, // stage-level config below
		SchemaID:      schema.ID,
		Enabled:       true,
	}
	if err := store.Pipelines.Create(ctx, pipe); err != nil {
		t.Fatal(err)
	}

	validateCfg, _ := json.Marshal(map[string]string{
		"schema_type": "json_schema",
		"content":     schemaContent,
	})
	filterCfg, _ := json.Marshal(map[string]any{
		"paths": []string{"customer.phone", "internal_notes"},
	})

	stages := []*storage.Stage{
		{StageOrder: 1, StageType: "validate", StageConfig: string(validateCfg), Enabled: true},
		{StageOrder: 2, StageType: "filter", StageConfig: string(filterCfg), Enabled: true},
		{StageOrder: 3, StageType: "transform", Enabled: true},
	}
	if err := store.Stages.ReplaceForPipeline(ctx, pipe.ID, stages); err != nil {
		t.Fatalf("replace stages: %v", err)
	}

	transforms := []*storage.Transform{
		{
			TransformType: "mask",
			SourcePath:    "customer.email",
			MaskPattern:   `(?P<head>[^@]).*?(?P<tail>@.*)`,
			MaskReplace:   `${head}***${tail}`,
			Order:         1,
		},
		{
			TransformType: "set",
			SourcePath:    "processed_by",
			SetValue:      "mqconnector-e2e",
			Order:         2,
		},
	}
	if err := store.Transforms.ReplaceForPipeline(ctx, pipe.ID, transforms); err != nil {
		t.Fatalf("replace transforms: %v", err)
	}

	// ----- 4. Pool wired to in-memory broker ----------------------------
	reg := mq.NewMemoryRegistry(64)
	pool := mq.NewPool(mq.PoolOptions{
		IdleTimeout:    time.Hour,
		HealthInterval: time.Hour,
		Logger:         logging.New("error", "json"),
	})
	defer pool.Close()

	memSrc := mq.NewMemoryConnector(reg, src.QueueName)
	memDst := mq.NewMemoryConnector(reg, dst.QueueName)
	if err := memSrc.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := memDst.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	// The Executor reaches for these IDs (see executor.go: "source-"+ID and
	// "dest-"+ID). Inject so it doesn't try to dial AMQP.
	pool.InjectForTest("source-"+pipe.ID, memSrc)
	pool.InjectForTest("dest-"+pipe.ID, memDst)

	metricsStore := metrics.New()
	dlqSvc := dlq.NewService(store, pool, dlq.Options{
		MaxRetries: 3,
		Logger:     logging.New("error", "json"),
	})

	// ----- 5. Boot the manager and push messages ------------------------
	mgr := NewManager(ctx, store, pool, metricsStore, dlqSvc, logging.New("error", "json"))
	if _, err := mgr.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer mgr.Stop()

	if mgr.ActiveCount() != 1 {
		t.Fatalf("expected 1 active pipeline, got %d", mgr.ActiveCount())
	}

	// Two valid messages with sensitive fields we expect to disappear, and
	// one malformed message (missing required customer.email) that must DLQ.
	valid1 := `{"id":"O-1","customer":{"name":"Alice","email":"alice@example.com","phone":"+97412345678"},"items":[{"sku":"S-1","qty":2}],"internal_notes":"VIP"}`
	valid2 := `{"id":"O-2","customer":{"name":"Bob","email":"bob@example.com","phone":"+1-555-0100"},"items":[{"sku":"S-2","qty":1}]}`
	invalid := `{"id":"O-3","customer":{"name":"Eve"}}` // missing customer.email

	for _, m := range []string{valid1, valid2, invalid} {
		if err := memSrc.SendMessage(ctx, []byte(m)); err != nil {
			t.Fatalf("seed source: %v", err)
		}
	}

	// ----- 6. Wait for the pipeline to drain ----------------------------
	// We expect 2 successful messages on dst + 1 DLQ entry.
	if err := waitFor(t, 3*time.Second, func() bool {
		dlqList, _, _ := store.DLQ.List(ctx, 1, 10)
		drained := reg.Drain(dst.QueueName)
		// drained messages we already removed — put them back so the test
		// assertions below can re-read them. (Simpler: pull once at the end.)
		for _, m := range drained {
			_ = memDst.SendMessage(ctx, m)
		}
		return len(drained) >= 2 && len(dlqList) >= 1
	}); err != nil {
		dlqList, _, _ := store.DLQ.List(ctx, 1, 10)
		drained := reg.Drain(dst.QueueName)
		t.Fatalf("pipeline did not drain in time: dst=%d dlq=%d", len(drained), len(dlqList))
	}

	// ----- 7. Assertions on destination payloads ------------------------
	destMsgs := reg.Drain(dst.QueueName)
	if len(destMsgs) != 2 {
		t.Fatalf("expected 2 messages on destination, got %d", len(destMsgs))
	}

	seen := map[string]map[string]any{}
	for _, m := range destMsgs {
		var d map[string]any
		if err := json.Unmarshal(m, &d); err != nil {
			t.Fatalf("dest msg not JSON: %v\n%s", err, m)
		}
		id, _ := d["id"].(string)
		seen[id] = d
	}

	for id, body := range seen {
		// processed_by set
		if body["processed_by"] != "mqconnector-e2e" {
			t.Errorf("%s: processed_by missing (%v)", id, body["processed_by"])
		}
		// internal_notes stripped
		if _, ok := body["internal_notes"]; ok {
			t.Errorf("%s: internal_notes should be filtered out", id)
		}
		// customer.phone stripped
		cust, _ := body["customer"].(map[string]any)
		if _, ok := cust["phone"]; ok {
			t.Errorf("%s: customer.phone should be filtered out", id)
		}
		// customer.email masked — must NOT contain the original local-part
		email, _ := cust["email"].(string)
		if !strings.Contains(email, "@") {
			t.Errorf("%s: email lost its @: %q", id, email)
		}
		if strings.HasPrefix(email, "alice@") || strings.HasPrefix(email, "bob@") {
			t.Errorf("%s: email was not masked: %q", id, email)
		}
	}

	// ----- 8. Assertions on DLQ ----------------------------------------
	dlqEntries, total, err := store.DLQ.List(ctx, 1, 10)
	if err != nil {
		t.Fatalf("list dlq: %v", err)
	}
	if total != 1 || len(dlqEntries) != 1 {
		t.Fatalf("expected 1 DLQ entry, got %d", total)
	}
	if !strings.Contains(dlqEntries[0].ErrorReason, "validate") {
		t.Errorf("DLQ reason should mention validation, got %q", dlqEntries[0].ErrorReason)
	}
	if !strings.Contains(string(dlqEntries[0].OriginalMsg), "O-3") {
		t.Errorf("DLQ should retain original message body")
	}

	// ----- 9. Metrics --------------------------------------------------
	snap := metricsStore.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("metrics snapshot length = %d, want 1", len(snap))
	}
	m, ok := snap[pipe.ID]
	if !ok {
		t.Fatalf("no metrics for pipeline %s", pipe.ID)
	}
	if m.MessagesProcessed < 2 {
		t.Errorf("messages_processed = %d, want >= 2", m.MessagesProcessed)
	}
	if m.MessagesFailed < 1 {
		t.Errorf("messages_failed = %d, want >= 1", m.MessagesFailed)
	}
}

// waitFor polls cond every 25 ms until it returns true or timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
	if cond() {
		return nil
	}
	return context.DeadlineExceeded
}
