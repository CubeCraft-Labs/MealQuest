package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// pantry is the household's ingredient list in its order.
var pantry = IdeasRequest{Ingredients: []string{"Chicken thighs", "rice", "cilantro", "black beans", "red onion"}}

// idea renders one idea of an ideas_from_ingredients reply.
func idea(name, method string, minutes int, uses, missing []string) string {
	u, _ := json.Marshal(append([]string{}, uses...))
	m, _ := json.Marshal(append([]string{}, missing...))
	return fmt.Sprintf(`{"name":%q,"uses":%s,"missing":%s,"est_minutes":%d,"method":%q,"difficulty":"easy","search_query":%q}`,
		name, u, m, minutes, method, strings.ToLower(name)+" recipe")
}

// ideasReply renders a reply holding the ideas given.
func ideasReply(ideas ...string) string {
	return `{"ideas":[` + strings.Join(ideas, ",") + `]}`
}

// ideas runs IdeasFromIngredients on in with f and checks f was called
// exactly calls times, each at the household temperature.
func ideas(t *testing.T, f *llm.Fake, in IdeasRequest, calls int) (Ideas, []llm.Request, error) {
	t.Helper()
	out, err := IdeasFromIngredients(context.Background(), testConfig(t, f), IdeasScope(in), in)
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
		if req.Schema == nil || req.Schema.Name != "ideas_from_ingredients" {
			t.Fatalf("request %d schema = %v, want ideas_from_ingredients", i+1, req.Schema)
		}
	}
	return out, reqs, err
}

func TestIdeasFromIngredientsPrompt(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(ideasReply(idea("Chicken Burrito Bowls", "stovetop", 35, []string{"Chicken thighs", "rice"}, nil))))
	_, reqs, err := ideas(t, &f, pantry, 1)
	if err != nil {
		t.Fatal(err)
	}
	system, user := reqs[0].System, reqs[0].Messages[0].Content
	golden(t, "ideas_from_ingredients_any.system.golden", system)
	golden(t, "ideas_from_ingredients_any.user.golden", user)
	for _, part := range []string{
		"## Household context",
		"### Ingredients on hand\n- Chicken thighs\n- rice\n- cilantro\n- black beans\n- red onion",
		"## Output contract",
		`"uses"`,
		`"missing"`,
	} {
		if !strings.Contains(system, part) {
			t.Errorf("system prompt lacks %q", part)
		}
	}
	if strings.Contains(system, "Quick dinners only") {
		t.Error("an Any time request asks for Quick dinners only")
	}
	if user != "Suggest 8 dinner ideas from the ingredients on hand." {
		t.Errorf("user message = %q", user)
	}
}

func TestIdeasFromIngredientsQuickOnlyPrompt(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(ideasReply(idea("Chicken Fried Rice", "stovetop", 25, []string{"Chicken thighs", "rice"}, nil))))
	in := pantry
	in.QuickOnly = true
	_, reqs, err := ideas(t, &f, in, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reqs[0].System, "The household wants Quick dinners only: each idea must be cooked in a crock pot, or take 30 minutes or less from prep to table.") {
		t.Errorf("Quick only request does not ask for Quick dinners:\n%s", reqs[0].System)
	}
}

func TestIdeasFromIngredientsSummaryLine(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(ideasReply(
		// Every ingredient, named in another order and spelling: uses is
		// in the household's order, as the household wrote it.
		idea("Chicken Burrito Bowls", "stovetop", 35, []string{"red onion", "Black Beans", "chicken thighs", "Rice", "cilantro!"}, []string{"1 lime"}),
		// Some of them.
		idea("Cilantro Chicken Rice", "stovetop", 30, []string{"rice", "cilantro", "Chicken thighs"}, []string{"1 lime", "cumin"}),
		// Every ingredient, nothing missing.
		idea("Black Bean Chicken Skillet", "stovetop", 30, []string{"Chicken thighs", "rice", "cilantro", "black beans", "red onion"}, nil),
		// Some, nothing missing.
		idea("Rice and Beans", "stovetop", 25, []string{"rice", "black beans"}, []string{}),
	)))
	out, _, err := ideas(t, &f, pantry, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Dropped) != 0 {
		t.Fatalf("dropped %+v", out.Dropped)
	}
	got := map[string][2]any{}
	for _, i := range out.Ideas {
		got[i.Name] = [2]any{i.Uses, i.Summary}
	}
	want := map[string][2]any{
		"Chicken Burrito Bowls":      {[]string{"Chicken thighs", "rice", "cilantro", "black beans", "red onion"}, "Uses all 5 · missing: 1 lime"},
		"Cilantro Chicken Rice":      {[]string{"Chicken thighs", "rice", "cilantro"}, "Uses 3 of 5 · missing: 1 lime, cumin"},
		"Black Bean Chicken Skillet": {[]string{"Chicken thighs", "rice", "cilantro", "black beans", "red onion"}, "Uses all 5 · nothing missing"},
		"Rice and Beans":             {[]string{"rice", "black beans"}, "Uses 2 of 5 · nothing missing"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uses and summaries = %v\nwant %v", got, want)
	}
}

func TestIdeasFromIngredientsDropsHallucinatedUses(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(ideasReply(
		// The model claims parmesan and lime, which the household never
		// listed, and repeats rice.
		idea("Cheesy Chicken Rice", "oven", 40, []string{"Chicken thighs", "parmesan", "rice", "lime", "rice"}, []string{"1 cup broth"}),
		// Uses only ingredients the household never listed.
		idea("Lemon Pasta", "stovetop", 20, []string{"pasta", "lemon"}, []string{"garlic"}),
		// Lists an ingredient on hand as missing.
		idea("Chicken Tacos", "stovetop", 25, []string{"Chicken thighs"}, []string{"tortillas", "Cilantro", "tortillas"}),
	)))
	out, _, err := ideas(t, &f, pantry, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := Ideas{
		Ideas: []Idea{
			{
				Name: "Cheesy Chicken Rice", Uses: []string{"Chicken thighs", "rice"}, Missing: []string{"1 cup broth"},
				EstMinutes: 40, Method: "oven", Difficulty: "easy", SearchQuery: "cheesy chicken rice recipe",
				Summary: "Uses 2 of 5 · missing: 1 cup broth",
			},
			{
				Name: "Chicken Tacos", Uses: []string{"Chicken thighs"}, Missing: []string{"tortillas"},
				EstMinutes: 25, Method: "stovetop", Difficulty: "easy", SearchQuery: "chicken tacos recipe",
				Summary: "Uses 1 of 5 · missing: tortillas",
			},
		},
		Dropped: []DroppedIdea{{Name: "Lemon Pasta", Why: "uses none of the household's ingredients"}},
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("output = %+v\nwant     %+v", out, want)
	}
}

func TestIdeasFromIngredientsDropsRepeatedIdea(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(ideasReply(
		idea("Chicken Burrito Bowls", "stovetop", 35, []string{"rice"}, nil),
		idea("Easy Chicken Burrito Bowls", "stovetop", 30, []string{"rice"}, nil),
	)))
	out, _, err := ideas(t, &f, pantry, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Ideas) != 1 || out.Ideas[0].Name != "Chicken Burrito Bowls" {
		t.Fatalf("ideas = %+v", out.Ideas)
	}
	if want := []DroppedIdea{{Name: "Easy Chicken Burrito Bowls", Why: `repeats the idea "Chicken Burrito Bowls"`}}; !reflect.DeepEqual(out.Dropped, want) {
		t.Fatalf("dropped = %+v, want %+v", out.Dropped, want)
	}
}

func TestIdeasFromIngredientsRepairsMalformedReply(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(ideasReply(idea("Chicken Burrito Bowls", "microwave", 0, []string{"rice"}, nil))))
	f.QueueGenerate(reply(ideasReply(idea("Chicken Burrito Bowls", "stovetop", 35, []string{"rice"}, nil))))
	out, reqs, err := ideas(t, &f, pantry, 2)
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

func TestIdeasFromIngredientsRefusesTooManyIdeas(t *testing.T) {
	var f llm.Fake
	var nine []string
	for i := range 9 {
		nine = append(nine, idea(fmt.Sprintf("Dish %d", i+1), "stovetop", 30, []string{"rice"}, nil))
	}
	f.QueueGenerate(reply(ideasReply(nine...)))
	f.QueueGenerate(reply(ideasReply(nine...)))
	if _, _, err := ideas(t, &f, pantry, 2); err == nil || !strings.Contains(err.Error(), "give at most 8 ideas") {
		t.Fatalf("err = %v, want a bad output naming the limit", err)
	}
}

func TestCleanIngredients(t *testing.T) {
	got, err := CleanIngredients([]string{"  1   lime ", "Chicken\tthighs"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1 lime", "Chicken thighs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CleanIngredients = %q, want %q", got, want)
	}
	tooMany := make([]string, MaxIngredients+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("thing %d", i)
	}
	for _, tc := range []struct {
		name string
		in   []string
		want string
	}{
		{"empty", nil, "list at least one ingredient"},
		{"too many", tooMany, "list at most 20"},
		{"blank", []string{"rice", "  "}, "ingredient 2 is blank"},
		{"too long", []string{strings.Repeat("a", IngredientLimit+1)}, "over 80 characters"},
		{"filler only", []string{"the"}, "names no ingredient"},
		{"repeat", []string{"Red onion", "red-onion"}, `repeats "Red onion"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := CleanIngredients(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestIdeasFromIngredientsRefusesBadListBeforeCalling(t *testing.T) {
	var f llm.Fake
	if _, _, err := ideas(t, &f, IdeasRequest{Ingredients: []string{"rice", "Rice"}}, 0); err == nil {
		t.Fatal("a repeated ingredient was accepted")
	}
}

func TestIdeasScopeIdentifiesTheRequest(t *testing.T) {
	a := IdeasScope(IdeasRequest{Ingredients: []string{"Rice", "black beans"}})
	if b := IdeasScope(IdeasRequest{Ingredients: []string{"rice", "Black Beans"}}); a != b {
		t.Errorf("scopes of the same list differ: %q, %q", a, b)
	}
	for _, other := range []IdeasRequest{
		{Ingredients: []string{"Rice", "black beans"}, QuickOnly: true},
		{Ingredients: []string{"black beans", "Rice"}},
		{Ingredients: []string{"Rice"}},
	} {
		if IdeasScope(other) == a {
			t.Errorf("%+v shares the scope %q", other, a)
		}
	}
}
