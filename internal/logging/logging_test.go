package logging

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestNew_JSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := NewWith(&buf, "info", "json")
	l.Info("hello", "key", "value")
	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("expected JSON output, got: %s", buf.String())
	}
}

func TestNew_TextFormat(t *testing.T) {
	var buf bytes.Buffer
	l := NewWith(&buf, "debug", "text")
	l.Debug("debug message")
	if !strings.Contains(buf.String(), "debug message") {
		t.Errorf("expected text output, got: %s", buf.String())
	}
}

func TestFromContext_FallbackToDefault(t *testing.T) {
	if FromContext(context.Background()) == nil {
		t.Error("FromContext should not return nil")
	}
}

func TestIntoContext_RoundTrip(t *testing.T) {
	var buf bytes.Buffer
	l := NewWith(&buf, "info", "json")
	ctx := IntoContext(context.Background(), l)
	if FromContext(ctx) != l {
		t.Error("FromContext did not return the injected logger")
	}
}
