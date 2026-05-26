// Package slo evaluates Prometheus alerting rules in-process against the
// same metrics the binary exposes at /api/metrics/prometheus, so the
// operator can see currently-firing SLO breaches without standing up an
// external Prometheus + Alertmanager.
//
// # Scope
//
// The package loads ONE or more Prometheus rule files (the canonical
// shape: top-level `groups: [{name, interval, rules: [{alert|record,
// expr, for, labels, annotations}]}]`) and evaluates only the *alert*
// rules. Recording rules are parsed and ignored — recording rules
// compute derived series that other rules reference; since the
// in-process evaluator already has direct access to every metric, it
// resolves recording-rule references at expression time rather than
// staging them.
//
// # Non-goals — this is NOT a full PromQL engine
//
// The expression evaluator implements the minimal subset of PromQL the
// project's own rules use (see deploy/prometheus/mqconnector-slos.yaml):
//
//   - Counter rate:    rate(METRIC[5m])
//   - Aggregation:     sum(EXPR), sum by (LABEL,…)(EXPR)
//   - Quantile:        histogram_quantile(Q, EXPR)
//   - Clamp:           clamp_min(EXPR, CONST), clamp_max(EXPR, CONST)
//   - Arithmetic:      + - * /
//   - Comparison:      > >= < <= == !=
//   - Logical:         and / or / unless / AND / OR / UNLESS
//   - Label selector:  METRIC{label=value}
//   - Number literal:  3.14, 1e-3, 0.001
//   - Parens:          (EXPR)
//
// Anything else (subqueries, label_replace, vector matching with
// on()/ignoring(), regex selectors with =~/!~, time-bound functions
// other than rate, etc.) is intentionally out of scope. A rule that
// uses an unsupported construct is logged with slog.Warn at load time
// and skipped — the rest of the rule set still loads.
//
// # Snapshot model
//
// rate() needs two samples (current + 5min-ago). The metrics store
// exposes only "current values"; the SLO evaluator therefore relies on
// a small ring buffer (internal/metrics.History) that samples the
// store every 30s for the last 5 minutes (10 samples). Loading the
// snapshot from the live store + N historical rows is O(pipelines) per
// evaluation tick — cheap enough at the 30s default interval.
//
// # Lifecycle
//
// One Evaluator goroutine per process. Cancel via the context passed
// to Run. The Snapshot() method is safe to call concurrently from HTTP
// handlers; it returns a copy of the currently-firing alerts.
//
// # Wire stability
//
// The FiringAlert type is the canonical wire shape consumed by
// /api/v1/alerts/active and the frontend AlertRibbon. Field additions
// are non-breaking; renames break consumers.
//
// # Best-effort degradation
//
// A malformed rule logs slog.Warn and is dropped from the rule set; the
// evaluator continues to run. A failing expression evaluation at run
// time logs slog.Warn at most once per minute per rule (to avoid
// log-spam from a permanently broken rule) and the rule is treated as
// inactive that tick.
package slo
