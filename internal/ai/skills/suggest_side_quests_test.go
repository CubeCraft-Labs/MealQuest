package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

func date(y int, m time.Month, d int) store.LocalDate {
	return store.LocalDateOf(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
}

// household asks for two Side quests, so four are requested.
var household = SideQuestsRequest{
	Context: harness.HouseholdContext{
		Today:    date(2026, 10, 6),
		Timezone: "America/New_York",
		Regulars: []harness.RegularDinner{
			{Name: "Beef Chili", Cuisine: "Tex-Mex", Method: "crockpot", Minutes: 240},
			{Name: "Chicken Fajitas", Cuisine: "Tex-Mex", Method: "stovetop", Minutes: 30, Favorite: true},
			{Name: "Spaghetti Bolognese", Cuisine: "Italian", Method: "stovetop", Minutes: 45},
		},
		QuestLog: []string{"Pad Thai"},
		Passed:   []string{"Chicken Tikka Masala"},
		Plans: []harness.PlannedDinner{
			{Date: date(2026, 10, 5), Name: "Chicken Fajitas"},
			{Date: date(2026, 10, 6), Name: "Beef Chili"},
		},
	},
	Needed:   2,
	Pending:  []string{"Shrimp Scampi"},
	Declined: []string{"Chicken Tikka Masala", "Fish Tacos"},
}

// sideQuest renders one Side quest of a reply.
func sideQuest(name, reason string, cited ...string) string {
	c, _ := json.Marshal(append([]string{}, cited...))
	r, _ := json.Marshal(reason)
	return fmt.Sprintf(`{"name":%q,"cuisine":"Fusion","method":"stovetop","est_minutes":35,"difficulty":"easy","reason":%s,"cited_regulars":%s,"search_query":%q}`,
		name, r, c, strings.ToLower(name)+" recipe")
}

// batch renders a reply holding the Side quests given.
func batch(quests ...string) string {
	return `{"suggestions":[` + strings.Join(quests, ",") + `]}`
}

var (
	shawarma = sideQuest("Chicken Shawarma Bowls", "Spiced chicken and crisp toppings, like your Chicken Fajitas.", "Chicken Fajitas")
	ragu     = sideQuest("Beef Ragu Pappardelle", "A slow beef sauce like your Spaghetti Bolognese, on wide noodles.", "Spaghetti Bolognese")
	verde    = sideQuest("Chili Verde", "Your Beef Chili gone green, with pork and tomatillos.", "beef chili")
	bulgogi  = sideQuest("Beef Bulgogi", "Sweet, quick-seared beef for fans of Chicken Fajitas and Beef Chili.", "Chicken Fajitas", "Beef Chili")
)

// want returns the Side quest that sideQuest renders, with cited as the
// household writes them.
func want(name, reason string, cited ...string) SideQuest {
	return SideQuest{
		Name: name, Cuisine: "Fusion", Method: "stovetop", EstMinutes: 35, Difficulty: "easy",
		Reason: reason, CitedRegulars: cited, SearchQuery: strings.ToLower(name) + " recipe",
	}
}

var (
	wantShawarma = want("Chicken Shawarma Bowls", "Spiced chicken and crisp toppings, like your Chicken Fajitas.", "Chicken Fajitas")
	wantRagu     = want("Beef Ragu Pappardelle", "A slow beef sauce like your Spaghetti Bolognese, on wide noodles.", "Spaghetti Bolognese")
	wantVerde    = want("Chili Verde", "Your Beef Chili gone green, with pork and tomatillos.", "Beef Chili")
	wantBulgogi  = want("Beef Bulgogi", "Sweet, quick-seared beef for fans of Chicken Fajitas and Beef Chili.", "Chicken Fajitas", "Beef Chili")
)

// suggest runs SuggestSideQuests on household with f and checks f was
// called exactly calls times, each at the household temperature.
func suggest(t *testing.T, f *llm.Fake, in SideQuestsRequest, calls int) (SideQuests, []llm.Request, error) {
	t.Helper()
	out, err := SuggestSideQuests(context.Background(), testConfig(t, f), "2026-W41", in)
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
		if req.Schema == nil || req.Schema.Name != "suggest_side_quests" {
			t.Fatalf("request %d schema = %v, want suggest_side_quests", i+1, req.Schema)
		}
	}
	return out, reqs, err
}

func assertSideQuests(t *testing.T, got, want SideQuests) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %+v\nwant     %+v", got, want)
	}
}

func TestSuggestSideQuestsCleanBatch(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(batch(shawarma, ragu, verde, bulgogi)))
	out, reqs, err := suggest(t, &f, household, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertSideQuests(t, out, SideQuests{Suggestions: []SideQuest{wantShawarma, wantRagu, wantVerde, wantBulgogi}})

	system, user := reqs[0].System, reqs[0].Messages[0].Content
	golden(t, "suggest_side_quests_clean.system.golden", system)
	golden(t, "suggest_side_quests_clean.user.golden", user)
	for _, part := range []string{
		"## Household context",
		"- Chicken Fajitas (Tex-Mex, stovetop, 30 min, favorite)",
		"### Quest log\n- Pad Thai",
		"### Passed, oldest first\n- Chicken Tikka Masala",
		"- 2026-10-06 Tue: Beef Chili",
		"### Pending Side quests\n- Shrimp Scampi",
		"## Output contract",
		`"cited_regulars"`,
		`"search_query"`,
	} {
		if !strings.Contains(system, part) {
			t.Errorf("system prompt lacks %q", part)
		}
	}
	if strings.Index(system, "## Household context") > strings.Index(system, "## Output contract") {
		t.Error("the output contract is not after the household context")
	}
	if user != "Suggest 4 Side quests." {
		t.Errorf("user message = %q, want twice the 2 needed", user)
	}
}

func TestSuggestSideQuestsPersonaFirst(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(batch(shawarma)))
	cfg := testConfig(t, &f)
	cfg.Persona = "You are the household's cheerful kitchen helper."
	if _, err := SuggestSideQuests(context.Background(), cfg, "2026-W41", household); err != nil {
		t.Fatal(err)
	}
	if system := f.Requests()[0].System; !strings.HasPrefix(system, cfg.Persona+"\n\n## Instructions\n") {
		t.Fatalf("system prompt does not start with the persona then the instructions:\n%s", system)
	}
}

func TestSuggestSideQuestsInventedRegularDropped(t *testing.T) {
	invented := sideQuest("Meatloaf Wellington", "Your Grandma's Meatloaf, wrapped in pastry.", "Grandma's Meatloaf")
	partly := sideQuest("Fajita Meatballs", "Chicken Fajitas meets your Grandma's Meatloaf.", "Chicken Fajitas", "Grandma's Meatloaf")
	none := sideQuest("Mushroom Risotto", "Creamy and comforting.")
	var f llm.Fake
	f.QueueGenerate(reply(batch(invented, shawarma, partly, none)))
	out, _, err := suggest(t, &f, household, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertSideQuests(t, out, SideQuests{
		Suggestions: []SideQuest{wantShawarma},
		Dropped: []DroppedSideQuest{
			{Name: "Meatloaf Wellington", Why: `cites "Grandma's Meatloaf", which is not a Regular`},
			{Name: "Fajita Meatballs", Why: `cites "Grandma's Meatloaf", which is not a Regular`},
			{Name: "Mushroom Risotto", Why: "cites no Regular"},
		},
	})
}

func TestSuggestSideQuestsSharedNormalizedRegularNotRewritten(t *testing.T) {
	in := household
	in.Context.Regulars = append(slices.Clone(household.Context.Regulars),
		harness.RegularDinner{Name: "Easy Beef Chili", Cuisine: "Tex-Mex", Method: "stovetop", Minutes: 30})
	plain := sideQuest("Chili Verde", "Your Beef Chili gone green, with pork and tomatillos.", "beef chili")
	easy := sideQuest("Frito Pie", "Your Easy Beef Chili over corn chips.", "EASY  beef chili")
	either := sideQuest("Tamale Bake", "Your Beef Chili under a cornbread crust.", "Beef Chili!")
	var f llm.Fake
	f.QueueGenerate(reply(batch(plain, easy, either)))
	out, _, err := suggest(t, &f, in, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertSideQuests(t, out, SideQuests{
		Suggestions: []SideQuest{
			want("Chili Verde", "Your Beef Chili gone green, with pork and tomatillos.", "Beef Chili"),
			want("Frito Pie", "Your Easy Beef Chili over corn chips.", "Easy Beef Chili"),
		},
		Dropped: []DroppedSideQuest{
			{Name: "Tamale Bake", Why: `cites "Beef Chili!", which could be any of the Regulars ["Beef Chili" "Easy Beef Chili"]`},
		},
	})
}

func TestSuggestSideQuestsDuplicatesDropped(t *testing.T) {
	declined := sideQuest("The Best Homemade Chicken Tikka-Masala", "Spiced like your Chicken Fajitas.", "Chicken Fajitas")
	oldDeclined := sideQuest("Fish Tacos!", "Tortillas like your Chicken Fajitas.", "Chicken Fajitas")
	regular := sideQuest("Easy Beef Chili", "Just like your Beef Chili.", "Beef Chili")
	questLog := sideQuest("Pad Thai", "Noodles like your Spaghetti Bolognese.", "Spaghetti Bolognese")
	pending := sideQuest("Shrimp Scampi", "Pasta like your Spaghetti Bolognese.", "Spaghetti Bolognese")
	again := sideQuest("Chicken Shawarma Bowls", "Chicken Fajitas, but Middle Eastern.", "Chicken Fajitas")
	var f llm.Fake
	f.QueueGenerate(reply(batch(declined, oldDeclined, regular, questLog, pending, shawarma, again)))
	in := household
	in.Needed = 4
	out, _, err := suggest(t, &f, in, 1)
	if err != nil {
		t.Fatal(err)
	}
	known := "repeats a Regular, Quest log dinner, pending or passed Side quest"
	assertSideQuests(t, out, SideQuests{
		Suggestions: []SideQuest{wantShawarma},
		Dropped: []DroppedSideQuest{
			{Name: "The Best Homemade Chicken Tikka-Masala", Why: known},
			{Name: "Fish Tacos!", Why: known},
			{Name: "Easy Beef Chili", Why: known},
			{Name: "Pad Thai", Why: known},
			{Name: "Shrimp Scampi", Why: known},
			{Name: "Chicken Shawarma Bowls", Why: `repeats the Side quest "Chicken Shawarma Bowls" in the same batch`},
		},
	})
}

func TestSuggestSideQuestsReasonRulesDropped(t *testing.T) {
	long := sideQuest("Beef Stroganoff", "Your Beef Chili"+strings.Repeat(" and more", 20)+".", "Beef Chili")
	unnamed := sideQuest("Chicken Satay", "Grilled skewers with a peanut sauce.", "Chicken Fajitas")
	wrongOne := sideQuest("Beef Tacos", "Like your Beef Chili, in a shell.", "Chicken Fajitas")
	var f llm.Fake
	f.QueueGenerate(reply(batch(long, unnamed, ragu, wrongOne)))
	out, _, err := suggest(t, &f, household, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertSideQuests(t, out, SideQuests{
		Suggestions: []SideQuest{wantRagu},
		Dropped: []DroppedSideQuest{
			{Name: "Beef Stroganoff", Why: "reason has 196 characters, over 180"},
			{Name: "Chicken Satay", Why: "reason names none of its cited Regulars"},
			{Name: "Beef Tacos", Why: "reason names none of its cited Regulars"},
		},
	})
}

func TestSuggestSideQuestsAllDroppedIsNotRepaired(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(batch(sideQuest("Pad Thai", "Like your Beef Chili.", "Beef Chili"))))
	out, _, err := suggest(t, &f, household, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertSideQuests(t, out, SideQuests{
		Suggestions: []SideQuest{},
		Dropped:     []DroppedSideQuest{{Name: "Pad Thai", Why: "repeats a Regular, Quest log dinner, pending or passed Side quest"}},
	})
}

func TestSuggestSideQuestsMalformedRepairedOnce(t *testing.T) {
	for name, tc := range map[string]struct{ first, want string }{
		"truncated JSON": {
			`{"suggestions":[` + shawarma + `,{"name":"Beef`,
			"reply does not decode: unexpected EOF",
		},
		"omitted field": {
			batch(strings.Replace(shawarma, `"cuisine":"Fusion",`, ``, 1)),
			`suggestion has no "cuisine" field; give every field the schema requires`,
		},
		"unknown field": {
			batch(strings.Replace(shawarma, `"easy",`, `"easy","spice":"mild",`, 1)),
			`suggestion has unknown field "spice"`,
		},
		"bad method": {
			batch(strings.Replace(shawarma, `"stovetop"`, `"air fryer"`, 1)),
			`suggestion 1 method "air fryer" is not one of stovetop, oven, crockpot, grill, other`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var f llm.Fake
			f.QueueGenerate(reply(tc.first), reply(batch(shawarma, ragu)))
			out, reqs, err := suggest(t, &f, household, 2)
			if err != nil {
				t.Fatal(err)
			}
			assertSideQuests(t, out, SideQuests{Suggestions: []SideQuest{wantShawarma, wantRagu}})
			repair := reqs[1].Messages
			if len(repair) != 3 || repair[1].Content != tc.first {
				t.Fatalf("repair turn does not quote the first reply: %+v", repair)
			}
			if !strings.Contains(repair[2].Content, tc.want) {
				t.Fatalf("repair message = %q, want it to contain %q", repair[2].Content, tc.want)
			}
		})
	}
}

func TestSuggestSideQuestsFillerNameRepaired(t *testing.T) {
	filler := sideQuest("The.", "Like your Beef Chili.", "Beef Chili")
	first := batch(filler, filler)
	var f llm.Fake
	f.QueueGenerate(reply(first), reply(batch(shawarma, ragu)))
	out, reqs, err := suggest(t, &f, household, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertSideQuests(t, out, SideQuests{Suggestions: []SideQuest{wantShawarma, wantRagu}})
	repair := reqs[1].Messages
	if len(repair) != 3 || repair[1].Content != first {
		t.Fatalf("repair turn does not quote the first reply: %+v", repair)
	}
	for _, want := range []string{
		`suggestion 1 name "The." has only filler words; name the dish`,
		`suggestion 2 name "The." has only filler words; name the dish`,
	} {
		if !strings.Contains(repair[2].Content, want) {
			t.Fatalf("repair message = %q, want it to contain %q", repair[2].Content, want)
		}
	}
}

func TestSuggestSideQuestsMalformedTwiceFails(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(`{"suggestions":`), reply(`{"suggestions":[]}`))
	out, _, err := suggest(t, &f, household, 2)
	var bad *harness.BadOutputError
	if !errors.As(err, &bad) || !errors.Is(err, llm.ErrBadOutput) {
		t.Fatalf("err = %v, want a BadOutputError", err)
	}
	if !slices.Equal(bad.Repair, []string{"suggestions is empty; give 4 Side quests"}) {
		t.Fatalf("repair problems = %q", bad.Repair)
	}
	if out.Suggestions != nil {
		t.Fatalf("output = %+v, want none", out)
	}
}

func TestSideQuestsValidate(t *testing.T) {
	s := suggestSideQuests{in: household}
	if p := s.Validate(SideQuests{Suggestions: []SideQuest{wantShawarma}}); len(p) != 0 {
		t.Fatalf("valid output has problems: %q", p)
	}
	if p := s.Validate(SideQuests{Suggestions: []SideQuest{}, Dropped: []DroppedSideQuest{{Name: "x", Why: "y"}}}); len(p) != 0 {
		t.Fatalf("output whose Side quests were all dropped has problems: %q", p)
	}
	for name, tc := range map[string]struct {
		out  SideQuests
		want []string
	}{
		"empty": {SideQuests{}, []string{"suggestions is empty; give 4 Side quests"}},
		"too many": {
			SideQuests{Suggestions: []SideQuest{wantShawarma, wantRagu, wantVerde, wantBulgogi, wantShawarma}},
			[]string{"suggestions has 5 entries; give at most 4 Side quests"},
		},
		"malformed": {
			SideQuests{Suggestions: []SideQuest{{Name: " ", Method: "air fryer", EstMinutes: 0, Difficulty: "trivial", CitedRegulars: []string{}}}},
			[]string{
				"suggestion 1 has an empty name; give one",
				"suggestion 1 has an empty cuisine; give one",
				"suggestion 1 has an empty reason; give one",
				"suggestion 1 has an empty search_query; give one",
				`suggestion 1 method "air fryer" is not one of stovetop, oven, crockpot, grill, other`,
				"suggestion 1 est_minutes 0 is not positive; give the minutes from prep to table",
				`suggestion 1 difficulty "trivial" is not one of easy, medium, hard`,
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

func TestSideQuestsPromptInputRejects(t *testing.T) {
	noRegulars := household
	noRegulars.Context.Regulars = nil
	none := household
	none.Needed = 0
	for name, tc := range map[string]struct {
		in   SideQuestsRequest
		want string
	}{
		"no Regulars": {noRegulars, "household has no Regulars for Side quests to be based on"},
		"none needed": {none, "0 Side quests needed; the number must be positive"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := suggestSideQuests{in: tc.in}.PromptInput(tc.in)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}
