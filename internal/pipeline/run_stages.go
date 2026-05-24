package pipeline

import (
	"context"
	"fmt"
	"time"
)

// StageRun captures the per-stage outcome of one RunStages call. The
// executor consumes this to feed per-stage metrics (validate
// attempts/failures for drift detection, stage-duration histograms)
// without needing a parallel observer pattern over the stages slice.
// On a failure, the last Run is the failing stage (Failed=true); any
// stages prior were successful.
type StageRun struct {
	Name     string
	Duration time.Duration
	Failed   bool
}

// StageOutcome is the result of running a message through a stage list. It
// reuses the same Result type the route stage emits so callers can show
// which destination(s) a message would hit without actually sending it.
type StageOutcome struct {
	Body   []byte
	Format Format
	Route  *Result
	// Runs is the per-stage observation log, in execution order. Even
	// on an early return (a stage failed), Runs is populated with the
	// stages that ran — including the failing one at the tail.
	Runs []StageRun
}

// RunStages executes a list of stages against a message and returns the
// final body + format + (optional) routing decision. It is the engine the
// live executor calls per message, but it knows nothing about MQ — pull it
// out and you can drive it from a preview handler, a unit test, or a
// future replay tool.
//
// A stage error stops the chain and is returned to the caller wrapped with
// the stage name. The executor turns that into a DLQ entry; the preview
// handler turns it into a 4xx response body the operator can read.
//
// Per-stage observations (timings + success flag) are also returned via
// StageOutcome.Runs so the caller can wire metrics / tracing without
// reaching into the stage list itself.
func RunStages(ctx context.Context, stages []Stage, message []byte) (StageOutcome, error) {
	format := Detect(message)
	current := message
	currentFormat := format
	var route *Result
	var runs []StageRun

	for _, stage := range stages {
		started := time.Now()
		out, newFormat, result, err := stage.Execute(ctx, current, currentFormat)
		runs = append(runs, StageRun{
			Name:     stage.Name(),
			Duration: time.Since(started),
			Failed:   err != nil,
		})
		if err != nil {
			return StageOutcome{Runs: runs}, fmt.Errorf("%s: %w", stage.Name(), err)
		}
		current = out
		if newFormat != "" {
			currentFormat = newFormat
		}
		if result != nil && len(result.Destinations) > 0 {
			route = result
		}
	}
	return StageOutcome{Body: current, Format: currentFormat, Route: route, Runs: runs}, nil
}
