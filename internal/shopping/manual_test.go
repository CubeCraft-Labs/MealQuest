package shopping_test

import (
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
)

// TestGenerateIgnoresManualNight: a plan day named with a free-text label
// adds no shopping group, no skipped dinner and no title-job dinner. The
// other nights' ingredients are unchanged.
func TestGenerateIgnoresManualNight(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.dinner(t, "Pot Roast", ing{"chuck roast", "3", "lb"})
	e.dinner(t, "Chicken Fajitas", ing{"chicken breast", "500", "g"})
	e.dinner(t, "Spaghetti Bolognese", ing{"spaghetti", "1", "lb"})
	e.dinner(t, "Sheet Pan Salmon", ing{"salmon fillets", "4", ""})
	e.dinner(t, "Pad Thai", ing{"rice noodles", "8", "oz"}, ing{"bean sprouts", "2", "cups"})
	e.dinner(t, "Shawarma Bowls", ing{"chicken thighs", "2", "lb"})
	e.plan(t, "Beef Chili", "Pot Roast", "Chicken Fajitas", "Spaghetti Bolognese", "Sheet Pan Salmon", "Pad Thai", "Shawarma Bowls")
	// Saturday is Pad Thai. Replace that night with a one-off label.
	res, err := e.st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET dinner_id = NULL, manual_label = 'mummy dogs', is_new = 0 WHERE date = ?`, "2026-10-17")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("updated %d plan days, want 1", n)
	}

	g := e.generate(t)
	for _, grp := range g.List.Groups {
		if grp.DinnerName == "Pad Thai" || grp.DinnerName == "mummy dogs" || grp.Date == "2026-10-17" {
			t.Errorf("group = %+v, want no Saturday group", grp)
		}
	}
	if len(g.List.Groups) != 6 {
		t.Errorf("groups = %d, want 6", len(g.List.Groups))
	}
	for _, skip := range g.Skipped {
		if skip.Date == "2026-10-17" || skip.DinnerName == "mummy dogs" || skip.DinnerName == "Pad Thai" {
			t.Errorf("skipped = %+v, want the manual night ignored", skip)
		}
	}
	if g.TitleJobID == nil {
		t.Fatal("generation enqueued no title job")
	}
	var payload string
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT payload FROM jobs WHERE id = ?`, *g.TitleJobID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "mummy dogs") || strings.Contains(payload, "Pad Thai") {
		t.Errorf("title job payload = %s, want neither the label nor the replaced dinner", payload)
	}
}

// TestGenerateAllManualOrClearedWeekEnqueuesNoTitleJob: a week whose nights
// are all manual labels or cleared has no dinner names. archive_title
// rejects that empty list, so generating the Supply run must not enqueue a
// title job. The list is still stored, untitled, with nothing to buy.
// (SHOP-01)
func TestGenerateAllManualOrClearedWeekEnqueuesNoTitleJob(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	names := make([]string, len(weekDates))
	for i := range names {
		names[i] = "Beef Chili"
	}
	e.plan(t, names...)
	manual := map[string]string{
		weekDates[0]: "leftovers",
		weekDates[1]: "mummy dogs",
		weekDates[2]: "soup night",
		weekDates[3]: "pizza from the freezer",
		weekDates[4]: "breakfast for dinner",
	}
	for date, label := range manual {
		res, err := e.st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET dinner_id = NULL, manual_label = ?, is_new = 0, calendar_event_id = NULL WHERE date = ?`, label, date)
		if err != nil {
			t.Fatal(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("manual %s updated %d rows, want 1", date, n)
		}
	}
	for _, date := range weekDates[5:] {
		res, err := e.st.Writer.ExecContext(t.Context(), `UPDATE plan_days SET dinner_id = NULL, manual_label = NULL, is_new = 0, calendar_event_id = NULL WHERE date = ?`, date)
		if err != nil {
			t.Fatal(err)
		}
		if n, _ := res.RowsAffected(); n != 1 {
			t.Fatalf("clear %s updated %d rows, want 1", date, n)
		}
	}

	g := e.generate(t)
	if len(g.List.Groups) != 0 || len(g.Skipped) != 0 || g.List.Title != nil {
		t.Fatalf("list groups %d skipped %d title %v, want an empty untitled Supply run", len(g.List.Groups), len(g.Skipped), g.List.Title)
	}
	var n int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM jobs WHERE kind = ?`, shopping.KindTitleSupplyRun).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 || g.TitleJobID != nil {
		t.Fatalf("title jobs = %d id %v, want none: an empty dinner list is not a title job", n, g.TitleJobID)
	}
}
