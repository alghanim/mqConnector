package pipeline

import (
	"context"
	"fmt"
)

// StageOutcome is the result of running a message through a stage list. It
// reuses the same Result type the route stage emits so callers can show
// which destination(s) a message would hit without actually sending it.
type StageOutcome struct {
	Body   []byte
	Format Format
	Route  *Result
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
func RunStages(ctx context.Context, stages []Stage, message []byte) (StageOutcome, error) {
	format := Detect(message)
	current := message
	currentFormat := format
	var route *Result

	for _, stage := range stages {
		out, newFormat, result, err := stage.Execute(ctx, current, currentFormat)
		if err != nil {
			return StageOutcome{}, fmt.Errorf("%s: %w", stage.Name(), err)
		}
		current = out
		if newFormat != "" {
			currentFormat = newFormat
		}
		if result != nil && len(result.Destinations) > 0 {
			route = result
		}
	}
	return StageOutcome{Body: current, Format: currentFormat, Route: route}, nil
}
