package ai

import (
	"sort"
	"strings"
	"sync"
)

// CallCounter is the Prometheus-shaped counter wrapper for
// mqconnector_ai_calls_total. Labels: feature, model, outcome. Thread-
// safe via an internal mutex; the snapshot is rendered into Prometheus
// text exposition format by the metrics handler.
//
// One global instance lives in cmd/mqconnector and is shared by every
// provider. The wrapper is intentionally trivial — we don't pull in
// prometheus/client_golang (no-new-deps policy); the renderer is hand-
// rolled and lives in this package so the labels stay consistent.
type CallCounter struct {
	mu     sync.Mutex
	counts map[callKey]int
}

type callKey struct {
	feature string
	model   string
	outcome string
}

// NewCallCounter returns a fresh counter with no series. Cheap — the
// map is lazy-allocated on first Inc.
func NewCallCounter() *CallCounter { return &CallCounter{} }

// Inc bumps the counter for (feature, model, outcome). Outcome is one
// of: "ok" | "timeout" | "error" | "rejected".
func (c *CallCounter) Inc(feature Capability, model, outcome string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.counts == nil {
		c.counts = make(map[callKey]int, 8)
	}
	k := callKey{feature: string(feature), model: model, outcome: outcome}
	c.counts[k]++
}

// Snapshot returns a copy of the current counter state keyed by
// "feature|model|outcome". Useful in tests; the Prometheus renderer
// uses snapshotKeyed below.
func (c *CallCounter) Snapshot() map[string]int {
	if c == nil {
		return map[string]int{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int, len(c.counts))
	for k, v := range c.counts {
		out[k.feature+"|"+k.model+"|"+k.outcome] = v
	}
	return out
}

// Prometheus renders the counter in the Prometheus text exposition
// format. Series are emitted in deterministic order (sorted by label
// tuple) so a scrape sees the same byte sequence between calls when
// nothing has changed — easier to diff for debugging.
//
// The HELP + TYPE lines are emitted unconditionally so the metric is
// always discoverable even when no calls have been made yet.
func (c *CallCounter) Prometheus() string {
	var b strings.Builder
	b.WriteString("# HELP mqconnector_ai_calls_total AI provider calls by feature, model, and outcome\n")
	b.WriteString("# TYPE mqconnector_ai_calls_total counter\n")
	if c == nil {
		return b.String()
	}
	c.mu.Lock()
	keys := make([]callKey, 0, len(c.counts))
	for k := range c.counts {
		keys = append(keys, k)
	}
	counts := make(map[callKey]int, len(c.counts))
	for k, v := range c.counts {
		counts[k] = v
	}
	c.mu.Unlock()

	sort.Slice(keys, func(i, j int) bool {
		if keys[i].feature != keys[j].feature {
			return keys[i].feature < keys[j].feature
		}
		if keys[i].model != keys[j].model {
			return keys[i].model < keys[j].model
		}
		return keys[i].outcome < keys[j].outcome
	})
	for _, k := range keys {
		b.WriteString("mqconnector_ai_calls_total{feature=\"")
		b.WriteString(escapeLabel(k.feature))
		b.WriteString("\",model=\"")
		b.WriteString(escapeLabel(k.model))
		b.WriteString("\",outcome=\"")
		b.WriteString(escapeLabel(k.outcome))
		b.WriteString("\"} ")
		// Plain int rendering — Prometheus accepts integers without
		// the trailing ".0".
		appendInt(&b, counts[k])
		b.WriteByte('\n')
	}
	return b.String()
}

// escapeLabel escapes the three special characters Prometheus
// requires escaped inside a label value: backslash, double quote, and
// newline. The label set in practice never contains them — feature
// names are constants, model names are alphanumerics — but the
// escaping keeps the renderer correct even if a future caller passes
// an unusual model name.
func escapeLabel(s string) string {
	if !strings.ContainsAny(s, "\\\"\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// appendInt is a fmt.Fprintf-free integer renderer for the hot path.
// Counter values fit comfortably in int64; production scrapes touch
// this every 15s so the allocation savings matter.
func appendInt(b *strings.Builder, v int) {
	if v == 0 {
		b.WriteByte('0')
		return
	}
	if v < 0 {
		b.WriteByte('-')
		v = -v
	}
	// Buffer big enough for any int.
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	b.Write(buf[i:])
}
