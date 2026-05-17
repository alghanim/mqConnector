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
	Logger  *slog.Logger
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

	logger.Info("pipeline starting", "workers", workers)
	defer logger.Info("pipeline stopped")

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

func (e *Executor) send(ctx context.Context, id string, cfg mq.Config, message []byte) error {
	conn, release, err := e.Pool.Get(ctx, id, cfg)
	if err != nil {
		return fmt.Errorf("get dest %s: %w", id, err)
	}
	defer release()
	return conn.SendMessage(ctx, message)
}
