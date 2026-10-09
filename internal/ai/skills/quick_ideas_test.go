package skills

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// quickTastes is the household context of the quick_ideas tests.
var quickTastes = harness.HouseholdContext{
	Today:    date(2026, 10, 8),
	Timezone: "America/New_York",
	Regulars: []harness.RegularDinner{
		{Name: "Beef Chili", Cuisine: "Tex-Mex", Method: "crockpot", Minutes: 240, Favorite: true},
		{Name: "Chicken Fajitas", Cuisine: "Tex-Mex", Method: "stovetop", Minutes: 30},
	},
	QuestLog: []string{"Pad Thai"},
}

// quickIdea renders one idea of a quick_ideas reply.
func quickIdea(name, method string, minutes int) string {
	return fmt.Sprintf(`{"name":%q,"est_minutes":%d,"method":%q,"difficulty":"easy","search_query":%q}`,
		name, minutes, method, strings.ToLower(name)+" recipe")
}

// quick runs QuickIdeas on in with f and checks f was called exactly
// calls times, each at the household temperature.
func quick(t *testing.T, f *llm.Fake, in QuickIdeasRequest, calls int) (QuickIdeaList, []llm.Request, error) {
	t.Helper()
	out, err := QuickIdeas(context.Background(), testConfig(t, f), QuickIdeasScope(in), in)
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
		if req.Schema == nil || req.Schema.Name != "quick_ideas" {
			t.Fatalf("request %d schema = %v, want quick_ideas", i+1, req.Schema)
		}
	}
	return out, reqs, err
}

func TestQuickIdeasPrompt(t *testing.T) {
	for _, tc := range []struct {
		kind, golden, user, rule string
	}{
		{QuickCrockpot, "quick_ideas_crockpot", "Suggest 12 crock pot dinner ideas.",
			`Every idea must be a crock pot dinner: cooked mainly in a crock pot (slow cooker), with method "crockpot".`},
		{QuickUnder30, "quick_ideas_under30", "Suggest 12 dinner ideas ready in 30 minutes or less.",
			"Every idea must take 30 minutes or less from starting prep to the table."},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			var f llm.Fake
			f.QueueGenerate(reply(ideasReply(quickIdea("Chicken Fried Rice", "stovetop", 25))))
			in := QuickIdeasRequest{Context: quickTastes, Kind: tc.kind, Exclude: []string{"Beef Stew", "Shrimp Tacos"}}
			_, reqs, err := quick(t, &f, in, 1)
			if err != nil {
				t.Fatal(err)
			}
			system, user := reqs[0].System, reqs[0].Messages[0].Content
			golden(t, tc.golden+".system.golden", system)
			golden(t, tc.golden+".user.golden", user)
			for _, part := range []string{
				"## Household context",
				"### Regulars\n- Beef Chili",
				"### Quest log\n- Pad Thai",
				"### Already shown\n- Beef Stew\n- Shrimp Tacos",
				tc.rule,
				"## Output contract",
			} {
				if !strings.Contains(system, part) {
					t.Errorf("system prompt lacks %q", part)
				}
			}
			if user != tc.user {
				t.Errorf("user message = %q, want %q", user, tc.user)
			}
		})
	}
}

func TestQuickIdeasNeverReturnExcludedNames(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(ideasReply(
		// The excluded names, spelled another way: the normalized name
		// decides.
		quickIdea("Easy Beef Stew!", "crockpot", 300),
		quickIdea("shrimp-tacos", "stovetop", 20),
		quickIdea("Pulled Pork", "crockpot", 480),
		// A repeat of an idea already kept.
		quickIdea("The Pulled Pork", "crockpot", 420),
		quickIdea("Chicken Fried Rice", "stovetop", 25),
	)))
	in := QuickIdeasRequest{Context: quickTastes, Kind: QuickCrockpot, Exclude: []string{"Beef Stew", "Shrimp Tacos"}}
	out, _, err := quick(t, &f, in, 1)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, i := range out.Ideas {
		kept = append(kept, i.Name)
	}
	// Chicken Fried Rice is not a crock pot dinner, but the grounded
	// recipe, not the model's estimate, decides that later.
	if want := []string{"Pulled Pork", "Chicken Fried Rice"}; !reflect.DeepEqual(kept, want) {
		t.Fatalf("ideas = %q, want %q", kept, want)
	}
	want := []DroppedIdea{
		{Name: "Easy Beef Stew!", Why: `repeats "Beef Stew", already shown`},
		{Name: "shrimp-tacos", Why: `repeats "Shrimp Tacos", already shown`},
		{Name: "The Pulled Pork", Why: `repeats the idea "Pulled Pork"`},
	}
	if !reflect.DeepEqual(out.Dropped, want) {
		t.Fatalf("dropped = %+v\nwant %+v", out.Dropped, want)
	}
}

func TestQuickIdeasRepairsMalformedReply(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(ideasReply(quickIdea("Pulled Pork", "microwave", 0))))
	f.QueueGenerate(reply(ideasReply(quickIdea("Pulled Pork", "crockpot", 480))))
	out, reqs, err := quick(t, &f, QuickIdeasRequest{Context: quickTastes, Kind: QuickCrockpot}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Ideas) != 1 {
		t.Fatalf("ideas = %+v", out.Ideas)
	}
	repair := reqs[1].Messages[len(reqs[1].Messages)-1].Content
	for _, part := range []string{`idea 1 method "microwave" is not one of`, "idea 1 est_minutes 0 is not positive"} {
		if !strings.Contains(repair, part) {
			t.Errorf("repair turn lacks %q:\n%s", part, repair)
		}
	}
}

func TestQuickIdeasRefusesTooManyIdeas(t *testing.T) {
	var f llm.Fake
	var many []string
	for i := range QuickIdeasRequested + 1 {
		many = append(many, quickIdea(fmt.Sprintf("Dish %d", i+1), "stovetop", 20))
	}
	f.QueueGenerate(reply(ideasReply(many...)))
	f.QueueGenerate(reply(ideasReply(many...)))
	if _, _, err := quick(t, &f, QuickIdeasRequest{Context: quickTastes, Kind: QuickUnder30}, 2); err == nil || !strings.Contains(err.Error(), "give at most 12 ideas") {
		t.Fatalf("err = %v, want a bad output naming the limit", err)
	}
}

func TestQuickIdeasRefuseBadRequestBeforeCalling(t *testing.T) {
	for _, in := range []QuickIdeasRequest{
		{Context: quickTastes, Kind: ""},
		{Context: quickTastes, Kind: "quick"},
		{Context: quickTastes, Kind: QuickUnder30, Exclude: []string{"Beef Stew", "beef-stew"}},
	} {
		var f llm.Fake
		if _, _, err := quick(t, &f, in, 0); err == nil {
			t.Fatalf("%+v was accepted", in)
		}
	}
}

func TestCleanExclude(t *testing.T) {
	got, err := CleanExclude([]string{"  Beef   Stew ", "Shrimp\tTacos"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Beef Stew", "Shrimp Tacos"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CleanExclude = %q, want %q", got, want)
	}
	if got, err := CleanExclude(nil); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("CleanExclude(nil) = %q, %v, want an empty list", got, err)
	}
	tooMany := make([]string, MaxExclude+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("dish %d", i)
	}
	for _, tc := range []struct {
		name string
		in   []string
		want string
	}{
		{"too many", tooMany, "exclude at most 60"},
		{"blank", []string{"Beef Stew", " "}, "excluded name 2 is blank"},
		{"too long", []string{strings.Repeat("a", ExcludeLimit+1)}, "over 120 characters"},
		{"filler only", []string{"Easy Recipe"}, "names no dinner"},
		{"repeat", []string{"Beef Stew", "easy beef-stew"}, `repeats "Beef Stew"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CleanExclude(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCheckQuickKind(t *testing.T) {
	for _, kind := range QuickKinds {
		if err := CheckQuickKind(kind); err != nil {
			t.Errorf("CheckQuickKind(%q) = %v", kind, err)
		}
	}
	for _, kind := range []string{"", "Crockpot", "under_30", "quick"} {
		if err := CheckQuickKind(kind); err == nil || !strings.Contains(err.Error(), "is not one of crockpot, under30") {
			t.Errorf("CheckQuickKind(%q) = %v, want a refusal naming the kinds", kind, err)
		}
	}
}

func TestQuickIdeasScopeIdentifiesTheRequest(t *testing.T) {
	a := QuickIdeasScope(QuickIdeasRequest{Kind: QuickUnder30, Exclude: []string{"Beef Stew"}})
	if b := QuickIdeasScope(QuickIdeasRequest{Kind: QuickUnder30, Exclude: []string{"beef-stew"}}); a != b {
		t.Errorf("scopes of the same request differ: %q, %q", a, b)
	}
	for _, other := range []QuickIdeasRequest{
		{Kind: QuickCrockpot, Exclude: []string{"Beef Stew"}},
		{Kind: QuickUnder30},
		{Kind: QuickUnder30, Exclude: []string{"Beef Stew", "Shrimp Tacos"}},
	} {
		if QuickIdeasScope(other) == a {
			t.Errorf("%+v shares the scope %q", other, a)
		}
	}
}
