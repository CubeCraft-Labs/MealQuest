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

// SetOfferLifetime sets how long candidates ground a dinner until t ends.
// A test calling it must not run in parallel.
func SetOfferLifetime(t *testing.T, d time.Duration) {
	old := offerLifetime
	offerLifetime = d
	t.Cleanup(func() { offerLifetime = old })
}

// SetUpdateCommitted runs f in Update once a rename is committed, before
// the dinner's offers are cleared, until t ends. A test calling it must
// not run in parallel.
func SetUpdateCommitted(t *testing.T, f func()) {
	old := updateCommitted
	updateCommitted = f
	t.Cleanup(func() { updateCommitted = old })
}

// AdvanceClock moves the service's clock d ahead of real time until t
// ends, without firing any timer. A test calling it must not run in
// parallel.
func AdvanceClock(t *testing.T, d time.Duration) {
	old := now
	now = func() time.Time { return old().Add(d) }
	t.Cleanup(func() { now = old })
}

// HasOffers reports whether the service holds candidates offered for
// dinner id.
func (s *Service) HasOffers(id string) bool {
	s.offersMu.Lock()
	defer s.offersMu.Unlock()
	_, ok := s.offers[id]
	return ok
}

// StoreOffers stores urls as dinner id's offers, as a Candidates search
// that read the dinner at grounding_revision revision would on finishing.
func (s *Service) StoreOffers(id string, urls map[string]string, revision int64) {
	keep, done := s.trackOffers(id)
	defer done()
	keep(urls, revision)
}

// OfferCount is the number of dinners whose candidates the service holds.
func (s *Service) OfferCount() int {
	s.offersMu.Lock()
	defer s.offersMu.Unlock()
	return len(s.offers)
}

// RunGroundJob runs the KindGroundDinner handler for p in the caller's
// goroutine, as the job runner would, and returns the grounding status it
// reports.
func (s *Service) RunGroundJob(ctx context.Context, p GroundJob) (string, error) {
	r, err := s.runGroundJob(ctx, p, nil)
	return r.GroundingStatus, err
}

// PageSiteName is pageSiteName.
var PageSiteName = pageSiteName
