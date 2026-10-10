package shopping

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// RolloverTime is when, every Monday in the household zone, week_rollover
// freezes the week that has just ended. It is fixed, not a setting.
const RolloverTime = "00:05"

// Reasons Autosend posts nothing.
const (
	AutosendNotConfigured = "no Google Chat webhook is configured"
	AutosendChatOff       = "list_ready is switched off for Google Chat"
	AutosendNoList        = "the week has no Supply run"
	AutosendAlreadySent   = "the Supply run was already sent to Google Chat"
	AutosendUnconfirmed   = "a send of the Supply run to Google Chat is unconfirmed"
)

// AutosendKey identifies the automatic send of the Supply run of the week
// starting weekStart, list_ready:chat:YYYY-MM-DD: it is the
// shopping_chat_autosend period key claimed in schedule_runs. The send
// itself is recorded in the list's sent_to_chat_at.
func AutosendKey(weekStart store.LocalDate) string {
	return string(notify.ListReady) + ":" + string(notify.ChannelChat) + ":" + weekStart.String()
}

// ChatAutosendSchedule is the shopping_chat_autosend schedule: at the
// household's chat_autosend_dow and chat_autosend_time it sends the Supply
// run of the coming week, the week after the fire date's, to Google Chat
// with Autosend. Its period is that week, scheduler.WeeklyForNextWeek's,
// claimed under AutosendKey of the week's Monday. A send that fails is the
// period's error and is not retried; the household can still send it from
// the Supply run.
func (s *Service) ChatAutosendSchedule() scheduler.Schedule {
	return scheduler.Schedule{
		Name: scheduler.ShoppingChatAutosend,
		Rule: func(settings household.Settings) scheduler.Rule {
			return scheduler.WeeklyForNextWeek(time.Weekday(settings.ChatAutosendDow), settings.ChatAutosendTime)
		},
		PeriodKey: func(p scheduler.Period) string {
			return AutosendKey(p.Week)
		},
		Run: func(ctx context.Context, p scheduler.Period) error {
			skipped, err := s.Autosend(ctx, p.Week)
			if err != nil {
				return err
			}
			log := s.logger.With("period", p.Key, "week_start", p.Week.String())
			if skipped != "" {
				log.InfoContext(ctx, "Supply run not sent to Google Chat: "+skipped)
				return nil
			}
			log.InfoContext(ctx, "Supply run sent to Google Chat")
			return nil
		},
	}
}

// Autosend sends the Supply run of the week starting weekStart to Google
// Chat with SendToChat, as the household's send does, when a webhook is
// stored, list_ready's Google Chat channel is switched on, and the week has
// a Supply run not yet sent and with no send unconfirmed. Otherwise it
// posts nothing and returns which of those reasons stopped it; it never
// resends. A send that fails is SendToChat's error.
func (s *Service) Autosend(ctx context.Context, weekStart store.LocalDate) (skipped string, err error) {
	settings, err := s.chat.Get(ctx)
	if err != nil {
		return "", fmt.Errorf("shopping: autosend: %w", err)
	}
	if settings.URLMasked == nil {
		return AutosendNotConfigured, nil
	}
	on, err := s.notifier.ChannelOn(ctx, notify.ListReady, notify.ChannelChat)
	if err != nil {
		return "", fmt.Errorf("shopping: autosend: %w", err)
	}
	if !on {
		return AutosendChatOff, nil
	}
	l, err := s.Get(ctx, weekStart)
	if errors.Is(err, ErrListNotFound) {
		return AutosendNoList, nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case l.SentToChatAt != nil:
		return AutosendAlreadySent, nil
	case l.ChatSendUnconfirmedAt != nil:
		return AutosendUnconfirmed, nil
	}
	if _, err := s.SendToChat(ctx, weekStart, false); err != nil {
		return "", err
	}
	return "", nil
}

// RolloverSchedule is the week_rollover schedule: every Monday at
// RolloverTime it freezes the Supply run of the week before the fire
// date's into the Supply archive with Freeze, the week ending by the fire
// time. Its period scheduler.Weekly keys week_rollover:YYYY-Www for the
// fire date's week. A week with no Supply run has nothing to freeze, and
// the period says so; a freeze that fails is the period's error and is not
// retried.
func (s *Service) RolloverSchedule() scheduler.Schedule {
	return scheduler.Schedule{
		Name: scheduler.WeekRollover,
		Rule: func(household.Settings) scheduler.Rule {
			return scheduler.Weekly(time.Monday, RolloverTime)
		},
		Run: func(ctx context.Context, p scheduler.Period) error {
			y, m, d := p.Week.Date()
			last := store.LocalDateOf(time.Date(y, m, d-7, 0, 0, 0, 0, time.UTC))
			err := s.freeze(ctx, last, p.FireAt)
			if errors.Is(err, ErrListNotFound) {
				s.logger.InfoContext(ctx, "week rollover froze nothing: the week has no Supply run",
					"period", p.Key, "week_start", last.String())
				return nil
			}
			return err
		},
	}
}
