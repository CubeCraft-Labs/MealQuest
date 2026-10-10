package scheduler_test

import (
	"context"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/scheduler"
)

// The tests in this file run plan's rotation_check schedule on the fake
// clock against the real plan service and notification outbox. The plan of
// the week of Monday Oct 12 (w42) has poolNames, all Regulars, Monday to
// Sunday; a test that needs a Quest-log dinner swaps Lemon Orzo in for
// Monday. The household's review time is the seeded 09:00, in Chicago.

// orzoDay is the Monday Lemon Orzo is planned on.
var orzoDay = mustLocalDate("2026-10-12")

// orzoWeek plans w42 with the Quest-log dinner Lemon Orzo on Monday in
// place of Beef Chili, and returns its id.
func (p *planEnv) orzoWeek() string {
	p.e.t.Helper()
	id := p.questLog("Lemon Orzo", 30)
	p.planWeek(w42)
	if _, err := p.svc.Swap(context.Background(), w42, orzoDay.String(), id); err != nil {
		p.e.t.Fatal(err)
	}
	return id
}

// rotationReviews returns the keys of the rotation_review notifications
// stored, oldest first. The seeded preference sends rotation_review to push
// only, so there is one row per event.
func (e *env) rotationReviews() []string {
	e.t.Helper()
	var keys []string
	for _, r := range notifytest.Rows(e.t, e.st, notify.RotationReview) {
		if r.Channel != notify.ChannelPush {
			e.t.Fatalf("rotation_review row %+v, want push only", r)
		}
		keys = append(keys, r.Key)
	}
	return keys
}

// status returns the status of the dinner name.
func (p *planEnv) status(name string) string {
	p.e.t.Helper()
	var s string
	if err := p.e.st.Reader.QueryRow(`SELECT status FROM dinners WHERE id = ?`, p.ids[name]).Scan(&s); err != nil {
		p.e.t.Fatal(err)
	}
	return s
}

// pendingReviews returns the plan dates of the pending Rotation checks of
// the dinner name.
func (p *planEnv) pendingReviews(name string) []string {
	p.e.t.Helper()
	rows, err := p.e.st.Reader.Query(`SELECT plan_date FROM rotation_reviews WHERE dinner_id = ? AND status = 'pending' ORDER BY plan_date`, p.ids[name])
	if err != nil {
		p.e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			p.e.t.Fatal(err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		p.e.t.Fatal(err)
	}
	return out
}

// wantOrzoTried fails unless Lemon Orzo, id, is tried with one pending
// Rotation check for orzoDay and exactly one rotation_review asking about
// it, which opens Home.
func (p *planEnv) wantOrzoTried(id string) {
	p.e.t.Helper()
	if got := p.status("Lemon Orzo"); got != "tried" {
		p.e.t.Errorf("Lemon Orzo status = %s, want tried", got)
	}
	wantKeys(p.e.t, "Lemon Orzo pending Rotation checks", p.pendingReviews("Lemon Orzo"), []string{orzoDay.String()})
	key := "rotation_review:" + id + ":" + orzoDay.String()
	wantKeys(p.e.t, "rotation_review", p.e.rotationReviews(), []string{key})
	got := notifytest.Rows(p.e.t, p.e.st, notify.RotationReview)[0].Payload
	want := notify.Payload{Event: notify.RotationReview, Title: "How was Lemon Orzo? Keep it in rotation?", URL: "/", Tag: key, Week: w42.String()}
	if got != want {
		p.e.t.Errorf("rotation_review = %+v, want %+v", got, want)
	}
}

// TestRotationCheckMarksQuestLogDinnerTried: at review_time 09:00 the day
// after a Quest-log dinner was planned, the schedule, period
// rotation_check:YYYY-MM-DD, marks it tried, opens its Rotation check and
// emits one rotation_review. It never makes the dinner a Regular.
func TestRotationCheckMarksQuestLogDinnerTried(t *testing.T) {
	e := newEnv(t)
	p := e.plans(e.pool())
	id := p.orzoWeek()

	// Tuesday 08:00: the catch-up of Monday 09:00 checks Sunday, unplanned.
	r := e.start(at(2026, 10, 13, 8, 0), p.svc.RotationCheckSchedule())
	r.runUntil(at(2026, 10, 13, 8, 59))
	wantKeys(t, "before 09:00", e.rotationReviews(), nil)
	if got := p.status("Lemon Orzo"); got != "quest_log" {
		t.Fatalf("Lemon Orzo status before 09:00 = %s, want quest_log", got)
	}

	r.runUntil(at(2026, 10, 13, 9, 0))
	p.wantOrzoTried(id)

	// Days later, still tried: only the household's keep makes a Regular.
	r.runUntil(at(2026, 10, 16, 12, 0))
	p.wantOrzoTried(id)
	wantKeys(t, "schedule_runs", e.claimed(scheduler.RotationCheck), []string{
		"rotation_check:2026-10-12", "rotation_check:2026-10-13", "rotation_check:2026-10-14",
		"rotation_check:2026-10-15", "rotation_check:2026-10-16",
	})
}

// TestRotationCheckLeavesRegulars: a day planning only Regulars marks
// nothing tried, opens no Rotation check and emits nothing.
func TestRotationCheckLeavesRegulars(t *testing.T) {
	e := newEnv(t)
	p := e.plans(e.pool())
	p.planWeek(w42)

	r := e.start(at(2026, 10, 13, 8, 0), p.svc.RotationCheckSchedule())
	r.runUntil(at(2026, 10, 13, 9, 0))
	wantKeys(t, "rotation_review", e.rotationReviews(), nil)
	if got := p.status("Beef Chili"); got != "regular" {
		t.Errorf("Beef Chili status = %s, want regular", got)
	}
	wantKeys(t, "Beef Chili pending Rotation checks", p.pendingReviews("Beef Chili"), nil)
	wantKeys(t, "schedule_runs", e.claimed(scheduler.RotationCheck), []string{"rotation_check:2026-10-12", "rotation_check:2026-10-13"})
}

// TestRotationCheckNoDuplicateSameDay: a restart the same day finds the
// period claimed, and MarkTried run again for the same day finds the
// Rotation check open, so neither opens a second check or notification.
func TestRotationCheckNoDuplicateSameDay(t *testing.T) {
	e := newEnv(t)
	ids := e.pool()
	p := e.plans(ids)
	id := p.orzoWeek()

	r := e.start(at(2026, 10, 13, 8, 0), p.svc.RotationCheckSchedule())
	r.runUntil(at(2026, 10, 13, 9, 0))
	p.wantOrzoTried(id)
	r.stop()
	p.stop()

	p2 := e.plans(ids)
	r2 := e.start(at(2026, 10, 13, 9, 30), p2.svc.RotationCheckSchedule())
	r2.runUntil(at(2026, 10, 13, 23, 59))
	p2.wantOrzoTried(id)

	if n, err := p2.svc.MarkTried(context.Background(), orzoDay); err != nil || n != 0 {
		t.Fatalf("MarkTried(%s) again = %d, %v, want 0", orzoDay, n, err)
	}
	p2.wantOrzoTried(id)
	wantKeys(t, "schedule_runs", e.claimed(scheduler.RotationCheck), []string{"rotation_check:2026-10-12", "rotation_check:2026-10-13"})
}

// TestRotationCheckCatchUpAtBoot: started 23 hours after a missed 09:00,
// the scheduler runs that day's Rotation check once at boot.
func TestRotationCheckCatchUpAtBoot(t *testing.T) {
	e := newEnv(t)
	p := e.plans(e.pool())
	id := p.orzoWeek()

	e.start(at(2026, 10, 14, 8, 0), p.svc.RotationCheckSchedule())
	p.wantOrzoTried(id)
	wantKeys(t, "schedule_runs", e.claimed(scheduler.RotationCheck), []string{"rotation_check:2026-10-13"})
}

// TestRotationCheckFollowsReviewTimeWithoutRestart: a review_time change
// takes effect in the running scheduler. Moved from 09:00 to 10:30, the
// Rotation check does not run at 09:00 and runs at 10:30.
func TestRotationCheckFollowsReviewTimeWithoutRestart(t *testing.T) {
	e := newEnv(t)
	p := e.plans(e.pool())
	id := p.orzoWeek()

	r := e.start(at(2026, 10, 13, 8, 0), p.svc.RotationCheckSchedule())
	e.update(household.SettingsPatch{ReviewTime: ptr("10:30")})
	r.changed()
	r.runUntil(at(2026, 10, 13, 10, 29))
	wantKeys(t, "before 10:30", e.rotationReviews(), nil)
	if got := p.status("Lemon Orzo"); got != "quest_log" {
		t.Fatalf("Lemon Orzo status before 10:30 = %s, want quest_log", got)
	}
	r.runUntil(at(2026, 10, 13, 10, 30))
	p.wantOrzoTried(id)
	wantKeys(t, "schedule_runs", e.claimed(scheduler.RotationCheck), []string{"rotation_check:2026-10-12", "rotation_check:2026-10-13"})
}
