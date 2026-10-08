package shopping

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Archive groupings: the period Archive groups its posts by.
const (
	ByWeek  = "week"
	ByMonth = "month"
	ByYear  = "year"
)

// Freeze moves the Supply run of the week starting weekStart into the
// Supply archive: its status becomes archived and frozen_at is set, after
// which it is read-only (Generate and SetNeeded are ErrFrozen). Freezing a
// frozen list does nothing and is no error.
//
// The week must have ended in the household's time zone: a week not yet
// ended is ErrWeekNotEnded, and a week with no Supply run ErrListNotFound.
//
// A list still untitled is titled first, with archive_title and the same
// conditional write as a KindTitleSupplyRun job, from the week's plan as
// Generate titles it, so its post in the Supply archive gets a title when
// the AI can write one. Titling never fails the freeze: a title that
// cannot be written is logged and the list is frozen untitled, when its
// week label stands in for the title for good. Freeze first waits for the
// title jobs in flight to store their titles or fail, and holds off new
// ones until the list is frozen, so a title a job's archive_title run
// wrote is stored before the freeze, and the freeze runs archive_title
// only when no job is running it. A job that runs after the freeze finds
// the list titled, or frozen untitled, and stores nothing.
func (s *Service) Freeze(ctx context.Context, weekStart store.LocalDate) error {
	if _, err := household.WeekDays(weekStart); err != nil {
		return fmt.Errorf("shopping: freeze: %w", err)
	}
	q := db.New(s.st.Writer)
	thisWeek, err := s.thisWeek(ctx, q)
	if err != nil {
		return err
	}
	if weekStart.String() >= thisWeek.String() {
		return fmt.Errorf("shopping: freeze %s: %w", weekStart, ErrWeekNotEnded)
	}
	s.titling.Lock()
	defer s.titling.Unlock()
	list, err := q.GetShoppingList(ctx, weekStart.String())
	if errors.Is(err, sql.ErrNoRows) {
		return ErrListNotFound
	}
	if err != nil {
		return fmt.Errorf("shopping: freeze: read list: %w", err)
	}
	if frozen(list) {
		return nil
	}
	if !list.Title.Valid {
		p, err := freezeTitleJob(ctx, q, list)
		if err != nil {
			return err
		}
		if _, err := s.storeTitle(ctx, p); err != nil {
			s.logger.WarnContext(ctx, "Supply run frozen untitled; the Supply archive shows its week label",
				"week", list.WeekStart, "list", list.ID, "error", err)
		}
	}
	// A list frozen meanwhile is left as it is: the freeze is done.
	now := store.Now().String()
	if _, err := q.FreezeShoppingList(ctx, db.FreezeShoppingListParams{
		FrozenAt: sql.NullString{String: now, Valid: true}, UpdatedAt: now, ID: list.ID,
	}); err != nil {
		return fmt.Errorf("shopping: freeze %s: %w", weekStart, err)
	}
	return nil
}

// freezeTitleJob is the title job of list as Generate would enqueue it:
// the week's plan revision and its dinners in day order.
func freezeTitleJob(ctx context.Context, q *db.Queries, list db.ShoppingList) (TitleJob, error) {
	plan, err := q.GetWeekPlan(ctx, list.WeekStart)
	if err != nil {
		return TitleJob{}, fmt.Errorf("shopping: freeze: read plan: %w", err)
	}
	days, err := q.ListPlanDays(ctx, plan.ID)
	if err != nil {
		return TitleJob{}, fmt.Errorf("shopping: freeze: read plan days: %w", err)
	}
	names := make([]string, len(days))
	for i, d := range days {
		names[i] = d.Dinner.Name
	}
	return TitleJob{ListID: list.ID, WeekStart: list.WeekStart, Revision: plan.Revision, Dinners: names}, nil
}

// thisWeek returns the Monday of the household's current week, in its
// time zone, by the service's clock.
func (s *Service) thisWeek(ctx context.Context, q *db.Queries) (store.LocalDate, error) {
	settings, err := q.GetHouseholdSettings(ctx)
	if err != nil {
		return store.LocalDate{}, fmt.Errorf("shopping: read household settings: %w", err)
	}
	zone, err := household.LoadZone(settings.Timezone)
	if err != nil {
		return store.LocalDate{}, fmt.Errorf("shopping: household settings: %w", err)
	}
	return household.WeekStart(household.Today(s.now(), zone)), nil
}

// Period is one heading of the Supply archive and its posts, newest first.
type Period struct {
	// Key identifies the period: the ISO week ("2026-W41"), the month
	// ("2026-10") or the year ("2026").
	Key string
	// Heading is the period as shown: the week label, the month and year
	// ("October 2026") or the year ("2026").
	Heading string
	Posts   []Post
}

// Post is one Supply run in the Supply archive.
type Post struct {
	WeekStart string
	// Label is the week label, such as "Week 41 · Oct 5 – 11".
	Label string
	// Title is nil while the list is untitled, when Label stands in.
	Title *string
	// Dinners are the names of the list's dinners, in day order.
	Dinners []string
	// Items is how many items the list has, and OnHand how many of them
	// the household marked "have it".
	Items, OnHand int
	// Current is true for the household's current week.
	Current bool
}

// Archive returns the Supply archive: the Supply run of every week up to
// and including the household's current week, newest first, grouped by
// by, which is ByWeek, ByMonth or ByYear (else ErrInvalidPeriod). A
// week's month and year are those of its Monday, a household-local date,
// so a week belongs to one period whatever the server's zone; the week
// starting Monday 2026-12-28 is in December 2026.
func (s *Service) Archive(ctx context.Context, by string) (out []Period, err error) {
	// period returns the key and heading of the period holding p, whose
	// week starts on Monday monday.
	var period func(monday store.LocalDate, p Post) (string, string)
	switch by {
	case ByWeek:
		period = func(monday store.LocalDate, p Post) (string, string) {
			return household.PeriodKey(monday), p.Label
		}
	case ByMonth:
		period = func(monday store.LocalDate, _ Post) (string, string) {
			y, m, _ := monday.Date()
			return fmt.Sprintf("%04d-%02d", y, int(m)), fmt.Sprintf("%s %d", m, y)
		}
	case ByYear:
		period = func(monday store.LocalDate, _ Post) (string, string) {
			y, _, _ := monday.Date()
			return strconv.Itoa(y), strconv.Itoa(y)
		}
	default:
		return nil, fmt.Errorf("shopping: archive by %q: %w", by, ErrInvalidPeriod)
	}
	tx, err := s.st.Reader.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("shopping: begin read: %w", err)
	}
	defer func() {
		err = errors.Join(err, tx.Rollback())
	}()
	q := db.New(tx)
	thisWeek, err := s.thisWeek(ctx, q)
	if err != nil {
		return nil, err
	}
	lists, err := q.ListShoppingListsThrough(ctx, thisWeek.String())
	if err != nil {
		return nil, fmt.Errorf("shopping: read Supply runs: %w", err)
	}
	out = []Period{}
	for _, row := range lists {
		monday, err := store.ParseLocalDate(row.WeekStart)
		if err != nil {
			return nil, fmt.Errorf("shopping: Supply run %s week: %w", row.ID, err)
		}
		l, err := readList(ctx, q, row)
		if err != nil {
			return nil, err
		}
		p, err := postOf(monday, l, monday == thisWeek)
		if err != nil {
			return nil, err
		}
		k, heading := period(monday, p)
		if n := len(out); n == 0 || out[n-1].Key != k {
			out = append(out, Period{Key: k, Heading: heading})
		}
		out[len(out)-1].Posts = append(out[len(out)-1].Posts, p)
	}
	return out, nil
}

// postOf is l, the Supply run of the week starting monday, as a post in
// the Supply archive.
func postOf(monday store.LocalDate, l List, current bool) (Post, error) {
	label, err := household.WeekLabel(monday)
	if err != nil {
		return Post{}, fmt.Errorf("shopping: Supply run %s: %w", l.ID, err)
	}
	dinners := make([]string, len(l.Groups))
	for i, g := range l.Groups {
		dinners[i] = g.DinnerName
	}
	c := l.Counts()
	return Post{
		WeekStart: l.WeekStart,
		Label:     label,
		Title:     l.Title,
		Dinners:   dinners,
		Items:     c.Total,
		OnHand:    c.Total - c.Needed,
		Current:   current,
	}, nil
}
