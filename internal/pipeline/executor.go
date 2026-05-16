package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

	logger.Info("pipeline starting")
	defer logger.Info("pipeline stopped")

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if err := e.processOne(ctx, logger); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			// Errors here are infrastructure-level (source connection lost).
			// Back off briefly to avoid a hot loop, then retry.
			logger.Warn("source error, backing off", "err", err)
			if e.Metrics != nil {
				e.Metrics.SetStatus(e.Pipeline.ID, "error", err.Error())
			}
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (e *Executor) processOne(ctx context.Context, logger *slog.Logger) error {
	sourceID := "source-" + e.Pipeline.ID
	source, release, err := e.Pool.Get(ctx, sourceID, e.Source)
	if err != nil {
		return fmt.Errorf("get source: %w", err)
	}
	// Once we have a healthy source connection, clear any prior error
	// status — otherwise an idle pipeline (waiting on its first message
	// after a broker bounce) shows up in /api/health as "error" forever
	// even though we're already reconnected.
	if e.Metrics != nil {
		e.Metrics.SetStatus(e.Pipeline.ID, "connected", "")
	}

	start := time.Now()
	message, err := source.ReceiveMessage(ctx)
	release()
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
		if e.DLQ != nil {
			_ = e.DLQ.Push(ctx, storage.DLQEntry{
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
		if e.DLQ != nil {
			_ = e.DLQ.Push(ctx, storage.DLQEntry{
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
		return nil
	}

	if e.Metrics != nil {
		e.Metrics.RecordSuccess(e.Pipeline.ID,
			int64(len(current)),
			float64(time.Since(start).Milliseconds()))
	}
	return nil
}

func (e *Executor) send(ctx context.Context, id string, cfg mq.Config, message []byte) error {
	conn, release, err := e.Pool.Get(ctx, id, cfg)
	if err != nil {
		return fmt.Errorf("get dest %s: %w", id, err)
	}
	defer release()
	return conn.SendMessage(ctx, message)
}
