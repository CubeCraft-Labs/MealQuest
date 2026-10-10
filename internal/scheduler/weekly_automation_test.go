package scheduler_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat/chattest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// The tests in this file run shopping's shopping_chat_autosend and
// week_rollover schedules, and harness's ai_runs_purge, on the fake clock
// against the real shopping service, Google Chat client (posting to a fake
// webhook), notification preferences and ai_runs purge. The seeded
// autosend is Saturday 08:00 in Chicago.

// w41 is the Monday of 2026-W41.
var w41 = mustLocalDate("2026-10-05")

// shopEnv is a shopping service over a scheduler env's store. Its job
// runner never starts, so Generate's title jobs stay queued and every
// Supply run stays untitled until Freeze titles it, which fails.
type shopEnv struct {
	e     *env
	svc   *shopping.Service
	chat  *chattest.Chat
	cs    *chat.Service
	plans *planEnv
	logs  *syncBuffer
}

// shopping builds a shopping service over e, with Regulars to plan from.
func (e *env) shopping() *shopEnv {
	e.t.Helper()
	s := &shopEnv{e: e, chat: chattest.New(e.t), plans: e.plans(e.pool()), logs: &syncBuffer{}}
	key := make([]byte, secretbox.KeySize)
	rand.Read(key)
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		e.t.Fatal(err)
	}
	s.cs, err = chat.NewService(e.st, box, publicURL, s.chat.Transport())
	if err != nil {
		e.t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(s.logs, nil))
	runner, err := jobs.NewRunner(e.st, logger, 1)
	if err != nil {
		e.t.Fatal(err)
	}
	noTitle := func(context.Context, string, skills.ArchiveTitleRequest) (skills.ArchiveTitle, error) {
		return skills.ArchiveTitle{}, errors.New("no AI in this test")
	}
	s.svc, err = shopping.NewService(e.st, noTitle, runner, notifytest.New(e.t, e.st), s.cs, logger)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

// list plans the week starting weekStart and generates its Supply run.
func (s *shopEnv) list(weekStart store.LocalDate) {
	s.e.t.Helper()
	s.plans.planWeek(weekStart)
	if _, err := s.svc.Generate(context.Background(), weekStart); err != nil {
		s.e.t.Fatal(err)
	}
}

// webhook stores the fake Google Chat webhook.
func (s *shopEnv) webhook() {
	s.e.t.Helper()
	if _, err := s.cs.Put(context.Background(), chattest.Webhook, "Family"); err != nil {
		s.e.t.Fatal(err)
	}
}

// get returns the Supply run of the week starting weekStart.
func (s *shopEnv) get(weekStart store.LocalDate) shopping.List {
	s.e.t.Helper()
	l, err := s.svc.Get(context.Background(), weekStart)
	if err != nil {
		s.e.t.Fatal(err)
	}
	return l
}

// wantLogged fails unless the service logged want and logged no error.
func (s *shopEnv) wantLogged(want string) {
	s.e.t.Helper()
	if logs := s.logs.String(); !strings.Contains(logs, want) || strings.Contains(logs, "level=ERROR") {
		s.e.t.Errorf("logs = %s, want %q and no error", logs, want)
	}
}

// TestChatAutosendSendsComingWeek: at the seeded Saturday 08:00 the
// schedule posts the coming week's Supply run, W43's, to Google Chat
// through SendToChat, in that week's thread, and records sent_to_chat_at;
// its period is claimed in schedule_runs as list_ready:chat:2026-10-19,
// the coming week's start. It posts once.
func TestChatAutosendSendsComingWeek(t *testing.T) {
	e := newEnv(t)
	s := e.shopping()
	s.list(w43)
	s.webhook()

	// Saturday 07:00: the previous Saturday's period is past the
	// catch-up window, so it is recorded as missed.
	r := e.start(at(2026, 10, 17, 7, 0), s.svc.ChatAutosendSchedule())
	r.runUntil(at(2026, 10, 17, 7, 59))
	if n := len(s.chat.Posts()); n != 0 || s.get(w43).SentToChatAt != nil {
		t.Fatalf("before 08:00: %d posts, sent_to_chat_at %v, want none", n, s.get(w43).SentToChatAt)
	}

	r.runUntil(at(2026, 10, 17, 8, 0))
	posts := s.chat.Posts()
	if len(posts) != 1 {
		t.Fatalf("at 08:00: %d posts, want 1", len(posts))
	}
	if !strings.HasPrefix(posts[0].Text(), "Supply run · Week 43 · Oct 19 – 25") {
		t.Errorf("posted %q, want the W43 Supply run", posts[0].Text())
	}
	if got := posts[0].Query.Get("threadKey"); got != chat.ThreadKey(w43) {
		t.Errorf("posted in thread %q, want %q", got, chat.ThreadKey(w43))
	}
	if s.get(w43).SentToChatAt == nil {
		t.Error("sent_to_chat_at not recorded")
	}
	s.wantLogged("Supply run sent to Google Chat")
	s.wantLogged("period=list_ready:chat:2026-10-19")

	r.runUntil(at(2026, 10, 24, 7, 0))
	if n := len(s.chat.Posts()); n != 1 {
		t.Errorf("a week on: %d posts, want 1", n)
	}
	wantKeys(t, "schedule_runs", e.claimed(scheduler.ShoppingChatAutosend), []string{"list_ready:chat:2026-10-12", "list_ready:chat:2026-10-19"})
}

// TestChatAutosendFollowsSettings: chat_autosend_dow and
// chat_autosend_time move the send without a restart, here to Friday
// 18:00, still for the coming week.
func TestChatAutosendFollowsSettings(t *testing.T) {
	e := newEnv(t)
	s := e.shopping()
	s.list(w43)
	s.webhook()

	r := e.start(at(2026, 10, 16, 12, 0), s.svc.ChatAutosendSchedule())
	e.update(household.SettingsPatch{ChatAutosendDow: ptr(5), ChatAutosendTime: ptr("18:00")})
	r.changed()
	r.runUntil(at(2026, 10, 16, 17, 59))
	if n := len(s.chat.Posts()); n != 0 {
		t.Fatalf("before Friday 18:00: %d posts, want 0", n)
	}
	r.runUntil(at(2026, 10, 16, 18, 0))
	if n := len(s.chat.Posts()); n != 1 || s.get(w43).SentToChatAt == nil {
		t.Fatalf("at Friday 18:00: %d posts, sent_to_chat_at %v, want 1 and set", n, s.get(w43).SentToChatAt)
	}
	r.runUntil(at(2026, 10, 17, 9, 0))
	if n := len(s.chat.Posts()); n != 1 {
		t.Errorf("Saturday: %d posts, want still 1", n)
	}
}

// TestChatAutosendSkips: the schedule posts nothing, records no send and
// fails nothing when the coming week's Supply run was already sent, when
// the week has no Supply run, when list_ready is switched off for Google
// Chat, and when no webhook is stored. Each period is still claimed and
// the reason logged.
func TestChatAutosendSkips(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, s *shopEnv)
		// posts is how many posts the setup itself made.
		posts  int
		reason string
	}{
		{
			name: "already sent",
			setup: func(t *testing.T, s *shopEnv) {
				s.list(w43)
				s.webhook()
				if _, err := s.svc.SendToChat(context.Background(), w43, false); err != nil {
					t.Fatal(err)
				}
			},
			posts:  1,
			reason: shopping.AutosendAlreadySent,
		},
		{
			name: "no list",
			setup: func(t *testing.T, s *shopEnv) {
				s.list(w42)
				s.webhook()
			},
			reason: shopping.AutosendNoList,
		},
		{
			name: "chat off for list_ready",
			setup: func(t *testing.T, s *shopEnv) {
				s.list(w43)
				s.webhook()
				if _, err := s.e.st.Writer.Exec(`UPDATE notification_prefs SET chat = 0 WHERE event = 'list_ready'`); err != nil {
					t.Fatal(err)
				}
			},
			reason: shopping.AutosendChatOff,
		},
		{
			name: "no webhook",
			setup: func(t *testing.T, s *shopEnv) {
				s.list(w43)
			},
			reason: shopping.AutosendNotConfigured,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			s := e.shopping()
			c.setup(t, s)
			var sentBefore *string
			if l, err := s.svc.Get(context.Background(), w43); err == nil {
				sentBefore = l.SentToChatAt
			}

			r := e.start(at(2026, 10, 17, 7, 0), s.svc.ChatAutosendSchedule())
			r.runUntil(at(2026, 10, 17, 9, 0))
			if n := len(s.chat.Posts()); n != c.posts {
				t.Errorf("posts = %d, want %d", n, c.posts)
			}
			if l, err := s.svc.Get(context.Background(), w43); err == nil && !equalPtr(l.SentToChatAt, sentBefore) {
				t.Errorf("sent_to_chat_at = %v, want %v", l.SentToChatAt, sentBefore)
			}
			s.wantLogged("Supply run not sent to Google Chat: " + c.reason)
			wantKeys(t, "schedule_runs", e.claimed(scheduler.ShoppingChatAutosend), []string{"list_ready:chat:2026-10-12", "list_ready:chat:2026-10-19"})
		})
	}
}

// TestChatAutosendFailureSurfaces: a send Google Chat refuses is the
// period's error, logged as such; the period stays claimed and is not
// retried, and sent_to_chat_at is not recorded.
func TestChatAutosendFailureSurfaces(t *testing.T) {
	e := newEnv(t)
	s := e.shopping()
	s.list(w43)
	s.webhook()
	s.chat.SetStatus(500)

	var logs syncBuffer
	sched, err := scheduler.New(e.st, e.settings, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	clock := newFakeClock(at(2026, 10, 17, 7, 0))
	scheduler.SetClock(sched, clock.Now, clock.After)
	if err := sched.Register(s.svc.ChatAutosendSchedule()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		sched.Wait()
	})
	if err := sched.Start(ctx); err != nil {
		t.Fatal(err)
	}
	r := &running{t: t, clock: clock, stop: cancel}
	r.idle()
	r.runUntil(at(2026, 10, 17, 9, 0))

	if n := len(s.chat.Posts()); n != 1 {
		t.Errorf("posts = %d, want the one refused", n)
	}
	if s.get(w43).SentToChatAt != nil {
		t.Error("sent_to_chat_at recorded for a refused send")
	}
	if got := logs.String(); !strings.Contains(got, "level=ERROR") || !strings.Contains(got, "scheduler: run failed") || !strings.Contains(got, "schedule=shopping_chat_autosend") {
		t.Errorf("scheduler logs = %s, want the refused send logged as a failed run", got)
	}
	wantKeys(t, "schedule_runs", e.claimed(scheduler.ShoppingChatAutosend), []string{"list_ready:chat:2026-10-12", "list_ready:chat:2026-10-19"})
}

func equalPtr(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// TestWeekRolloverFreezesPreviousWeek: at Monday 00:05 the schedule
// freezes exactly the week that has just ended into the Supply archive,
// not the week before it nor the new week; the next Monday freezes the
// next week; a Monday whose previous week has no Supply run freezes
// nothing and fails nothing. Its periods are week_rollover:YYYY-Www of the
// Monday.
func TestWeekRolloverFreezesPreviousWeek(t *testing.T) {
	e := newEnv(t)
	s := e.shopping()
	s.list(w41)
	s.list(w42)
	s.list(w43)

	status := func() []string {
		return []string{s.get(w41).Status, s.get(w42).Status, s.get(w43).Status}
	}
	cur, arc := shopping.StatusCurrent, shopping.StatusArchived

	// Sunday 23:00: last Monday's rollover is past the catch-up window and
	// recorded as missed, so W41 is never frozen by it.
	r := e.start(at(2026, 10, 18, 23, 0), s.svc.RolloverSchedule())
	r.runUntil(at(2026, 10, 19, 0, 4))
	wantKeys(t, "before Monday 00:05", status(), []string{cur, cur, cur})

	r.runUntil(at(2026, 10, 19, 0, 5))
	wantKeys(t, "at Monday 00:05", status(), []string{cur, arc, cur})
	if s.get(w42).Title != nil {
		t.Errorf("W42 title = %q, want frozen untitled", *s.get(w42).Title)
	}
	if _, err := s.svc.Generate(context.Background(), w42); !errors.Is(err, shopping.ErrFrozen) {
		t.Errorf("generate W42 after the rollover: %v, want ErrFrozen", err)
	}

	r.runUntil(at(2026, 10, 26, 0, 5))
	wantKeys(t, "next Monday", status(), []string{cur, arc, arc})

	r.runUntil(at(2026, 11, 2, 0, 5))
	s.wantLogged("week rollover froze nothing: the week has no Supply run")
	wantKeys(t, "schedule_runs", e.claimed(scheduler.WeekRollover), []string{"2026-W42", "2026-W43", "2026-W44", "2026-W45"})
}

// aiRun stores an ai_runs row created at created and returns its id.
func (e *env) aiRun(created time.Time) string {
	e.t.Helper()
	id := store.NewID()
	ts := store.Timestamp{Time: created.UTC()}.String()
	if err := db.New(e.st.Writer).CreateAIRun(context.Background(), db.CreateAIRunParams{
		ID: id, Skill: "draft_week", SkillVersion: "1", Provider: "anthropic", Model: "claude-test",
		Temperature: 0.7, Status: "ok", RequestRedacted: "{}", ResponseRedacted: sql.NullString{String: "{}", Valid: true},
		CreatedAt: ts, UpdatedAt: ts,
	}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// aiRuns returns the ids of the ai_runs rows, oldest first.
func (e *env) aiRuns() []string {
	e.t.Helper()
	rows, err := e.st.Reader.Query(`SELECT id FROM ai_runs ORDER BY created_at`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			e.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		e.t.Fatal(err)
	}
	return ids
}

// TestAIRunsPurgeDaily: every day at 03:00 the schedule deletes the
// ai_runs rows created more than 30 days before, and no younger row; its
// periods are ai_runs_purge:YYYY-MM-DD.
func TestAIRunsPurgeDaily(t *testing.T) {
	e := newEnv(t)
	e.aiRun(at(2026, 9, 10, 12, 0)) // older than Sep 11 03:00
	edge := e.aiRun(at(2026, 9, 12, 2, 59))
	young := e.aiRun(at(2026, 9, 12, 3, 1))
	recent := e.aiRun(at(2026, 10, 11, 12, 0))

	sch, err := harness.PurgeSchedule(e.st, slog.New(slog.NewTextHandler(&syncBuffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// Monday 02:00: the catch-up of Sunday 03:00 purges before Sep 11 03:00.
	r := e.start(at(2026, 10, 12, 2, 0), sch)
	wantKeys(t, "after the catch-up", e.aiRuns(), []string{edge, young, recent})

	r.runUntil(at(2026, 10, 12, 2, 59))
	wantKeys(t, "before 03:00", e.aiRuns(), []string{edge, young, recent})
	r.runUntil(at(2026, 10, 12, 3, 0))
	wantKeys(t, "at 03:00", e.aiRuns(), []string{young, recent})
	r.runUntil(at(2026, 10, 13, 3, 0))
	wantKeys(t, "next day", e.aiRuns(), []string{recent})
	wantKeys(t, "schedule_runs", e.claimed(scheduler.AIRunsPurge), []string{"2026-10-11", "2026-10-12", "2026-10-13"})
}

// TestPurgeScheduleRejectsNil: the purge schedule needs a store and a
// logger.
func TestPurgeScheduleRejectsNil(t *testing.T) {
	e := newEnv(t)
	if _, err := harness.PurgeSchedule(nil, slog.Default()); err == nil {
		t.Error("nil store accepted")
	}
	if _, err := harness.PurgeSchedule(e.st, nil); err == nil {
		t.Error("nil logger accepted")
	}
}
