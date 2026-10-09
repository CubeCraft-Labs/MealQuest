package household

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// NotificationPref is which channels one event's notifications go to.
type NotificationPref struct {
	Event string `json:"event"`
	Push  bool   `json:"push"`
	Chat  bool   `json:"chat"`
}

// NotificationSettings are the household's notification preferences: one
// NotificationPref for every event, in NotificationEvents order, and the
// day-of dinner reminder's time, which is the household settings'
// reminder_time.
type NotificationSettings struct {
	Prefs        []NotificationPref `json:"prefs"`
	ReminderTime string             `json:"reminder_time"`
}

// NotificationEvents are the events notification_prefs holds a row for, in
// the order the Notifications screen lists them.
var NotificationEvents = func() []string {
	events := make([]string, len(seedNotificationPrefs))
	for i, p := range seedNotificationPrefs {
		events[i] = p.Event
	}
	return events
}()

// Notifications returns the notification preferences. A missing event row is
// an error: SeedNotificationPrefs creates every one at boot.
func (s *SettingsService) Notifications(ctx context.Context) (NotificationSettings, error) {
	return readNotifications(ctx, db.New(s.st.Reader))
}

// UpdateNotifications validates ns, which must name every event of
// NotificationEvents exactly once and a 24-hour HH:MM reminder time, and
// replaces the notification preferences and reminder_time with it in one
// transaction, recording updatedBy (which must not be empty). The new
// household settings are published to every subscriber, as Update does. An
// invalid ns is a *ValidationError and changes nothing.
func (s *SettingsService) UpdateNotifications(ctx context.Context, ns NotificationSettings, updatedBy string) (NotificationSettings, error) {
	if updatedBy == "" {
		return NotificationSettings{}, errors.New("household: update notifications: no member to record as updated_by")
	}
	if err := ns.validate(); err != nil {
		return NotificationSettings{}, err
	}

	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	var (
		row db.HouseholdSetting
		out NotificationSettings
	)
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		now := store.Now().String()
		for _, p := range ns.Prefs {
			n, err := q.UpdateNotificationPref(ctx, db.UpdateNotificationPrefParams{
				Push: boolInt(p.Push), Chat: boolInt(p.Chat), UpdatedAt: now, Event: p.Event,
			})
			if err != nil {
				return fmt.Errorf("household: update notification preference %s: %w", p.Event, err)
			}
			if n != 1 {
				return fmt.Errorf("household: no notification preference for %s", p.Event)
			}
		}
		var err error
		row, err = applyPatch(ctx, q, SettingsPatch{ReminderTime: &ns.ReminderTime}, updatedBy)
		if err != nil {
			return err
		}
		out, err = readNotifications(ctx, q)
		return err
	})
	if err != nil {
		return NotificationSettings{}, err
	}

	s.publish(fromRow(row))
	return out, nil
}

func readNotifications(ctx context.Context, q *db.Queries) (NotificationSettings, error) {
	rows, err := q.ListNotificationPrefs(ctx)
	if err != nil {
		return NotificationSettings{}, fmt.Errorf("household: read notification preferences: %w", err)
	}
	byEvent := make(map[string]db.NotificationPref, len(rows))
	for _, r := range rows {
		byEvent[r.Event] = r
	}
	prefs := make([]NotificationPref, len(NotificationEvents))
	for i, event := range NotificationEvents {
		r, ok := byEvent[event]
		if !ok {
			return NotificationSettings{}, fmt.Errorf("household: no notification preference for %s", event)
		}
		prefs[i] = NotificationPref{Event: event, Push: r.Push == 1, Chat: r.Chat == 1}
	}
	settings, err := q.GetHouseholdSettings(ctx)
	if err != nil {
		return NotificationSettings{}, fmt.Errorf("household: read settings: %w", err)
	}
	return NotificationSettings{Prefs: prefs, ReminderTime: settings.ReminderTime}, nil
}

func (ns NotificationSettings) validate() error {
	var problems []Problem
	fail := func(field, reason string) {
		problems = append(problems, Problem{Field: field, Reason: reason})
	}
	known := make(map[string]bool, len(NotificationEvents))
	for _, event := range NotificationEvents {
		known[event] = true
	}
	seen := make(map[string]bool, len(ns.Prefs))
	for _, p := range ns.Prefs {
		switch {
		case !known[p.Event]:
			fail("prefs", fmt.Sprintf("unknown event %q", p.Event))
		case seen[p.Event]:
			fail("prefs", fmt.Sprintf("event %q is listed more than once", p.Event))
		}
		seen[p.Event] = true
	}
	for _, event := range NotificationEvents {
		if !seen[event] {
			fail("prefs", fmt.Sprintf("event %q is missing", event))
		}
	}
	if !clockTime.MatchString(ns.ReminderTime) {
		fail("reminder_time", fmt.Sprintf("invalid time %q (want 24-hour HH:MM, 00:00-23:59)", ns.ReminderTime))
	}
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
