package slo

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Rule is one alerting rule extracted from a Prometheus rules YAML.
// Recording rules are not represented here — the loader filters them
// out. Only the fields the in-process evaluator actually consumes get
// dedicated struct members; the rest of the rule (rare fields like
// keep_firing_for that Prometheus 2.42+ added but mqConnector's rule
// set does not yet use) is preserved in Raw for round-trip / debugging
// via /alerts/active.
type Rule struct {
	// Name is the `alert:` value, e.g. "mqConnectorAvailabilityFastBurn".
	Name string

	// Expr is the raw PromQL expression text (after YAML block-string
	// folding). Parsed lazily by the expression engine; we keep the
	// raw text so the /alerts/active endpoint and structured logs can
	// echo it back.
	Expr string

	// For is the duration the expression must continuously be true
	// before the rule transitions from pending → firing. Zero means
	// the rule fires the first tick its expression is true.
	For time.Duration

	// Labels are static labels attached to the alert. Common keys:
	// severity (info|warning|critical), slo, team.
	Labels map[string]string

	// Annotations are descriptive labels that the alert renderers use
	// (summary, description, runbook_url). Untouched by the
	// evaluator — passed through to FiringAlert.
	Annotations map[string]string

	// Group is the `groups[].name` the rule was nested under. Useful
	// when one rule file declares multiple groups.
	Group string

	// Raw preserves the YAML node so callers wanting to render the
	// full rule (e.g. an "edit in Prometheus" tooltip in the UI) can
	// do so without re-parsing. Nil when the loader constructed the
	// rule programmatically (tests).
	Raw map[string]any
}

// promRuleFile is the canonical Prometheus rules.yaml shape. Only the
// fields we read are declared; the YAML decoder ignores the rest.
type promRuleFile struct {
	Groups []promRuleGroup `yaml:"groups"`
}

type promRuleGroup struct {
	Name     string        `yaml:"name"`
	Interval time.Duration `yaml:"interval"`
	Rules    []promRuleRaw `yaml:"rules"`
}

// promRuleRaw mirrors one entry of the groups[].rules list. Either
// `alert` or `record` is set; the loader skips the record case. `For`
// is parsed via yaml as a string so we can hand it to
// time.ParseDuration — yaml.v3 will not coerce "5m" to a Duration
// directly.
type promRuleRaw struct {
	Alert       string            `yaml:"alert"`
	Record      string            `yaml:"record"`
	Expr        string            `yaml:"expr"`
	For         string            `yaml:"for"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
	// Rest captures unknown fields so we can echo them back in Raw
	// without losing data. yaml.v3 emits the unmatched keys into the
	// inline map.
	Rest map[string]any `yaml:",inline"`
}

// LoadFile reads one Prometheus rules file and returns the alerting
// rules within. Recording rules are silently skipped. A malformed
// individual rule is logged at warn level (via slog.Default — callers
// who want structured loader output should wrap LoadFile in their
// own logger context) and skipped; the rest of the file still loads.
//
// The first error returned is for I/O / YAML-syntax failures only —
// failures the operator should fix before booting. Per-rule
// validation failures (bad `for:`, missing alert name, unsupported
// expr) are degradation, not boot-blocking.
func LoadFile(path string) ([]Rule, error) {
	return loadFile(path, slog.Default())
}

// LoadFileWithLogger is the logger-injectable LoadFile. The cmd/
// wiring uses this so the loader's warns end up on the application's
// configured slog handler.
func LoadFileWithLogger(path string, logger *slog.Logger) ([]Rule, error) {
	return loadFile(path, logger)
}

func loadFile(path string, logger *slog.Logger) ([]Rule, error) {
	if logger == nil {
		logger = slog.Default()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules file %s: %w", path, err)
	}
	var doc promRuleFile
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse rules file %s: %w", path, err)
	}
	out := make([]Rule, 0, 16)
	for _, g := range doc.Groups {
		for _, rr := range g.Rules {
			if rr.Alert == "" {
				// Recording rule (rr.Record != "") or malformed —
				// either way, not an alert. Recording rules are
				// silently skipped because the in-process
				// evaluator resolves their RHS expressions
				// directly.
				continue
			}
			if strings.TrimSpace(rr.Expr) == "" {
				logger.Warn("slo: skipping alert with empty expr",
					"file", path, "group", g.Name, "alert", rr.Alert)
				continue
			}
			r := Rule{
				Name:        rr.Alert,
				Expr:        strings.TrimSpace(rr.Expr),
				Labels:      rr.Labels,
				Annotations: rr.Annotations,
				Group:       g.Name,
			}
			if rr.For != "" {
				d, err := time.ParseDuration(rr.For)
				if err != nil {
					logger.Warn("slo: bad for: duration; treating as 0",
						"file", path, "alert", rr.Alert, "for", rr.For, "err", err)
				} else {
					r.For = d
				}
			}
			r.Raw = rawFromRule(rr)
			out = append(out, r)
		}
	}
	// Stable order so tests and /alerts/active output are
	// deterministic across loads.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// LoadDir reads every *.yaml / *.yml file directly under dir (not
// recursive) and concatenates the results. Useful when an operator
// drops a couple of rule files alongside the bundled one.
func LoadDir(dir string) ([]Rule, error) {
	return loadDir(dir, slog.Default())
}

// LoadDirWithLogger is the logger-injectable LoadDir.
func LoadDirWithLogger(dir string, logger *slog.Logger) ([]Rule, error) {
	return loadDir(dir, logger)
}

func loadDir(dir string, logger *slog.Logger) ([]Rule, error) {
	if logger == nil {
		logger = slog.Default()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read rules dir %s: %w", dir, err)
	}
	var all []Rule
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if !(strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml")) {
			continue
		}
		path := filepath.Join(dir, name)
		rs, err := loadFile(path, logger)
		if err != nil {
			// Don't fail the whole directory load on one malformed
			// file — log and continue. Operators typically drop in
			// multiple rule files; one broken one shouldn't take
			// the others offline.
			logger.Warn("slo: skipping bad rules file", "path", path, "err", err)
			continue
		}
		all = append(all, rs...)
	}
	return all, nil
}

// rawFromRule normalises an annotated rule back to a generic map so
// Rule.Raw is JSON-serializable for the UI.
func rawFromRule(rr promRuleRaw) map[string]any {
	m := map[string]any{
		"alert": rr.Alert,
		"expr":  rr.Expr,
	}
	if rr.For != "" {
		m["for"] = rr.For
	}
	if len(rr.Labels) > 0 {
		m["labels"] = stringMapToAny(rr.Labels)
	}
	if len(rr.Annotations) > 0 {
		m["annotations"] = stringMapToAny(rr.Annotations)
	}
	for k, v := range rr.Rest {
		m[k] = v
	}
	return m
}

func stringMapToAny(in map[string]string) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// loadRawRules reads the raw rule-records (alerts + records) out of a
// rule file. Used by RecordingRulesFromFile to harvest recording-rule
// expressions without going through the alerts-only LoadFile path.
func loadRawRules(path string) ([]promRuleRaw, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules file %s: %w", path, err)
	}
	var doc promRuleFile
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse rules file %s: %w", path, err)
	}
	var out []promRuleRaw
	for _, g := range doc.Groups {
		out = append(out, g.Rules...)
	}
	return out, nil
}

// listYAML returns every .yaml / .yml direct child of dir.
func listYAML(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := strings.ToLower(e.Name())
		if strings.HasSuffix(n, ".yaml") || strings.HasSuffix(n, ".yml") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out, nil
}
