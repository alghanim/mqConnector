package main

import (
	"log/slog"
	"os"
	"time"

	"mqConnector/internal/config"
	"mqConnector/internal/metrics"
	"mqConnector/internal/slo"
)

// buildSLOEvaluator wires the in-process SLO evaluator from the loaded
// rules YAML. Returns (nil, nil) when SLO is disabled (no rules
// file/dir configured) OR when the configured path fails to load —
// either case logs a warn and lets the binary boot in a degraded
// state. We never return an error: SLO observability is best-effort
// and must not block startup.
//
// The History returned alongside the Evaluator is the 5-minute ring
// buffer that backs rate() lookback; main.go owns its goroutine
// lifecycle.
func buildSLOEvaluator(cfg config.SLOConfig, store *metrics.Store, logger *slog.Logger) (*slo.Evaluator, *metrics.History) {
	if cfg.RulesFile == "" && cfg.RulesDir == "" {
		logger.Info("SLO evaluator disabled (slo.rules_file / slo.rules_dir unset)")
		return nil, nil
	}

	var rules []slo.Rule
	var recordings map[string]string
	if cfg.RulesFile != "" {
		if _, err := os.Stat(cfg.RulesFile); err != nil {
			logger.Warn("SLO evaluator disabled: rules file not readable",
				"path", cfg.RulesFile, "err", err)
			return nil, nil
		}
		loaded, err := slo.LoadFileWithLogger(cfg.RulesFile, logger)
		if err != nil {
			logger.Warn("SLO evaluator disabled: rules file failed to load",
				"path", cfg.RulesFile, "err", err)
			return nil, nil
		}
		rules = loaded
		// Recording rules are stripped by LoadFile; harvest them
		// separately so alerts referencing
		// `mqconnector:availability:ratio5m` resolve.
		if recs, err := slo.RecordingRulesFromFile(cfg.RulesFile); err == nil {
			recordings = recs
		}
	} else {
		loaded, err := slo.LoadDirWithLogger(cfg.RulesDir, logger)
		if err != nil {
			logger.Warn("SLO evaluator disabled: rules dir failed to load",
				"path", cfg.RulesDir, "err", err)
			return nil, nil
		}
		rules = loaded
		if recs, err := slo.RecordingRulesFromDir(cfg.RulesDir); err == nil {
			recordings = recs
		}
	}

	if len(rules) == 0 {
		logger.Warn("SLO evaluator disabled: no alerting rules in source", "file", cfg.RulesFile, "dir", cfg.RulesDir)
		return nil, nil
	}

	history := metrics.NewHistory(store)
	if cfg.HistoryInterval > 0 {
		history = history.WithInterval(cfg.HistoryInterval)
	}
	if cfg.HistoryKeep > 0 {
		history = history.WithKeep(cfg.HistoryKeep)
	}

	src := slo.StoreSource{Store: store, History: history}
	eval := slo.NewEvaluator(rules, recordings, src, logger)
	if cfg.Interval > 0 {
		eval.Interval = cfg.Interval
	} else {
		eval.Interval = 30 * time.Second
	}
	return eval, history
}
