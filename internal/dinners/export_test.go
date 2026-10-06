package dinners

import (
	"testing"
	"time"
)

// SetGroundTimeout sets the deadline of grounding by hand until t ends. A
// test calling it must not run in parallel.
func SetGroundTimeout(t *testing.T, d time.Duration) {
	old := groundTimeout
	groundTimeout = d
	t.Cleanup(func() { groundTimeout = old })
}
