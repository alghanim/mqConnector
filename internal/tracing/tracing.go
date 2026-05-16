// Package tracing produces W3C trace-context headers (traceparent) and
// propagates trace + span ids through the request context. It does NOT
// pull in the OpenTelemetry SDK — we emit trace data as fields on
// existing slog log lines, which is everything an aggregator (Loki,
// Splunk, Elastic) needs to correlate requests across the HTTP layer
// and the pipeline executor.
//
// Migrating to a full OTLP exporter is a follow-up: swap NewSpan's
// implementation for `tracer.Start(ctx, name)` and keep the public
// surface the same.
//
// Wire format: W3C Trace Context v1
//
//	traceparent: 00-{trace-id-32hex}-{span-id-16hex}-{flags-2hex}
//
// flags=01 means "sampled". We always set 01 today — sampling is a
// follow-up. The headers are read on inbound requests; if absent, a
// fresh trace-id is minted so every request has correlation data.
package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type ctxKey struct{}

// SpanContext carries the immutable trace-id and the current span-id.
// New child spans get a fresh span-id while keeping the trace-id.
type SpanContext struct {
	TraceID string // 32 hex chars
	SpanID  string // 16 hex chars
	Sampled bool
}

// Valid reports whether both ids look well-formed. Empty or wrong-length
// values count as invalid and the middleware mints a fresh context.
func (s SpanContext) Valid() bool {
	return len(s.TraceID) == 32 && len(s.SpanID) == 16
}

// String returns the traceparent header value for this context.
func (s SpanContext) String() string {
	flags := "00"
	if s.Sampled {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", s.TraceID, s.SpanID, flags)
}

// LogAttrs returns the slog group every log line should embed so an
// aggregator can join across services. Use:
//
//	logger.With(tracing.LogAttrs(ctx)...).Info("...")
func LogAttrs(ctx context.Context) []any {
	sc, ok := FromContext(ctx)
	if !ok || !sc.Valid() {
		return nil
	}
	return []any{
		slog.Group("trace",
			slog.String("trace_id", sc.TraceID),
			slog.String("span_id", sc.SpanID),
		),
	}
}

// Parse extracts the SpanContext from a traceparent header value.
// Returns (zero, false) if the header is missing or malformed.
func Parse(traceparent string) (SpanContext, bool) {
	parts := strings.Split(strings.TrimSpace(traceparent), "-")
	if len(parts) != 4 {
		return SpanContext{}, false
	}
	if parts[0] != "00" {
		// Only v1 of the spec; treat unknown versions as missing.
		return SpanContext{}, false
	}
	sc := SpanContext{
		TraceID: parts[1],
		SpanID:  parts[2],
		Sampled: parts[3] != "" && parts[3][len(parts[3])-1] == '1',
	}
	if !sc.Valid() {
		return SpanContext{}, false
	}
	return sc, true
}

// NewRoot mints a fresh trace + span id. Used when a request arrives
// without a traceparent header.
func NewRoot() SpanContext {
	return SpanContext{
		TraceID: randomHex(16), // 128 bits
		SpanID:  randomHex(8),  // 64 bits
		Sampled: true,
	}
}

// Child returns a new SpanContext under the same trace but with a fresh
// span id. Use to demarcate sub-operations within a request.
func (s SpanContext) Child() SpanContext {
	return SpanContext{
		TraceID: s.TraceID,
		SpanID:  randomHex(8),
		Sampled: s.Sampled,
	}
}

// WithContext stores the SpanContext in ctx.
func WithContext(ctx context.Context, sc SpanContext) context.Context {
	return context.WithValue(ctx, ctxKey{}, sc)
}

// FromContext extracts the SpanContext from ctx, if any.
func FromContext(ctx context.Context) (SpanContext, bool) {
	if ctx == nil {
		return SpanContext{}, false
	}
	sc, ok := ctx.Value(ctxKey{}).(SpanContext)
	return sc, ok
}

// Span is a tiny stand-in for an OTel span. End() emits a structured
// log line so an aggregator joins the span data with the rest of the
// request's logs without a separate OTLP pipeline.
type Span struct {
	ctx    context.Context
	logger *slog.Logger
	name   string
	start  time.Time
	attrs  []slog.Attr
}

// Start opens a child span on the SpanContext in ctx. If ctx has no
// SpanContext, a root is created.
func Start(ctx context.Context, logger *slog.Logger, name string) (context.Context, *Span) {
	parent, ok := FromContext(ctx)
	var sc SpanContext
	if ok && parent.Valid() {
		sc = parent.Child()
	} else {
		sc = NewRoot()
	}
	ctx = WithContext(ctx, sc)
	return ctx, &Span{
		ctx:    ctx,
		logger: logger,
		name:   name,
		start:  time.Now(),
	}
}

// SetAttr adds a key/value attribute that will be emitted on End().
// Cheap — no formatting work happens until End fires.
func (s *Span) SetAttr(key string, value any) {
	if s == nil {
		return
	}
	s.attrs = append(s.attrs, slog.Any(key, value))
}

// End closes the span and emits an INFO-level log line with name,
// duration, and any attributes. Pass an error to record a failed span;
// nil means success.
func (s *Span) End(err error) {
	if s == nil || s.logger == nil {
		return
	}
	d := time.Since(s.start)
	attrs := append([]any{}, LogAttrs(s.ctx)...)
	attrs = append(attrs,
		"span", s.name,
		"duration_ms", d.Milliseconds(),
	)
	for _, a := range s.attrs {
		attrs = append(attrs, a)
	}
	if err != nil {
		attrs = append(attrs, "status", "error", "err", err.Error())
		s.logger.Warn("span", attrs...)
		return
	}
	attrs = append(attrs, "status", "ok")
	s.logger.Info("span", attrs...)
}

// randomHex returns 2*n hex characters from crypto/rand. Panics on RNG
// failure — same posture as internal/server's nonce code.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("tracing: random read: " + err.Error())
	}
	return hex.EncodeToString(b)
}
