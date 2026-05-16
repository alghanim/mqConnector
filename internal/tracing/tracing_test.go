package tracing

import (
	"context"
	"strings"
	"testing"
)

func TestParse_Valid(t *testing.T) {
	got, ok := Parse("00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if got.TraceID != "0123456789abcdef0123456789abcdef" {
		t.Errorf("trace_id: %q", got.TraceID)
	}
	if got.SpanID != "0123456789abcdef" {
		t.Errorf("span_id: %q", got.SpanID)
	}
	if !got.Sampled {
		t.Error("expected sampled=true")
	}
}

func TestParse_Invalid(t *testing.T) {
	cases := []string{
		"",
		"too-short",
		"01-aa-bb-00", // wrong version
		"00-tooshort-tooshort-00",
	}
	for _, in := range cases {
		if _, ok := Parse(in); ok {
			t.Errorf("expected %q to fail parse", in)
		}
	}
}

func TestNewRoot_GeneratesValidIDs(t *testing.T) {
	sc := NewRoot()
	if !sc.Valid() {
		t.Fatalf("root must be valid: %+v", sc)
	}
	// Two roots should differ — random ids, not fixtures.
	other := NewRoot()
	if sc.TraceID == other.TraceID || sc.SpanID == other.SpanID {
		t.Error("two roots should not collide on either id")
	}
}

func TestChild_SharesTraceIDFreshSpan(t *testing.T) {
	parent := NewRoot()
	child := parent.Child()
	if child.TraceID != parent.TraceID {
		t.Error("child must inherit trace_id")
	}
	if child.SpanID == parent.SpanID {
		t.Error("child must have its own span_id")
	}
}

func TestRoundTripThroughContext(t *testing.T) {
	parent := NewRoot()
	ctx := WithContext(context.Background(), parent)
	got, ok := FromContext(ctx)
	if !ok || got != parent {
		t.Fatalf("round-trip lost data: got %+v", got)
	}
}

// The traceparent serialiser/parser pair must round-trip exactly so a
// downstream client sees the same value we'd accept from them.
func TestStringRoundTrip(t *testing.T) {
	sc := NewRoot()
	parsed, ok := Parse(sc.String())
	if !ok || parsed != sc {
		t.Fatalf("round-trip mismatch: in=%+v out=%+v", sc, parsed)
	}
	if !strings.HasPrefix(sc.String(), "00-") {
		t.Errorf("traceparent must use version 00: %q", sc.String())
	}
}
