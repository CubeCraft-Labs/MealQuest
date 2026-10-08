package gcal

import "time"

// SetClock replaces s's clock.
func SetClock(s *Service, now func() time.Time) { s.now = now }

// SetRetryDelay replaces s's wait before the first retry of a transient
// Google failure.
func SetRetryDelay(s *Service, d time.Duration) { s.retryDelay = d }
