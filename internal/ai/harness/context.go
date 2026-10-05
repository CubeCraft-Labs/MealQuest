package harness

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// PassedLimit is how many of the most recently passed Side quests the
// household context holds.
const PassedLimit = 60

// PlanWeeks is how many ISO weeks of plans, ending with the current week,
// the household context holds.
const PlanWeeks = 4

// HouseholdContext is what a skill knows about the household: layer 3 of
// its system prompt.
type HouseholdContext struct {
	// Today is the household-local date, in Timezone.
	Today store.LocalDate
	// Timezone is the household's IANA zone.
	Timezone string
	// Regulars are in name order. They are never trimmed.
	Regulars []RegularDinner
	// QuestLog holds the Quest log dinner names, in name order.
	QuestLog []string
	// Passed holds the names of the most recently passed Side quests,
	// oldest first.
	Passed []string
	// Plans are the planned dinners of the last PlanWeeks weeks, oldest
	// first.
	Plans []PlannedDinner
}

// RegularDinner is one Regular. Empty and zero fields are unknown.
type RegularDinner struct {
	Name     string
	Cuisine  string
	Method   string
	Minutes  int
	Favorite bool
}

// PlannedDinner is the dinner planned for one household-local date.
type PlannedDinner struct {
	Date store.LocalDate
	Name string
}

// BuildContext reads the household context from st at instant now, in the
// household's time zone, and trims it until its block (see Block) is at
// most maxChars characters long. Trimming drops the oldest plans first,
// then the oldest passed Side quests; Regulars, the Quest log and today's
// date are never trimmed, so a budget too small for them is an error.
// About four characters make a token.
func BuildContext(ctx context.Context, st *store.Store, now time.Time, maxChars int) (HouseholdContext, error) {
	if maxChars <= 0 {
		return HouseholdContext{}, fmt.Errorf("household context: budget %d characters is not positive", maxChars)
	}
	h, err := readContext(ctx, st, now)
	if err != nil {
		return HouseholdContext{}, fmt.Errorf("household context: %w", err)
	}
	for {
		block, err := h.Block()
		if err != nil {
			return HouseholdContext{}, err
		}
		size := utf8.RuneCountInString(block)
		switch {
		case size <= maxChars:
			return h, nil
		case len(h.Plans) > 0:
			h.Plans = h.Plans[1:]
		case len(h.Passed) > 0:
			h.Passed = h.Passed[1:]
		default:
			return HouseholdContext{}, fmt.Errorf("household context: today, Regulars and Quest log need %d characters, over the budget of %d", size, maxChars)
		}
	}
}

// readContext reads the untrimmed context in one read transaction, so every
// part comes from the same snapshot.
func readContext(ctx context.Context, st *store.Store, now time.Time) (h HouseholdContext, err error) {
	tx, err := st.Reader.BeginTx(ctx, nil)
	if err != nil {
		return h, fmt.Errorf("begin read: %w", err)
	}
	defer func() {
		err = errors.Join(err, tx.Rollback())
	}()
	q := db.New(tx)

	settings, err := q.GetHouseholdSettings(ctx)
	if err != nil {
		return h, fmt.Errorf("read household settings: %w", err)
	}
	loc, err := household.LoadZone(settings.Timezone)
	if err != nil {
		return h, fmt.Errorf("household settings: %w", err)
	}
	h.Timezone = settings.Timezone
	h.Today = household.Today(now, loc)

	regulars, err := q.ListDinnersByStatus(ctx, "regular")
	if err != nil {
		return h, fmt.Errorf("read Regulars: %w", err)
	}
	h.Regulars = []RegularDinner{}
	for _, d := range regulars {
		h.Regulars = append(h.Regulars, RegularDinner{
			Name:     d.Name,
			Cuisine:  nullString(d.Cuisine),
			Method:   nullString(d.Method),
			Minutes:  int(d.MinutesTotal.Int64),
			Favorite: d.IsFavorite == 1,
		})
	}

	questLog, err := q.ListDinnersByStatus(ctx, "quest_log")
	if err != nil {
		return h, fmt.Errorf("read Quest log: %w", err)
	}
	h.QuestLog = []string{}
	for _, d := range questLog {
		h.QuestLog = append(h.QuestLog, d.Name)
	}

	passed, err := q.ListRecentDeclinedNames(ctx, PassedLimit)
	if err != nil {
		return h, fmt.Errorf("read passed Side quests: %w", err)
	}
	slices.Reverse(passed) // newest first from the query; oldest first here
	h.Passed = append([]string{}, passed...)

	thisWeek := household.WeekStart(h.Today)
	plans, err := q.ListPlannedDinners(ctx, db.ListPlannedDinnersParams{
		FromDate: addDays(thisWeek, -7*(PlanWeeks-1)).String(),
		ToDate:   addDays(thisWeek, 7).String(),
	})
	if err != nil {
		return h, fmt.Errorf("read plans: %w", err)
	}
	h.Plans = []PlannedDinner{}
	for _, p := range plans {
		date, err := store.ParseLocalDate(p.Date)
		if err != nil {
			return h, fmt.Errorf("read plans: %w", err)
		}
		h.Plans = append(h.Plans, PlannedDinner{Date: date, Name: p.Name})
	}
	return h, nil
}

// Sections returns the context as the sections of the household context
// block, in a fixed order.
func (h HouseholdContext) Sections() []ContextSection {
	var regulars, plans []string
	for _, r := range h.Regulars {
		regulars = append(regulars, r.line())
	}
	for _, p := range h.Plans {
		plans = append(plans, fmt.Sprintf("%s %s: %s", p.Date, weekday(p.Date).String()[:3], p.Name))
	}
	return []ContextSection{
		{Title: "Today", Lines: []string{fmt.Sprintf("%s %s (%s)", weekday(h.Today), h.Today, h.Timezone)}},
		{Title: "Regulars", Lines: regulars},
		{Title: "Quest log", Lines: h.QuestLog},
		{Title: "Passed, oldest first", Lines: h.Passed},
		{Title: "Recent plans", Lines: plans},
	}
}

// Block renders the context as layer 3 of a system prompt.
func (h HouseholdContext) Block() (string, error) {
	return contextBlock(h.Sections())
}

// line renders r as "Name (cuisine, method, N min, favorite)", leaving out
// what is unknown.
func (r RegularDinner) line() string {
	var attrs []string
	if r.Cuisine != "" {
		attrs = append(attrs, r.Cuisine)
	}
	if r.Method != "" {
		attrs = append(attrs, r.Method)
	}
	if r.Minutes > 0 {
		attrs = append(attrs, fmt.Sprintf("%d min", r.Minutes))
	}
	if r.Favorite {
		attrs = append(attrs, "favorite")
	}
	if len(attrs) == 0 {
		return r.Name
	}
	return fmt.Sprintf("%s (%s)", r.Name, strings.Join(attrs, ", "))
}

// weekday returns the day of the week of d.
func weekday(d store.LocalDate) time.Weekday {
	y, m, day := d.Date()
	return time.Date(y, m, day, 0, 0, 0, 0, time.UTC).Weekday()
}

// addDays returns d moved by n calendar days.
func addDays(d store.LocalDate, n int) store.LocalDate {
	y, m, day := d.Date()
	return store.LocalDateOf(time.Date(y, m, day+n, 0, 0, 0, 0, time.UTC))
}

func nullString(s sql.NullString) string {
	if !s.Valid {
		return ""
	}
	return s.String
}
