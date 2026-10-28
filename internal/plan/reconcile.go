package plan

import (
	"context"
	"fmt"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
)

// RemoveCalendarEvents deletes eventIDs on the household's chosen calendar.
// An event Google does not have is already gone. Nothing is written here:
// the caller is inside the dinner update's transaction and commits only
// when this returns nil. A failure leaves that transaction uncommitted, so
// the dinner stays planned when its event cannot be removed.
func (s *Service) RemoveCalendarEvents(ctx context.Context, eventIDs []string) error {
	if len(eventIDs) == 0 {
		return fmt.Errorf("plan: no calendar events to remove")
	}
	events := make([]gcal.Event, len(eventIDs))
	for i, id := range eventIDs {
		events[i] = gcal.Event{ID: id, Remove: true}
	}
	if err := s.cal.PushEvents(ctx, events, func() error { return nil }); err != nil {
		return fmt.Errorf("plan: remove calendar events: %w", err)
	}
	return nil
}
