package skills

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// golden compares got with testdata/name, or rewrites it with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from the rendered output:\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// testConfig returns a Config whose provider is f, at the household
// temperature 0.7, recording into a fresh migrated store.
func testConfig(t *testing.T, f *llm.Fake) harness.Config {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx, st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	g, err := harness.NewGuard(harness.DefaultMaxCalls)
	if err != nil {
		t.Fatal(err)
	}
	return harness.Config{
		Settings:    llm.Settings{Provider: "anthropic", Model: "claude-test", Temperature: 0.7},
		NewProvider: func(llm.Settings, string) (llm.Provider, error) { return f, nil },
		Store:       st,
		Guard:       g,
	}
}

func reply(js string) llm.GenerateResult {
	return llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}}
}

// normalize runs NormalizeRecipe on in with f and checks f was called
// exactly calls times, each at temperature 0.
func normalize(t *testing.T, f *llm.Fake, in RecipeText, calls int) (NormalizedRecipe, []llm.Request, error) {
	t.Helper()
	out, err := NormalizeRecipe(context.Background(), testConfig(t, f), "https://example.com/recipe", in)
	reqs := f.Requests()
	if len(reqs) != calls {
		t.Fatalf("Generate called %d times, want %d", len(reqs), calls)
	}
	if g, _, _ := f.Remaining(); g != 0 {
		t.Fatalf("%d scripted Generate results unused", g)
	}
	for i, req := range reqs {
		if req.Temperature == nil || *req.Temperature != 0 {
			t.Fatalf("request %d temperature = %v, want 0", i+1, req.Temperature)
		}
		if req.Schema == nil || req.Schema.Name != "normalize_recipe" {
			t.Fatalf("request %d schema = %v, want normalize_recipe", i+1, req.Schema)
		}
	}
	return out, reqs, err
}

func minutes(n int) *int { return &n }

// garlicPasta is a typical stovetop recipe with no JSON-LD times.
var garlicPasta = RecipeText{
	Title: "Garlic Butter Pasta",
	Ingredients: []string{
		"12 oz spaghetti",
		"4 tbsp unsalted butter",
		"6 cloves garlic, minced",
		"Salt, to taste",
	},
	Steps: []string{
		"Boil the spaghetti in salted water until al dente.",
		"Melt the butter in a skillet and cook the garlic\n  for one minute.",
		"Toss the pasta with the garlic butter and season.",
	},
}

const garlicPastaReply = `{"ingredients":[
	{"name":"spaghetti","qty":"12","unit":"oz","note":""},
	{"name":"unsalted butter","qty":"4","unit":"tbsp","note":""},
	{"name":" garlic ","qty":"6","unit":"clove","note":"minced"},
	{"name":"salt","qty":"","unit":"","note":"to taste"}],
	"method":"stovetop","difficulty":"easy","minutes_total":25}`

var garlicPastaIngredients = []Ingredient{
	{Name: "spaghetti", Qty: "12", Unit: "oz"},
	{Name: "unsalted butter", Qty: "4", Unit: "tbsp"},
	{Name: "garlic", Qty: "6", Unit: "clove", Note: "minced"},
	{Name: "salt", Note: "to taste"},
}

func assertOutput(t *testing.T, got, want NormalizedRecipe) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Fatalf("output = %s\nwant     %s", g, w)
	}
}

func TestNormalizeRecipeTypical(t *testing.T) {
	var f llm.Fake
	f.QueueGenerate(reply(garlicPastaReply))
	out, reqs, err := normalize(t, &f, garlicPasta, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertOutput(t, out, NormalizedRecipe{
		Ingredients:  garlicPastaIngredients,
		Method:       "stovetop",
		Difficulty:   "easy",
		MinutesTotal: minutes(25),
	})
	golden(t, "normalize_recipe_typical.system.golden", reqs[0].System)
	golden(t, "normalize_recipe_typical.user.golden", reqs[0].Messages[0].Content)
}

func TestNormalizeRecipeLineCountRepaired(t *testing.T) {
	var f llm.Fake
	short := `{"ingredients":[
		{"name":"spaghetti","qty":"12","unit":"oz","note":""},
		{"name":"unsalted butter","qty":"4","unit":"tbsp","note":""},
		{"name":"garlic","qty":"6","unit":"clove","note":"minced"}],
		"method":"stovetop","difficulty":"easy","minutes_total":25}`
	f.QueueGenerate(reply(short), reply(garlicPastaReply))
	out, reqs, err := normalize(t, &f, garlicPasta, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertOutput(t, out, NormalizedRecipe{
		Ingredients:  garlicPastaIngredients,
		Method:       "stovetop",
		Difficulty:   "easy",
		MinutesTotal: minutes(25),
	})
	repair := reqs[1].Messages
	if len(repair) != 3 || repair[1].Content != short {
		t.Fatalf("repair turn does not quote the first reply: %+v", repair)
	}
	want := "ingredients has 3 entries but there are 4 ingredient lines; give exactly one ingredient per line, in order"
	if !strings.Contains(repair[2].Content, want) {
		t.Fatalf("repair message = %q, want it to contain %q", repair[2].Content, want)
	}
}

func TestNormalizeRecipeSlowCookerForcedToCrockpot(t *testing.T) {
	in := RecipeText{
		Title:       "Easy Pulled Pork",
		Ingredients: []string{"3 lb pork shoulder", "1 cup barbecue sauce"},
		Steps: []string{
			"Rub the pork with salt.",
			"Cook in the Crock-Pot on low for 8 hours.",
			"Shred the pork and stir in the sauce.",
		},
	}
	var f llm.Fake
	f.QueueGenerate(reply(`{"ingredients":[
		{"name":"pork shoulder","qty":"3","unit":"lb","note":""},
		{"name":"barbecue sauce","qty":"1","unit":"cup","note":""}],
		"method":"stovetop","difficulty":null,"minutes_total":490}`))
	out, _, err := normalize(t, &f, in, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertOutput(t, out, NormalizedRecipe{
		Ingredients: []Ingredient{
			{Name: "pork shoulder", Qty: "3", Unit: "lb"},
			{Name: "barbecue sauce", Qty: "1", Unit: "cup"},
		},
		Method: "crockpot",
		// Derived: three steps, and a crockpot's 490 minutes are unattended.
		Difficulty:   "easy",
		MinutesTotal: minutes(490),
	})
}

func TestNormalizeRecipeJSONLDMinutesWin(t *testing.T) {
	in := garlicPasta
	in.PrepMinutes, in.CookMinutes, in.TotalMinutes = minutes(10), minutes(25), minutes(35)
	var f llm.Fake
	f.QueueGenerate(reply(strings.Replace(garlicPastaReply, `"minutes_total":25`, `"minutes_total":20`, 1)))
	out, reqs, err := normalize(t, &f, in, 1)
	if err != nil {
		t.Fatal(err)
	}
	assertOutput(t, out, NormalizedRecipe{
		Ingredients:  garlicPastaIngredients,
		Method:       "stovetop",
		Difficulty:   "easy",
		MinutesTotal: minutes(35),
	})
	if user := reqs[0].Messages[0].Content; !strings.Contains(user, "\nTimes: prep 10 min, cook 25 min, total 35 min\n") {
		t.Fatalf("user message does not give the JSON-LD times:\n%s", user)
	}
}

func TestNormalizeRecipeUnrepairedFails(t *testing.T) {
	var f llm.Fake
	empty := strings.Replace(garlicPastaReply, `"name":"spaghetti"`, `"name":"  "`, 1)
	f.QueueGenerate(reply(empty), reply(empty))
	_, _, err := normalize(t, &f, garlicPasta, 2)
	var bad *harness.BadOutputError
	if !errors.As(err, &bad) || !errors.Is(err, llm.ErrBadOutput) {
		t.Fatalf("err = %v, want a BadOutputError", err)
	}
	want := []string{"ingredient 1 has an empty name; name the ingredient"}
	if !slices.Equal(bad.First, want) || !slices.Equal(bad.Repair, want) {
		t.Fatalf("problems = %q then %q, want %q", bad.First, bad.Repair, want)
	}
}

// stirFry has two ingredient lines that share the word "oil".
var stirFry = RecipeText{
	Title:       "Vegetable Stir Fry",
	Ingredients: []string{"2 tbsp olive oil", "1 tbsp sesame oil"},
	Steps:       []string{"Stir-fry the vegetables in the olive oil, then finish with the sesame oil."},
}

const stirFryReply = `{"ingredients":[
	{"name":"olive oil","qty":"2","unit":"tbsp","note":""},
	{"name":"sesame oil","qty":"1","unit":"tbsp","note":""}],
	"method":"stovetop","difficulty":"easy","minutes_total":15}`

func TestNormalizeRecipeSwapSharingAWordRepaired(t *testing.T) {
	swapped := `{"ingredients":[
		{"name":"sesame oil","qty":"1","unit":"tbsp","note":""},
		{"name":"olive oil","qty":"2","unit":"tbsp","note":""}],
		"method":"stovetop","difficulty":"easy","minutes_total":15}`
	var f llm.Fake
	f.QueueGenerate(reply(swapped), reply(stirFryReply))
	out, reqs, err := normalize(t, &f, stirFry, 2)
	if err != nil {
		t.Fatal(err)
	}
	assertOutput(t, out, NormalizedRecipe{
		Ingredients: []Ingredient{
			{Name: "olive oil", Qty: "2", Unit: "tbsp"},
			{Name: "sesame oil", Qty: "1", Unit: "tbsp"},
		},
		Method:       "stovetop",
		Difficulty:   "easy",
		MinutesTotal: minutes(15),
	})
	for _, want := range []string{
		`ingredient 1 "sesame oil" matches line 2 "1 tbsp sesame oil", not line 1 "2 tbsp olive oil"; keep the ingredients in line order`,
		`ingredient 2 "olive oil" matches line 1 "2 tbsp olive oil", not line 2 "1 tbsp sesame oil"; keep the ingredients in line order`,
	} {
		if repair := reqs[1].Messages[2].Content; !strings.Contains(repair, want) {
			t.Fatalf("repair message = %q, want it to contain %q", repair, want)
		}
	}
}

func TestNormalizeRecipeOmittedFieldRepaired(t *testing.T) {
	for name, tc := range map[string]struct{ first, want string }{
		"omitted qty": {
			strings.Replace(stirFryReply, `"qty":"1",`, ``, 1),
			`ingredient has no "qty" field; give every field the schema requires`,
		},
		"null note": {
			strings.Replace(stirFryReply, `"tbsp","note":""}]`, `"tbsp","note":null}]`, 1),
			`ingredient field "note" is null, which the schema does not allow`,
		},
		"omitted unit": {
			strings.Replace(stirFryReply, `"unit":"tbsp",`, ``, 1),
			`ingredient has no "unit" field; give every field the schema requires`,
		},
		"omitted minutes_total": {
			strings.Replace(stirFryReply, `,"minutes_total":15`, ``, 1),
			`output has no "minutes_total" field; give every field the schema requires`,
		},
		"unknown field": {
			strings.Replace(stirFryReply, `"note":""}]`, `"note":"","brand":"x"}]`, 1),
			`ingredient has unknown field "brand"`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.first == stirFryReply {
				t.Fatal("replacement did not change the reply")
			}
			var f llm.Fake
			f.QueueGenerate(reply(tc.first), reply(stirFryReply))
			_, reqs, err := normalize(t, &f, stirFry, 2)
			if err != nil {
				t.Fatal(err)
			}
			if repair := reqs[1].Messages[2].Content; !strings.Contains(repair, tc.want) {
				t.Fatalf("repair message = %q, want it to contain %q", repair, tc.want)
			}
		})
	}
}

func TestNormalizeRecipeZeroTotalRejected(t *testing.T) {
	in := garlicPasta
	in.TotalMinutes = minutes(0)
	var f llm.Fake
	_, _, err := normalize(t, &f, in, 0)
	want := "recipe total time is 0 minutes; a given total must be positive"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
}

func TestValidate(t *testing.T) {
	s := normalizeRecipe{in: RecipeText{
		Title:       "Tomato soup",
		Ingredients: []string{"4 ripe tomatoes", "1 onion, diced"},
	}}
	ok := NormalizedRecipe{
		Ingredients: []Ingredient{{Name: "tomato", Qty: "4", Note: "ripe"}, {Name: "onion", Qty: "1", Note: "diced"}},
		Method:      "stovetop",
	}
	if p := s.Validate(ok); len(p) != 0 {
		t.Fatalf("valid output has problems: %q", p)
	}
	renamed := ok
	renamed.Ingredients = []Ingredient{{Name: "roma"}, {Name: "shallot"}}
	if p := s.Validate(renamed); len(p) != 0 {
		t.Fatalf("names matching no line are accepted, got problems: %q", p)
	}
	for name, tc := range map[string]struct {
		out  NormalizedRecipe
		want []string
	}{
		"swapped": {
			NormalizedRecipe{Ingredients: []Ingredient{{Name: "onion"}, {Name: "tomatoes"}}, Method: "oven"},
			[]string{
				`ingredient 1 "onion" matches line 2 "1 onion, diced", not line 1 "4 ripe tomatoes"; keep the ingredients in line order`,
				`ingredient 2 "tomatoes" matches line 1 "4 ripe tomatoes", not line 2 "1 onion, diced"; keep the ingredients in line order`,
			},
		},
		"swapped sharing a word": {
			NormalizedRecipe{Ingredients: []Ingredient{{Name: "diced onion"}, {Name: "ripe tomato"}}, Method: "oven"},
			[]string{
				`ingredient 1 "diced onion" matches line 2 "1 onion, diced", not line 1 "4 ripe tomatoes"; keep the ingredients in line order`,
				`ingredient 2 "ripe tomato" matches line 1 "4 ripe tomatoes", not line 2 "1 onion, diced"; keep the ingredients in line order`,
			},
		},
		"too many and bad enums": {
			NormalizedRecipe{
				Ingredients:  []Ingredient{{Name: "tomato"}, {Name: "onion"}, {Name: ""}},
				Method:       "microwave",
				Difficulty:   "trivial",
				MinutesTotal: minutes(0),
			},
			[]string{
				"ingredients has 3 entries but there are 2 ingredient lines; give exactly one ingredient per line, in order",
				"ingredient 3 has an empty name; name the ingredient",
				`method "microwave" is not one of stovetop, oven, crockpot, grill, other`,
				`difficulty "trivial" is not one of easy, medium, hard, or null`,
				"minutes_total 0 is not positive; give the minutes or null",
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

func TestPromptInputRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		in   RecipeText
		want string
	}{
		"no title":       {RecipeText{Title: " ", Ingredients: []string{"1 egg"}}, "recipe has no title"},
		"no ingredients": {RecipeText{Title: "Egg"}, "recipe has no ingredient lines"},
		"negative total": {RecipeText{Title: "Egg", Ingredients: []string{"1 egg"}, TotalMinutes: minutes(-5)}, "recipe total time is -5 minutes; a given total must be positive"},
		"blank line":     {RecipeText{Title: "Egg", Ingredients: []string{"1 egg", "\t"}}, "ingredient line 2 is blank"},
		"blank step":     {RecipeText{Title: "Egg", Ingredients: []string{"1 egg"}, Steps: []string{""}}, "step 1 is blank"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeRecipe{in: tc.in}.PromptInput(tc.in)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMentionsSlowCooker(t *testing.T) {
	for _, tc := range []struct {
		in   RecipeText
		want bool
	}{
		{RecipeText{Title: "Slow Cooker Chili"}, true},
		{RecipeText{Title: "Chili", Steps: []string{"Add everything to a slow-cooker."}}, true},
		{RecipeText{Title: "Crockpot Beef Stew"}, true},
		{RecipeText{Title: "Chili", Steps: []string{"Transfer to a crock pot."}}, true},
		{RecipeText{Title: "Chili", Steps: []string{"Simmer slowly, then cook the beans."}}, false},
	} {
		if got := mentionsSlowCooker(tc.in); got != tc.want {
			t.Errorf("mentionsSlowCooker(%+v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDeriveDifficulty(t *testing.T) {
	for _, tc := range []struct {
		steps, minutes int
		crockpot       bool
		want           string
	}{
		{3, 0, false, "easy"},
		{5, 45, false, "easy"},
		{6, 30, false, "medium"},
		{4, 60, false, "medium"},
		{11, 30, false, "hard"},
		{4, 120, false, "hard"},
		{4, 480, true, "easy"},
		{12, 480, true, "hard"},
	} {
		if got := deriveDifficulty(tc.steps, tc.minutes, tc.crockpot); got != tc.want {
			t.Errorf("deriveDifficulty(%d, %d, %v) = %q, want %q", tc.steps, tc.minutes, tc.crockpot, got, tc.want)
		}
	}
}

func TestDifficultyDerivedWhenMissing(t *testing.T) {
	in := garlicPasta
	in.TotalMinutes = minutes(60)
	var f llm.Fake
	f.QueueGenerate(reply(strings.Replace(garlicPastaReply, `"difficulty":"easy"`, `"difficulty":null`, 1)))
	out, _, err := normalize(t, &f, in, 1)
	if err != nil {
		t.Fatal(err)
	}
	if out.Difficulty != "medium" || *out.MinutesTotal != 60 {
		t.Fatalf("difficulty %q, minutes %d; want medium from three steps and 60 minutes", out.Difficulty, *out.MinutesTotal)
	}
}
