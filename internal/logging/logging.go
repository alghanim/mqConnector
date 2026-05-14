// Package logging configures a structured slog logger and provides context
// helpers so handlers can carry a request-scoped logger through the call stack.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

type ctxKey struct{}

// New constructs a slog.Logger from a level + format string. Level must be one
// of debug|info|warn|error. Format must be text or json. Validation is the
// caller's responsibility — this only switches on known values and falls back
// to JSON+info otherwise.
func New(level, format string) *slog.Logger {
	return NewWith(os.Stdout, level, format)
}

// NewWith is like New but writes to the given io.Writer. Useful for tests.
func NewWith(w io.Writer, level, format string) *slog.Logger {
	lvl := parseLevel(level)
	opts := &slog.HandlerOptions{Level: lvl}

	var h slog.Handler
	switch strings.ToLower(format) {
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// IntoContext returns a child context carrying the given logger.
func IntoContext(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, logger)
}

// FromContext returns the logger attached to ctx, or slog.Default() if none.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}
