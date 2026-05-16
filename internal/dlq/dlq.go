// Package dlq is the dead-letter queue. Failed messages are persisted via
// storage.DLQRepo; retries actually re-publish via the connection pool.
//
// Tenant boundary: every public method takes an explicit tenantID. The
// internal Push path (called by the pipeline executor) is the only caller
// that can write into DLQ on behalf of a specific tenant — the executor
// passes the pipeline's tenant. Retry rebuilds the destination connector
// using ConnectionRepo.GetUnsafe because the executor already trusts the
// pipeline-id-to-tenant mapping it consults; HTTP callers go through the
// tenant-scoped Retry signature.
package dlq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"mqConnector/internal/events"
	"mqConnector/internal/mq"
	"mqConnector/internal/mqcfg"
	"mqConnector/internal/storage"
)

// EventSink is the minimal surface dlq uses to emit dlq.pushed events.
// internal/events.Publisher satisfies it. Nil disables emission.
type EventSink interface {
	Publish(ctx context.Context, e events.Event) int
}

// Service is the DLQ controller. It owns no goroutines; callers drive it.
type Service struct {
	store      *storage.Store
	pool       *mq.Pool
	maxRetries int
	logger     *slog.Logger

	sinkMu sync.RWMutex
	sink   EventSink
}

// SetEventSink installs the publisher Push will emit dlq.pushed to.
func (s *Service) SetEventSink(sink EventSink) {
	s.sinkMu.Lock()
	defer s.sinkMu.Unlock()
	s.sink = sink
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

// Push satisfies pipeline.DLQSink — used by Executor to drop failed
// messages. The entry already carries its tenant id (set by the executor
// from the pipeline's tenant), so we pass it straight through.
func (s *Service) Push(ctx context.Context, entry storage.DLQEntry) error {
	tenant := entry.TenantID
	if tenant == "" {
		tenant = storage.DefaultTenantID
	}
	// If the source pipeline carries a retry policy, schedule the row
	// for the reaper. Failure to look up the pipeline (deleted between
	// Push and now, or row exists with no pipeline_id) just means
	// "manual triage only" — we still want the entry persisted.
	if entry.PipelineID != "" {
		if pipe, err := s.store.Pipelines.GetUnsafe(ctx, entry.PipelineID); err == nil {
			retryMax := pipe.RetryMax
			if retryMax == 0 {
				retryMax = s.maxRetries
			}
			if retryMax > 0 {
				delay := backoffDelay(0, pipe.RetryBackoffMs)
				t := time.Now().UTC().Add(delay)
				entry.NextRetryAt = &t
			}
		}
	}
	if err := s.store.DLQ.Insert(ctx, tenant, &entry); err != nil {
		s.logger.Error("dlq insert failed", "err", err)
		return err
	}
	s.logger.Warn("message sent to DLQ",
		"tenant_id", tenant,
		"pipeline_id", entry.PipelineID,
		"source_queue", entry.SourceQueue,
		"reason", entry.ErrorReason,
	)
	// Lifecycle event: a message landed in DLQ. Best-effort emission —
	// a slow webhook subscriber must never back-pressure the pipeline.
	s.sinkMu.RLock()
	sink := s.sink
	s.sinkMu.RUnlock()
	if sink != nil {
		sink.Publish(ctx, events.Event{
			Type:     events.TypeDLQPushed,
			TenantID: tenant,
			Data: map[string]any{
				"dlq_id":       entry.ID,
				"pipeline_id":  entry.PipelineID,
				"source_queue": entry.SourceQueue,
				"reason":       entry.ErrorReason,
			},
		})
	}
	return nil
}

// ErrMaxRetries is returned by Retry when the entry has hit the retry cap.
var ErrMaxRetries = errors.New("dlq: max retries exceeded")

// Retry re-publishes the message to the pipeline's original destination.
// Tenant-scoped — callers (HTTP layer) must pass the active tenant.
// Returns ErrNotFound when the id doesn't exist OR exists in a different
// tenant (deliberately indistinguishable — no information leak).
func (s *Service) Retry(ctx context.Context, tenantID, id string) error {
	entry, err := s.store.DLQ.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if entry.RetryCount >= s.maxRetries {
		return ErrMaxRetries
	}
	if entry.PipelineID == "" {
		return fmt.Errorf("dlq: entry %s has no pipeline_id", id)
	}

	// Once we've verified the entry belongs to the caller's tenant, the
	// downstream lookups can use the per-id scoped path.
	pipe, err := s.store.Pipelines.Get(ctx, tenantID, entry.PipelineID)
	if err != nil {
		return fmt.Errorf("dlq retry: pipeline lookup: %w", err)
	}
	dest, err := s.store.Connections.Get(ctx, tenantID, pipe.DestinationID)
	if err != nil {
		return fmt.Errorf("dlq retry: destination lookup: %w", err)
	}

	cfg := mqcfg.From(dest)
	conn, release, err := s.pool.Get(ctx, "dlq-retry-"+entry.ID, cfg)
	if err != nil {
		return fmt.Errorf("dlq retry: pool get: %w", err)
	}
	defer release()

	if err := conn.SendMessage(ctx, entry.OriginalMsg); err != nil {
		return fmt.Errorf("dlq retry: send: %w", err)
	}

	if err := s.store.DLQ.IncrementRetry(ctx, tenantID, id); err != nil {
		s.logger.Warn("dlq increment after successful retry failed",
			"id", id, "err", err)
	}
	s.logger.Info("dlq message retried",
		"tenant_id", tenantID,
		"id", id,
		"pipeline_id", entry.PipelineID,
		"retry_count", entry.RetryCount+1,
	)
	return nil
}

// Delete removes a DLQ entry by id, scoped to the named tenant.
func (s *Service) Delete(ctx context.Context, tenantID, id string) error {
	return s.store.DLQ.Delete(ctx, tenantID, id)
}

// List returns a paginated DLQ slice for one tenant (no filter).
func (s *Service) List(ctx context.Context, tenantID string, page, perPage int) ([]*storage.DLQEntry, int, error) {
	return s.store.DLQ.List(ctx, tenantID, page, perPage)
}

// ListFiltered exposes the storage-level filter unchanged.
func (s *Service) ListFiltered(ctx context.Context, tenantID string, f storage.DLQFilter, page, perPage int) ([]*storage.DLQEntry, int, error) {
	return s.store.DLQ.ListFiltered(ctx, tenantID, f, page, perPage)
}
