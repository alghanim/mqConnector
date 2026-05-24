package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"mqConnector/internal/mq"
	"mqConnector/internal/storage"
	"mqConnector/internal/tracing"
)

// MetricsSink is the minimal surface the executor uses to report per-pipeline
// metrics. The concrete implementation lives in internal/metrics — broken out
// here as an interface to avoid an import cycle and keep tests independent.
type MetricsSink interface {
	Register(pipelineID, sourceQueue, destQueue string)
	Unregister(pipelineID string)
	SetStatus(pipelineID, status, lastError string)
	RecordSuccess(pipelineID string, bytes int64, latencyMs float64)
	RecordFailure(pipelineID string)
	// RecordDedupSkipped counts outbound sends short-circuited by the
	// dedup window. Always paired with a finalize(...,true) so the
	// source still gets committed (the operator opted in to "treat
	// these as the same message").
	RecordDedupSkipped(pipelineID string)
	// RecordValidateAttempt is called once per validate-stage
	// execution. ok=true means the schema accepted the payload;
	// ok=false means the validate stage returned an error and the
	// pipeline routed the message to DLQ. The pair feeds the schema
	// drift alarm — see deploy/prometheus/mqconnector-slos.yaml.
	RecordValidateAttempt(pipelineID string, ok bool)
	// SetSourceDepth is called by the depth-sampling goroutine when
	// the source connector implements mq.DepthReporter. A negative
	// value clears the most recent reading (so the Prometheus
	// renderer drops the series instead of reporting stale data).
	SetSourceDepth(pipelineID string, depth int64)
}

// Deduper is the minimal surface the executor uses to consult the
// dedup window before forwarding to the destination. A nil Deduper
// (or a window of 0) disables the check entirely — the original
// at-least-once contract is preserved by default. The concrete
// implementation lives in internal/storage.DedupRepo.
type Deduper interface {
	CheckAndRecord(ctx context.Context, pipelineID, payloadHash string, windowSeconds int) (bool, error)
}

// DLQSink is the minimal surface the executor uses to push failures into the
// dead-letter queue.
type DLQSink interface {
	Push(ctx context.Context, entry storage.DLQEntry) error
}

// ConnectionResolver returns a connector Config for the given connection ID,
// loading it from storage.
type ConnectionResolver interface {
	Resolve(ctx context.Context, id string) (mq.Config, string, error) // returns cfg + queueName
}

// Executor owns one running pipeline: it pulls messages from the source,
// pushes them through the stages, and forwards to the configured
// destination(s).
type Executor struct {
	Pipeline    *storage.Pipeline
	Stages      []Stage
	Pool        *mq.Pool
	Source      mq.Config
	SourceQueue string
	DefaultDest mq.Config
	DestQueue   string
	// RouteDests maps routing-rule destination connection IDs to their
	// resolved mq.Config — needed because routing rules emit destination IDs
	// at runtime and the executor must already have the configs in hand.
	RouteDests map[string]mq.Config

	Metrics MetricsSink
	DLQ     DLQSink
	Dedup   Deduper // nil disables dedup; honoured only when Pipeline.DedupWindowSeconds > 0
	Logger  *slog.Logger

	// budget is set in Run() when the pipeline carries
	// MaxMsgsPerMinute > 0. Shared across all workers so the cap
	// applies per-pipeline, not per-worker.
	budget *budget

	// breaker is the per-pipeline outbound circuit breaker. Set in
	// Run(); shared by every worker so a broker outage trips once
	// for the pipeline, not once per worker. nil disables the
	// circuit (used in tests that don't want the open-state
	// behaviour to interfere).
	breaker *breaker
}

// Run blocks until ctx is cancelled or the executor encounters an unrecoverable
// error. Recoverable errors (e.g. validation failures, transform errors) are
// pushed to the DLQ; the loop continues.
func (e *Executor) Run(ctx context.Context) error {
	if e.Logger == nil {
		e.Logger = slog.Default()
	}
	logger := e.Logger.With(
		"pipeline_id", e.Pipeline.ID,
		"pipeline_name", e.Pipeline.Name,
	)

	if e.Metrics != nil {
		e.Metrics.Register(e.Pipeline.ID, e.SourceQueue, e.DestQueue)
		defer e.Metrics.Unregister(e.Pipeline.ID)
	}

	// Worker count comes from the pipeline row. Clamp at runtime: ≥1
	// (a 0-worker pipeline would silently never process anything), and
	// ≤ a generous upper bound (the destination broker, not extra
	// goroutines, is the usual throughput ceiling).
	workers := e.Pipeline.Workers
	if workers < 1 {
		workers = 1
	}
	if workers > 16 {
		workers = 16
	}

	// Outbound circuit breaker. One per pipeline, shared across
	// workers. Default threshold + cool-down; per-pipeline overrides
	// could be added but the defaults handle the typical "destination
	// broker restart" recovery window without operator tuning.
	e.breaker = newBreaker(defaultBreakerThreshold, defaultBreakerCooldown)

	// Per-pipeline message budget. When > 0 the executor enforces a
	// simple token-bucket cap: budget messages per minute, refilled
	// every refill window. Shared across workers so the cap is
	// per-pipeline not per-worker (which would multiply by N). Zero
	// disables — legacy behaviour.
	if e.Pipeline.MaxMsgsPerMinute > 0 {
		e.budget = newBudget(e.Pipeline.MaxMsgsPerMinute, time.Minute)
		logger.Info("pipeline starting", "workers", workers,
			"budget_per_minute", e.Pipeline.MaxMsgsPerMinute)
	} else {
		logger.Info("pipeline starting", "workers", workers)
	}
	defer logger.Info("pipeline stopped")

	// Source-depth sampler. Spawned only when the source connector
	// declares the DepthReporter capability; saves a goroutine per
	// pipeline for sources that can't report depth (NATS Core, MQTT
	// QoS 0). Uses its OWN pool key so it doesn't contend with worker
	// receives — depth probes are best-effort and shouldn't stall on
	// a blocking receive. Cancels with ctx; reports -1 to the metrics
	// sink on shutdown so a Prometheus scrape during teardown doesn't
	// see a stale gauge.
	go e.runDepthSampler(ctx, logger)

	// Each worker runs the same receive→stages→send→commit loop
	// independently. To preserve at-least-once semantics we give every
	// worker its OWN pool slot (key includes worker index) — otherwise
	// concurrent ReceiveMessage calls into a shared connector would
	// race on the per-connector "pending delivery" slot used by
	// Commit/Nack. With per-worker connectors the broker handles
	// parallelism the way each protocol intends: Kafka rebalances
	// partitions across group members, RabbitMQ round-robins
	// deliveries, JetStream fans out to subscriptions.
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		workerIdx := w
		workerLogger := logger.With("worker", workerIdx)
		go func() {
			defer wg.Done()
			e.runWorker(ctx, workerIdx, workerLogger)
		}()
	}
	wg.Wait()
	return nil
}

// runWorker is the per-goroutine receive loop. Each worker has its own
// pool key so the per-broker Commit/Nack token (delivery tag, Kafka
// session, JetStream msg ack) is unambiguous — see Run for the design
// note on per-worker connectors.
func (e *Executor) runWorker(ctx context.Context, workerIdx int, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := e.processOne(ctx, workerIdx, logger); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			// Errors here are infrastructure-level (source connection lost).
			// Back off briefly to avoid a hot loop, then retry.
			logger.Warn("source error, backing off", "err", err)
			if e.Metrics != nil {
				e.Metrics.SetStatus(e.Pipeline.ID, "error", err.Error())
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (e *Executor) processOne(ctx context.Context, workerIdx int, logger *slog.Logger) error {
	// Throttle. Blocks until the per-pipeline budget admits one
	// more message or ctx cancels. No-op when budget is nil.
	if e.budget != nil {
		if err := e.budget.take(ctx); err != nil {
			return err
		}
	}
	// Single-worker pipelines keep the historical key so existing
	// dashboards and test injections continue to work. Multi-worker
	// pipelines get one connector per worker.
	sourceID := "source-" + e.Pipeline.ID
	if workerIdx > 0 {
		sourceID = fmt.Sprintf("%s-w%d", sourceID, workerIdx)
	}
	source, release, err := e.Pool.Get(ctx, sourceID, e.Source)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	// Hold the source connection through receive→send→commit so the
	// per-connector "pending delivery" slot is consistent across the
	// whole transaction. Releasing here means the pool can evict it on
	// idle timeout once we're done, but other workers won't grab it
	// mid-flight because each worker has a unique pool key.
	defer release()
	// Once we have a healthy source connection, clear any prior error
	// status — otherwise an idle pipeline (waiting on its first message
	// after a broker bounce) shows up in /api/health as "error" forever
	// even though we're already reconnected.
	if e.Metrics != nil {
		e.Metrics.SetStatus(e.Pipeline.ID, "connected", "")
	}

	start := time.Now()
	message, err := source.ReceiveMessage(ctx)
	if err != nil {
		return err
	}

	// Every consumed message gets a fresh trace context. The receive →
	// stages → send chain emits one span per phase so a slow operator
	// can see exactly where time is going via the structured logs.
	ctx, span := tracing.Start(ctx, logger, "pipeline.processOne")
	span.SetAttr("pipeline_id", e.Pipeline.ID)
	span.SetAttr("bytes_in", len(message))
	defer span.End(nil)

	outcome, runErr := RunStages(ctx, e.Stages, message)
	// Per-stage observations regardless of whether the chain errored.
	// Drift detection relies on attempts (success+failure) being
	// counted on both paths; a failing stage is the last entry in
	// outcome.Runs.
	e.recordStageObservations(outcome.Runs)
	if runErr != nil {
		logger.Warn("stage failed, sending to DLQ", "err", runErr)
		// At-least-once handoff: a stage failure is "handled" once the
		// DLQ owns the bytes. If the DLQ push succeeds we Commit (so
		// the broker stops redelivering); if it fails we Nack with
		// requeue so the broker redelivers and we get another shot
		// next iteration.
		dlqErr := errors.New("dlq disabled")
		if e.DLQ != nil {
			dlqErr = e.DLQ.Push(ctx, storage.DLQEntry{
				TenantID:    e.Pipeline.TenantID,
				PipelineID:  e.Pipeline.ID,
				SourceQueue: e.SourceQueue,
				OriginalMsg: message,
				ErrorReason: runErr.Error(),
			})
		}
		if e.Metrics != nil {
			e.Metrics.RecordFailure(e.Pipeline.ID)
		}
		e.finalize(ctx, source, dlqErr == nil, logger)
		return nil
	}
	current := outcome.Body
	routeResult := outcome.Route
	_ = outcome.Format // currentFormat is consumed inside RunStages

	// Circuit-breaker gate. If the destination has been failing the
	// breaker will block the send and we Nack the source delivery so
	// the broker re-delivers it after the cool-down. This is what
	// keeps a broken destination from filling the DLQ with millions
	// of "destination unreachable" entries.
	if e.breaker != nil && !e.breaker.allow() {
		logger.Warn("circuit open; nack to source for redelivery")
		e.finalize(ctx, source, false, logger)
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
		}
		return nil
	}

	// Destination-side dedup gate. Pipelines opt in via
	// DedupWindowSeconds > 0. The hash is computed over the post-stage
	// payload — so two source messages that transform into the same
	// outbound payload still collapse cleanly, which is the whole
	// point. A check failure is logged but doesn't block delivery (a
	// dedup-store outage would otherwise reduce availability of the
	// pipeline; we prefer at-least-once + log over fail-closed here).
	if e.Pipeline.DedupWindowSeconds > 0 && e.Dedup != nil {
		hash := payloadHash(current)
		dupe, err := e.Dedup.CheckAndRecord(ctx, e.Pipeline.ID, hash, e.Pipeline.DedupWindowSeconds)
		if err != nil {
			logger.Warn("dedup check failed; forwarding anyway", "err", err)
		} else if dupe {
			if e.Metrics != nil {
				e.Metrics.RecordDedupSkipped(e.Pipeline.ID)
			}
			logger.Debug("dedup hit; skipping send",
				"window_seconds", e.Pipeline.DedupWindowSeconds)
			// Treat as handled: the operator opted in to "this
			// payload is equivalent to one already delivered."
			// Committing the source stops broker redelivery; the
			// downstream stays consistent.
			e.finalize(ctx, source, true, logger)
			return nil
		}
	}

	// Forward
	var sendErr error
	if routeResult != nil {
		for _, destID := range routeResult.Destinations {
			cfg, ok := e.RouteDests[destID]
			if !ok {
				logger.Warn("route destination not resolved", "dest_id", destID)
				continue
			}
			if err := e.send(ctx, "route-"+destID, cfg, current); err != nil {
				sendErr = err
				logger.Warn("route send failed", "dest_id", destID, "err", err)
			}
		}
	} else {
		sendErr = e.send(ctx, "dest-"+e.Pipeline.ID, e.DefaultDest, current)
	}

	if e.breaker != nil {
		e.breaker.recordResult(sendErr == nil)
	}

	if sendErr != nil {
		dlqErr := errors.New("dlq disabled")
		if e.DLQ != nil {
			dlqErr = e.DLQ.Push(ctx, storage.DLQEntry{
				TenantID:    e.Pipeline.TenantID,
				PipelineID:  e.Pipeline.ID,
				SourceQueue: e.SourceQueue,
				OriginalMsg: message,
				ErrorReason: "send: " + sendErr.Error(),
			})
		}
		if e.Metrics != nil {
			e.Metrics.RecordFailure(e.Pipeline.ID)
		}
		e.finalize(ctx, source, dlqErr == nil, logger)
		return nil
	}

	if e.Metrics != nil {
		e.Metrics.RecordSuccess(e.Pipeline.ID,
			int64(len(current)),
			float64(time.Since(start).Milliseconds()))
	}
	e.finalize(ctx, source, true, logger)
	return nil
}

// finalize closes out the per-message ack/nack with the source broker.
// handled=true (send succeeded OR DLQ owns the bytes) → Commit.
// handled=false (both downstream and DLQ failed) → Nack with requeue
// so the source broker redelivers on the next loop iteration.
func (e *Executor) finalize(ctx context.Context, source mq.Connector, handled bool, logger *slog.Logger) {
	if handled {
		if err := source.Commit(ctx); err != nil {
			logger.Warn("source commit failed", "err", err)
		}
		return
	}
	if err := source.Nack(ctx, true); err != nil {
		logger.Warn("source nack failed", "err", err)
	}
}

// recordStageObservations forwards per-stage outcomes to the metrics
// sink. Currently it only emits validate-stage observations — the
// foundation of the schema-drift alarm. Other per-stage signals
// (latency histograms, error breakdown by stage) layer in here
// without touching the executor's hot path.
func (e *Executor) recordStageObservations(runs []StageRun) {
	if e.Metrics == nil || len(runs) == 0 {
		return
	}
	for _, run := range runs {
		if run.Name == "validate" {
			e.Metrics.RecordValidateAttempt(e.Pipeline.ID, !run.Failed)
		}
	}
}

func (e *Executor) send(ctx context.Context, id string, cfg mq.Config, message []byte) error {
	conn, release, err := e.Pool.Get(ctx, id, cfg)
	if err != nil {
		return fmt.Errorf("get dest %s: %w", id, err)
	}
	defer release()
	return conn.SendMessage(ctx, message)
}

// depthSamplerInterval is how often the per-pipeline depth probe asks
// the source broker for its current backlog. 30s is the natural cadence
// for an alerting metric — fast enough that a stuck pipeline shows up
// before the on-call is paged, slow enough that we're not hammering the
// broker on every Prometheus scrape (which can fire every 15s).
const depthSamplerInterval = 30 * time.Second

// runDepthSampler periodically probes the source connector's depth and
// reports it to the metrics sink. The probe uses a dedicated pool key
// (id + "-depth") so it gets its own connector instance and doesn't
// contend with the worker receive lock — receive can block on the
// broker for many seconds, which would block the metric otherwise.
//
// The goroutine exits cleanly when ctx is cancelled. If the source
// connector doesn't implement DepthReporter the goroutine still runs
// but reports nothing — the cost is one type assertion per tick.
func (e *Executor) runDepthSampler(ctx context.Context, logger *slog.Logger) {
	if e.Metrics == nil || e.Pool == nil {
		return
	}
	// First tick fires immediately so the metric appears within a
	// scrape interval of pipeline start.
	probe := func() {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conn, release, err := e.Pool.Get(probeCtx, e.Pipeline.ID+"-depth", e.Source)
		if err != nil {
			// Don't log on every tick — a broker outage will already
			// be visible through pipeline_up; this would just spam.
			return
		}
		defer release()
		reporter, ok := conn.(mq.DepthReporter)
		if !ok {
			return
		}
		depth, err := reporter.Depth(probeCtx)
		if err != nil || depth < 0 {
			return
		}
		e.Metrics.SetSourceDepth(e.Pipeline.ID, depth)
	}
	probe()
	t := time.NewTicker(depthSamplerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// Clear the metric so a scrape during teardown doesn't see
			// stale data attached to a stopped pipeline.
			e.Metrics.SetSourceDepth(e.Pipeline.ID, -1)
			return
		case <-t.C:
			probe()
		}
	}
}
