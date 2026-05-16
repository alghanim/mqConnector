// Self-healing DLQ retry. When a pipeline carries a retry policy
// (pipelines.retry_max > 0 or the service-wide default), Push stamps
// next_retry_at on the new row; Reaper.Run wakes on an interval, walks
// every row whose next_retry_at <= now, attempts a re-publish through
// the pipeline's destination, and either clears next_retry_at (manual
// triage from here) or schedules the next attempt with exponential
// backoff.
//
// Idempotency: retries are not transactional across pipeline stages.
// If a stage has a side effect (HTTP webhook fire, mutation in an
// external store) it will run again on each retry. Operators who
// can't tolerate that should set retry_max=0 on the affected
// pipeline.

package dlq

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"mqConnector/internal/storage"
)

// ReaperOptions tunes the retry loop. Zero values fall back to safe
// defaults.
type ReaperOptions struct {
	// Interval between scans of the dlq table. Smaller = tighter
	// retry timing; larger = lower DB load. Default 10s.
	Interval time.Duration
	// BatchSize caps the number of rows pulled per scan. Default 50.
	BatchSize int
	// MaxJitter is the upper bound of the random jitter added to each
	// backoff. Prevents a herd of synchronously-failed messages from
	// retrying in lockstep. Default 250ms.
	MaxJitter time.Duration
}

// StartReaper spawns the retry goroutine. The returned stop function
// signals ctx cancellation and blocks until the loop exits — safe to
// pair with defer in main().
func (s *Service) StartReaper(ctx context.Context, opts ReaperOptions) (stop func()) {
	if opts.Interval <= 0 {
		opts.Interval = 10 * time.Second
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 50
	}
	if opts.MaxJitter <= 0 {
		opts.MaxJitter = 250 * time.Millisecond
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.logger.Info("dlq retry reaper started",
			"interval", opts.Interval.String(),
			"batch_size", opts.BatchSize,
		)
		t := time.NewTicker(opts.Interval)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				s.logger.Info("dlq retry reaper stopped")
				return
			case <-t.C:
				s.reapOnce(runCtx, opts)
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *Service) reapOnce(ctx context.Context, opts ReaperOptions) {
	rows, err := s.store.DLQ.ListDue(ctx, time.Now(), opts.BatchSize)
	if err != nil {
		s.logger.Warn("dlq reaper: list due failed", "err", err)
		return
	}
	for _, entry := range rows {
		if ctx.Err() != nil {
			return
		}
		s.attemptOne(ctx, entry, opts)
	}
}

// attemptOne tries to re-publish one DLQ entry. On success, clears
// next_retry_at. On failure, either schedules the next attempt with
// exponential backoff + jitter or — if the retry cap is exhausted —
// clears the schedule too (the row stays in the DLQ awaiting manual
// triage, which is the right end state for poison messages).
func (s *Service) attemptOne(ctx context.Context, entry *storage.DLQEntry, opts ReaperOptions) {
	tenant := entry.TenantID
	if tenant == "" {
		tenant = storage.DefaultTenantID
	}
	// Single Retry path. The error tells us whether to reschedule or
	// give up; success clears the schedule.
	err := s.Retry(ctx, tenant, entry.ID)
	if err == nil {
		// Clearing next_retry_at means the reaper won't pick this row
		// up again — operators see the success in retry_count +
		// last_retry_at.
		if cerr := s.store.DLQ.ScheduleRetry(ctx, tenant, entry.ID, nil); cerr != nil {
			s.logger.Warn("dlq reaper: clear schedule failed",
				"dlq_id", entry.ID, "err", cerr)
		}
		return
	}
	// ErrMaxRetries → out of budget. Clear the schedule so the row
	// stops cycling.
	if errors.Is(err, ErrMaxRetries) {
		s.logger.Warn("dlq reaper: max retries exhausted, dropping schedule",
			"dlq_id", entry.ID, "pipeline_id", entry.PipelineID,
			"retry_count", entry.RetryCount,
		)
		_ = s.store.DLQ.ScheduleRetry(ctx, tenant, entry.ID, nil)
		return
	}
	// Any other error → bump the schedule and try again later. Use the
	// pipeline's policy where available; service default otherwise.
	baseMs := 0
	if entry.PipelineID != "" {
		if pipe, perr := s.store.Pipelines.GetUnsafe(ctx, entry.PipelineID); perr == nil {
			baseMs = pipe.RetryBackoffMs
		}
	}
	delay := backoffDelay(entry.RetryCount, baseMs)
	if opts.MaxJitter > 0 {
		// Cheap PRNG — non-cryptographic randomness is fine here; the
		// jitter exists only to break herds of synchronously-failed
		// retries.
		delay += time.Duration(rand.Int63n(int64(opts.MaxJitter))) // #nosec G404
	}
	next := time.Now().UTC().Add(delay)
	if serr := s.store.DLQ.ScheduleRetry(ctx, tenant, entry.ID, &next); serr != nil {
		s.logger.Warn("dlq reaper: reschedule failed",
			"dlq_id", entry.ID, "err", serr)
	}
	s.logger.Info("dlq reaper: retry failed, rescheduled",
		"dlq_id", entry.ID,
		"err", err.Error(),
		"next_retry_at", next.Format(time.RFC3339),
		"retry_count", entry.RetryCount,
	)
}

// backoffDelay returns exponential backoff for the n-th retry given a
// base in milliseconds. base=0 falls back to 5000 (5s). Capped at 10
// minutes so a deeply-failed message doesn't sit idle for hours.
func backoffDelay(attempt, baseMs int) time.Duration {
	if baseMs <= 0 {
		baseMs = 5000
	}
	const capMs = int64(10 * 60 * 1000) // 10 minutes
	// 2^attempt — clamp the shift to avoid overflow on absurd retry
	// counts (max meaningful attempt < 30 anyway since cap kicks in
	// long before).
	if attempt > 30 {
		attempt = 30
	}
	ms := int64(baseMs) << uint(attempt)
	if ms > capMs || ms < 0 {
		ms = capMs
	}
	return time.Duration(ms) * time.Millisecond
}
