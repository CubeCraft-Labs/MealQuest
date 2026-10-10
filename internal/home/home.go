// Package home reads the Home screen: tonight's dinner, the Side quests to
// swipe, the Supply run and whether there are recent updates. It owns no
// data: every value is read through the service that owns it, and "now"
// and "tonight" are in the household's zone, never UTC.
package home

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/suggest"
)

// Service reads the Home screen.
type Service struct {
	settings *household.SettingsService
	plans    *plan.Service
	suggest  *suggest.Service
	shopping *shopping.Service
	notifier *notify.Service
	// now is the clock "now" and "tonight" are read on; tests replace it.
	now func() time.Time
}

// NewService returns a service reading through the given services.
func NewService(settings *household.SettingsService, plans *plan.Service, suggestSvc *suggest.Service, shop *shopping.Service, notifier *notify.Service) (*Service, error) {
	switch {
	case settings == nil:
		return nil, errors.New("home: nil household settings service")
	case plans == nil:
		return nil, errors.New("home: nil plan service")
	case suggestSvc == nil:
		return nil, errors.New("home: nil suggestions service")
	case shop == nil:
		return nil, errors.New("home: nil shopping service")
	case notifier == nil:
		return nil, errors.New("home: nil notifier")
	}
	return &Service{settings: settings, plans: plans, suggest: suggestSvc, shopping: shop, notifier: notifier, now: time.Now}, nil
}

// Home is the Home screen as the API shows it.
type Home struct {
	// DateLabel is today in the household's zone, e.g. "Friday, Oct 2".
	DateLabel string `json:"dateLabel"`
	// Tonight is the dinner planned for today on the current week's plan;
	// nil when the week has no plan.
	Tonight *Dinner `json:"tonight"`
	// SideQuestCount is how many Side quests are pending on the deck.
	SideQuestCount int `json:"sideQuestCount"`
	// ShoppingList is the Supply run of the upcoming week when it has one,
	// else of the current week.
	ShoppingList  ShoppingStatus `json:"shoppingList"`
	RecentUpdates RecentUpdates  `json:"recentUpdates"`
}

// Dinner is tonight's dinner.
type Dinner struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Photo *string `json:"photo"`
	// Origin is regular or side_quest.
	Origin       string `json:"origin"`
	MinutesTotal *int64 `json:"minutes_total"`
}

// ShoppingStatus is whether a week's Supply run is ready.
type ShoppingStatus struct {
	// WeekStart is the Monday of the week, YYYY-MM-DD.
	WeekStart string `json:"weekStart"`
	// Label is the week label, e.g. "Week 41 · Oct 5 – 11".
	Label string `json:"label"`
	// Ready is true when the week has a Supply run.
	Ready bool `json:"ready"`
	// SentToChatAt is when the Supply run was sent to Google Chat; nil
	// until it is, and when there is no Supply run.
	SentToChatAt *string `json:"sentToChatAt"`
}

// RecentUpdates says whether there is something new for the bell.
type RecentUpdates struct {
	// Unread is true when a notification was stored after the household
	// last saw its updates (household_settings.updates_seen_at), or at all
	// when it never has.
	Unread bool `json:"unread"`
}

// Get reads the Home screen at the service's "now".
func (s *Service) Get(ctx context.Context) (Home, error) {
	settings, err := s.settings.Get(ctx)
	if err != nil {
		return Home{}, fmt.Errorf("home: read household settings: %w", err)
	}
	zone, err := household.LoadZone(settings.Timezone)
	if err != nil {
		return Home{}, fmt.Errorf("home: household settings: %w", err)
	}
	now := s.now().In(zone)
	today := household.Today(now, zone)
	thisWeek := household.WeekStart(today)

	tonight, err := s.tonight(ctx, thisWeek, today)
	if err != nil {
		return Home{}, err
	}
	deck, err := s.suggest.Deck(ctx)
	if err != nil {
		return Home{}, fmt.Errorf("home: read deck: %w", err)
	}
	list, err := s.shoppingStatus(ctx, thisWeek)
	if err != nil {
		return Home{}, err
	}
	unread, err := s.unread(ctx, settings.UpdatesSeenAt)
	if err != nil {
		return Home{}, err
	}
	return Home{
		DateLabel:      now.Format("Monday, Jan 2"),
		Tonight:        tonight,
		SideQuestCount: len(deck.Suggestions),
		ShoppingList:   list,
		RecentUpdates:  RecentUpdates{Unread: unread},
	}, nil
}

// tonight returns the dinner planned on today in the plan of the week
// starting thisWeek, or nil when the week has no plan.
func (s *Service) tonight(ctx context.Context, thisWeek, today store.LocalDate) (*Dinner, error) {
	p, err := s.plans.Get(ctx, thisWeek)
	if errors.Is(err, plan.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("home: read plan: %w", err)
	}
	for _, d := range p.Days {
		if d.Date == today.String() {
			return &Dinner{
				ID: d.Dinner.ID, Name: d.Dinner.Name, Photo: d.Dinner.Photo,
				Origin: d.Dinner.Origin, MinutesTotal: d.Dinner.Minutes,
			}, nil
		}
	}
	return nil, fmt.Errorf("home: plan of week %s has no day %s", thisWeek, today)
}

// shoppingStatus returns the status of the upcoming week's Supply run when
// it has one, else of the current week's, ready or not.
func (s *Service) shoppingStatus(ctx context.Context, thisWeek store.LocalDate) (ShoppingStatus, error) {
	y, m, d := thisWeek.Date()
	nextWeek := store.LocalDateOf(time.Date(y, m, d+7, 0, 0, 0, 0, time.UTC))
	for _, week := range []store.LocalDate{nextWeek, thisWeek} {
		l, err := s.shopping.Get(ctx, week)
		if errors.Is(err, shopping.ErrListNotFound) {
			continue
		}
		if err != nil {
			return ShoppingStatus{}, fmt.Errorf("home: read Supply run: %w", err)
		}
		return weekStatus(week, true, l.SentToChatAt)
	}
	return weekStatus(thisWeek, false, nil)
}

func weekStatus(week store.LocalDate, ready bool, sentToChatAt *string) (ShoppingStatus, error) {
	label, err := household.WeekLabel(week)
	if err != nil {
		return ShoppingStatus{}, fmt.Errorf("home: %w", err)
	}
	return ShoppingStatus{WeekStart: week.String(), Label: label, Ready: ready, SentToChatAt: sentToChatAt}, nil
}

// unread reports whether a notification was stored after seenAt, the
// stored updates_seen_at, or at all when it is nil.
func (s *Service) unread(ctx context.Context, seenAt *string) (bool, error) {
	var since *time.Time
	if seenAt != nil {
		var ts store.Timestamp
		if err := ts.Scan(*seenAt); err != nil {
			return false, fmt.Errorf("home: household settings updates_seen_at: %w", err)
		}
		since = &ts.Time
	}
	unread, err := s.notifier.StoredSince(ctx, since)
	if err != nil {
		return false, fmt.Errorf("home: %w", err)
	}
	return unread, nil
}
