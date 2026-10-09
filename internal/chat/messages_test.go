package chat

import (
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

func mustDate(t *testing.T, s string) store.LocalDate {
	t.Helper()
	d, err := store.ParseLocalDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// payloadOf is the payload notify stores for ev.
func payloadOf(ev notify.Event) notify.Payload {
	p := notify.Payload{Event: ev.Type, Title: ev.Title, Body: ev.Body, URL: ev.URL, Tag: ev.Key}
	if !ev.Week.IsZero() {
		p.Week = ev.Week.String()
	}
	return p
}

func TestMessages(t *testing.T) {
	t.Parallel()
	week := mustDate(t, "2026-10-12")
	for _, tc := range []struct {
		name               string
		ev                 notify.Event
		key, url, week     string
		title, body, chatT string
	}{
		{
			name:  "plan ready",
			ev:    PlanReady(week, week, []string{"Tacos", "Bolognese", "Tikka masala", "Lemon orzo", "Bibimbap", "Shakshuka", "Chili"}),
			key:   "plan_ready:2026-10-12",
			url:   "/plan",
			week:  "2026-10-12",
			title: "This week's plan is ready",
			body:  "Tacos, Bolognese, Tikka masala + 4 more",
			chatT: "This week's plan is ready: Tacos, Bolognese, Tikka masala + 4 more. Open the plan →",
		},
		{
			name:  "plan ready with three dinners",
			ev:    PlanReady(week, week, []string{"Tacos", "Bolognese", "Tikka masala"}),
			key:   "plan_ready:2026-10-12",
			url:   "/plan",
			week:  "2026-10-12",
			title: "This week's plan is ready",
			body:  "Tacos, Bolognese, Tikka masala",
			chatT: "This week's plan is ready: Tacos, Bolognese, Tikka masala. Open the plan →",
		},
		{
			name:  "plan ready for next week",
			ev:    PlanReady(week, mustDate(t, "2026-10-05"), []string{"Tacos", "Bolognese", "Tikka masala", "Lemon orzo"}),
			key:   "plan_ready:2026-10-12",
			url:   "/plan",
			week:  "2026-10-12",
			title: "Next week's plan is ready",
			body:  "Tacos, Bolognese, Tikka masala + 1 more",
			chatT: "Next week's plan is ready: Tacos, Bolognese, Tikka masala + 1 more. Open the plan →",
		},
		{
			name:  "plan ready for a later week",
			ev:    PlanReady(mustDate(t, "2026-10-26"), mustDate(t, "2026-10-05"), []string{"Tacos"}),
			key:   "plan_ready:2026-10-26",
			url:   "/plan",
			week:  "2026-10-26",
			title: "The plan for the week of Oct 26 is ready",
			body:  "Tacos",
			chatT: "The plan for the week of Oct 26 is ready: Tacos. Open the plan →",
		},
		{
			name:  "list ready",
			ev:    ListReady(week, 19, 7),
			key:   "list_ready:2026-10-12",
			url:   "/plan/shopping",
			week:  "2026-10-12",
			title: "This week's Supply run is ready",
			body:  "19 items to buy for 7 dinners",
			chatT: "This week's Supply run is ready: 19 items to buy for 7 dinners. Open the Supply run →",
		},
		{
			name:  "list ready singular",
			ev:    ListReady(week, 1, 1),
			key:   "list_ready:2026-10-12",
			url:   "/plan/shopping",
			week:  "2026-10-12",
			title: "This week's Supply run is ready",
			body:  "1 item to buy for 1 dinner",
			chatT: "This week's Supply run is ready: 1 item to buy for 1 dinner. Open the Supply run →",
		},
		{
			name:  "day-of reminder",
			ev:    DayOfReminder(mustDate(t, "2026-10-15"), "01J9Z8Y7X6W5V4T3S2R1Q0P9N8", "Tacos"),
			key:   "day_of_reminder:2026-10-15",
			url:   "/dinner/01J9Z8Y7X6W5V4T3S2R1Q0P9N8",
			week:  "2026-10-12",
			title: "Tonight's dinner",
			body:  "Tacos",
			chatT: "Tonight's dinner: Tacos. Open the recipe →",
		},
		{
			name:  "new suggestions",
			ev:    NewSuggestions("01J9BATCH", week, []string{"Lemon orzo", "Bibimbap", "Shakshuka", "Pho", "Paella"}),
			key:   "new_suggestions:01J9BATCH",
			url:   "/discover",
			week:  "2026-10-12",
			title: "New Side quests are in",
			body:  "Lemon orzo, Bibimbap, Shakshuka + 2 more",
			chatT: "New Side quests are in: Lemon orzo, Bibimbap, Shakshuka + 2 more. Open Discover →",
		},
		{
			name:  "rotation review",
			ev:    RotationReview("01J9DINNER", "Lemon orzo", week),
			key:   "rotation_review:01J9DINNER",
			url:   "/",
			week:  "2026-10-12",
			title: "Keep it in rotation?",
			body:  "You tried Lemon orzo. Make it a Regular?",
			chatT: "Keep it in rotation? You tried Lemon orzo. Make it a Regular? Open MealQuest →",
		},
	} {
		ev := tc.ev
		if ev.Key != tc.key || ev.URL != tc.url || ev.Week.String() != tc.week || ev.Title != tc.title || ev.Body != tc.body {
			t.Errorf("%s: event = %+v, want key %q url %q week %s title %q body %q", tc.name, ev, tc.key, tc.url, tc.week, tc.title, tc.body)
		}
		if got := Text(payloadOf(ev)); got != tc.chatT {
			t.Errorf("%s: Text = %q, want %q", tc.name, got, tc.chatT)
		}
	}
}

func TestSupplyRunText(t *testing.T) {
	t.Parallel()
	got := SupplyRunText("Week 42 · Oct 12 – 18", []SupplyRunGroup{
		{Dinner: "Tacos", Items: []SupplyRunItem{{Name: "ground beef", Qty: "1 lb"}, {Name: "tortillas"}}},
		{Dinner: "Bolognese"},
		{Dinner: "Pho", Items: []SupplyRunItem{{Name: "rice noodles", Qty: "200 g"}}},
	})
	want := "Supply run · Week 42 · Oct 12 – 18\n\n*Tacos*\n• ground beef — 1 lb\n• tortillas\n\n*Pho*\n• rice noodles — 200 g\n\nOpen the Supply run →"
	if got != want {
		t.Errorf("SupplyRunText =\n%s\nwant\n%s", got, want)
	}
	got = SupplyRunText("Cozy week", []SupplyRunGroup{{Dinner: "Tacos"}})
	if want := "Supply run · Cozy week\n\nNothing left to buy.\n\nOpen the Supply run →"; got != want {
		t.Errorf("SupplyRunText with nothing needed = %q, want %q", got, want)
	}
}

func TestTextWithoutBody(t *testing.T) {
	t.Parallel()
	got := Text(notify.Payload{Event: notify.PlanReady, Title: "This week's plan is ready", URL: "/plan"})
	if want := "This week's plan is ready. Open the plan →"; got != want {
		t.Errorf("Text = %q, want %q", got, want)
	}
}

func TestThreadKey(t *testing.T) {
	t.Parallel()
	for date, want := range map[string]string{
		"2026-10-12": "mealquest-week-2026-W42",
		"2026-12-28": "mealquest-week-2026-W53",
		"2027-01-04": "mealquest-week-2027-W01",
	} {
		if got := ThreadKey(mustDate(t, date)); got != want {
			t.Errorf("ThreadKey(%s) = %q, want %q", date, got, want)
		}
	}
}
