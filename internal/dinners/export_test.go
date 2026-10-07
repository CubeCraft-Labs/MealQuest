package dinners

import (
	"context"
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

// RunGroundJob runs the KindGroundDinner handler for p in the caller's
// goroutine, as the job runner would, and returns the grounding status it
// reports.
func (s *Service) RunGroundJob(ctx context.Context, p GroundJob) (string, error) {
	r, err := s.runGroundJob(ctx, p, nil)
	return r.GroundingStatus, err
}
