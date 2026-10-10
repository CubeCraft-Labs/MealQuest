package home

import "time"

// SetClock makes now the clock s reads "now" and "tonight" on.
func SetClock(s *Service, now func() time.Time) { s.now = now }
