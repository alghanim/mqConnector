// Package webhooks delivers internal events to operator-registered
// outbound HTTP endpoints, signed with HMAC-SHA256 against each
// webhook's secret.
//
// Reliability posture
//
// The dispatcher is best-effort: at-most-once delivery with three
// attempts and a short exponential backoff. We deliberately don't
// queue undelivered events to disk — the audit log is the durable
// "what happened" record; webhooks are a notification channel. If a
// receiver is down longer than the retry window, the operator sees
// last_status / last_error on the webhook row and can replay manually.
//
// Concurrency
//
// One goroutine per dispatcher. The goroutine reads events from the
// publisher's channel, looks up matching webhooks for the event's
// tenant, and fires deliveries in parallel (one goroutine per
// webhook + event combination — bounded by the bursts the source
// emits, not by any internal worker pool).
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"mqConnector/internal/events"
	"mqConnector/internal/storage"
)

// Store is the slice of storage.WebhookRepo the dispatcher needs.
// Extracted so tests can stub.
type Store interface {
	ListAll(ctx context.Context) ([]*storage.Webhook, error)
	RecordAttempt(ctx context.Context, id string, status int, errText string) error
}

// Dispatcher receives events and POSTs them to matching webhooks.
type Dispatcher struct {
	store  Store
	pub    *events.Publisher
	client *http.Client
	logger *slog.Logger

	maxRetries int
	timeout    time.Duration

	// Subscription handle. Subscribe is performed in New() (not in
	// Run()) so the subscription is established synchronously and a
	// caller that publishes immediately after New is guaranteed to
	// hit a registered subscriber.
	events <-chan events.Event
	unsub  func()

	stopMu  sync.Mutex
	stopped bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// Options configures the dispatcher. Sensible defaults are applied for
// zero-valued fields so callers can pass `Options{}`.
type Options struct {
	MaxRetries  int           // default 3
	HTTPTimeout time.Duration // default 5s
	BufSize     int           // unused — kept for forward-compat
}

// New builds a Dispatcher and immediately subscribes to the publisher.
// Run() must be called to start consuming. Events that arrive before
// Run() are buffered in the subscriber channel up to the publisher's
// buffer size.
func New(store Store, pub *events.Publisher, opts Options, logger *slog.Logger) *Dispatcher {
	if opts.MaxRetries == 0 {
		opts.MaxRetries = 3
	}
	if opts.HTTPTimeout == 0 {
		opts.HTTPTimeout = 5 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	ch, unsub := pub.Subscribe()
	return &Dispatcher{
		store:      store,
		pub:        pub,
		client:     &http.Client{Timeout: opts.HTTPTimeout},
		logger:     logger.With("component", "webhooks"),
		maxRetries: opts.MaxRetries,
		timeout:    opts.HTTPTimeout,
		events:     ch,
		unsub:      unsub,
		done:       make(chan struct{}),
	}
}

// Run consumes events from the subscription installed in New() until
// Stop is called or ctx is cancelled. Blocks; call as a goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	defer d.unsub()
	defer close(d.done)

	runCtx, cancel := context.WithCancel(ctx)
	d.stopMu.Lock()
	d.cancel = cancel
	d.stopMu.Unlock()
	defer cancel()

	d.logger.Info("dispatcher started")
	defer d.logger.Info("dispatcher stopped")

	for {
		select {
		case <-runCtx.Done():
			return
		case e, ok := <-d.events:
			if !ok {
				return
			}
			d.fanOut(runCtx, e)
		}
	}
}

// Stop signals Run to exit. Safe to call multiple times.
func (d *Dispatcher) Stop() {
	d.stopMu.Lock()
	defer d.stopMu.Unlock()
	if d.stopped {
		return
	}
	d.stopped = true
	if d.cancel != nil {
		d.cancel()
	}
}

// Done blocks until Run has actually exited. Used by tests; production
// startup relies on context cancellation propagation.
func (d *Dispatcher) Done() <-chan struct{} { return d.done }

// fanOut looks up matching webhooks for the event's tenant and fires
// one delivery goroutine per match.
func (d *Dispatcher) fanOut(ctx context.Context, e events.Event) {
	hooks, err := d.store.ListAll(ctx)
	if err != nil {
		d.logger.Warn("list webhooks failed", "err", err)
		return
	}
	for _, h := range hooks {
		if h.TenantID != e.TenantID {
			continue
		}
		if !h.Matches(e.Type) {
			continue
		}
		go d.deliver(ctx, h, e)
	}
}

// deliver POSTs the event to one webhook, retrying with exponential
// backoff (200ms, 1s, 5s) up to maxRetries on 5xx or network error.
// Records the final attempt's status + error to the webhook row.
func (d *Dispatcher) deliver(ctx context.Context, h *storage.Webhook, e events.Event) {
	body, err := json.Marshal(e)
	if err != nil {
		d.logger.Warn("marshal event failed", "err", err, "webhook_id", h.ID)
		return
	}
	sig := sign(h.Secret, body)

	var lastStatus int
	var lastErrText string
	backoff := []time.Duration{0, 200 * time.Millisecond, 1 * time.Second, 5 * time.Second}
	for attempt := 0; attempt < d.maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff[min(attempt, len(backoff)-1)]):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
		if err != nil {
			lastErrText = err.Error()
			break // unrecoverable
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-MQC-Event", e.Type)
		req.Header.Set("X-MQC-Signature", "sha256="+sig)
		req.Header.Set("User-Agent", "mqConnector-webhooks/1.0")

		resp, err := d.client.Do(req)
		if err != nil {
			lastStatus = 0
			lastErrText = err.Error()
			continue
		}
		// Read + discard body so the connection can be reused.
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		lastStatus = resp.StatusCode
		if lastStatus >= 200 && lastStatus < 300 {
			lastErrText = ""
			break
		}
		lastErrText = fmt.Sprintf("HTTP %d", lastStatus)
		// 4xx is the receiver telling us we're wrong — no point retrying.
		if lastStatus >= 400 && lastStatus < 500 {
			break
		}
	}

	if err := d.store.RecordAttempt(ctx, h.ID, lastStatus, lastErrText); err != nil {
		d.logger.Debug("record webhook attempt failed", "err", err, "webhook_id", h.ID)
	}
}

// sign returns the HMAC-SHA256 hex digest of payload under secret.
// Receivers verify by recomputing and constant-time comparing against
// the X-MQC-Signature header (sha256=...).
func sign(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
