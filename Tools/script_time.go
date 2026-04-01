package tools

import "time"

// currentTimeMs returns the current Unix timestamp in milliseconds.
func currentTimeMs() int64 {
	return time.Now().UnixMilli()
}
