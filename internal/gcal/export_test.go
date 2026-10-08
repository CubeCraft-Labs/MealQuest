package gcal

import "time"

// SetClock replaces s's clock.
func SetClock(s *Service, now func() time.Time) { s.now = now }
