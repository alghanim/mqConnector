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

// Sealer is the minimal surface dlq uses to encrypt the pre-redaction
// payload (envelope encryption under the configured master key). The
// concrete implementation lives in internal/secrets; a nil Sealer
// disables redaction — the raw form is too sensitive to leave at rest
// in plaintext, so we refuse to store it when no master key is
// configured. Push falls back to the legacy "no redaction" path in
// that case.
type Sealer interface {
	Encrypt(plaintext string) (string, error)
	Decrypt(ciphertext string) (string, error)
	Enabled() bool
}

// Service is the DLQ controller. It owns no goroutines; callers drive it.
type Service struct {
	store      *storage.Store
	pool       *mq.Pool
	maxRetries int
	logger     *slog.Logger
	redactor   *Redactor

	sinkMu sync.RWMutex
	sink   EventSink

	sealerMu sync.RWMutex
	sealer   Sealer
}

// SetSealer installs the envelope-encryption service used to seal the
// pre-redaction payload before it lands in raw_msg. Push falls back to
// the legacy non-redacting path when the sealer is nil or disabled —
// the contract is "only store raw if you can store it encrypted."
func (s *Service) SetSealer(sealer Sealer) {
	s.sealerMu.Lock()
	defer s.sealerMu.Unlock()
	s.sealer = sealer
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
		redactor:   NewRedactor(),
	}
}

// MaxRetries returns the configured retry cap.
func (s *Service) MaxRetries() int { return s.maxRetries }

// Push satisfies pipeline.DLQSink — used by Executor to drop failed
// messages. The entry already carries its tenant id (set by the executor
// from the pipeline's tenant), so we pass it straight through.
//
// Redaction (Phase 19): before the row is persisted, Push consults the
// pipeline's dlq_redaction_rules. If any rule matches, the
// pre-redaction bytes are sealed under the master key and stored in
// raw_msg; OriginalMsg is overwritten with the redacted form; the
// Redacted flag is set so the operator UI knows to display a
// redaction-applied affordance and (when permitted) a "show raw"
// action. When the sealer isn't configured we refuse to store the raw
// — Push silently skips redaction, leaving the entry untouched, so
// the operator never ends up with a partially-protected DLQ.
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
	// Redaction lookup is scoped to the pipeline. The rules table
	// owns the (pipeline_id, tenant_id) pair so we can use the
	// unsafe-by-pipeline-id path; the executor's binding is already
	// trusted here.
	if entry.PipelineID != "" && s.store.DLQRedaction != nil {
		rules, err := s.store.DLQRedaction.ListForPipelineUnsafe(ctx, entry.PipelineID)
		if err != nil {
			s.logger.Warn("dlq redaction rule lookup failed",
				"pipeline_id", entry.PipelineID, "err", err)
		} else if len(rules) > 0 {
			s.applyRedaction(&entry, rules)
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

// applyRedaction runs the configured rules against entry.OriginalMsg.
// If any rule mutates the payload AND the sealer is configured, the
// pre-redaction bytes are sealed and stashed in entry.RawMsg, the
// payload is overwritten with the redacted form, and Redacted is set.
// If the sealer is unavailable we deliberately leave the entry alone:
// redacting without preserving the raw would silently destroy the
// original payload (no replay possible), while storing the raw in
// cleartext defeats the compliance goal. The operator is expected to
// configure MQC_MASTER_KEY before turning rules on; if they didn't,
// the rules become a no-op and we log a warning so the misconfiguration
// surfaces.
func (s *Service) applyRedaction(entry *storage.DLQEntry, rules []storage.DLQRedactionRule) {
	if s.redactor == nil || entry == nil || len(entry.OriginalMsg) == 0 {
		return
	}
	s.sealerMu.RLock()
	sealer := s.sealer
	s.sealerMu.RUnlock()
	if sealer == nil || !sealer.Enabled() {
		s.logger.Warn("dlq redaction rules configured but no master key — skipping",
			"pipeline_id", entry.PipelineID)
		return
	}
	redacted, mutated := s.redactor.Apply(entry.OriginalMsg, rules)
	if !mutated {
		return
	}
	sealed, err := sealer.Encrypt(string(entry.OriginalMsg))
	if err != nil {
		s.logger.Error("dlq redaction seal failed — skipping to avoid plaintext raw",
			"pipeline_id", entry.PipelineID, "err", err)
		return
	}
	entry.RawMsg = []byte(sealed)
	entry.OriginalMsg = redacted
	entry.Redacted = true
}

// ErrMaxRetries is returned by Retry when the entry has hit the retry cap.
var ErrMaxRetries = errors.New("dlq: max retries exceeded")

// ErrRawNotAvailable indicates a request for the raw payload of a DLQ
// row that wasn't redacted — there is no separate raw form, the
// caller should display OriginalMsg directly.
var ErrRawNotAvailable = errors.New("dlq: raw payload not stored (entry was not redacted)")

// ErrSealerUnavailable is returned by GetRaw when the row carries a
// sealed raw payload but the service has no sealer configured to
// decrypt it. The almost-certain cause is a config change that
// disabled the master key after redacted rows were written.
var ErrSealerUnavailable = errors.New("dlq: sealer not configured; cannot decrypt raw payload")

// GetRaw returns the unredacted payload of one DLQ row. Tenant-scoped
// like the rest of the public surface. Returns ErrRawNotAvailable when
// the row was not redacted in the first place — callers should fall
// back to OriginalMsg from List/Get.
//
// The decrypt is the operator-trust boundary: every successful call
// MUST be paired with an audit-log entry by the HTTP handler. This
// function intentionally takes no logging-side parameters; the
// handler-side audit middleware already captures actor + remote IP +
// request id, and we don't want to double-log from here.
func (s *Service) GetRaw(ctx context.Context, tenantID, id string) ([]byte, error) {
	entry, err := s.store.DLQ.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if !entry.Redacted || len(entry.RawMsg) == 0 {
		return nil, ErrRawNotAvailable
	}
	s.sealerMu.RLock()
	sealer := s.sealer
	s.sealerMu.RUnlock()
	if sealer == nil || !sealer.Enabled() {
		return nil, ErrSealerUnavailable
	}
	plain, err := sealer.Decrypt(string(entry.RawMsg))
	if err != nil {
		return nil, fmt.Errorf("dlq: decrypt raw: %w", err)
	}
	return []byte(plain), nil
}

// Retry re-publishes the message to the pipeline's original destination.
// Tenant-scoped — callers (HTTP layer) must pass the active tenant.
// Returns ErrNotFound when the id doesn't exist OR exists in a different
// tenant (deliberately indistinguishable — no information leak).
func (s *Service) Retry(ctx context.Context, tenantID, id string) error {
	entry, err := s.store.DLQ.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}
	// Service-wide cap check first — fast-fails the obvious case
	// without a pipeline lookup. The per-pipeline cap (which can be
	// higher than the service default) is enforced after the pipeline
	// is loaded below.
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

	// Per-pipeline retry_max overrides the service default upward only
	// (a pipeline's policy can extend the cap, not loosen it below the
	// service-wide minimum). The service-wide check above already
	// short-circuited any entry with RetryCount >= s.maxRetries, so
	// here we only need to widen the cap when pipe.RetryMax allows it.
	limit := s.maxRetries
	if pipe.RetryMax > limit {
		limit = pipe.RetryMax
	}
	if entry.RetryCount >= limit {
		return ErrMaxRetries
	}

	// Count the attempt up-front so failures during pool.Get / SendMessage
	// still consume one of the retry-budget slots. The reaper otherwise
	// loops forever against a permanently-broken destination (caught
	// during the live deploy test). Errors on increment are non-fatal —
	// log and keep trying the send.
	if err := s.store.DLQ.IncrementRetry(ctx, tenantID, id); err != nil {
		s.logger.Warn("dlq increment failed", "id", id, "err", err)
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

	// When the row was redacted, OriginalMsg holds the [REDACTED]-
	// substituted form. Re-publishing that to the destination would
	// corrupt the downstream consumer; replay must use the sealed
	// raw payload. Falls back to OriginalMsg for legacy rows and
	// any pipeline without redaction rules.
	payload := entry.OriginalMsg
	if entry.Redacted && len(entry.RawMsg) > 0 {
		s.sealerMu.RLock()
		sealer := s.sealer
		s.sealerMu.RUnlock()
		if sealer == nil || !sealer.Enabled() {
			return ErrSealerUnavailable
		}
		plain, err := sealer.Decrypt(string(entry.RawMsg))
		if err != nil {
			return fmt.Errorf("dlq retry: decrypt raw: %w", err)
		}
		payload = []byte(plain)
	}
	if err := conn.SendMessage(ctx, payload); err != nil {
		return fmt.Errorf("dlq retry: send: %w", err)
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
