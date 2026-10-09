package shopping

import (
	"context"
	"database/sql"
	"errors"
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
// A week is sent once: a Supply run already sent, or with a send
// unconfirmed, is returned as it is, without posting again, unless resend
// is true. A week with no Supply run is ErrListNotFound and one frozen into
// the Supply archive ErrFrozen.
//
// The send is recorded as begun, in chat_send_unconfirmed_at, before it is
// posted, so a post whose outcome is lost is never taken for one never
// made. A post Google Chat refuses, or that is never made, returns
// chat.PostSupplyRun's error and clears it. A post Google Chat does not
// answer, or one it accepts that cannot then be recorded, is
// ErrSendUnconfirmed and leaves it, as does the process stopping mid-send.
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
	if (l.SentToChatAt != nil || l.ChatSendUnconfirmedAt != nil) && !resend {
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
	q := db.New(s.st.Writer)
	n, err := q.BeginShoppingListChatSend(ctx, db.BeginShoppingListChatSendParams{
		ChatSendUnconfirmedAt: sql.NullString{String: store.Now().String(), Valid: true},
		UpdatedAt:             store.Now().String(),
		ID:                    l.ID,
	})
	if err != nil {
		return List{}, fmt.Errorf("shopping: record send to Google Chat begun: %w", err)
	}
	if n != 1 {
		return List{}, ErrFrozen
	}
	postErr := s.chat.PostSupplyRun(ctx, weekStart, name, groups)
	// The post's outcome is known: record it even if the client has gone.
	write, cancel := jobs.FinalWriteContext(ctx)
	defer cancel()
	if errors.Is(postErr, chat.ErrUnreachable) {
		return List{}, fmt.Errorf("%w: %w", ErrSendUnconfirmed, postErr)
	}
	if postErr != nil {
		if _, err := q.ClearShoppingListChatSend(write, db.ClearShoppingListChatSendParams{
			UpdatedAt: store.Now().String(),
			ID:        l.ID,
		}); err != nil {
			return List{}, errors.Join(postErr, fmt.Errorf("shopping: clear refused send to Google Chat: %w", err))
		}
		return List{}, postErr
	}
	now := store.Now().String()
	n, err = q.MarkShoppingListSentToChat(write, db.MarkShoppingListSentToChatParams{
		SentToChatAt: sql.NullString{String: now, Valid: true},
		UpdatedAt:    now,
		ID:           l.ID,
	})
	if err != nil {
		return List{}, fmt.Errorf("%w: record sent to Google Chat: %w", ErrSendUnconfirmed, err)
	}
	if n != 1 {
		return List{}, ErrFrozen
	}
	return s.Get(write, weekStart)
}
