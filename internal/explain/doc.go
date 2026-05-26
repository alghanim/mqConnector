// Package explain composes existing telemetry into structured
// "why is this state happening" explanations. It is the engine
// behind the Wave-4 drilldown drawer in the UI — but the design is
// useful for any caller (CLI debug, audit log enrichment, future
// automation).
//
// # Composable-modules pattern
//
// One Engine dispatches subjects ("circuit", "drift", "latency",
// "dlq_cluster", "dlq_entry") to one explainer each. Adding a new
// subject is a self-contained operation: implement the explainer
// interface, wire it into engine.go's subject map, and the HTTP
// surface picks it up without further changes.
//
// # Source interfaces
//
// The Engine never reaches into the storage / metrics / pipeline
// packages directly. Every dependency is fronted by a minimal
// interface declared in engine.go (MetricsSource, DLQSource,
// AuditSource, PipelinesSource, BreakersSource). Adapters in
// cmd/mqconnector/main.go wire the live services to those
// interfaces; tests substitute fakes. The interfaces carry only
// the read-shape the explainers actually consume — they don't
// re-export entire repo surfaces.
//
// # Best-effort degradation
//
// Each explainer treats every Source as optional. A sub-source
// that errors logs a warning (handled at the HTTP layer that owns
// the slog) and yields a degraded Explanation rather than failing
// the whole request. "We don't have signal X" is a useful answer
// for an operator; "the explainer crashed" is not.
//
// # Wire stability
//
// The Explanation type in explanation.go is the canonical wire
// shape; frontend renderers (the Wave-4 drilldown drawer) bind
// to it directly. Field additions are non-breaking (omitempty);
// renames are breaking and require a version bump on the consumer.
package explain
