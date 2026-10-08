package shopping

import "time"

// SetClock makes now the clock s ends and dates weeks on.
func SetClock(s *Service, now func() time.Time) { s.now = now }
