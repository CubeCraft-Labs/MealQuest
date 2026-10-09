package shopping

import (
	"context"
	"database/sql"
	"fmt"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// SendToChat posts the Supply run of the week starting weekStart to Google
// Chat (chat.PostSupplyRun): each dinner with its items still needed, and a
// link to the Supply run. It records sent_to_chat_at and returns the list.
//
// A week is sent once: a Supply run already sent is returned as it is,
// without posting again, unless resend is true. A week with no Supply run
// is ErrListNotFound and one frozen into the Supply archive ErrFrozen. A
// post that fails returns chat.PostSupplyRun's error and records nothing.
func (s *Service) SendToChat(ctx context.Context, weekStart store.LocalDate, resend bool) (List, error) {
	s.sending.Lock()
	defer s.sending.Unlock()
	l, err := s.Get(ctx, weekStart)
	if err != nil {
		return List{}, err
	}
	if l.Status != StatusCurrent {
		return List{}, ErrFrozen
	}
	if l.SentToChatAt != nil && !resend {
		return l, nil
	}
	name, err := household.WeekLabel(weekStart)
	if err != nil {
		return List{}, fmt.Errorf("shopping: send to Google Chat: %w", err)
	}
	if l.Title != nil {
		name = *l.Title
	}
	groups := make([]chat.SupplyRunGroup, len(l.Groups))
	for i, g := range l.Groups {
		groups[i].Dinner = g.DinnerName
		for _, it := range g.Items {
			if !it.Needed {
				continue
			}
			item := chat.SupplyRunItem{Name: it.Name}
			if it.Qty != nil {
				item.Qty = *it.Qty
			}
			groups[i].Items = append(groups[i].Items, item)
		}
	}
	if err := s.chat.PostSupplyRun(ctx, weekStart, name, groups); err != nil {
		return List{}, err
	}
	// The post is made: record it even if the client has gone.
	write, cancel := jobs.FinalWriteContext(ctx)
	defer cancel()
	now := store.Now().String()
	n, err := db.New(s.st.Writer).MarkShoppingListSentToChat(write, db.MarkShoppingListSentToChatParams{
		SentToChatAt: sql.NullString{String: now, Valid: true},
		UpdatedAt:    now,
		ID:           l.ID,
	})
	if err != nil {
		return List{}, fmt.Errorf("shopping: record sent to Google Chat: %w", err)
	}
	if n != 1 {
		return List{}, ErrFrozen
	}
	return s.Get(write, weekStart)
}
