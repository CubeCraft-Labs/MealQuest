package shopping_test

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// week is the Monday the tests plan, and weekDates its days.
var (
	week      = mustDate("2026-10-12")
	weekDates = []string{"2026-10-12", "2026-10-13", "2026-10-14", "2026-10-15", "2026-10-16", "2026-10-17", "2026-10-18"}
)

func mustDate(s string) store.LocalDate {
	d, err := store.ParseLocalDate(s)
	if err != nil {
		panic(err)
	}
	return d
}

type env struct {
	svc     *shopping.Service
	st      *store.Store
	dataDir string
	planID  string
	// ids are the dinners' ids by name.
	ids map[string]string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(t.Context(), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(t.Context(), st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	svc, err := shopping.NewService(st)
	if err != nil {
		t.Fatal(err)
	}
	return &env{svc: svc, st: st, dataDir: dataDir, ids: map[string]string{}}
}

// ing is a recipe ingredient as recipe grounding stores it: an empty
// qty or unit is NULL.
type ing struct {
	name, qty, unit string
}

// dinner stores a Regular named name and returns its id. With ings it is
// grounded in a fetched recipe listing them; with none it has no recipe.
func (e *env) dinner(t *testing.T, name string, ings ...ing) string {
	t.Helper()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	var recipeID sql.NullString
	status := "pending"
	if ings != nil {
		recipeID = e.recipe(t, name, ings)
		status = "grounded"
	}
	d, err := q.CreateDinner(t.Context(), db.CreateDinnerParams{
		ID:              store.NewID(),
		Name:            name,
		NormalizedName:  harness.Normalize(name),
		Status:          "regular",
		Origin:          "regular",
		RecipeID:        recipeID,
		GroundingStatus: status,
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.ids[name] = d.ID
	return d.ID
}

// recipe stores a fetched recipe titled title with ings, as
// recipes.Grounder stores a normalized page, and returns its id.
func (e *env) recipe(t *testing.T, title string, ings []ing) sql.NullString {
	t.Helper()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	r, err := q.UpsertRecipe(t.Context(), db.UpsertRecipeParams{
		ID:        store.NewID(),
		SourceUrl: "https://recipes.example/" + strings.ReplaceAll(strings.ToLower(title), " ", "-"),
		Title:     title,
		RawJsonld: "{}",
		FetchedAt: now,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.setIngredients(t, r.ID, ings)
	return sql.NullString{String: r.ID, Valid: true}
}

// setIngredients replaces recipe id's ingredients with ings, as a
// regrounding does.
func (e *env) setIngredients(t *testing.T, id string, ings []ing) {
	t.Helper()
	q := db.New(e.st.Writer)
	if err := q.DeleteRecipeIngredients(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	now := store.Now().String()
	for i, in := range ings {
		qtyUnit := strings.TrimSpace(in.qty + " " + in.unit)
		if err := q.CreateRecipeIngredient(t.Context(), db.CreateRecipeIngredientParams{
			ID:        store.NewID(),
			RecipeID:  id,
			Position:  int64(i),
			RawText:   strings.TrimSpace(qtyUnit + " " + in.name),
			Name:      sql.NullString{String: in.name, Valid: true},
			Qty:       sql.NullString{String: in.qty, Valid: in.qty != ""},
			Unit:      sql.NullString{String: in.unit, Valid: in.unit != ""},
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// recipeOf returns the id of dinner name's recipe.
func (e *env) recipeOf(t *testing.T, name string) string {
	t.Helper()
	d, err := db.New(e.st.Reader).GetDinner(t.Context(), e.ids[name])
	if err != nil {
		t.Fatal(err)
	}
	return d.RecipeID.String
}

// plan stores the week's plan with the dinners named names on weekDates,
// in order.
func (e *env) plan(t *testing.T, names ...string) {
	t.Helper()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	e.planID = store.NewID()
	if err := q.CreateWeekPlan(t.Context(), db.CreateWeekPlanParams{ID: e.planID, WeekStart: week.String(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		if err := q.PutPlanDay(t.Context(), db.PutPlanDayParams{
			ID: store.NewID(), PlanID: e.planID, Date: weekDates[i],
			DinnerID: sql.NullString{String: e.ids[name], Valid: true}, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// swap plans the dinner named name on date, as a swap on the Draft board
// does.
func (e *env) swap(t *testing.T, date, name string) {
	t.Helper()
	n, err := db.New(e.st.Writer).SwapPlanDay(t.Context(), db.SwapPlanDayParams{
		DinnerID: sql.NullString{String: e.ids[name], Valid: true}, UpdatedAt: store.Now().String(),
		PlanID: e.planID, Date: date,
	})
	if err != nil || n != 1 {
		t.Fatalf("swap %s to %s: %d rows, %v", date, name, n, err)
	}
}

// haveIt marks the item named name of dinner's group "have it".
func (e *env) haveIt(t *testing.T, l shopping.List, dinner, name string) string {
	t.Helper()
	for _, g := range l.Groups {
		for _, it := range g.Items {
			if g.DinnerName == dinner && it.Name == name {
				if _, err := e.st.Writer.Exec(`UPDATE shopping_items SET needed = 0 WHERE id = ?`, it.ID); err != nil {
					t.Fatal(err)
				}
				return it.ID
			}
		}
	}
	t.Fatalf("no item %q of %s", name, dinner)
	return ""
}

func (e *env) generate(t *testing.T) shopping.Generated {
	t.Helper()
	g, err := e.svc.Generate(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// lines renders l one item a line as "position date dinner: position
// name [qty] needed|have it".
func lines(l shopping.List) []string {
	var out []string
	for _, g := range l.Groups {
		for _, it := range g.Items {
			qty := "-"
			if it.Qty != nil {
				qty = *it.Qty
			}
			mark := "needed"
			if !it.Needed {
				mark = "have it"
			}
			out = append(out, fmt.Sprintf("%d %s %s: %d %s [%s] %s", g.Position, g.Date, g.DinnerName, it.Position, it.Name, qty, mark))
		}
	}
	return out
}

// stored renders the stored list of the week as lines does, so a test
// checks the database holds what Generate returned.
func (e *env) stored(t *testing.T) []string {
	t.Helper()
	rows, err := e.st.Reader.Query(`SELECT g.position, g.date, d.name, i.position, i.name, COALESCE(i.qty, '-'), i.needed
		FROM shopping_lists l
		JOIN shopping_groups g ON g.list_id = l.id
		JOIN dinners d ON d.id = g.dinner_id
		JOIN shopping_items i ON i.group_id = g.id
		WHERE l.week_start = ?
		ORDER BY g.position, i.position`, week.String())
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var (
			gpos, ipos, needed int
			date, dinner, name string
			qty                string
		)
		if err := rows.Scan(&gpos, &date, &dinner, &ipos, &name, &qty, &needed); err != nil {
			t.Fatal(err)
		}
		mark := "needed"
		if needed == 0 {
			mark = "have it"
		}
		out = append(out, fmt.Sprintf("%d %s %s: %d %s [%s] %s", gpos, date, dinner, ipos, name, qty, mark))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// count returns the number of rows in table.
func (e *env) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	if err := e.st.Reader.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func wantLines(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s =\n  %s\nwant\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestGenerateSevenDayPlan(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"}, ing{"kidney beans", "2", "cans"}, ing{"onion", "1", ""})
	e.dinner(t, "Pot Roast", ing{"chuck roast", "3", "lb"}, ing{"carrots", "4", ""})
	e.dinner(t, "Chicken Fajitas", ing{"chicken breast", "500", "g"}, ing{"bell peppers", "3", ""}, ing{"tortillas", "8", ""})
	e.dinner(t, "Spaghetti Bolognese", ing{"spaghetti", "1", "lb"}, ing{"crushed tomatoes", "28", "oz"})
	e.dinner(t, "Sheet Pan Salmon", ing{"salmon fillets", "4", ""}, ing{"lemon", "1", ""}, ing{"dill", "", "sprig"})
	e.dinner(t, "Pad Thai", ing{"rice noodles", "8", "oz"}, ing{"bean sprouts", "2", "cups"}, ing{"salt", "", ""})
	e.dinner(t, "Shawarma Bowls", ing{"chicken thighs", "2", "lb"}, ing{"pita", "4", ""})
	e.plan(t, "Beef Chili", "Pot Roast", "Chicken Fajitas", "Spaghetti Bolognese", "Sheet Pan Salmon", "Pad Thai", "Shawarma Bowls")

	g := e.generate(t)

	want := []string{
		"0 2026-10-12 Beef Chili: 0 ground beef [1 lb] needed",
		"0 2026-10-12 Beef Chili: 1 kidney beans [2 cans] needed",
		"0 2026-10-12 Beef Chili: 2 onion [1] needed",
		"1 2026-10-13 Pot Roast: 0 chuck roast [3 lb] needed",
		"1 2026-10-13 Pot Roast: 1 carrots [4] needed",
		"2 2026-10-14 Chicken Fajitas: 0 chicken breast [500 g] needed",
		"2 2026-10-14 Chicken Fajitas: 1 bell peppers [3] needed",
		"2 2026-10-14 Chicken Fajitas: 2 tortillas [8] needed",
		"3 2026-10-15 Spaghetti Bolognese: 0 spaghetti [1 lb] needed",
		"3 2026-10-15 Spaghetti Bolognese: 1 crushed tomatoes [28 oz] needed",
		"4 2026-10-16 Sheet Pan Salmon: 0 salmon fillets [4] needed",
		"4 2026-10-16 Sheet Pan Salmon: 1 lemon [1] needed",
		"4 2026-10-16 Sheet Pan Salmon: 2 dill [sprig] needed",
		"5 2026-10-17 Pad Thai: 0 rice noodles [8 oz] needed",
		"5 2026-10-17 Pad Thai: 1 bean sprouts [2 cups] needed",
		"5 2026-10-17 Pad Thai: 2 salt [-] needed",
		"6 2026-10-18 Shawarma Bowls: 0 chicken thighs [2 lb] needed",
		"6 2026-10-18 Shawarma Bowls: 1 pita [4] needed",
	}
	wantLines(t, "list", lines(g.List), want)
	wantLines(t, "stored list", e.stored(t), want)
	if len(g.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none", g.Skipped)
	}
	for _, grp := range g.List.Groups {
		if grp.DinnerID != e.ids[grp.DinnerName] {
			t.Errorf("group %s dinner id = %s, want %s", grp.DinnerName, grp.DinnerID, e.ids[grp.DinnerName])
		}
	}
	if g.List.WeekStart != "2026-10-12" || g.List.PlanID != e.planID || g.List.Status != shopping.StatusCurrent || g.List.Title != nil {
		t.Errorf("list = %+v, want the week's current, untitled list of plan %s", g.List, e.planID)
	}
	var title sql.NullString
	if err := e.st.Reader.QueryRow(`SELECT title FROM shopping_lists WHERE id = ?`, g.List.ID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title.Valid {
		t.Errorf("stored title = %q, want NULL until the AI titles the list", title.String)
	}

	// Generating again from the same plan changes nothing: same rows, same
	// ids, no extra rows.
	again := e.generate(t)
	if !reflect.DeepEqual(again, g) {
		t.Errorf("second generation =\n %+v\nwant the first\n %+v", again, g)
	}
	if n := e.count(t, "shopping_lists"); n != 1 {
		t.Errorf("%d lists, want 1", n)
	}
	if n := e.count(t, "shopping_items"); n != len(want) {
		t.Errorf("%d items stored, want %d", n, len(want))
	}
}

// TestGenerateManualRecipe grounds a dinner with a recipe typed in, through
// dinners.Service as the household does: its lines are stored unnormalized,
// so each is an item as typed, with no quantity.
func TestGenerateManualRecipe(t *testing.T) {
	e := newEnv(t)
	model := &llm.Fake{}
	grounder := recipestest.NewGrounder(t, e.st, e.dataDir, &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, model)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(e.st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	dsvc, err := dinners.NewService(e.st, grounder, runner, logger)
	if err != nil {
		t.Fatal(err)
	}
	e.dinner(t, "Grandma's Goulash")
	minutes, method := 45, "stovetop"
	if _, err := dsvc.Ground(t.Context(), e.ids["Grandma's Goulash"], dinners.Fix{Manual: &dinners.ManualRecipe{
		Ingredients: []string{"2 lb beef chuck", "1 onion, diced", "paprika to taste"},
		Steps:       []string{"Brown the beef."},
		Minutes:     &minutes,
		Method:      &method,
	}}, "member-1"); err != nil {
		t.Fatal(err)
	}
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.plan(t, "Grandma's Goulash", "Beef Chili")

	g := e.generate(t)

	want := []string{
		"0 2026-10-12 Grandma's Goulash: 0 2 lb beef chuck [-] needed",
		"0 2026-10-12 Grandma's Goulash: 1 1 onion, diced [-] needed",
		"0 2026-10-12 Grandma's Goulash: 2 paprika to taste [-] needed",
		"1 2026-10-13 Beef Chili: 0 ground beef [1 lb] needed",
	}
	wantLines(t, "list", lines(g.List), want)
	wantLines(t, "stored list", e.stored(t), want)
	if len(g.Skipped) != 0 {
		t.Errorf("skipped = %+v, want none", g.Skipped)
	}
	if reqs := model.Requests(); len(reqs) != 0 {
		t.Errorf("%d model requests, want none", len(reqs))
	}
}

// TestGenerateSkipsDinnersWithoutIngredients plans a dinner with no recipe
// (its grounding pending) and one whose recipe lists no ingredients: each
// day gets no group and is reported with why.
func TestGenerateSkipsDinnersWithoutIngredients(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Mystery Stew")
	e.dinner(t, "Toast", []ing{}...) // a recipe with no ingredients
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.plan(t, "Mystery Stew", "Toast", "Beef Chili")

	g := e.generate(t)

	want := []string{"0 2026-10-14 Beef Chili: 0 ground beef [1 lb] needed"}
	wantLines(t, "list", lines(g.List), want)
	wantLines(t, "stored list", e.stored(t), want)
	wantSkipped := []shopping.SkippedDay{
		{Date: "2026-10-12", DinnerID: e.ids["Mystery Stew"], DinnerName: "Mystery Stew", Reason: shopping.SkipNoRecipe},
		{Date: "2026-10-13", DinnerID: e.ids["Toast"], DinnerName: "Toast", Reason: shopping.SkipNoIngredients},
	}
	if !reflect.DeepEqual(g.Skipped, wantSkipped) {
		t.Errorf("skipped = %+v, want %+v", g.Skipped, wantSkipped)
	}
	if n := e.count(t, "shopping_groups"); n != 1 {
		t.Errorf("%d groups stored, want 1: a skipped day gets no empty group", n)
	}
}

// TestGenerateAfterSwapKeepsHaveIt swaps a day to a new dinner and moves a
// dinner to another day, and regrounds a dinner so its ingredients change
// order and quantity: an item whose dinner and name are unchanged keeps
// its id and "have it" wherever it now is; the rest is added as needed or
// removed.
func TestGenerateAfterSwapKeepsHaveIt(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"}, ing{"onion", "1", ""}, ing{"kidney beans", "2", "cans"})
	e.dinner(t, "Chicken Fajitas", ing{"chicken breast", "500", "g"}, ing{"tortillas", "8", ""})
	e.dinner(t, "Sheet Pan Salmon", ing{"salmon fillets", "4", ""}, ing{"lemon", "1", ""})
	e.dinner(t, "Pad Thai", ing{"rice noodles", "8", "oz"}, ing{"lime", "2", ""})
	e.plan(t, "Beef Chili", "Chicken Fajitas", "Sheet Pan Salmon")
	first := e.generate(t).List
	onion := e.haveIt(t, first, "Beef Chili", "onion")
	tortillas := e.haveIt(t, first, "Chicken Fajitas", "tortillas")
	lemon := e.haveIt(t, first, "Sheet Pan Salmon", "lemon")

	// Tuesday becomes Pad Thai and Fajitas moves to Wednesday, dropping
	// the Salmon; the Chili is regrounded: onion first, no beans, a
	// pepper added and more beef.
	e.swap(t, "2026-10-13", "Pad Thai")
	e.swap(t, "2026-10-14", "Chicken Fajitas")
	e.setIngredients(t, e.recipeOf(t, "Beef Chili"), []ing{{"onion", "2", ""}, {"ground beef", "2", "lb"}, {"jalapeño", "1", ""}})

	g := e.generate(t)

	want := []string{
		"0 2026-10-12 Beef Chili: 0 onion [2] have it",
		"0 2026-10-12 Beef Chili: 1 ground beef [2 lb] needed",
		"0 2026-10-12 Beef Chili: 2 jalapeño [1] needed",
		"1 2026-10-13 Pad Thai: 0 rice noodles [8 oz] needed",
		"1 2026-10-13 Pad Thai: 1 lime [2] needed",
		"2 2026-10-14 Chicken Fajitas: 0 chicken breast [500 g] needed",
		"2 2026-10-14 Chicken Fajitas: 1 tortillas [8] have it",
	}
	wantLines(t, "list", lines(g.List), want)
	wantLines(t, "stored list", e.stored(t), want)
	ids := map[string]string{}
	for _, grp := range g.List.Groups {
		for _, it := range grp.Items {
			ids[grp.DinnerName+"/"+it.Name] = it.ID
		}
	}
	if ids["Beef Chili/onion"] != onion || ids["Chicken Fajitas/tortillas"] != tortillas {
		t.Errorf("kept items have ids %s and %s, want %s and %s", ids["Beef Chili/onion"], ids["Chicken Fajitas/tortillas"], onion, tortillas)
	}
	var n int
	if err := e.st.Reader.QueryRow(`SELECT count(*) FROM shopping_items WHERE id = ?`, lemon).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the Salmon's lemon is still stored after the Salmon left the plan")
	}
	if got := e.count(t, "shopping_items"); got != len(want) {
		t.Errorf("%d items stored, want %d", got, len(want))
	}
	if got := e.count(t, "shopping_groups"); got != 3 {
		t.Errorf("%d groups stored, want 3", got)
	}
	if g.List.ID != first.ID {
		t.Errorf("list id = %s, want the first generation's %s", g.List.ID, first.ID)
	}
}

// TestGenerateMatchesRepeatedNamesInOrder lists a name twice in one
// dinner: the second "salt" marked "have it" stays the second.
func TestGenerateMatchesRepeatedNamesInOrder(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Pasta", ing{"salt", "1", "tbsp"}, ing{"spaghetti", "1", "lb"}, ing{"salt", "", "pinch"})
	e.plan(t, "Pasta")
	first := e.generate(t).List
	second := first.Groups[0].Items[2].ID
	if _, err := e.st.Writer.Exec(`UPDATE shopping_items SET needed = 0 WHERE id = ?`, second); err != nil {
		t.Fatal(err)
	}
	e.setIngredients(t, e.recipeOf(t, "Pasta"), []ing{{"spaghetti", "1", "lb"}, {"salt", "1", "tbsp"}, {"salt", "", "pinch"}})

	g := e.generate(t)

	wantLines(t, "list", lines(g.List), []string{
		"0 2026-10-12 Pasta: 0 spaghetti [1 lb] needed",
		"0 2026-10-12 Pasta: 1 salt [1 tbsp] needed",
		"0 2026-10-12 Pasta: 2 salt [pinch] have it",
	})
	if id := g.List.Groups[0].Items[2].ID; id != second {
		t.Errorf("second salt id = %s, want %s", id, second)
	}
}

func TestGenerateWeekWithoutPlan(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Generate(t.Context(), week); !errors.Is(err, shopping.ErrPlanNotFound) {
		t.Fatalf("Generate = %v, want ErrPlanNotFound", err)
	}
	if n := e.count(t, "shopping_lists"); n != 0 {
		t.Errorf("%d lists stored, want none", n)
	}
}

func TestGenerateRefusesNonMonday(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Generate(t.Context(), mustDate("2026-10-13")); err == nil {
		t.Fatal("Generate of a Tuesday succeeded")
	}
}

// TestGenerateRefusesFrozenList leaves a list frozen into the Supply
// archive as it is.
func TestGenerateRefusesFrozenList(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.dinner(t, "Pad Thai", ing{"rice noodles", "8", "oz"})
	e.plan(t, "Beef Chili")
	e.generate(t)
	if _, err := e.st.Writer.Exec(`UPDATE shopping_lists SET status = 'archived', frozen_at = '2026-10-19T04:05:00Z'`); err != nil {
		t.Fatal(err)
	}
	e.swap(t, "2026-10-12", "Pad Thai")

	if _, err := e.svc.Generate(t.Context(), week); !errors.Is(err, shopping.ErrFrozen) {
		t.Fatalf("Generate = %v, want ErrFrozen", err)
	}
	wantLines(t, "stored list", e.stored(t), []string{"0 2026-10-12 Beef Chili: 0 ground beef [1 lb] needed"})
}

// TestGeneratePushedRefusesEditedPlan makes the Supply run of a pushed
// plan at the revision pushed, then edits the plan: the pushed revision no
// longer makes one, and the Supply run is left as it was.
func TestGeneratePushedRefusesEditedPlan(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.dinner(t, "Pad Thai", ing{"rice noodles", "8", "oz"})
	e.plan(t, "Beef Chili")
	q := db.New(e.st.Writer)
	pushed, err := q.GetWeekPlan(t.Context(), week.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.GeneratePushed(t.Context(), week, pushed.Revision); err != nil {
		t.Fatalf("GeneratePushed at the pushed revision: %v", err)
	}
	e.swap(t, "2026-10-12", "Pad Thai")
	if err := q.MarkWeekPlanEdited(t.Context(), db.MarkWeekPlanEditedParams{UpdatedAt: store.Now().String(), ID: e.planID}); err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.GeneratePushed(t.Context(), week, pushed.Revision); !errors.Is(err, shopping.ErrPlanChanged) {
		t.Fatalf("GeneratePushed after an edit = %v, want ErrPlanChanged", err)
	}
	wantLines(t, "stored list", e.stored(t), []string{"0 2026-10-12 Beef Chili: 0 ground beef [1 lb] needed"})
}

// TestGenerateRefusesUnnamedIngredient: grounding always names a fetched
// recipe's ingredient, so one without a name is an error, and nothing is
// stored.
func TestGenerateRefusesUnnamedIngredient(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"}, ing{"onion", "1", ""})
	e.plan(t, "Beef Chili")
	if _, err := e.st.Writer.Exec(`UPDATE recipe_ingredients SET name = NULL WHERE position = 1`); err != nil {
		t.Fatal(err)
	}

	_, err := e.svc.Generate(t.Context(), week)
	if err == nil || !strings.Contains(err.Error(), `ingredient 2 ("1 onion") has no normalized name`) {
		t.Fatalf("Generate = %v, want the unnamed ingredient refused", err)
	}
	if n := e.count(t, "shopping_lists"); n != 0 {
		t.Errorf("%d lists stored, want none", n)
	}
}

// TestGetReadsStoredList reads back what Generate stored, with what the
// household marked, and counts it.
func TestGetReadsStoredList(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"}, ing{"onion", "1", ""})
	e.dinner(t, "Pot Roast", ing{"carrots", "4", ""})
	e.plan(t, "Beef Chili", "Pot Roast")
	made := e.generate(t).List
	e.haveIt(t, made, "Beef Chili", "onion")

	got, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, "read list", lines(got), []string{
		"0 2026-10-12 Beef Chili: 0 ground beef [1 lb] needed",
		"0 2026-10-12 Beef Chili: 1 onion [1] have it",
		"1 2026-10-13 Pot Roast: 0 carrots [4] needed",
	})
	if got.ID != made.ID || got.WeekStart != week.String() || got.Status != shopping.StatusCurrent || got.Title != nil || got.SentToChatAt != nil {
		t.Errorf("read list = %+v", got)
	}
	if c := got.Counts(); c != (shopping.Counts{Needed: 2, Total: 3}) {
		t.Errorf("Counts = %+v, want 2 of 3", c)
	}
}

func TestGetWeekWithoutList(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Get(t.Context(), week); !errors.Is(err, shopping.ErrListNotFound) {
		t.Fatalf("Get = %v, want ErrListNotFound", err)
	}
	if _, err := e.svc.Get(t.Context(), mustDate("2026-10-13")); err == nil {
		t.Fatal("Get of a Tuesday succeeded")
	}
}

// TestSetNeeded marks items and returns the counts after each change.
func TestSetNeeded(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"}, ing{"onion", "1", ""}, ing{"garlic", "2", "cloves"})
	e.plan(t, "Beef Chili")
	l := e.generate(t).List
	onion, garlic := l.Groups[0].Items[1].ID, l.Groups[0].Items[2].ID

	for _, step := range []struct {
		id     string
		needed bool
		want   shopping.Counts
	}{
		{onion, false, shopping.Counts{Needed: 2, Total: 3}},
		{onion, false, shopping.Counts{Needed: 2, Total: 3}},
		{garlic, false, shopping.Counts{Needed: 1, Total: 3}},
		{onion, true, shopping.Counts{Needed: 2, Total: 3}},
	} {
		c, err := e.svc.SetNeeded(t.Context(), step.id, step.needed)
		if err != nil {
			t.Fatal(err)
		}
		if c != step.want {
			t.Errorf("SetNeeded(%s, %v) = %+v, want %+v", step.id, step.needed, c, step.want)
		}
	}
	wantLines(t, "stored list", e.stored(t), []string{
		"0 2026-10-12 Beef Chili: 0 ground beef [1 lb] needed",
		"0 2026-10-12 Beef Chili: 1 onion [1] needed",
		"0 2026-10-12 Beef Chili: 2 garlic [2 cloves] have it",
	})
}

func TestSetNeededUnknownItem(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.SetNeeded(t.Context(), store.NewID(), false); !errors.Is(err, shopping.ErrItemNotFound) {
		t.Fatalf("SetNeeded = %v, want ErrItemNotFound", err)
	}
}

// TestSetNeededRefusesFrozenList leaves an item of a list in the Supply
// archive as it was.
func TestSetNeededRefusesFrozenList(t *testing.T) {
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.plan(t, "Beef Chili")
	l := e.generate(t).List
	if _, err := e.st.Writer.Exec(`UPDATE shopping_lists SET status = 'archived', frozen_at = '2026-10-19T04:05:00Z'`); err != nil {
		t.Fatal(err)
	}

	if _, err := e.svc.SetNeeded(t.Context(), l.Groups[0].Items[0].ID, false); !errors.Is(err, shopping.ErrFrozen) {
		t.Fatalf("SetNeeded = %v, want ErrFrozen", err)
	}
	wantLines(t, "stored list", e.stored(t), []string{"0 2026-10-12 Beef Chili: 0 ground beef [1 lb] needed"})
}
