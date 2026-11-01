package home_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal/gcaltest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/home"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/suggest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// zone is the household's zone in these tests: not UTC, so a late local
// evening is already the next day in UTC.
const zone = "America/Chicago"

// weekDates is the week of 2026-10-12, Monday first.
var weekDates = []string{"2026-10-12", "2026-10-13", "2026-10-14", "2026-10-15", "2026-10-16", "2026-10-17", "2026-10-18"}

type env struct {
	svc      *home.Service
	st       *store.Store
	settings *household.SettingsService
	notifier *notify.Service
	loc      *time.Location
}

// newEnv returns a home service over a migrated temp database whose
// household is in zone, reading "now" from a clock set to at.
func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx, st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := household.SeedSettings(ctx, st, zone); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	notifier := notifytest.New(t, st)
	pub := &url.URL{Scheme: "https", Host: "mealquest.example"}

	box, err := secretbox.New(base64.StdEncoding.EncodeToString(make([]byte, secretbox.KeySize)))
	if err != nil {
		t.Fatal(err)
	}
	google := gcaltest.New(t, "https://mealquest.example"+gcal.CallbackPath)
	googleSvc, err := gcal.NewService(st, box, gcal.Config{
		ClientID: gcaltest.ClientID, ClientSecret: gcaltest.ClientSecret, PublicURL: pub,
		SecretKey: []byte("server-secret-key"), Endpoint: google.Endpoint(), RevokeURL: google.RevokeURL(),
		CalendarAPI: google.CalendarAPI(),
	})
	if err != nil {
		t.Fatal(err)
	}
	draft := func(context.Context, string, skills.WeekRequest) (skills.Week, error) {
		return skills.Week{}, errors.New("no model in this test")
	}
	plans, err := plan.NewService(st, draft, runner, googleSvc, notifier, func() {}, pub, time.Now, logger)
	if err != nil {
		t.Fatal(err)
	}
	g := recipestest.NewGrounder(t, st, t.TempDir(), &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, &llm.Fake{})
	sideQuests := func(context.Context, string, skills.SideQuestsRequest) (skills.SideQuests, error) {
		return skills.SideQuests{}, errors.New("no model in this test")
	}
	suggestSvc, err := suggest.NewService(st, g, sideQuests, runner, notifier, logger)
	if err != nil {
		t.Fatal(err)
	}
	chatSvc, err := chat.NewService(st, box, pub, http.DefaultTransport)
	if err != nil {
		t.Fatal(err)
	}
	title := func(context.Context, string, skills.ArchiveTitleRequest) (skills.ArchiveTitle, error) {
		return skills.ArchiveTitle{}, errors.New("no model in this test")
	}
	shop, err := shopping.NewService(st, title, runner, notifier, chatSvc, logger)
	if err != nil {
		t.Fatal(err)
	}
	settings := household.NewSettingsService(st)
	svc, err := home.NewService(settings, plans, suggestSvc, shop, notifier)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, st: st, settings: settings, notifier: notifier, loc: loc}
}

// at reads the Home screen with the clock at the household-local wall
// time "YYYY-MM-DD HH:MM".
func (e *env) at(t *testing.T, local string) home.Home {
	t.Helper()
	now, err := time.ParseInLocation("2006-01-02 15:04", local, e.loc)
	if err != nil {
		t.Fatal(err)
	}
	home.SetClock(e.svc, func() time.Time { return now })
	h, err := e.svc.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// dinner stores a dinner and returns its id.
func (e *env) dinner(t *testing.T, name, status, origin string, minutes int64) string {
	t.Helper()
	now := store.Now().String()
	d, err := db.New(e.st.Writer).CreateDinner(t.Context(), db.CreateDinnerParams{
		ID: store.NewID(), Name: name, NormalizedName: harness.Normalize(name), Status: status, Origin: origin,
		MinutesTotal:    sql.NullInt64{Int64: minutes, Valid: minutes > 0},
		GroundingStatus: "manual", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d.ID
}

// planWeek stores the plan of the week of 2026-10-12: the dinner named for
// its date each day, a Regular of 30 minutes, but Wednesday a Side quest of
// unknown minutes. It returns the plan's id.
func (e *env) planWeek(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	planID := store.NewID()
	if err := q.CreateWeekPlan(ctx, db.CreateWeekPlanParams{ID: planID, WeekStart: weekDates[0], CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, date := range weekDates {
		id := e.dinner(t, "Dinner "+date, "regular", "regular", 30)
		if date == "2026-10-14" {
			id = e.dinner(t, "Dinner "+date, "quest_log", "side_quest", 0)
		}
		if err := q.PutPlanDay(ctx, db.PutPlanDayParams{ID: store.NewID(), PlanID: planID, Date: date,
			DinnerID: sql.NullString{String: id, Valid: true}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	return planID
}

// TestTonightManualNightHasNoDinner: a one-off label is tonight's name and
// not a dinner. A cleared night is neither.
func TestTonightManualNightHasNoDinner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.planWeek(t)
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET dinner_id = NULL, manual_label = 'mummy dogs', is_new = 0 WHERE date = '2026-10-13'`); err != nil {
		t.Fatal(err)
	}
	h := e.at(t, "2026-10-13 18:00")
	if h.Tonight != nil || h.ManualTonight == nil || *h.ManualTonight != "mummy dogs" || h.Evening != nil {
		t.Fatalf("tonight = %+v, manual = %v, evening = %v, want mummy dogs and no dinner", h.Tonight, h.ManualTonight, h.Evening)
	}
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET manual_label = NULL WHERE date = '2026-10-13'`); err != nil {
		t.Fatal(err)
	}
	h = e.at(t, "2026-10-13 18:00")
	if h.Tonight != nil || h.ManualTonight != nil || h.Evening != nil {
		t.Fatalf("cleared tonight = %+v, manual = %v, evening = %v, want neither", h.Tonight, h.ManualTonight, h.Evening)
	}
}

// TestTonightDeliveryEveningHasNoDinner: a Delivery or Dining-out evening
// is tonight's state and not a dinner, a free-text label, or a cleared
// night. A cooked night on another day is still its dinner.
func TestTonightDeliveryEveningHasNoDinner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.planWeek(t)
	for _, source := range []string{"delivery", "dining_out"} {
		if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET dinner_id = NULL, manual_label = NULL, meal_source = ?, is_new = 0 WHERE date = '2026-10-13'`, source); err != nil {
			t.Fatal(err)
		}
		h := e.at(t, "2026-10-13 18:00")
		if h.Tonight != nil || h.ManualTonight != nil || h.Evening == nil || *h.Evening != source {
			t.Fatalf("%s tonight = %+v, manual = %v, evening = %v, want the evening and no dinner", source, h.Tonight, h.ManualTonight, h.Evening)
		}
	}
	monday := e.at(t, "2026-10-12 18:00")
	if monday.Tonight == nil || monday.Tonight.Name != "Dinner 2026-10-12" || monday.ManualTonight != nil || monday.Evening != nil {
		t.Fatalf("monday = %+v manual %v evening %v, want Dinner 2026-10-12", monday.Tonight, monday.ManualTonight, monday.Evening)
	}
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET meal_source = 'cook' WHERE date = '2026-10-13'`); err != nil {
		t.Fatal(err)
	}
	cleared := e.at(t, "2026-10-13 18:00")
	if cleared.Tonight != nil || cleared.ManualTonight != nil || cleared.Evening != nil {
		t.Fatalf("cleared tonight = %+v, manual = %v, evening = %v, want neither", cleared.Tonight, cleared.ManualTonight, cleared.Evening)
	}
}

// Tonight's dinner and the date label follow the household's local
// midnight, not UTC's: late on Tuesday evening in Chicago it is already
// Wednesday in UTC, and Home still shows Tuesday's dinner.
func TestTonightSwitchesAtLocalMidnight(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.planWeek(t)

	for _, tc := range []struct {
		local, label, dinner string
	}{
		// 23:59 CDT is 04:59 UTC the next day.
		{"2026-10-13 23:59", "Tuesday, Oct 13", "Dinner 2026-10-13"},
		{"2026-10-14 00:00", "Wednesday, Oct 14", "Dinner 2026-10-14"},
		// 19:00 CDT on Sunday is Monday 00:00 UTC, a week with no plan.
		{"2026-10-18 19:00", "Sunday, Oct 18", "Dinner 2026-10-18"},
	} {
		h := e.at(t, tc.local)
		if h.DateLabel != tc.label {
			t.Errorf("at %s: dateLabel = %q, want %q", tc.local, h.DateLabel, tc.label)
		}
		if h.Tonight == nil || h.Tonight.Name != tc.dinner {
			t.Errorf("at %s: tonight = %+v, want %s", tc.local, h.Tonight, tc.dinner)
		}
	}

	// Late Sunday evening of the week before, the plan's week has not
	// begun locally, though in UTC it has.
	if h := e.at(t, "2026-10-11 23:30"); h.Tonight != nil || h.DateLabel != "Sunday, Oct 11" {
		t.Errorf("at 2026-10-11 23:30: tonight = %+v, dateLabel = %q, want no dinner on Sunday, Oct 11", h.Tonight, h.DateLabel)
	}
}

// Tonight carries the dinner's id, name, photo, origin and minutes.
func TestTonightDinner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	planID := e.planWeek(t)
	planned := func(date string) string {
		t.Helper()
		var id string
		if err := e.st.Reader.QueryRowContext(ctx, `SELECT dinner_id FROM plan_days WHERE plan_id = ? AND date = ?`, planID, date).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	h := e.at(t, "2026-10-13 18:00")
	if h.Tonight == nil || h.Tonight.ID != planned("2026-10-13") || h.Tonight.Status != "regular" || h.Tonight.Origin != "regular" || h.Tonight.Photo != nil ||
		h.Tonight.MinutesTotal == nil || *h.Tonight.MinutesTotal != 30 {
		t.Errorf("tonight = %+v, want Tuesday's 30-minute Regular %s with no photo", h.Tonight, planned("2026-10-13"))
	}
	h = e.at(t, "2026-10-14 18:00")
	if h.Tonight == nil || h.Tonight.ID != planned("2026-10-14") || h.Tonight.Origin != "side_quest" || h.Tonight.MinutesTotal != nil {
		t.Errorf("tonight = %+v, want Wednesday's Side quest %s of unknown minutes", h.Tonight, planned("2026-10-14"))
	}

	// Thursday's dinner has a photo: Home serves its list-sized copy.
	now := store.Now().String()
	photo, err := db.New(e.st.Writer).CreatePhoto(ctx, db.CreatePhotoParams{
		ID: store.NewID(), SourceUrl: "https://example.com/thursday.jpg", LocalPath: "photos/thursday.jpg",
		Width: 800, Height: 600, Sha256: "thursday", FetchedAt: now, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	thursday := planned("2026-10-15")
	if _, err := e.st.Writer.ExecContext(ctx, `UPDATE dinners SET photo_id = ? WHERE id = ?`, photo.ID, thursday); err != nil {
		t.Fatal(err)
	}
	h = e.at(t, "2026-10-15 18:00")
	wantPhoto := "/media/photos/" + photo.ID + "-400.jpg"
	if h.Tonight == nil || h.Tonight.ID != thursday || h.Tonight.Photo == nil || *h.Tonight.Photo != wantPhoto {
		t.Errorf("tonight = %+v, want Thursday's dinner %s with photo %s", h.Tonight, thursday, wantPhoto)
	}

	// A dinner archived after the plan was made is still tonight's, archived.
	if _, err := e.st.Writer.ExecContext(ctx, `UPDATE dinners SET status = 'archived' WHERE id = ?`, planned("2026-10-14")); err != nil {
		t.Fatal(err)
	}
	if h = e.at(t, "2026-10-14 18:00"); h.Tonight == nil || h.Tonight.Status != "archived" {
		t.Errorf("tonight = %+v, want Wednesday's dinner archived", h.Tonight)
	}
}

// With no plan, no suggestions, no Supply run and no notifications, Home
// says so: no dinner tonight, nothing to swipe, the current week's Supply
// run not ready and nothing unread.
func TestHomeEmpty(t *testing.T) {
	t.Parallel()
	e := newEnv(t)

	h := e.at(t, "2026-10-13 23:59")
	want := home.Home{
		DateLabel: "Tuesday, Oct 13",
		Timezone:  zone,
		ShoppingList: home.ShoppingStatus{
			WeekStart: "2026-10-12", Label: "Week 42 · Oct 12 – 18",
		},
	}
	if h != want {
		t.Errorf("home = %+v, want %+v", h, want)
	}
}

// The Side quest count is the pending cards of the deck: a decided
// suggestion is not counted.
func TestSideQuestCount(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	batch, err := q.CreateSuggestionBatch(ctx, db.CreateSuggestionBatchParams{ID: store.NewID(), Trigger: "manual", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.FinishSuggestionBatch(ctx, db.FinishSuggestionBatchParams{Status: "done", UpdatedAt: now, ID: batch.ID}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Pho", "Ramen", "Tagine"} {
		id := e.dinner(t, name, "suggested", "side_quest", 0)
		if err := q.CreateSuggestion(ctx, db.CreateSuggestionParams{
			ID: store.NewID(), DinnerID: id, BatchID: batch.ID, Name: name, Reason: "Because", CitedRegularIds: "[]",
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if h := e.at(t, "2026-10-13 12:00"); h.SideQuestCount != 3 {
		t.Errorf("sideQuestCount = %d, want 3", h.SideQuestCount)
	}
	if _, err := e.st.Writer.ExecContext(ctx, `UPDATE suggestions SET status = 'declined', decided_at = ? WHERE name = 'Pho'`, now); err != nil {
		t.Fatal(err)
	}
	if h := e.at(t, "2026-10-13 12:00"); h.SideQuestCount != 2 {
		t.Errorf("sideQuestCount after a Pass = %d, want 2", h.SideQuestCount)
	}
}

// Reading Home writes nothing: a batch still pending whose job is gone,
// which a Deck read would fail, is left as it was, and the read succeeds.
func TestGetDoesNotWriteBatches(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	created := "2026-10-13T12:00:00.000Z"
	batch, err := db.New(e.st.Writer).CreateSuggestionBatch(ctx, db.CreateSuggestionBatchParams{ID: store.NewID(), Trigger: "manual", CreatedAt: created, UpdatedAt: created})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != "pending" {
		t.Fatalf("new batch status = %q, want pending", batch.Status)
	}
	if h := e.at(t, "2026-10-13 12:00"); h.SideQuestCount != 0 {
		t.Errorf("sideQuestCount = %d, want 0", h.SideQuestCount)
	}
	var status, updatedAt string
	if err := e.st.Reader.QueryRowContext(ctx, `SELECT status, updated_at FROM suggestion_batches WHERE id = ?`, batch.ID).Scan(&status, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || updatedAt != created {
		t.Errorf("batch after a Home read = %s at %s, want pending at %s", status, updatedAt, created)
	}
}

// The Supply run shown is the upcoming week's when it has one, else the
// current week's; the weeks are the household's local weeks.
func TestShoppingStatus(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	planID := e.planWeek(t)
	list := func(weekStart string, sent sql.NullString) {
		t.Helper()
		now := store.Now().String()
		if _, err := e.st.Writer.ExecContext(ctx,
			`INSERT INTO shopping_lists (id, week_start, plan_id, status, sent_to_chat_at, created_at, updated_at) VALUES (?, ?, ?, 'current', ?, ?, ?)`,
			store.NewID(), weekStart, planID, sent, now, now); err != nil {
			t.Fatal(err)
		}
	}

	list("2026-10-12", sql.NullString{})
	want := home.ShoppingStatus{WeekStart: "2026-10-12", Label: "Week 42 · Oct 12 – 18", Ready: true}
	if h := e.at(t, "2026-10-16 09:00"); h.ShoppingList != want {
		t.Errorf("shoppingList = %+v, want %+v", h.ShoppingList, want)
	}

	sent := "2026-10-17T13:00:00.000Z"
	list("2026-10-19", sql.NullString{String: sent, Valid: true})
	h := e.at(t, "2026-10-16 09:00")
	if s := h.ShoppingList; s.WeekStart != "2026-10-19" || s.Label != "Week 43 · Oct 19 – 25" || !s.Ready || s.SentToChatAt == nil || *s.SentToChatAt != sent {
		t.Errorf("shoppingList = %+v, want the upcoming week's, sent at %s", s, sent)
	}

	// Late Sunday evening locally is Monday in UTC: the current week is
	// still the week of 2026-10-12, so its upcoming week's list shows.
	if s := e.at(t, "2026-10-18 23:30").ShoppingList; s.WeekStart != "2026-10-19" {
		t.Errorf("late Sunday: shoppingList week = %s, want 2026-10-19", s.WeekStart)
	}
	// On Monday the week of 2026-10-19 is current and has no upcoming
	// list.
	if s := e.at(t, "2026-10-19 08:00").ShoppingList; s.WeekStart != "2026-10-19" || !s.Ready {
		t.Errorf("Monday: shoppingList = %+v, want the current week's", s)
	}
	// A week later neither the current nor the upcoming week has a list.
	want = home.ShoppingStatus{WeekStart: "2026-10-26", Label: "Week 44 · Oct 26 – Nov 1"}
	if s := e.at(t, "2026-10-26 08:00").ShoppingList; s != want {
		t.Errorf("no list: shoppingList = %+v, want %+v", s, want)
	}
}

// Recent updates are unread when a notification was stored after the
// household last saw its updates, or ever when it never has.
func TestRecentUpdatesUnread(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()

	if h := e.at(t, "2026-10-13 12:00"); h.RecentUpdates.Unread {
		t.Error("unread with no notifications")
	}
	if err := e.notifier.Emit(ctx, notify.Event{Type: notify.NewSuggestions, Key: "new_suggestions:1", Title: "New Side quests", URL: "/discover"}); err != nil {
		t.Fatal(err)
	}
	if h := e.at(t, "2026-10-13 12:00"); !h.RecentUpdates.Unread {
		t.Error("not unread with a notification never seen")
	}
	seen := func(at time.Time) {
		t.Helper()
		if _, err := e.st.Writer.ExecContext(ctx, `UPDATE household_settings SET updates_seen_at = ?`, store.Timestamp{Time: at.UTC().Truncate(time.Millisecond)}.String()); err != nil {
			t.Fatal(err)
		}
	}
	seen(time.Now().Add(time.Hour))
	if h := e.at(t, "2026-10-13 12:00"); h.RecentUpdates.Unread {
		t.Error("unread with every notification seen")
	}
	seen(time.Now().Add(-time.Hour))
	if h := e.at(t, "2026-10-13 12:00"); !h.RecentUpdates.Unread {
		t.Error("not unread with a notification stored after updates were seen")
	}
}

func TestNewServiceRequiresServices(t *testing.T) {
	t.Parallel()
	if _, err := home.NewService(nil, nil, nil, nil, nil); err == nil {
		t.Fatal("NewService accepted nil services")
	}
}

// store stores event key of type typ in the outbox at instant at, for each
// of channels, as notify stores an event.
func (e *env) store(t *testing.T, typ notify.EventType, key string, at time.Time, channels ...notify.Channel) {
	t.Helper()
	payload, err := json.Marshal(notify.Payload{Event: typ, Title: "Title " + key, Body: "Body " + key, URL: "/u/" + key, Tag: key})
	if err != nil {
		t.Fatal(err)
	}
	ts := store.Timestamp{Time: at.UTC().Truncate(time.Millisecond)}.String()
	for _, ch := range channels {
		if _, err := db.New(e.st.Writer).InsertNotification(t.Context(), db.InsertNotificationParams{
			ID: store.NewID(), EventKey: key, Channel: string(ch), Payload: string(payload),
			NextAttemptAt: sql.NullString{String: ts, Valid: true}, Now: ts,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// Updates are the last ten distinct events, newest first: an event stored
// for push and Google Chat is listed once, whatever became of its delivery.
func TestUpdates(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()

	got, err := e.svc.Updates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("updates with no notifications = %#v, want an empty list", got)
	}

	base := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	for i := range 12 {
		key := fmt.Sprintf("plan_ready:%02d", i)
		e.store(t, notify.PlanReady, key, base.Add(time.Duration(i)*time.Minute), notify.ChannelPush, notify.ChannelChat)
	}
	// A delivered and a failed row of one event are still one event.
	if _, err := e.st.Writer.ExecContext(ctx, `UPDATE notification_outbox SET status = 'sent' WHERE event_key = 'plan_ready:11' AND channel = 'push'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer.ExecContext(ctx, `UPDATE notification_outbox SET status = 'failed' WHERE event_key = 'plan_ready:11' AND channel = 'chat'`); err != nil {
		t.Fatal(err)
	}

	got, err = e.svc.Updates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != home.RecentLimit {
		t.Fatalf("updates = %d, want %d: %+v", len(got), home.RecentLimit, got)
	}
	for i, u := range got {
		n := 11 - i
		key := fmt.Sprintf("plan_ready:%02d", n)
		want := home.Update{
			Type: "plan_ready", Title: "Title " + key, Body: "Body " + key, URL: "/u/" + key,
			CreatedAt: store.Timestamp{Time: base.Add(time.Duration(n) * time.Minute)}.String(),
		}
		if u != want {
			t.Errorf("updates[%d] = %+v, want %+v", i, u, want)
		}
	}
}

// TestUpdatesOfferARecipeOnlyThroughItsDinner: a reminder offers a recipe
// in Recent updates only with the dinner it names, so the app can check it
// with the server before opening it. A reminder of a dinner archived since
// it was planned names none and opens the week's plan, and so does a
// reminder stored with a recipe URL before reminders named their dinner:
// neither opens the dinner's detail unchecked.
func TestUpdatesOfferARecipeOnlyThroughItsDinner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	at := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	// A reminder stored before reminders named their dinner.
	legacy, err := json.Marshal(notify.Payload{
		Event: notify.DayOfReminder, Title: "Tonight: Old Stew · Recipe →", URL: "/dinner/01J9LEGACY",
		Tag: "day_of_reminder:2026-10-13", Week: "2026-10-12",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := store.Timestamp{Time: at}.String()
	if _, err := db.New(e.st.Writer).InsertNotification(ctx, db.InsertNotificationParams{
		ID: store.NewID(), EventKey: "day_of_reminder:2026-10-13", Channel: string(notify.ChannelPush), Payload: string(legacy),
		NextAttemptAt: sql.NullString{String: ts, Valid: true}, Now: ts,
	}); err != nil {
		t.Fatal(err)
	}
	mustDate := func(s string) store.LocalDate {
		d, err := store.ParseLocalDate(s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if err := e.notifier.Emit(ctx, chat.DayOfReminder(mustDate("2026-10-14"), "01J9TACOS", "Tacos", true)); err != nil {
		t.Fatal(err)
	}
	if err := e.notifier.Emit(ctx, chat.DayOfReminder(mustDate("2026-10-15"), "01J9CHILI", "Chili", false)); err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.Updates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]home.Update{}
	for _, u := range got {
		byTitle[u.Title] = u
	}
	if u := byTitle["Tonight: Tacos · Recipe →"]; u.URL != "/dinner/01J9TACOS" || u.Dinner == nil || *u.Dinner != (notify.Dinner{ID: "01J9TACOS", Name: "Tacos"}) {
		t.Errorf("viewable reminder = %+v, want the recipe of its dinner Tacos", u)
	}
	if u := byTitle["Tonight: Chili"]; u.URL != "/plan?week=2026-10-12" || u.Dinner != nil || u.Body != notify.ArchivedBody {
		t.Errorf("archived reminder = %+v, want the week's plan and no recipe", u)
	}
	if u := byTitle["Tonight: Old Stew · Recipe →"]; u.URL != "/plan?week=2026-10-12" || u.Dinner != nil {
		t.Errorf("legacy reminder = %+v, want the week's plan and no recipe", u)
	}
}

// An event emitted to push and Google Chat is one update.
func TestUpdatesEmittedToTwoChannels(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	if err := e.notifier.Emit(ctx, notify.Event{Type: notify.PlanReady, Key: "plan_ready:2026-10-12", Title: "Your week is drafted", Body: "Take a look", URL: "/plan"}); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := e.st.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_outbox`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("outbox rows = %d, want push and chat", rows)
	}
	got, err := e.svc.Updates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != "plan_ready" || got[0].Title != "Your week is drafted" || got[0].Body != "Take a look" || got[0].URL != "/plan" {
		t.Errorf("updates = %+v, want the one plan_ready event", got)
	}
}

// Marking updates seen through the newest clears unread until a newer event
// is stored; marking them seen through an older time does not move the
// mark back, and a time after now is refused.
func TestMarkUpdatesSeen(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	unread := func() bool {
		t.Helper()
		return e.at(t, "2026-10-13 12:00").RecentUpdates.Unread
	}
	first := time.Date(2026, 10, 13, 16, 0, 0, 0, time.UTC)
	e.store(t, notify.NewSuggestions, "new_suggestions:1", first, notify.ChannelPush)
	if !unread() {
		t.Fatal("not unread with an event never seen")
	}
	// The settings were last edited well before: marking updates seen must
	// keep that time and tell no settings subscriber.
	edited := (store.Timestamp{Time: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}).String()
	if _, err := e.st.Writer.ExecContext(ctx, `UPDATE household_settings SET updated_at = ?`, edited); err != nil {
		t.Fatal(err)
	}
	var notified int
	unsubscribe := e.settings.Subscribe(func(household.Settings) { notified++ })
	defer unsubscribe()
	if err := e.svc.MarkUpdatesSeen(ctx, first); err != nil {
		t.Fatal(err)
	}
	if unread() {
		t.Error("unread after marking the newest event seen")
	}

	second := first.Add(time.Millisecond)
	e.store(t, notify.PlanReady, "plan_ready:2", second, notify.ChannelPush, notify.ChannelChat)
	if !unread() {
		t.Error("not unread with an event stored after the mark")
	}
	// Seen again through the older event, as a sheet opened before the
	// newer one arrived would: nothing moves, so it stays unread.
	if err := e.svc.MarkUpdatesSeen(ctx, first); err != nil {
		t.Fatal(err)
	}
	if !unread() {
		t.Error("an older mark hid the newer event")
	}
	if err := e.svc.MarkUpdatesSeen(ctx, second); err != nil {
		t.Fatal(err)
	}
	if unread() {
		t.Error("unread after marking the newer event seen")
	}
	if err := e.svc.MarkUpdatesSeen(ctx, first); err != nil {
		t.Fatal(err)
	}
	settings, err := e.settings.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (store.Timestamp{Time: second}).String(); settings.UpdatesSeenAt == nil || *settings.UpdatesSeenAt != want {
		t.Errorf("updates_seen_at = %v, want %s: an older mark moved it back", settings.UpdatesSeenAt, want)
	}
	if settings.UpdatedBy != nil {
		t.Errorf("updated_by = %q, want unset: marking updates seen is not a settings edit", *settings.UpdatedBy)
	}
	if settings.UpdatedAt != edited {
		t.Errorf("updated_at = %s, want %s: marking updates seen is not a settings edit", settings.UpdatedAt, edited)
	}
	if notified != 0 {
		t.Errorf("settings subscribers told %d times, want none: marking updates seen is not a settings edit", notified)
	}

	// The clock reads 17:00 UTC.
	if err := e.svc.MarkUpdatesSeen(ctx, first.Add(2*time.Hour)); !errors.Is(err, home.ErrSeenInFuture) {
		t.Errorf("mark seen through an hour from now = %v, want ErrSeenInFuture", err)
	}
}

// An event stored again for a channel switched on later is as new as that
// row: the update shows its time, and marking it seen clears unread.
func TestUpdateStoredAgainForAnotherChannel(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	ctx := t.Context()
	first := time.Date(2026, 10, 13, 15, 0, 0, 0, time.UTC)
	later := first.Add(30 * time.Minute)
	e.store(t, notify.PlanReady, "plan_ready:1", first, notify.ChannelPush)
	e.store(t, notify.PlanReady, "plan_ready:1", later, notify.ChannelChat)

	got, err := e.svc.Updates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := (store.Timestamp{Time: later}).String(); len(got) != 1 || got[0].CreatedAt != want {
		t.Fatalf("updates = %+v, want the one event at %s", got, want)
	}
	e.at(t, "2026-10-13 12:00")
	if err := e.svc.MarkUpdatesSeen(ctx, later); err != nil {
		t.Fatal(err)
	}
	if e.at(t, "2026-10-13 12:00").RecentUpdates.Unread {
		t.Error("unread after the event was marked seen through its listed time")
	}
}
