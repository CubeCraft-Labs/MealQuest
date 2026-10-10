package scheduler

import "time"

// SetClock makes now the clock and after the timer s fires by.
func SetClock(s *Scheduler, now func() time.Time, after func(time.Duration) <-chan time.Time) {
	s.now = now
	s.after = after
}
