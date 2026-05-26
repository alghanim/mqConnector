package explain

import (
	"encoding/json"
	"time"
)

// Explanation is the canonical structured output of any explainer.
// Stable wire format — frontend renders Sections in order, Facts
// as label/value rows, and Headline as the headline above all.
//
// Two callers consume this shape today: the GET /api/v1/explain/{subject}/{id}
// HTTP handler (which wraps it in explainResponse to add the optional
// AI sidecar), and a future CLI debug command. The shape is stable —
// additions land as omitempty fields, never as renames.
type Explanation struct {
	// Subject is one of "circuit" | "drift" | "latency" |
	// "dlq_cluster" | "dlq_entry". Mirrors the URL path segment so
	// the response is self-describing for non-HTTP callers.
	Subject string `json:"subject"`
	// ID is the keying id appropriate for the subject (pipeline_id
	// for circuit/drift/latency, fingerprint for dlq_cluster, entry
	// id for dlq_entry).
	ID string `json:"id"`
	// Headline is the one-line summary in operator language. Always
	// present — even a degraded explanation gets a useful headline.
	Headline string `json:"headline"`
	// Severity is the explainer's verdict on operational impact.
	Severity Severity `json:"severity"`
	// Facts are the labeled values the headline rests on. Empty
	// slice (not nil) when an explainer has no facts to surface.
	Facts []Fact `json:"facts"`
	// Sections are optional deeper rendered panels. omitempty so
	// the wire stays tight when an explainer only emits the
	// headline + facts.
	Sections []Section `json:"sections,omitempty"`
	// AsOf is when this explanation was computed. Operators
	// compare against current state to detect staleness.
	AsOf time.Time `json:"as_of"`
	// Sources is the human-readable list of telemetry sources the
	// explainer consulted (e.g. "metrics.Snapshot", "audit.List").
	// Operators use it to drill into a value they doubt; omitempty
	// when the explainer didn't track its sources.
	Sources []string `json:"sources,omitempty"`
}

// Severity grades operational impact. Frontend renderers pick the
// gutter colour from this value alone — the three tiers map to
// info (neutral), warning (amber), critical (red).
type Severity string

const (
	// SeverityInfo signals "healthy / within envelope". The
	// explainer ran successfully but nothing demands attention.
	SeverityInfo Severity = "info"
	// SeverityWarning signals "something is off but service is
	// still flowing". A breaker probing, a drift ratio creeping
	// up, a stage trending slow.
	SeverityWarning Severity = "warning"
	// SeverityCritical signals "service is or imminently will be
	// degraded". A breaker open, drift past the alarm tier, a
	// stage dominating end-to-end latency.
	SeverityCritical Severity = "critical"
)

// Fact is a single labeled value. Source attribution makes the
// explanation auditable — operators can drill into the source if
// they doubt the value. Label is human-friendly; Value is the
// rendered string (formatting is the explainer's responsibility,
// not the frontend's).
type Fact struct {
	// Label is the operator-facing field name (e.g. "Current state",
	// "Top error template", "p99 latency").
	Label string `json:"label"`
	// Value is the rendered value. Pre-formatted by the explainer
	// so the frontend renders it verbatim — units, percentages,
	// truncation are baked in.
	Value string `json:"value"`
	// Source names the underlying telemetry (e.g.
	// "mqconnector_validate_failures_total", "audit.List"). Empty
	// when the value is derived (a ratio, a count of facts).
	Source string `json:"source,omitempty"`
	// AsOf is the timestamp of the underlying observation. RFC3339;
	// empty when the value is from the current snapshot or has no
	// meaningful timestamp.
	AsOf string `json:"as_of,omitempty"`
}

// Section is a structured sub-panel. Kind determines the renderer
// the frontend picks. Data is the payload — typed per Kind,
// validated at the boundary, not in this struct.
//
// Known Kinds (the explainers in this package emit these):
//   - "timeline"  — breaker transitions, recent deploys, anything time-keyed.
//   - "stages"    — per-stage waterfall (latency explainer).
//   - "fields"    — template-extracted variable fields (dlq explainer).
//   - "narrative" — a one-or-two-sentence prose paragraph.
//   - "table"     — generic tabular data.
//
// The frontend ignores unknown Kinds rather than failing — new
// section kinds are additive.
type Section struct {
	Kind  string          `json:"kind"`
	Title string          `json:"title"`
	Data  json.RawMessage `json:"data"`
}
