package dlq

import (
	"context"
	"log/slog"
	"time"
)

// Retention runs periodic pruning so the DLQ table doesn't grow without
// bound during long broker outages. Two policies, applied each tick:
//
//   - Age:   delete rows older than MaxAge.
//   - Count: keep only the newest MaxRows entries.
//
// Either policy can be disabled by setting its value to zero. The goroutine
// exits when ctx is cancelled.
type Retention struct {
	store         storeForRetention
	maxAge        time.Duration
	maxRows       int
	sweepInterval time.Duration
	logger        *slog.Logger
}

// storeForRetention is the slice of *storage.Store the retention loop needs.
// Defining it as an interface keeps this file independent of the storage
// package and makes the loop trivially mockable.
type storeForRetention interface {
	PruneOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
	PruneToMaxRows(ctx context.Context, maxRows int) (int64, error)
}

// NewRetention builds a sweeper. Sensible defaults are applied for any
// zero-valued knob:
//   - sweepInterval: 10m
//   - maxAge:        0 (disabled)
//   - maxRows:       0 (disabled)
//
// A retention with both age and count disabled is a no-op; Run returns
// immediately.
func NewRetention(s storeForRetention, maxAge time.Duration, maxRows int, sweepInterval time.Duration, logger *slog.Logger) *Retention {
	if sweepInterval <= 0 {
		sweepInterval = 10 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Retention{
		store:         s,
		maxAge:        maxAge,
		maxRows:       maxRows,
		sweepInterval: sweepInterval,
		logger:        logger.With("component", "dlq.retention"),
	}
}

// Run loops until ctx is done. Each tick runs both pruners and emits a
// debug log line with the counts.
func (r *Retention) Run(ctx context.Context) {
	if r.maxAge <= 0 && r.maxRows <= 0 {
		r.logger.Debug("retention disabled (no maxAge or maxRows)")
		return
	}
	// Do one sweep right away so a freshly-booted process catches up.
	r.sweep(ctx)
	t := time.NewTicker(r.sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sweep(ctx)
		}
	}
}

func (r *Retention) sweep(ctx context.Context) {
	if r.maxAge > 0 {
		cutoff := time.Now().UTC().Add(-r.maxAge)
		if n, err := r.store.PruneOlderThan(ctx, cutoff); err != nil {
			r.logger.Warn("prune by age failed", "err", err)
		} else if n > 0 {
			r.logger.Info("pruned DLQ entries by age", "removed", n, "cutoff", cutoff)
		}
	}
	if r.maxRows > 0 {
		if n, err := r.store.PruneToMaxRows(ctx, r.maxRows); err != nil {
			r.logger.Warn("prune by count failed", "err", err)
		} else if n > 0 {
			r.logger.Info("pruned DLQ entries by count", "removed", n, "max_rows", r.maxRows)
		}
	}
}
