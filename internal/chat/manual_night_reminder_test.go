package chat

import (
	"database/sql"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// suppressedManualNight is last_error when a queued manual-night reminder
// is not sent because that night's label changed or the night was cleared.
const suppressedManualNight = "suppressed: the manual night no longer has the label it was queued for"

// planManualNight stores a one-off night named label on date, with no dinner.
func planManualNight(t *testing.T, st *store.Store, date, label string) {
	t.Helper()
	ctx := t.Context()
	if _, err := st.Writer.ExecContext(ctx, `INSERT INTO week_plans (id, week_start, status, created_at, updated_at)
		VALUES ('plan-manual', '2026-10-12', 'draft', 'c', 'u')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Writer.ExecContext(ctx, `INSERT INTO plan_days (id, plan_id, date, manual_label, is_new, created_at, updated_at)
		VALUES ('day-manual', 'plan-manual', ?, ?, 0, 'c', 'u')`, date, label); err != nil {
		t.Fatal(err)
	}
}

// setManualLabel replaces the one-off label of date.
func setManualLabel(t *testing.T, st *store.Store, date, label string) {
	t.Helper()
	res, err := st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET manual_label = ? WHERE date = ?`, label, date)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("relabeled %d plan days on %s, want 1", n, date)
	}
}

// clearManualNight clears the one-off label of date, leaving no dinner.
func clearManualNight(t *testing.T, st *store.Store, date string) {
	t.Helper()
	res, err := st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET manual_label = NULL WHERE date = ?`, date)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("cleared %d plan days on %s, want 1", n, date)
	}
}

// queuedManualLabel is the manual_label the outbox stored for the reminder.
func queuedManualLabel(t *testing.T, payload, label string) {
	t.Helper()
	got := queuedObject(t, payload)
	if got["manual_label"] != label {
		t.Errorf("queued manual_label = %v, want %q", got["manual_label"], label)
	}
	if _, ok := got["planned_dinner_id"]; ok || got["dinner"] != nil {
		t.Errorf("manual night payload = %v, want no dinner id", got)
	}
}

// queueManualNight plans label on 2026-10-14 and emits its day-of reminder.
func queueManualNight(t *testing.T, label string) (*notify.Service, *store.Store, notify.Event) {
	t.Helper()
	const date = "2026-10-14"
	svc, st := newReminderService(t)
	planManualNight(t, st, date, label)
	ev := ManualNightReminder(mustDate(t, date), label)
	if err := svc.Emit(t.Context(), ev); err != nil {
		t.Fatal(err)
	}
	return svc, st, ev
}

// TestQueuedManualNightClearedAfterQueueingIsNotDelivered is MQ23-01. A
// manual-night reminder records the label it was queued for, and is not
// sent once that night is cleared.
func TestQueuedManualNightClearedAfterQueueingIsNotDelivered(t *testing.T) {
	const label = "mummy dogs"
	svc, st, ev := queueManualNight(t, label)
	clearManualNight(t, st, "2026-10-14")

	sender := &recordingSender{}
	status, lastError, payload := deliverReminder(t, svc, st, sender, ev.Key)
	queuedManualLabel(t, payload, label)
	if n := len(sender.messages()); n != 0 || status != "failed" || lastError != suppressedManualNight {
		t.Fatalf("delivered %d messages, status %s error %q, want the reminder suppressed (%s)", n, status, lastError, suppressedManualNight)
	}
}

// TestQueuedManualNightLabelChangedAfterQueueingIsNotDelivered is MQ23-01.
// A manual-night reminder is not sent once its night's label has changed.
func TestQueuedManualNightLabelChangedAfterQueueingIsNotDelivered(t *testing.T) {
	const label = "mummy dogs"
	svc, st, ev := queueManualNight(t, label)
	setManualLabel(t, st, "2026-10-14", "pizza night")

	sender := &recordingSender{}
	status, lastError, payload := deliverReminder(t, svc, st, sender, ev.Key)
	queuedManualLabel(t, payload, label)
	if n := len(sender.messages()); n != 0 || status != "failed" || lastError != suppressedManualNight {
		t.Fatalf("delivered %d messages, status %s error %q, want the reminder suppressed (%s)", n, status, lastError, suppressedManualNight)
	}
}

// TestQueuedManualNightWithUnchangedLabelIsDelivered is MQ23-01. A
// manual-night reminder whose night still carries the queued label is
// delivered, still with no dinner and no recipe.
func TestQueuedManualNightWithUnchangedLabelIsDelivered(t *testing.T) {
	const label = "mummy dogs"
	svc, st, ev := queueManualNight(t, label)

	sender := &recordingSender{}
	status, lastError, payload := deliverReminder(t, svc, st, sender, ev.Key)
	queuedManualLabel(t, payload, label)
	gotMsg := sender.messages()
	if status != "sent" || len(gotMsg) != 1 {
		t.Fatalf("status %s error %q messages %d, want the manual night delivered", status, lastError, len(gotMsg))
	}
	if gotMsg[0].Payload.Title != "Tonight: mummy dogs" || gotMsg[0].Payload.Dinner != nil || gotMsg[0].Payload.URL != "/plan?week=2026-10-12" {
		t.Errorf("delivered payload = %+v, want the label, the week's plan and no dinner", gotMsg[0].Payload)
	}
}

// TestManualNightReminderStoredBeforeItCarriedALabelIsDelivered: a queued
// manual-night reminder from before payloads recorded the label has nothing
// to check, so it is delivered even when that night was later cleared.
func TestManualNightReminderStoredBeforeItCarriedALabelIsDelivered(t *testing.T) {
	svc, st := newReminderService(t)
	planManualNight(t, st, "2026-10-14", "mummy dogs")
	clearManualNight(t, st, "2026-10-14")
	const key = "day_of_reminder:2026-10-14"
	const payload = `{"event":"day_of_reminder","title":"Tonight: mummy dogs","body":"","url":"/plan?week=2026-10-12","tag":"day_of_reminder:2026-10-14","week":"2026-10-12"}`
	now := store.Now().String()
	if _, err := db.New(st.Writer).InsertNotification(t.Context(), db.InsertNotificationParams{
		ID: store.NewID(), EventKey: key, Channel: string(notify.ChannelPush), Payload: payload,
		NextAttemptAt: sql.NullString{String: now, Valid: true}, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	sender := &recordingSender{}
	status, lastError, _ := deliverReminder(t, svc, st, sender, key)
	if status != "sent" || len(sender.messages()) != 1 || lastError != "" {
		t.Fatalf("status %s error %q messages %d, want the unlabeled reminder delivered", status, lastError, len(sender.messages()))
	}
}
