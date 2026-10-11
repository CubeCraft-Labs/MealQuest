package shopping_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// zoned makes zone the household's time zone and at the instant the
// service's clock reads.
func (e *env) zoned(t *testing.T, zone, at string) {
	t.Helper()
	if err := household.SeedSettings(t.Context(), e.st, zone); err != nil {
		t.Fatal(err)
	}
	e.at(t, at)
}

// at sets the service's clock to the RFC 3339 instant at.
func (e *env) at(t *testing.T, at string) {
	t.Helper()
	now, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatal(err)
	}
	shopping.SetClock(e.svc, func() time.Time { return now })
}

// frozenState returns the week's list's status and frozen_at; frozen_at is
// "" while NULL.
func (e *env) frozenState(t *testing.T, monday store.LocalDate) (string, string) {
	t.Helper()
	var status string
	var frozenAt sql.NullString
	if err := e.st.Reader.QueryRow(`SELECT status, frozen_at FROM shopping_lists WHERE week_start = ?`, monday.String()).Scan(&status, &frozenAt); err != nil {
		t.Fatal(err)
	}
	return status, frozenAt.String
}

// weekList plans the dinners named names on the days of the week starting
// monday, in order, and generates its Supply run.
func (e *env) weekList(t *testing.T, monday string, names ...string) shopping.List {
	t.Helper()
	start := mustDate(monday)
	days, err := household.WeekDays(start)
	if err != nil {
		t.Fatal(err)
	}
	q := db.New(e.st.Writer)
	now := store.Now().String()
	planID := store.NewID()
	if err := q.CreateWeekPlan(t.Context(), db.CreateWeekPlanParams{ID: planID, WeekStart: monday, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		if err := q.PutPlanDay(t.Context(), db.PutPlanDayParams{
			ID: store.NewID(), PlanID: planID, Date: days[i].String(),
			DinnerID: sql.NullString{String: e.ids[name], Valid: true}, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	g, err := e.svc.Generate(t.Context(), start)
	if err != nil {
		t.Fatal(err)
	}
	return g.List
}

// endedWeek is the env with the tests' week generated, in New York, on
// the Monday after it, so the week has ended.
func endedWeek(t *testing.T) (*env, shopping.List) {
	t.Helper()
	e := newEnv(t)
	e.zoned(t, "America/New_York", "2026-10-19T12:00:00Z")
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"}, ing{"onion", "1", ""})
	e.plan(t, "Beef Chili")
	return e, e.generate(t).List
}

// TestFreeze freezes an ended week's list: it is archived with frozen_at,
// read back read-only, and freezing it again changes nothing.
func TestFreeze(t *testing.T) {
	e, l := endedWeek(t)

	if err := e.svc.Freeze(t.Context(), week); err != nil {
		t.Fatal(err)
	}
	status, frozenAt := e.frozenState(t, week)
	if status != shopping.StatusArchived || frozenAt == "" {
		t.Fatalf("after Freeze: status %q, frozen_at %q; want archived and set", status, frozenAt)
	}
	got, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != shopping.StatusArchived {
		t.Errorf("read back status = %q, want archived", got.Status)
	}
	wantLines(t, "read back", lines(got), []string{
		"0 2026-10-12 Beef Chili: 0 ground beef [1 lb] needed",
		"0 2026-10-12 Beef Chili: 1 onion [1] needed",
	})

	time.Sleep(2 * time.Millisecond) // a second freeze would stamp a later frozen_at
	if err := e.svc.Freeze(t.Context(), week); err != nil {
		t.Fatalf("second Freeze = %v, want no error", err)
	}
	if s, again := e.frozenState(t, week); s != shopping.StatusArchived || again != frozenAt {
		t.Errorf("after second Freeze: status %q, frozen_at %q; want archived, %q kept", s, again, frozenAt)
	}

	if _, err := e.svc.SetNeeded(t.Context(), l.Groups[0].Items[1].ID, false); !errors.Is(err, shopping.ErrFrozen) {
		t.Errorf("SetNeeded after Freeze = %v, want ErrFrozen", err)
	}
	if _, err := e.svc.Generate(t.Context(), week); !errors.Is(err, shopping.ErrFrozen) {
		t.Errorf("Generate after Freeze = %v, want ErrFrozen", err)
	}
	wantLines(t, "stored list", e.stored(t), []string{
		"0 2026-10-12 Beef Chili: 0 ground beef [1 lb] needed",
		"0 2026-10-12 Beef Chili: 1 onion [1] needed",
	})
}

// TestFreezeWeekNotEnded: a week has not ended until Monday comes in the
// household's zone. At 23:30 on the Sunday in Los Angeles it is already
// Monday in UTC, and the list is still not frozen; at 00:05 on the Monday
// it is.
func TestFreezeWeekNotEnded(t *testing.T) {
	e := newEnv(t)
	e.zoned(t, "America/Los_Angeles", "2026-10-19T06:30:00Z") // Sun 18 Oct 23:30 PDT
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.plan(t, "Beef Chili")
	e.generate(t)

	if err := e.svc.Freeze(t.Context(), week); !errors.Is(err, shopping.ErrWeekNotEnded) {
		t.Fatalf("Freeze on the Sunday = %v, want ErrWeekNotEnded", err)
	}
	if status, frozenAt := e.frozenState(t, week); status != shopping.StatusCurrent || frozenAt != "" {
		t.Fatalf("after refused Freeze: status %q, frozen_at %q; want current, unset", status, frozenAt)
	}
	if err := e.svc.Freeze(t.Context(), mustDate("2026-10-19")); !errors.Is(err, shopping.ErrWeekNotEnded) {
		t.Errorf("Freeze of a later week = %v, want ErrWeekNotEnded", err)
	}

	e.at(t, "2026-10-19T07:05:00Z") // Mon 19 Oct 00:05 PDT
	if err := e.svc.Freeze(t.Context(), week); err != nil {
		t.Fatalf("Freeze on the Monday = %v", err)
	}
	if status, _ := e.frozenState(t, week); status != shopping.StatusArchived {
		t.Errorf("status = %q, want archived", status)
	}
}

func TestFreezeWeekWithoutList(t *testing.T) {
	e := newEnv(t)
	e.zoned(t, "America/New_York", "2026-10-19T12:00:00Z")
	if err := e.svc.Freeze(t.Context(), week); !errors.Is(err, shopping.ErrListNotFound) {
		t.Fatalf("Freeze = %v, want ErrListNotFound", err)
	}
	if err := e.svc.Freeze(t.Context(), mustDate("2026-10-13")); err == nil {
		t.Fatal("Freeze of a Tuesday succeeded")
	}
}

// TestFreezeTitlesUntitledList: a list still untitled when its week is
// frozen is titled by archive_title first, from the plan's dinners, and
// frozen with that title. Its title job, still queued, runs after the
// freeze, finds the title and succeeds without asking the model again.
func TestFreezeTitlesUntitledList(t *testing.T) {
	e := newEnv(t)
	e.zoned(t, "America/New_York", "2026-10-19T12:00:00Z")
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.dinner(t, "Fish Tacos")
	e.plan(t, "Fish Tacos", "Beef Chili")
	g := e.generate(t) // the runner is not started: its title job waits
	e.model.QueueGenerate(titled("Tacos then Chili"))

	if err := e.svc.Freeze(t.Context(), week); err != nil {
		t.Fatal(err)
	}
	wantTitle(t, "stored title", e.storedTitle(t), "Tacos then Chili")
	if status, frozenAt := e.frozenState(t, week); status != shopping.StatusArchived || frozenAt == "" {
		t.Errorf("status %q, frozen_at %q; want archived and set", status, frozenAt)
	}
	reqs := e.model.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d model calls, want 1", len(reqs))
	}
	if want := "Title the week whose dinners are, in day order from Monday:\n- Fish Tacos\n- Beef Chili"; reqs[0].Messages[0].Content != want {
		t.Errorf("user message = %q, want %q", reqs[0].Messages[0].Content, want)
	}

	e.start(t)
	wantJob(t, e.waitTitle(t, g), jobs.StatusSucceeded, "")
	if n := len(e.model.Requests()); n != 1 {
		t.Errorf("%d model calls, want 1: the job finds the list titled", n)
	}
	wantTitle(t, "stored title after the job", e.storedTitle(t), "Tacos then Chili")
}

// pauseTitle makes the env's first archive_title run pause once it has
// returned, before its title is stored, until the returned release is
// called or the test ends; paused is closed as it pauses.
func (e *env) pauseTitle(t *testing.T) (paused <-chan struct{}, release func()) {
	t.Helper()
	p, r := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	e.afterTitle = func() {
		if first.CompareAndSwap(false, true) {
			close(p)
			select {
			case <-r:
			case <-t.Context().Done():
			}
		}
	}
	var released sync.Once
	return p, func() { released.Do(func() { close(r) }) }
}

// waitClosed fails t unless c is closed within 10 seconds.
func waitClosed(t *testing.T, what string, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// TestFreezeWaitsForTitleJob: a freeze that starts while a title job's
// archive_title run has returned but its title is not yet stored waits for
// the job to store it, freezes the list with that title, and asks the
// model nothing itself.
func TestFreezeWaitsForTitleJob(t *testing.T) {
	e := newEnv(t)
	e.zoned(t, "America/New_York", "2026-10-19T12:00:00Z")
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.plan(t, "Beef Chili")
	e.model.QueueGenerate(titled("Chili Night"))
	paused, release := e.pauseTitle(t)
	e.start(t)
	g := e.generate(t)
	waitClosed(t, "the title job's archive_title run", paused)

	froze := make(chan error, 1)
	go func() { froze <- e.svc.Freeze(context.Background(), week) }()
	var err error
	select {
	case err = <-froze:
		t.Errorf("Freeze = %v before the title job stored its title, want it to wait", err)
		release()
	case <-time.After(time.Second):
		release()
		err = <-froze
	}
	if err != nil {
		t.Fatal(err)
	}

	wantJob(t, e.waitTitle(t, g), jobs.StatusSucceeded, "")
	wantTitle(t, "stored title", e.storedTitle(t), "Chili Night")
	if status, _ := e.frozenState(t, week); status != shopping.StatusArchived {
		t.Errorf("status = %q, want archived", status)
	}
	if n := len(e.model.Requests()); n != 1 {
		t.Errorf("%d model calls, want 1: the freeze waits for the job's title", n)
	}
}

// TestTitleJobWaitsForFreeze: a title job that starts while a freeze's
// archive_title run has returned but its title is not yet stored waits for
// the freeze, then finds the list titled and asks the model nothing.
func TestTitleJobWaitsForFreeze(t *testing.T) {
	e := newEnv(t)
	e.zoned(t, "America/New_York", "2026-10-19T12:00:00Z")
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.plan(t, "Beef Chili")
	e.model.QueueGenerate(titled("Chili Night"))
	g := e.generate(t) // the runner is not started: its title job waits
	paused, release := e.pauseTitle(t)

	froze := make(chan error, 1)
	go func() { froze <- e.svc.Freeze(context.Background(), week) }()
	waitClosed(t, "the freeze's archive_title run", paused)
	e.start(t)
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, err := e.runner.Get(t.Context(), *g.TitleJobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == jobs.StatusRunning {
			break
		}
		if job.Status != jobs.StatusQueued || time.Now().After(deadline) {
			t.Fatalf("title job %s, want it running while the freeze is paused", job.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	release()
	if err := <-froze; err != nil {
		t.Fatal(err)
	}

	wantJob(t, e.waitTitle(t, g), jobs.StatusSucceeded, "")
	wantTitle(t, "stored title", e.storedTitle(t), "Chili Night")
	if status, _ := e.frozenState(t, week); status != shopping.StatusArchived {
		t.Errorf("status = %q, want archived", status)
	}
	if n := len(e.model.Requests()); n != 1 {
		t.Errorf("%d model calls, want 1: the job waits for the freeze's title", n)
	}
}

// TestFreezeUntitledWhenTitleFails: when archive_title cannot title the
// list, it is frozen untitled all the same, the failure is logged, and the
// Supply archive shows its week label for good.
func TestFreezeUntitledWhenTitleFails(t *testing.T) {
	e, _ := endedWeek(t) // nothing queued on the model: archive_title fails

	if err := e.svc.Freeze(t.Context(), week); err != nil {
		t.Fatal(err)
	}
	wantUntitled(t, "stored title", e.storedTitle(t))
	if status, _ := e.frozenState(t, week); status != shopping.StatusArchived {
		t.Errorf("status = %q, want archived", status)
	}
	if !strings.Contains(e.log.String(), "Supply run frozen untitled") {
		t.Errorf("log lacks the failure:\n%s", e.log.String())
	}
	periods, err := e.svc.Archive(t.Context(), shopping.ByWeek)
	if err != nil {
		t.Fatal(err)
	}
	if len(periods) != 1 || len(periods[0].Posts) != 1 || periods[0].Posts[0].Title != nil ||
		periods[0].Posts[0].Label != "Week 42 · Oct 12 – 18" {
		t.Errorf("archive = %+v, want the untitled week under its label", periods)
	}
}

// TestFreezeKeepsTitle: a titled list is frozen with its title and the
// model is not asked again.
func TestFreezeKeepsTitle(t *testing.T) {
	e, _ := endedWeek(t)
	if _, err := e.st.Writer.Exec(`UPDATE shopping_lists SET title = 'Chili Night'`); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.Freeze(t.Context(), week); err != nil {
		t.Fatal(err)
	}
	wantTitle(t, "stored title", e.storedTitle(t), "Chili Night")
	if n := len(e.model.Requests()); n != 0 {
		t.Errorf("%d model calls, want none", n)
	}
}

// postLine renders p as "weekStart label | title | dinners | items/on hand
// | current".
func postLine(p shopping.Post) string {
	title := "-"
	if p.Title != nil {
		title = *p.Title
	}
	cur := ""
	if p.Current {
		cur = " | current"
	}
	if !p.Frozen {
		cur += " | open"
	}
	return p.WeekStart + " " + p.Label + " | " + title + " | " + strings.Join(p.Dinners, ", ") + " | " +
		strconv.Itoa(p.Items) + " items, " + strconv.Itoa(p.OnHand) + " on hand" + cur
}

// periodLines renders periods one line per heading and post.
func periodLines(periods []shopping.Period) []string {
	var out []string
	for _, p := range periods {
		out = append(out, "# "+p.Key+" "+p.Heading)
		for _, post := range p.Posts {
			out = append(out, postLine(post))
		}
	}
	return out
}

// TestArchiveGroups reads the Supply archive across a month and a year
// boundary in Auckland, where it is already Monday 4 January 2027 while
// UTC still reads Sunday 3 January: that week is current, the next one is
// not yet in the archive, and each week falls in the month and year of
// its Monday.
func TestArchiveGroups(t *testing.T) {
	e := newEnv(t)
	e.zoned(t, "Pacific/Auckland", "2027-01-03T12:00:00Z") // Mon 4 Jan 2027 01:00 NZDT
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"}, ing{"onion", "1", ""}, ing{"kidney beans", "2", "cans"})
	e.dinner(t, "Pad Thai", ing{"rice noodles", "8", "oz"}, ing{"lime", "2", ""})
	e.dinner(t, "Fish Tacos") // no recipe: no group, not counted
	e.dinner(t, "Pot Roast", ing{"carrots", "4", ""})

	nov := e.weekList(t, "2026-11-30", "Pot Roast") // Nov 30 – Dec 6
	e.weekList(t, "2026-12-07", "Pad Thai", "Fish Tacos")
	dec := e.weekList(t, "2026-12-28", "Beef Chili", "Fish Tacos", "Pad Thai") // ISO 2026-W53, into January
	jan := e.weekList(t, "2027-01-04", "Pad Thai", "Beef Chili")
	e.weekList(t, "2027-01-11", "Pot Roast") // next week: not in the archive yet
	e.haveIt(t, dec, "Beef Chili", "onion")
	e.haveIt(t, dec, "Pad Thai", "lime")
	e.haveIt(t, jan, "Pad Thai", "lime")
	e.haveIt(t, nov, "Pot Roast", "carrots")
	if _, err := e.st.Writer.Exec(`UPDATE shopping_lists SET title = 'Chili, Noodles & Co' WHERE week_start = '2026-12-28'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Writer.Exec(`UPDATE shopping_lists SET status = 'archived', frozen_at = '2027-01-03T11:05:00.000Z' WHERE week_start < '2027-01-04'`); err != nil {
		t.Fatal(err)
	}

	posts := map[string]string{
		"jan": "2027-01-04 Week 1 · Jan 4 – 10 | - | Pad Thai, Beef Chili | 5 items, 1 on hand | current | open",
		"dec": "2026-12-28 Week 53 · Dec 28 – Jan 3 | Chili, Noodles & Co | Beef Chili, Pad Thai | 5 items, 2 on hand",
		"d07": "2026-12-07 Week 50 · Dec 7 – 13 | - | Pad Thai | 2 items, 0 on hand",
		"nov": "2026-11-30 Week 49 · Nov 30 – Dec 6 | - | Pot Roast | 1 items, 1 on hand",
	}
	for _, tc := range []struct {
		by   string
		want []string
	}{
		{shopping.ByWeek, []string{
			"# 2027-W01 Week 1 · Jan 4 – 10", posts["jan"],
			"# 2026-W53 Week 53 · Dec 28 – Jan 3", posts["dec"],
			"# 2026-W50 Week 50 · Dec 7 – 13", posts["d07"],
			"# 2026-W49 Week 49 · Nov 30 – Dec 6", posts["nov"],
		}},
		{shopping.ByMonth, []string{
			"# 2027-01 January 2027", posts["jan"],
			"# 2026-12 December 2026", posts["dec"], posts["d07"],
			"# 2026-11 November 2026", posts["nov"],
		}},
		{shopping.ByYear, []string{
			"# 2027 2027", posts["jan"],
			"# 2026 2026", posts["dec"], posts["d07"], posts["nov"],
		}},
	} {
		got, err := e.svc.Archive(t.Context(), tc.by)
		if err != nil {
			t.Fatalf("Archive(%s): %v", tc.by, err)
		}
		if lines := periodLines(got); !reflect.DeepEqual(lines, tc.want) {
			t.Errorf("Archive(%s) =\n  %s\nwant\n  %s", tc.by, strings.Join(lines, "\n  "), strings.Join(tc.want, "\n  "))
		}
	}
}

func TestArchiveEmptyAndInvalidPeriod(t *testing.T) {
	e := newEnv(t)
	e.zoned(t, "America/New_York", "2026-10-19T12:00:00Z")
	got, err := e.svc.Archive(t.Context(), shopping.ByMonth)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("Archive of no Supply runs = %#v, %v; want empty", got, err)
	}
	for _, by := range []string{"", "day", "Week", "months"} {
		if _, err := e.svc.Archive(t.Context(), by); !errors.Is(err, shopping.ErrInvalidPeriod) {
			t.Errorf("Archive(%q) = %v, want ErrInvalidPeriod", by, err)
		}
	}
}
