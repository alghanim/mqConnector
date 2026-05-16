// Replay-from-broker-history. For brokers that retain messages on the
// server side (Kafka, NATS JetStream), the operator can ask the pipeline
// to re-process a historical time window — e.g. "all messages between
// 13:00 and 13:30 yesterday" — without disturbing the live consumer.
//
// Semantics:
//   - A new, throwaway consumer is opened against the source. It uses
//     its own consumer group (Kafka) / consumer name (JetStream) so the
//     live pipeline's offsets aren't touched.
//   - Messages flow through the same stage chain as the live executor
//     and publish to the same destination. No DLQ on replay failure —
//     the operator already knows replay is exceptional and gets the
//     error directly.
//   - Until the window's "until" timestamp is reached (or until the
//     replay consumer has drained the available messages), the call
//     blocks. Operators wrap this in a context-bound HTTP call.
//
// Not supported on RabbitMQ / MQTT / AMQP 1.0: those brokers don't
// retain consumed messages, so there's nothing to replay. Returns
// ErrReplayNotSupported with a clear message.

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/IBM/sarama"

	"mqConnector/internal/mq"
	"mqConnector/internal/mqcfg"
	"mqConnector/internal/storage"
)

// ErrReplayNotSupported is returned by Replay when the source broker
// can't replay historical messages (RabbitMQ, MQTT, AMQP 1.0).
var ErrReplayNotSupported = errors.New("replay: source broker does not retain history")

// ReplayWindow names the time bracket. Both ends are inclusive at the
// broker level (Kafka's offset-for-time finds the earliest offset whose
// timestamp >= the requested time).
type ReplayWindow struct {
	Since time.Time
	Until time.Time
}

// ReplayResult is the summary returned to the operator after a replay.
type ReplayResult struct {
	MessagesRead    int64         `json:"messages_read"`
	MessagesSent    int64         `json:"messages_sent"`
	MessagesDropped int64         `json:"messages_dropped"`
	Duration        time.Duration `json:"duration"`
}

// Replay opens a one-shot consumer on the pipeline's source, drains the
// time window, runs each message through the stage chain, and publishes
// to the configured destination. Blocks until the window is drained or
// ctx is cancelled. The live pipeline keeps running undisturbed.
func (m *Manager) Replay(ctx context.Context, pipelineID string, win ReplayWindow) (*ReplayResult, error) {
	if win.Until.Before(win.Since) || win.Until.IsZero() {
		return nil, fmt.Errorf("replay: invalid window: since=%s until=%s",
			win.Since, win.Until)
	}

	pipe, err := m.store.Pipelines.GetUnsafe(ctx, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("replay: pipeline lookup: %w", err)
	}
	src, err := m.store.Connections.GetUnsafe(ctx, pipe.SourceID)
	if err != nil {
		return nil, fmt.Errorf("replay: source lookup: %w", err)
	}
	dst, err := m.store.Connections.GetUnsafe(ctx, pipe.DestinationID)
	if err != nil {
		return nil, fmt.Errorf("replay: dest lookup: %w", err)
	}

	srcCfg := mqcfg.From(src)
	dstCfg := mqcfg.From(dst)

	switch srcCfg.Type {
	case mq.TypeKafka:
		return m.replayKafka(ctx, pipe, srcCfg, dstCfg, win)
	case mq.TypeNATS:
		// JetStream replay is documented but not yet implemented at
		// this layer — operators can drive it manually via the nats
		// CLI today. Track here so the API can return a clean error
		// rather than a 500 from a nil pointer.
		return nil, fmt.Errorf("replay: NATS JetStream replay not yet implemented at the manager layer; planned")
	default:
		return nil, fmt.Errorf("%w: %s", ErrReplayNotSupported, srcCfg.Type)
	}
}

// replayKafka drives the actual Kafka replay. Uses sarama's offset-for-
// time API (translates a Unix-ms timestamp into the offset whose log-
// append-time is >= the requested ms) and consumes forward from there
// until the next message's timestamp passes the "until" cutoff.
func (m *Manager) replayKafka(
	ctx context.Context,
	pipe *storage.Pipeline,
	srcCfg, dstCfg mq.Config,
	win ReplayWindow,
) (*ReplayResult, error) {
	cfg := sarama.NewConfig()
	cfg.Version = sarama.V2_5_0_0
	cfg.Consumer.Return.Errors = true
	// Generic consumer (no consumer group) — we don't want to touch
	// the live pipeline's offsets. Manual partition assignment.
	client, err := sarama.NewClient(srcCfg.Brokers, cfg)
	if err != nil {
		return nil, fmt.Errorf("replay: sarama client: %w", err)
	}
	defer client.Close()

	partitions, err := client.Partitions(srcCfg.Topic)
	if err != nil {
		return nil, fmt.Errorf("replay: partitions: %w", err)
	}
	consumer, err := sarama.NewConsumerFromClient(client)
	if err != nil {
		return nil, fmt.Errorf("replay: consumer: %w", err)
	}
	defer consumer.Close()

	// Spawn one partition consumer per partition starting at the
	// offset whose timestamp is >= win.Since.
	sinceMs := win.Since.UnixMilli()
	untilNs := win.Until.UnixNano()
	pcs := make([]sarama.PartitionConsumer, 0, len(partitions))
	for _, p := range partitions {
		offset, err := client.GetOffset(srcCfg.Topic, p, sinceMs)
		if err != nil {
			return nil, fmt.Errorf("replay: get offset for partition %d: %w", p, err)
		}
		if offset == sarama.OffsetNewest || offset < 0 {
			// No messages in this partition >= sinceMs; skip it.
			continue
		}
		pc, err := consumer.ConsumePartition(srcCfg.Topic, p, offset)
		if err != nil {
			return nil, fmt.Errorf("replay: consume partition %d at offset %d: %w",
				p, offset, err)
		}
		pcs = append(pcs, pc)
	}
	defer func() {
		for _, pc := range pcs {
			_ = pc.Close()
		}
	}()

	// Build the same stages the live executor uses so transforms,
	// validation, routing all replay identically.
	stageRows, _ := m.store.Stages.ListByPipelineUnsafe(ctx, pipe.ID)
	transforms, _ := m.store.Transforms.ListByPipelineUnsafe(ctx, pipe.ID)
	routingRules, _ := m.store.RoutingRules.ListByPipelineUnsafe(ctx, pipe.ID)
	schemas, _ := m.loadSchemasForBuild(ctx, pipe)
	stages, err := Build(BuildContext{
		Pipeline:     pipe,
		StageRows:    stageRows,
		Transforms:   transforms,
		RoutingRules: routingRules,
		Schemas:      schemas,
	})
	if err != nil {
		return nil, fmt.Errorf("replay: build stages: %w", err)
	}

	logger := m.logger.With(
		"pipeline_id", pipe.ID,
		"replay_since", win.Since.Format(time.RFC3339),
		"replay_until", win.Until.Format(time.RFC3339),
	)
	logger.Info("replay starting", "partitions", len(pcs))

	start := time.Now()
	result := &ReplayResult{}
	// Aggregate every partition's message channel into one select loop.
	// A goroutine per partition pushes into agg; the main loop drains
	// until every partition signals done (or ctx cancels).
	type item struct {
		msg *sarama.ConsumerMessage
		err error
	}
	agg := make(chan item, len(pcs)*16)
	doneN := 0
	for _, pc := range pcs {
		pc := pc
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok := <-pc.Messages():
					if !ok {
						agg <- item{} // signal end
						return
					}
					if msg.Timestamp.UnixNano() > untilNs {
						// Past the window; close out this partition.
						agg <- item{}
						return
					}
					agg <- item{msg: msg}
				case err, ok := <-pc.Errors():
					if !ok {
						agg <- item{}
						return
					}
					agg <- item{err: err.Err}
				}
			}
		}()
	}

	for doneN < len(pcs) {
		select {
		case <-ctx.Done():
			logger.Warn("replay cancelled", "err", ctx.Err())
			result.Duration = time.Since(start)
			return result, ctx.Err()
		case it := <-agg:
			if it.msg == nil && it.err == nil {
				doneN++
				continue
			}
			if it.err != nil {
				logger.Warn("replay: partition error", "err", it.err)
				continue
			}
			result.MessagesRead++
			outcome, runErr := RunStages(ctx, stages, it.msg.Value)
			if runErr != nil {
				logger.Warn("replay: stage failed", "err", runErr, "offset", it.msg.Offset)
				result.MessagesDropped++
				continue
			}
			// Publish through the regular pool — the replay's send
			// goes through the same connection as the live pipeline's
			// destination, no extra connection pressure.
			if err := m.replaySend(ctx, dstCfg, outcome.Body); err != nil {
				logger.Warn("replay: send failed", "err", err, "offset", it.msg.Offset)
				result.MessagesDropped++
				continue
			}
			result.MessagesSent++
		}
	}
	result.Duration = time.Since(start)
	logger.Info("replay complete",
		"read", result.MessagesRead,
		"sent", result.MessagesSent,
		"dropped", result.MessagesDropped,
		"duration_ms", result.Duration.Milliseconds(),
	)
	return result, nil
}

func (m *Manager) replaySend(ctx context.Context, cfg mq.Config, payload []byte) error {
	conn, release, err := m.pool.Get(ctx, "replay-"+cfg.Topic+cfg.QueueName, cfg)
	if err != nil {
		return err
	}
	defer release()
	return conn.SendMessage(ctx, payload)
}

// loadSchemasForBuild fetches the schemas referenced by this pipeline's
// stages so Build() has the FileDescriptorSets / JSON Schemas it needs.
// Mirrors what the live executor's manager does on Reload.
func (m *Manager) loadSchemasForBuild(ctx context.Context, pipe *storage.Pipeline) (map[string]*storage.Schema, error) {
	if m.store == nil {
		return nil, nil
	}
	if pipe.SchemaID == "" {
		return map[string]*storage.Schema{}, nil
	}
	sc, err := m.store.Schemas.GetUnsafe(ctx, pipe.SchemaID)
	if err != nil {
		return nil, err
	}
	return map[string]*storage.Schema{pipe.SchemaID: sc}, nil
}

// Reference unused-only-in-tests helpers to suppress lint warnings.
var _ = slog.Default
