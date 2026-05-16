package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"mqConnector/internal/logging"
	"mqConnector/internal/tracing"
)

// TraceContext middleware reads the W3C `traceparent` header from
// inbound requests and seeds the request context with a SpanContext.
// If the header is absent or malformed, a fresh root span is minted so
// every request has correlation ids. The chosen ids are echoed back on
// the response so clients can join their logs with ours.
//
// Sits early in the stack — before LogRequests, so the request access
// log carries the trace id from the very first line.
func TraceContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sc, ok := tracing.Parse(r.Header.Get("traceparent"))
		if !ok {
			sc = tracing.NewRoot()
		}
		w.Header().Set("traceparent", sc.String())
		next.ServeHTTP(w, r.WithContext(tracing.WithContext(r.Context(), sc)))
	})
}

type ctxKeyRequestID struct{}

// RequestID assigns each request a UUID, exposes it on the response header
// and via context, and attaches it as a log attribute.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the request ID, or "" if none.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID{}).(string)
	return id
}

// LogRequests wraps each request in an info-level log line with method, path,
// status, and duration. Trace ids from the TraceContext middleware are
// folded into the log line via slog's group attribute so an aggregator can
// join with downstream span logs.
func LogRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		logger := logging.FromContext(r.Context())
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
			"request_id", RequestIDFromContext(r.Context()),
		}
		attrs = append(attrs, tracing.LogAttrs(r.Context())...)
		logger.Info("http request", attrs...)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.NewResponseController(...) reach the underlying
// writer's Flush/SetWriteDeadline methods through our wrapper. Without
// this, long-lived SSE handlers can't drive the flusher because the
// type assertion fails on the recorder.
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// MaxBodyBytes returns middleware that caps the request body size.
func MaxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// CORS returns middleware that allows the given origins. Empty list disables.
func CORS(origins []string) func(http.Handler) http.Handler {
	allow := map[string]bool{}
	for _, o := range origins {
		allow[o] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && allow[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-Id")
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

type cspNonceKey struct{}

// CSPNonceFromContext returns the per-request CSP nonce the SecurityHeaders
// middleware attached, or "" if there isn't one (e.g. a request that didn't
// go through the middleware stack — only test paths should hit that).
func CSPNonceFromContext(ctx context.Context) string {
	if n, ok := ctx.Value(cspNonceKey{}).(string); ok {
		return n
	}
	return ""
}

// generateNonce produces 16 random bytes encoded as URL-safe base64. RFC 8888
// wants at least 128 bits of entropy; we use exactly that.
func generateNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Crypto RNG failure is fatal for a security primitive — fall back
		// to a fixed value would silently weaken CSP. Panic so the request
		// fails loudly (Recover middleware turns it into a 500).
		panic("csp nonce: crypto/rand.Read: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// SecurityHeaders adds a standard set of response headers. The CSP is
// deliberately strict: the embedded SvelteKit build produces a self-contained
// bundle, so no third-party origins are needed. If the deployment ever pulls
// fonts/scripts from a CDN, loosen this carefully — but the brand standard
// explicitly forbids CDN-loaded fonts, so the default is locked down.
//
// SvelteKit emits two inline <script> blocks in app.html (the FOUC theme
// reader and the hydration bootstrap). Rather than allow 'unsafe-inline' —
// which would defeat XSS protection — we mint a fresh nonce per request,
// put it in the request context for the static-file handler to inject into
// those <script> tags, and reference it from the CSP header.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := generateNonce()
		ctx := context.WithValue(r.Context(), cspNonceKey{}, nonce)

		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		h.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'nonce-"+nonce+"'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; "+
				"font-src 'self' data:; "+
				"connect-src 'self'; "+
				"object-src 'none'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'; "+
				"form-action 'self'")
		h.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestContextTimeout sets an upper bound on how long any single handler
// can run, regardless of the inner work. Without this, a stuck downstream
// (e.g. an unreachable MQ broker hanging a publish) could pin a goroutine
// past the server's Write timeout. Pair with WriteTimeout for defence in
// depth.
//
// Long-lived streaming endpoints (SSE today, websockets later) opt out:
// requests with Accept: text/event-stream bypass the timeout. The SSE
// handler additionally clears the per-connection write deadline via
// http.NewResponseController, so neither side of the timeout chain
// severs the stream.
func RequestContextTimeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if timeout <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isStreaming(r) {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), timeout)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isStreaming returns true for long-lived streaming requests that must not
// be cut off by the per-request timeout.
func isStreaming(r *http.Request) bool {
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		return true
	}
	// Path-based fallback for clients that don't set the Accept header
	// (curl, k6, etc.) — keeps testing/observability flexible.
	if strings.HasSuffix(r.URL.Path, "/events") {
		return true
	}
	return false
}
