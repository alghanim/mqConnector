package server

import "context"

// contextBackground returns a Background context. Wrapped so that future
// instrumentation (e.g. propagating tenant id) can be added in one place.
func contextBackground() context.Context {
	return context.Background()
}
