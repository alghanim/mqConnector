// OTLP/HTTP exporter wiring. The rest of the tracing package emits
// structured-log "spans" (one INFO line per Span.End()) which is enough
// for many deploys, but operators with a Jaeger / Tempo / Honeycomb /
// Grafana Cloud collector want real spans pushed over the wire. This
// file is the bridge: when SetOTelTracer is called with a non-nil
// tracer, the rest of the tracing package mirrors each span into the
// OpenTelemetry SDK.
//
// We use otlptracehttp (not the gRPC exporter) deliberately — the HTTP
// variant skips the otlptracegrpc + grpc-go transitive dependencies
// that aren't otherwise in the build graph. Any OTLP collector accepts
// both protocols on different ports (4317 gRPC, 4318 HTTP).

package tracing

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// otelTracer is the runtime hook. Stored as an atomic.Value so reads
// from hot paths (every Span.Start) are lock-free, and a one-time
// initialisation race during boot can't tear the pointer.
var otelTracer atomic.Pointer[oteltrace.Tracer]

// OTLPConfig is what main.go reads from the YAML config and hands to
// EnableOTLP. Zero-value means "tracing stays as structured logs only".
type OTLPConfig struct {
	// Endpoint is the OTLP/HTTP collector base URL. The exporter posts
	// to <Endpoint>/v1/traces. Empty disables OTLP entirely. Standard
	// collectors listen on :4318 for HTTP.
	Endpoint string

	// ServiceName lands as service.name on every exported span. Used by
	// most UIs to scope a view to one app.
	ServiceName string

	// Version lands as service.version. Populated from main.version.
	Version string

	// Insecure switches the exporter to plaintext HTTP. TLS by default.
	Insecure bool

	// SampleRatio is the head-based sample rate, 0 ≤ r ≤ 1. 1.0 records
	// every span; 0.0 records none (same effect as not configuring OTLP).
	// Defaults to 1.0 when EnableOTLP is called — tracing is opt-in via
	// the Endpoint anyway, so once it's on, sample-by-default-everything
	// matches operator expectations.
	SampleRatio float64
}

// EnableOTLP wires up the OTLP exporter and installs an OpenTelemetry
// TracerProvider as the global one. Returns a shutdown function that
// main.go should defer; flushes the in-memory queue before exit.
func EnableOTLP(ctx context.Context, cfg OTLPConfig) (shutdown func(context.Context) error, err error) {
	if cfg.Endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}

	opts := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(cfg.Endpoint),
	}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("otlp exporter: %w", err)
	}

	svc := cfg.ServiceName
	if svc == "" {
		svc = "mqconnector"
	}
	ver := cfg.Version
	if ver == "" {
		ver = "dev"
	}
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(svc),
			semconv.ServiceVersion(ver),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otlp resource: %w", err)
	}

	ratio := cfg.SampleRatio
	if ratio == 0 {
		ratio = 1.0
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
	)
	otel.SetTracerProvider(tp)

	tr := tp.Tracer("mqconnector")
	otelTracer.Store(&tr)

	return tp.Shutdown, nil
}

// otelStart opens an OTel span if a tracer is installed. Returns the
// span (nil if no tracer) and the ctx threaded with the OTel span
// context so downstream code that grabs trace IDs via the standard
// OTel API works seamlessly.
func otelStart(ctx context.Context, name string) (context.Context, oteltrace.Span) {
	trPtr := otelTracer.Load()
	if trPtr == nil {
		return ctx, nil
	}
	return (*trPtr).Start(ctx, name)
}

// otelSetAttr forwards a single attr from the structured-log span into
// the OTel span. Type-narrowed to the four shapes mqConnector emits in
// practice — strings, int64s, float64s, bools. Anything else falls
// back to fmt.Sprint.
func otelSetAttr(span oteltrace.Span, key string, value any) {
	if span == nil {
		return
	}
	switch v := value.(type) {
	case string:
		span.SetAttributes(attribute.String(key, v))
	case int:
		span.SetAttributes(attribute.Int(key, v))
	case int64:
		span.SetAttributes(attribute.Int64(key, v))
	case float64:
		span.SetAttributes(attribute.Float64(key, v))
	case bool:
		span.SetAttributes(attribute.Bool(key, v))
	default:
		span.SetAttributes(attribute.String(key, fmt.Sprint(v)))
	}
}

// otelRef implements otelSpanRef so tracing.go can hold an OTel span
// without importing the otel package itself. nil-tolerant on both
// methods so the caller doesn't need to branch.
type otelRef struct{ s oteltrace.Span }

func (r otelRef) end() { r.s.End() }
func (r otelRef) setAttr(k string, v any) {
	otelSetAttr(r.s, k, v)
}

// wrapOTel turns an oteltrace.Span into the interface tracing.Span
// holds. Returns nil if span is nil, which the consumer treats as
// "OTel not enabled, do nothing on End/SetAttr".
func wrapOTel(span oteltrace.Span) otelSpanRef {
	if span == nil {
		return nil
	}
	return otelRef{s: span}
}
