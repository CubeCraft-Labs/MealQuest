package skills

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// weekOf asks for the week of Monday 2026-10-12 from a pool of eight, two
// of them planned last week.
var weekOf = WeekRequest{
	Context: harness.HouseholdContext{
		Today:    date(2026, 10, 8),
		Timezone: "America/New_York",
		Regulars: []harness.RegularDinner{
			{Name: "Beef Chili", Cuisine: "Tex-Mex", Method: "crockpot", Minutes: 240},
			{Name: "Chicken Fajitas", Cuisine: "Tex-Mex", Method: "stovetop", Minutes: 30, Favorite: true},
		},
		QuestLog: []string{"Pad Thai"},
		Passed:   []string{},
		Plans: []harness.PlannedDinner{
			{Date: date(2026, 10, 5), Name: "Chicken Fajitas"},
			{Date: date(2026, 10, 6), Name: "Fish Tacos"},
		},
	},
	WeekStart: date(2026, 10, 12),
	Pool: []Candidate{
		{ID: "chili", Name: "Beef Chili", Kind: KindRegular, Method: "crockpot", Minutes: 240},
		{ID: "fajitas", Name: "Chicken Fajitas", Kind: KindRegular, Method: "stovetop", Minutes: 30},
		{ID: "bolognese", Name: "Spaghetti Bolognese", Kind: KindRegular, Method: "stovetop", Minutes: 45},
		{ID: "salmon", Name: "Sheet Pan Salmon", Kind: KindRegular, Method: "oven"},
		{ID: "tacos", Name: "Fish Tacos", Kind: KindRegular, Minutes: 25},
		{ID: "padthai", Name: "Pad Thai", Kind: KindQuestLog, Method: "stovetop", Minutes: 35},
		{ID: "roast", Name: "Pot Roast", Kind: KindRegular, Method: "crockpot"},
		{ID: "shawarma", Name: "Chicken Shawarma Bowls", Kind: KindQuestLog, Method: "oven", Minutes: 40},
	},
	LastWeek:   []string{"fajitas", "tacos", "gone"},
	DinnerTime: "17:30",
}

// weekDates are the dates of weekOf, Monday to Sunday.
var weekDates = []string{"2026-10-12", "2026-10-13", "2026-10-14", "2026-10-15", "2026-10-16", "2026-10-17", "2026-10-18"}

// week renders a reply planning ids on dates, in order.
func week(note string, dates []string, ids ...string) string {
	var days []string
	for i, id := range ids {
		days = append(days, fmt.Sprintf(`{"date":%q,"dinner_id":%q}`, dates[i], id))
	}
	return fmt.Sprintf(`{"days":[%s],"note":%q}`, strings.Join(days, ","), note)
}

// clean plans no dinner of last week.
var clean = []string{"chili", "padthai", "bolognese", "salmon", "shawarma", "roast", "fajitas"}

// wantWeek is the output for a reply planning ids on weekDates with note.
func wantWeek(note string, ids ...string) Week {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	out := Week{Note: note}
	for i, id := range ids {
		day := WeekDay{Date: weekDates[i], DinnerID: id, IsNew: id == "padthai" || id == "shawarma"}
		if id == "chili" {
			y, m, d := date(2026, 10, 12+i).Date()
			day.StartBy = time.Date(y, m, d, 13, 30, 0, 0, newYork)
		}
		out.Days = append(out.Days, day)
	}
	return out
}

// draft runs DraftWeek on in with f and checks f was called exactly calls
// times, each at the household temperature with the draft_week schema.
func draft(t *testing.T, f *llm.Fake, in WeekRequest, calls int) (Week, []llm.Request, error) {
	t.Helper()
	out, err := DraftWeek(context.Background(), testConfig(t, f), "2026-W42", in)
	reqs := f.Requests()
	if len(reqs) != calls {
		t.Fatalf("Generate called %d times, want %d", len(reqs), calls)
	}
	if g, _, _ := f.Remaining(); g != 0 {
		t.Fatalf("%d scripted Generate results unused", g)
	}
	for i, req := range reqs {
		if req.Temperature == nil || *req.Temperature != 0.7 {
			t.Fatalf("request %d temperature = %v, want 0.7", i+1, req.Temperature)
		}
		if req.Schema == nil || req.Schema.Name != "draft_week" {
			t.Fatalf("request %d schema = %v, want draft_week", i+1, req.Schema)
		}
	}
	return out, reqs, err
}

func assertWeek(t *testing.T, got, want Week) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %+v\nwant     %+v", got, want)
	}
}

func TestDraftWeekValid(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(week("  A cosy week,\nwith Pad Thai midweek. ", weekDates, clean...)))
	out, reqs, err := draft(t, &f, weekOf, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertWeek(t, out, wantWeek("A cosy week, with Pad Thai midweek.", clean...))
	if !out.Days[0].StartBy.Equal(time.Date(2026, 10, 12, 17, 30, 0, 0, time.UTC)) {
		t.Errorf("Beef Chili StartBy = %v, want 13:30 EDT", out.Days[0].StartBy)
	}

	system, user := reqs[0].System, reqs[0].Messages[0].Content
	golden(t, "draft_week_valid.system.golden", system)
	golden(t, "draft_week_valid.user.golden", user)
	for _, part := range []string{
		"## Household context",
		"### Candidate pool",
		`- id "chili": Beef Chili (Regular, crockpot, 240 min)`,
		`- id "fajitas": Chicken Fajitas (Regular, stovetop, 30 min, planned last week)`,
		`- id "salmon": Sheet Pan Salmon (Regular, oven)`,
		`- id "tacos": Fish Tacos (Regular, 25 min, planned last week)`,
		`- id "padthai": Pad Thai (Quest log, stovetop, 35 min)`,
		"## Output contract",
		`"dinner_id"`,
	} {
		if !strings.Contains(system, part) {
			t.Errorf("system prompt lacks %q", part)
		}
	}
	if strings.Index(system, "### Candidate pool") > strings.Index(system, "## Output contract") {
		t.Error("the output contract is not after the candidate pool")
	}
	want := "Draft the week of Mon 2026-10-12, Tue 2026-10-13, Wed 2026-10-14, Thu 2026-10-15, Fri 2026-10-16, Sat 2026-10-17, Sun 2026-10-18."
	if user != want {
		t.Errorf("user message = %q, want %q", user, want)
	}
}

// repaired checks a first reply is repaired once, quoting it and the
// problems wanted.
func repaired(t *testing.T, first string, want ...string) {
	t.Helper()
	var f llm.Fake
	f.QueueGenerate(reply(first), reply(week("Fixed.", weekDates, clean...)))
	out, reqs, err := draft(t, &f, weekOf, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertWeek(t, out, wantWeek("Fixed.", clean...))
	repair := reqs[1].Messages
	if len(repair) != 3 || repair[1].Content != first {
		t.Fatalf("repair turn does not quote the first reply: %+v", repair)
	}
	for _, w := range want {
		if !strings.Contains(repair[2].Content, w) {
			t.Fatalf("repair message = %q, want it to contain %q", repair[2].Content, w)
		}
	}
}

func TestDraftWeekUnknownIDRepaired(t *testing.T) {
	ids := slices.Clone(clean)
	ids[2] = "lasagna"
	repaired(t, week("A week.", weekDates, ids...),
		`day 3 dinner_id "lasagna" is not in the candidate pool; use only ids from it`)
}

func TestDraftWeekDuplicateRepaired(t *testing.T) {
	ids := slices.Clone(clean)
	ids[5] = "chili"
	repaired(t, week("A week.", weekDates, ids...),
		`day 6 dinner_id "chili" (Beef Chili) is also on day 1; plan each dinner at most once`)
}

func TestDraftWeekWrongDatesTwiceFails(t *testing.T) {
	sundayFirst := append([]string{"2026-10-11"}, weekDates[:6]...)
	var f llm.Fake
	f.QueueGenerate(reply(week("A week.", sundayFirst, clean...)), reply(week("A week.", sundayFirst, clean...)))
	out, _, err := draft(t, &f, weekOf, 2)
	var bad *harness.BadOutputError
	if !errors.As(err, &bad) || !errors.Is(err, llm.ErrBadOutput) {
		t.Fatalf("err = %v, want a BadOutputError", err)
	}
	if !slices.Contains(bad.Repair, `day 1 date "2026-10-11" is not 2026-10-12; give the dates 2026-10-12 to 2026-10-18 in order`) {
		t.Fatalf("repair problems = %q", bad.Repair)
	}
	if out.Days != nil {
		t.Fatalf("output = %+v, want none", out)
	}
}

func TestDraftWeekNotEnoughDinners(t *testing.T) {
	small := weekOf
	small.Pool = slices.Clone(weekOf.Pool[:6])
	var f llm.Fake
	_, _, err := draft(t, &f, small, 0)
	var short *NotEnoughDinnersError
	if !errors.As(err, &short) || short.Pool != 6 {
		t.Fatalf("err = %v, want a NotEnoughDinnersError for a pool of 6", err)
	}
	if err.Error() != "not enough dinners: the pool holds 6, a week needs 7" {
		t.Fatalf("err = %q", err)
	}
}

func TestDraftWeekLastWeekRepeats(t *testing.T) {
	// The pool has six dinners not planned last week, so the week needs one
	// of last week's two and may plan it without a warning.
	one := []string{"chili", "padthai", "bolognese", "salmon", "shawarma", "roast", "fajitas"}
	both := []string{"chili", "padthai", "bolognese", "tacos", "shawarma", "roast", "fajitas"}
	for name, tc := range map[string]struct {
		ids  []string
		warn []string
	}{
		"needed repeat":  {one, nil},
		"avoidable both": {both, []string{"repeats 2 of last week's dinners (Fish Tacos, Chicken Fajitas) though the pool needed at most 1"}},
	} {
		t.Run(name, func(t *testing.T) {
			var f llm.Fake
			f.QueueGenerate(reply(week("A week.", weekDates, tc.ids...)))
			out, _, err := draft(t, &f, weekOf, 1)
			if err != nil {
				t.Fatal(err)
			}
			want := wantWeek("A week.", tc.ids...)
			want.Warnings = tc.warn
			assertWeek(t, out, want)
		})
	}
}

func TestWeekValidate(t *testing.T) {
	s, err := newDraftWeek(weekOf)
	if err != nil {
		t.Fatal(err)
	}
	reply := func(note string, ids ...string) Week {
		var w Week
		if err := w.UnmarshalJSON([]byte(week(note, weekDates, ids...))); err != nil {
			t.Fatal(err)
		}
		return w
	}
	if p := s.Validate(reply(strings.Repeat("é", NoteLimit), clean...)); len(p) != 0 {
		t.Fatalf("valid output has problems: %q", p)
	}
	for name, tc := range map[string]struct {
		out  Week
		want []string
	}{
		"empty": {Week{}, []string{"days has 0 entries; give exactly 7, one for each date from 2026-10-12 to 2026-10-18"}},
		"six days": {reply("", clean[:6]...), []string{
			"days has 6 entries; give exactly 7, one for each date from 2026-10-12 to 2026-10-18",
		}},
		"eight days": {
			Week{Days: append(reply("", clean...).Days, WeekDay{Date: "2026-10-19", DinnerID: "tacos"})},
			[]string{"days has 8 entries; give exactly 7, one for each date from 2026-10-12 to 2026-10-18"},
		},
		"long note": {reply(strings.Repeat("a", NoteLimit+1), clean...), []string{"note has 161 characters; keep it to at most 160"}},
		"dates out of order": {
			Week{Days: []WeekDay{
				{weekDates[1], "chili", false, time.Time{}}, {weekDates[0], "padthai", false, time.Time{}},
				{weekDates[2], "bolognese", false, time.Time{}}, {weekDates[3], "salmon", false, time.Time{}},
				{weekDates[4], "shawarma", false, time.Time{}}, {weekDates[5], "roast", false, time.Time{}},
				{weekDates[6], "fajitas", false, time.Time{}},
			}},
			[]string{
				`day 1 date "2026-10-13" is not 2026-10-12; give the dates 2026-10-12 to 2026-10-18 in order`,
				`day 2 date "2026-10-12" is not 2026-10-13; give the dates 2026-10-12 to 2026-10-18 in order`,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := s.Validate(tc.out); !slices.Equal(got, tc.want) {
				t.Fatalf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}

func TestDraftWeekRequestRejected(t *testing.T) {
	with := func(change func(*WeekRequest)) WeekRequest {
		in := weekOf
		in.Pool = slices.Clone(weekOf.Pool)
		change(&in)
		return in
	}
	for name, tc := range map[string]struct {
		in   WeekRequest
		want string
	}{
		"not a Monday": {with(func(in *WeekRequest) { in.WeekStart = date(2026, 10, 13) }),
			"draft_week: household: week start 2026-10-13 is a Tuesday, want Monday"},
		"bad kind": {with(func(in *WeekRequest) { in.Pool[0].Kind = "suggestion" }),
			`draft_week: pool dinner 1: chili kind "suggestion" is not regular or quest_log`},
		"bad method": {with(func(in *WeekRequest) { in.Pool[1].Method = "air fryer" }),
			`draft_week: pool dinner 2: fajitas method "air fryer" is not one of stovetop, oven, crockpot, grill, other`},
		"empty id": {with(func(in *WeekRequest) { in.Pool[2].ID = " " }),
			`draft_week: pool dinner 3: id " " is empty or has a line break`},
		"id twice": {with(func(in *WeekRequest) { in.Pool[3].ID = "chili" }),
			`draft_week: pool dinner 4: id "chili" is in the pool twice`},
		"bad dinner time": {with(func(in *WeekRequest) { in.DinnerTime = "5:30pm" }),
			`draft_week: dinner time: household: invalid time "5:30pm": want HH:MM`},
	} {
		t.Run(name, func(t *testing.T) {
			var f llm.Fake
			if _, _, err := draft(t, &f, tc.in, 0); err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
