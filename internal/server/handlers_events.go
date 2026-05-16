package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"mqConnector/internal/auth"
	"mqConnector/internal/logging"
	"mqConnector/internal/storage"
)

// handleEvents streams live operations data to authenticated browsers over
// Server-Sent Events. Replaces the dashboard's 5 s poll for metrics and the
// 30 s poll for the DLQ count badge.
//
// Stream contract
//
//	event: hello       — one-shot, sent right after the headers
//	  data: {"interval_ms":2000,"heartbeat_ms":15000}
//
//	event: metrics     — periodic
//	  data: {"uptime":"…","pipelines":{…},"active":N}
//
//	event: dlq_total   — periodic, only when value changes
//	  data: {"total":N}
//
//	event: health      — periodic, only when status changes
//	  data: {"status":"…","active_pipelines":N}
//
// SSE comments (lines starting with `:`) act as keep-alives so a strict
// reverse proxy doesn't tear the idle connection down between message
// bursts. The handler clears the server-wide WriteTimeout via
// http.NewResponseController so long-lived streams aren't killed at 30 s.
//
// Cadence: a single ticker fires every `interval`. On each tick we send a
// fresh metrics frame, then check whether the dlq total / health status
// changed since the last snapshot and only emit those events when they
// did. That keeps the wire quiet during steady state while still pushing
// every change inside one tick of arrival.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	// ResponseController unwraps middleware-wrapped writers (statusRecorder)
	// so it can find the underlying flusher + deadline setter. The type
	// assertion `w.(http.Flusher)` would otherwise fail because the audit +
	// log-request middlewares wrap the writer.
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil && err != http.ErrNotSupported {
		// First flush is also a probe — if the runtime can't flush at
		// all, fall back to a normal error.
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Clear the server's WriteTimeout for this connection — SSE streams
	// are long-lived. SetWriteDeadline with the zero value disables it.
	_ = rc.SetWriteDeadline(time.Time{})

	// Cadence — `interval=` query param lets the client request a slower
	// stream on a constrained link. Clamp to 500 ms .. 30 s so a hostile
	// client can't ask for a busy-loop or an effectively-dead stream.
	interval := 2 * time.Second
	if v := r.URL.Query().Get("interval"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			d := time.Duration(n) * time.Millisecond
			if d >= 500*time.Millisecond && d <= 30*time.Second {
				interval = d
			}
		}
	}
	const heartbeat = 15 * time.Second

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering
	w.WriteHeader(http.StatusOK)

	logger := logging.FromContext(r.Context())
	logger.Debug("sse client connected",
		"interval_ms", interval.Milliseconds(),
		"remote", r.RemoteAddr,
	)

	send := func(event string, payload any) error {
		buf, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, buf)
		if err != nil {
			return err
		}
		_ = rc.Flush()
		return nil
	}
	comment := func(text string) error {
		if _, err := fmt.Fprintf(w, ": %s\n\n", text); err != nil {
			return err
		}
		_ = rc.Flush()
		return nil
	}

	// Initial frame — gives the client the negotiated cadence and lets
	// EventSource confirm the connection without waiting for the first
	// data tick.
	if err := send("hello", map[string]any{
		"interval_ms":  interval.Milliseconds(),
		"heartbeat_ms": heartbeat.Milliseconds(),
		"server_time":  time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return
	}

	// State diff trackers. Only emit dlq_total / health when they
	// actually change — keeps the stream quiet during steady state.
	var (
		lastDLQ      int64 = -1
		lastStatus         = ""
		lastActive   int   = -1
	)

	// Push an immediate snapshot so the dashboard renders without
	// waiting for the first tick.
	pushOnce := func() error {
		if err := s.pushMetrics(send); err != nil {
			return err
		}
		if total, ok := s.pushDLQ(r.Context(), send, lastDLQ); ok {
			lastDLQ = total
		}
		if status, active, ok := s.pushHealth(r.Context(), send, lastStatus, lastActive); ok {
			lastStatus = status
			lastActive = active
		}
		return nil
	}
	if err := pushOnce(); err != nil {
		return
	}

	tick := time.NewTicker(interval)
	hb := time.NewTicker(heartbeat)
	defer tick.Stop()
	defer hb.Stop()

	for {
		select {
		case <-r.Context().Done():
			logger.Debug("sse client disconnected", "reason", r.Context().Err())
			return
		case <-hb.C:
			if err := comment("hb"); err != nil {
				return
			}
		case <-tick.C:
			if err := s.pushMetrics(send); err != nil {
				return
			}
			if total, ok := s.pushDLQ(r.Context(), send, lastDLQ); ok {
				lastDLQ = total
			}
			if status, active, ok := s.pushHealth(r.Context(), send, lastStatus, lastActive); ok {
				lastStatus = status
				lastActive = active
			}
		}
	}
}

// pushMetrics emits the current per-pipeline metrics snapshot. Always
// fires — the dashboard's sparkline buffer depends on a regular sample.
func (s *Server) pushMetrics(send func(string, any) error) error {
	snap := s.metrics.Snapshot()
	return send("metrics", map[string]any{
		"uptime":    s.metrics.Uptime().String(),
		"pipelines": snap,
		"active":    s.metrics.ActiveCount(),
	})
}

// pushDLQ counts the current operator's tenant-scoped DLQ entries and
// emits the total when it differs from `last`. Reuses the request context
// so tenant scoping is honoured by ListFiltered.
func (s *Server) pushDLQ(ctx context.Context, send func(string, any) error, last int64) (int64, bool) {
	tenant := auth.TenantID(ctx)
	// per_page=1 page=1 — we only need the total. Use an empty filter so
	// the badge mirrors the unfiltered count on /dlq.
	_, total, err := s.dlq.ListFiltered(ctx, tenant, storage.DLQFilter{}, 1, 1)
	if err != nil {
		return last, false
	}
	if int64(total) == last {
		return last, false
	}
	if err := send("dlq_total", map[string]any{"total": total}); err != nil {
		return last, false
	}
	return int64(total), true
}

// pushHealth emits a coarse health snapshot (status + active count) when
// either changes. The dashboard's pulse strip is the main consumer.
func (s *Server) pushHealth(ctx context.Context, send func(string, any) error, lastStatus string, lastActive int) (string, int, bool) {
	status := s.health.Check(ctx)
	if status.Status == lastStatus && status.Active == lastActive {
		return lastStatus, lastActive, false
	}
	if err := send("health", map[string]any{
		"status":           status.Status,
		"active_pipelines": status.Active,
		"version":          status.Version,
		"uptime":           status.Uptime,
		"connections":      status.Connections,
	}); err != nil {
		return lastStatus, lastActive, false
	}
	return status.Status, status.Active, true
}
