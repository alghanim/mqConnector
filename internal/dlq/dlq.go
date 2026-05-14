// Package dlq is the dead-letter queue. Failed messages are persisted via
// storage.DLQRepo; retries actually re-publish via the connection pool.
package dlq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"mqConnector/internal/mq"
	"mqConnector/internal/pipeline"
	"mqConnector/internal/storage"
)

// Service is the DLQ controller. It owns no goroutines; callers drive it.
type Service struct {
	store       *storage.Store
	pool        *mq.Pool
	maxRetries  int
	logger      *slog.Logger
}

// Options bundles the constructor arguments.
type Options struct {
	MaxRetries int
	Logger     *slog.Logger
}

// NewService constructs a Service.
func NewService(store *storage.Store, pool *mq.Pool, opts Options) *Service {
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = 3
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:      store,
		pool:       pool,
		maxRetries: opts.MaxRetries,
		logger:     logger.With("component", "dlq"),
	}
}

// MaxRetries returns the configured retry cap.
func (s *Service) MaxRetries() int { return s.maxRetries }

// Push satisfies pipeline.DLQSink — used by Executor to drop failed messages.
func (s *Service) Push(ctx context.Context, entry storage.DLQEntry) error {
	if err := s.store.DLQ.Insert(ctx, &entry); err != nil {
		s.logger.Error("dlq insert failed", "err", err)
		return err
	}
	s.logger.Warn("message sent to DLQ",
		"pipeline_id", entry.PipelineID,
		"source_queue", entry.SourceQueue,
		"reason", entry.ErrorReason,
	)
	return nil
}

// ErrMaxRetries is returned by Retry when the entry has hit the retry cap.
var ErrMaxRetries = errors.New("dlq: max retries exceeded")

// Retry re-publishes the message to the pipeline's original destination. The
// destination is rediscovered from storage each time, so config edits made
// since the failure are honoured.
func (s *Service) Retry(ctx context.Context, id string) error {
	entry, err := s.store.DLQ.Get(ctx, id)
	if err != nil {
		return err
	}
	if entry.RetryCount >= s.maxRetries {
		return ErrMaxRetries
	}
	if entry.PipelineID == "" {
		return fmt.Errorf("dlq: entry %s has no pipeline_id", id)
	}

	pipe, err := s.store.Pipelines.Get(ctx, entry.PipelineID)
	if err != nil {
		return fmt.Errorf("dlq retry: pipeline lookup: %w", err)
	}
	dest, err := s.store.Connections.Get(ctx, pipe.DestinationID)
	if err != nil {
		return fmt.Errorf("dlq retry: destination lookup: %w", err)
	}

	cfg := pipeline.ToMQConfig(dest)
	conn, release, err := s.pool.Get(ctx, "dlq-retry-"+entry.ID, cfg)
	if err != nil {
		return fmt.Errorf("dlq retry: pool get: %w", err)
	}
	defer release()

	if err := conn.SendMessage(ctx, entry.OriginalMsg); err != nil {
		return fmt.Errorf("dlq retry: send: %w", err)
	}

	if err := s.store.DLQ.IncrementRetry(ctx, id); err != nil {
		s.logger.Warn("dlq increment after successful retry failed",
			"id", id, "err", err)
	}
	s.logger.Info("dlq message retried",
		"id", id,
		"pipeline_id", entry.PipelineID,
		"retry_count", entry.RetryCount+1,
	)
	return nil
}

// Delete removes a DLQ entry by id.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.DLQ.Delete(ctx, id)
}

// List returns a paginated DLQ slice.
func (s *Service) List(ctx context.Context, page, perPage int) ([]*storage.DLQEntry, int, error) {
	return s.store.DLQ.List(ctx, page, perPage)
}
