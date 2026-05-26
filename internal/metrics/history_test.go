package metrics

import (
	"testing"
	"time"
)

func TestHistory_RetainsLastNSamples(t *testing.T) {
	s := New()
	s.Register("p1", "src", "dst")
	now := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	clock := now
	h := NewHistory(s).WithKeep(3).WithClock(func() time.Time { return clock })
	s.RecordFailure("p1")
	h.Sample()
	clock = clock.Add(30 * time.Second)
	s.RecordFailure("p1")
	h.Sample()
	clock = clock.Add(30 * time.Second)
	s.RecordFailure("p1")
	h.Sample()
	clock = clock.Add(30 * time.Second)
	s.RecordFailure("p1")
	h.Sample()
	if got := h.FrameCount(); got != 3 {
		t.Fatalf("FrameCount: got %d, want 3 (ring should trim)", got)
	}
}

func TestHistory_ValueAt(t *testing.T) {
	s := New()
	s.Register("p1", "src", "dst")
	start := time.Date(2026, 5, 26, 0, 0, 0, 0, time.UTC)
	clock := start
	h := NewHistory(s).WithKeep(10).WithClock(func() time.Time { return clock })

	// 5min ago: 10 failures
	for i := 0; i < 10; i++ {
		s.RecordFailure("p1")
	}
	h.Sample()
	// now: advance 5min, 50 more failures
	clock = clock.Add(5 * time.Minute)
	for i := 0; i < 50; i++ {
		s.RecordFailure("p1")
	}
	h.Sample()

	v, ok := h.ValueAt("mqconnector_messages_failed_total", "p1", 5*time.Minute)
	if !ok {
		t.Fatalf("ValueAt: ok=false, want true")
	}
	if v != 10 {
		t.Errorf("ValueAt 5min ago = %v, want 10", v)
	}
}

func TestHistory_ValueAtColdCache(t *testing.T) {
	s := New()
	h := NewHistory(s)
	if _, ok := h.ValueAt("x", "p1", time.Minute); ok {
		t.Error("ValueAt on empty ring should return ok=false")
	}
}
