package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat/chattest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const shoppingPath = "/api/v1/shopping/"

// shopIng is a recipe ingredient as recipe grounding stores it: an empty
// qty or unit is NULL.
type shopIng struct {
	name, qty, unit string
}

// groundDay grounds the dinner planned on date of the plans env's week in
// a fetched recipe listing ings.
func (e plansEnv) groundDay(t *testing.T, date string, ings ...shopIng) {
	t.Helper()
	ctx := t.Context()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	r, err := q.UpsertRecipe(ctx, db.UpsertRecipeParams{
		ID: store.NewID(), SourceUrl: "https://recipes.example/" + date, Title: "Recipe of " + date,
		RawJsonld: "{}", FetchedAt: now, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, in := range ings {
		if err := q.CreateRecipeIngredient(ctx, db.CreateRecipeIngredientParams{
			ID: store.NewID(), RecipeID: r.ID, Position: int64(i),
			RawText:   strings.TrimSpace(in.qty + " " + in.unit + " " + in.name),
			Name:      sql.NullString{String: in.name, Valid: true},
			Qty:       sql.NullString{String: in.qty, Valid: in.qty != ""},
			Unit:      sql.NullString{String: in.unit, Valid: in.unit != ""},
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.st.Writer.ExecContext(ctx, `UPDATE dinners SET recipe_id = ?, grounding_status = 'grounded'
		WHERE id = (SELECT dinner_id FROM plan_days WHERE date = ?)`, r.ID, date); err != nil {
		t.Fatal(err)
	}
}

// groundWeek grounds Monday's Beef Chili and Tuesday's Regular: five items,
// one with no quantity.
func (e plansEnv) groundWeek(t *testing.T) {
	t.Helper()
	e.groundDay(t, "2026-10-12", shopIng{"ground beef", "1", "lb"}, shopIng{"kidney beans", "2", "cans"}, shopIng{"onion", "1", ""})
	e.groundDay(t, "2026-10-13", shopIng{"spaghetti", "500", "g"}, shopIng{"basil", "", ""})
}

// archive freezes the week's Supply run into the Supply archive.
func (e plansEnv) archive(t *testing.T) {
	t.Helper()
	if _, err := e.st.Writer.ExecContext(t.Context(),
		`UPDATE shopping_lists SET status = 'archived', frozen_at = '2026-10-19T04:05:00Z'; UPDATE shopping_groups SET frozen_dinner_name = (SELECT name FROM dinners WHERE dinners.id = dinner_id) WHERE list_id IN (SELECT id FROM shopping_lists WHERE frozen_at IS NOT NULL)`); err != nil {
		t.Fatal(err)
	}
}

// supplyRunJSON is a Supply run answer decoded with every field the API
// promises.
type supplyRunJSON struct {
	WeekStart             string  `json:"weekStart"`
	Label                 string  `json:"label"`
	Title                 *string `json:"title"`
	Status                string  `json:"status"`
	SentToChatAt          *string `json:"sent_to_chat_at"`
	ChatSendUnconfirmedAt *string `json:"chat_send_unconfirmed_at"`
	NeededCount           int     `json:"needed_count"`
	TotalCount            int     `json:"total_count"`
	Groups                []struct {
		Dinner struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"dinner"`
		Date  string `json:"date"`
		Day   string `json:"day"`
		Items []struct {
			ID     string  `json:"id"`
			Name   string  `json:"name"`
			Qty    *string `json:"qty"`
			Needed bool    `json:"needed"`
		} `json:"items"`
	} `json:"groups"`
}

// render is l one item a line as "day date dinner: name [qty] needed|have
// it".
func (l supplyRunJSON) render() []string {
	var out []string
	for _, g := range l.Groups {
		for _, it := range g.Items {
			qty := "null"
			if it.Qty != nil {
				qty = *it.Qty
			}
			mark := "needed"
			if !it.Needed {
				mark = "have it"
			}
			out = append(out, g.Day+" "+g.Date+" "+g.Dinner.Name+": "+it.Name+" ["+qty+"] "+mark)
		}
	}
	return out
}

// itemID is the id of the item named name.
func (l supplyRunJSON) itemID(t *testing.T, name string) string {
	t.Helper()
	for _, g := range l.Groups {
		for _, it := range g.Items {
			if it.Name == name {
				return it.ID
			}
		}
	}
	t.Fatalf("no item %q", name)
	return ""
}

// callShopping makes a request and decodes a 200 answer into out.
func callShopping(t *testing.T, e plansEnv, method, target, body string, out any) (int, map[string]any) {
	t.Helper()
	rec, got := callDinners(t, e.h, method, target, body)
	if rec.Code == http.StatusOK && out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, got
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

var weekLines = []string{
	"Monday 2026-10-12 Beef Chili: ground beef [1 lb] needed",
	"Monday 2026-10-12 Beef Chili: kidney beans [2 cans] needed",
	"Monday 2026-10-12 Beef Chili: onion [1] needed",
	"Tuesday 2026-10-13 Regular 2026-10-13: spaghetti [500 g] needed",
	"Tuesday 2026-10-13 Regular 2026-10-13: basil [null] needed",
}

// TestSupplyRunShape generates the week's Supply run and reads it back:
// both answers carry exactly the promised fields.
func TestSupplyRunShape(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)

	if status, body := callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", nil); status != http.StatusNotFound || errorCode(body) != "not_found" {
		t.Fatalf("GET before generating = %d %v, want 404 not_found", status, body)
	}

	var made supplyRunJSON
	status, raw := callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", &made)
	if status != http.StatusOK {
		t.Fatalf("generate = %d %v", status, raw)
	}
	wantTop := []string{"chat_send_unconfirmed_at", "groups", "label", "needed_count", "sent_to_chat_at", "skipped", "status", "title", "title_job_id", "total_count", "weekStart"}
	if got := keys(raw); !reflect.DeepEqual(got, wantTop) {
		t.Errorf("generate fields = %v, want %v", got, wantTop)
	}
	if skipped, _ := raw["skipped"].([]any); len(skipped) != 5 {
		t.Errorf("skipped = %v, want the five days without a recipe", raw["skipped"])
	}

	var got supplyRunJSON
	status, raw = callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", &got)
	if status != http.StatusOK {
		t.Fatalf("GET = %d %v", status, raw)
	}
	if k := keys(raw); !reflect.DeepEqual(k, slices.DeleteFunc(slices.Clone(wantTop), func(s string) bool { return s == "skipped" || s == "title_job_id" })) {
		t.Errorf("GET fields = %v", k)
	}
	group := raw["groups"].([]any)[0].(map[string]any)
	if k := keys(group); !reflect.DeepEqual(k, []string{"date", "day", "dinner", "items"}) {
		t.Errorf("group fields = %v", k)
	}
	if k := keys(group["dinner"].(map[string]any)); !reflect.DeepEqual(k, []string{"id", "name"}) {
		t.Errorf("dinner fields = %v", k)
	}
	if k := keys(group["items"].([]any)[0].(map[string]any)); !reflect.DeepEqual(k, []string{"id", "name", "needed", "qty"}) {
		t.Errorf("item fields = %v", k)
	}
	if !reflect.DeepEqual(got, made) {
		t.Errorf("GET = %+v, want what generate made %+v", got, made)
	}
	if got.WeekStart != "2026-10-12" || got.Label != "Week 42 · Oct 12 – 18" || got.Title != nil ||
		got.Status != "current" || got.SentToChatAt != nil || got.NeededCount != 5 || got.TotalCount != 5 {
		t.Errorf("GET = %+v", got)
	}
	if !reflect.DeepEqual(got.render(), weekLines) {
		t.Errorf("items =\n  %s\nwant\n  %s", strings.Join(got.render(), "\n  "), strings.Join(weekLines, "\n  "))
	}
	if got.Groups[0].Dinner.ID == "" || got.Groups[0].Items[0].ID == "" {
		t.Errorf("ids missing: %+v", got.Groups[0])
	}
}

// TestShoppingItemToggle marks items "have it" and needed again: each
// answer carries the item and the new counts, and the list read back
// agrees.
func TestShoppingItemToggle(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)
	var l supplyRunJSON
	if status, body := callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", &l); status != http.StatusOK {
		t.Fatalf("generate = %d %v", status, body)
	}
	onion, basil := l.itemID(t, "onion"), l.itemID(t, "basil")

	for _, step := range []struct {
		id           string
		needed       bool
		neededCount  int
		wantAfterGet string
	}{
		{onion, false, 4, "Monday 2026-10-12 Beef Chili: onion [1] have it"},
		{onion, false, 4, "Monday 2026-10-12 Beef Chili: onion [1] have it"}, // sets, never flips
		{basil, false, 3, "Tuesday 2026-10-13 Regular 2026-10-13: basil [null] have it"},
		{onion, true, 4, "Monday 2026-10-12 Beef Chili: onion [1] needed"},
	} {
		body := `{"needed":false}`
		if step.needed {
			body = `{"needed":true}`
		}
		status, got := callShopping(t, e, http.MethodPatch, shoppingPath+"items/"+step.id, body, nil)
		want := map[string]any{"id": step.id, "needed": step.needed, "needed_count": float64(step.neededCount), "total_count": float64(5)}
		if status != http.StatusOK || !reflect.DeepEqual(got, want) {
			t.Fatalf("PATCH %s = %d %v, want %v", body, status, got, want)
		}
		var after supplyRunJSON
		callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", &after)
		if after.NeededCount != step.neededCount || after.TotalCount != 5 || !slices.Contains(after.render(), step.wantAfterGet) {
			t.Errorf("GET after PATCH %s = %d/%d %v", body, after.NeededCount, after.TotalCount, after.render())
		}
	}
}

func TestShoppingItemToggleErrors(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)
	var l supplyRunJSON
	callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", &l)
	onion := l.itemID(t, "onion")

	for _, tc := range []struct {
		name, id, body string
		status         int
		code           string
	}{
		{"no needed", onion, `{}`, http.StatusBadRequest, "invalid_request"},
		{"needed null", onion, `{"needed":null}`, http.StatusBadRequest, "invalid_request"},
		{"needed not a bool", onion, `{"needed":"no"}`, http.StatusBadRequest, "invalid_request"},
		{"unknown field", onion, `{"needed":false,"qty":"2"}`, http.StatusBadRequest, "invalid_request"},
		{"unknown item", store.NewID(), `{"needed":false}`, http.StatusNotFound, "not_found"},
	} {
		status, body := callShopping(t, e, http.MethodPatch, shoppingPath+"items/"+tc.id, tc.body, nil)
		if status != tc.status || errorCode(body) != tc.code {
			t.Errorf("%s: PATCH = %d %v, want %d %s", tc.name, status, body, tc.status, tc.code)
		}
	}

	e.archive(t)
	status, body := callShopping(t, e, http.MethodPatch, shoppingPath+"items/"+onion, `{"needed":false}`, nil)
	if status != http.StatusConflict || errorCode(body) != "archived" {
		t.Errorf("PATCH of an archived list = %d %v, want 409 archived", status, body)
	}
	var after supplyRunJSON
	callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", &after)
	if after.Status != "archived" || !reflect.DeepEqual(after.render(), weekLines) || after.NeededCount != 5 {
		t.Errorf("archived list after PATCH = %s %v, want unchanged", after.Status, after.render())
	}
}

// TestSupplyRunRegenerate remakes the list after a swap: the swapped-out
// dinner's group goes, and what the household marked "have it" stays.
func TestSupplyRunRegenerate(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)
	var l supplyRunJSON
	callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", &l)
	onion := l.itemID(t, "onion")
	callShopping(t, e, http.MethodPatch, shoppingPath+"items/"+onion, `{"needed":false}`, nil)
	if _, err := e.svc.Swap(t.Context(), mustLocalDate(t, "2026-10-12"), "2026-10-13", e.questLog); err != nil {
		t.Fatal(err)
	}

	var again supplyRunJSON
	if status, body := callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", &again); status != http.StatusOK {
		t.Fatalf("generate again = %d %v", status, body)
	}
	want := []string{
		"Monday 2026-10-12 Beef Chili: ground beef [1 lb] needed",
		"Monday 2026-10-12 Beef Chili: kidney beans [2 cans] needed",
		"Monday 2026-10-12 Beef Chili: onion [1] have it",
	}
	if !reflect.DeepEqual(again.render(), want) || again.NeededCount != 2 || again.TotalCount != 3 {
		t.Errorf("remade = %d/%d %v, want 2/3 %v", again.NeededCount, again.TotalCount, again.render(), want)
	}
	if again.itemID(t, "onion") != onion {
		t.Error("onion has a new id after the remake")
	}
}

func TestSupplyRunErrors(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)
	for _, tc := range []struct {
		name, method, path string
		status             int
		code               string
	}{
		{"GET not a Monday", http.MethodGet, "2026-10-13", http.StatusBadRequest, "invalid_week"},
		{"GET no list", http.MethodGet, "2026-10-19", http.StatusNotFound, "not_found"},
		{"generate not a Monday", http.MethodPost, "2026-10-13/generate", http.StatusBadRequest, "invalid_week"},
		{"generate no plan", http.MethodPost, "2026-10-19/generate", http.StatusNotFound, "not_found"},
	} {
		status, body := callShopping(t, e, tc.method, shoppingPath+tc.path, "", nil)
		if status != tc.status || errorCode(body) != tc.code {
			t.Errorf("%s = %d %v, want %d %s", tc.name, status, body, tc.status, tc.code)
		}
	}

	callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", nil)
	e.archive(t)
	status, body := callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", nil)
	if status != http.StatusConflict || errorCode(body) != "archived" {
		t.Errorf("generate of an archived list = %d %v, want 409 archived", status, body)
	}
}

// TestPushPlanMakesSupplyRun: a Push to Calendar makes the week's Supply
// run from the plan pushed.
func TestPushPlanMakesSupplyRun(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)
	e.connectCalendar(t, true)

	rec, body := e.push(t, "2026-10-12")
	if rec.Code != http.StatusOK || body["status"] != "synced" {
		t.Fatalf("push = %d %s", rec.Code, rec.Body)
	}
	run, _ := body["supply_run"].(map[string]any)
	if k := keys(run); run["status"] != "generated" || run["error"] != nil || !reflect.DeepEqual(k, []string{"error", "status", "title_job_id"}) {
		t.Errorf("supply_run = %v, want generated with no error", run)
	}
	if job := titleJob(t, e, run["title_job_id"]); job["status"] != "queued" {
		t.Errorf("title job = %v, want queued: no runner in this test", job)
	}
	var l supplyRunJSON
	if status, got := callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", &l); status != http.StatusOK {
		t.Fatalf("GET after push = %d %v, want the Supply run", status, got)
	}
	if !reflect.DeepEqual(l.render(), weekLines) {
		t.Errorf("Supply run after push = %v", l.render())
	}
}

// heldTitler is a shopping.Titler that reports each call on entered and
// answers title only once release is closed.
type heldTitler struct {
	entered chan struct{}
	release chan struct{}
}

func newHeldTitler() heldTitler {
	return heldTitler{entered: make(chan struct{}, 4), release: make(chan struct{})}
}

func (h heldTitler) title(ctx context.Context, _ string, _ skills.ArchiveTitleRequest) (skills.ArchiveTitle, error) {
	h.entered <- struct{}{}
	select {
	case <-h.release:
		return skills.ArchiveTitle{Title: "Chili and Friends"}, nil
	case <-ctx.Done():
		return skills.ArchiveTitle{}, ctx.Err()
	}
}

// startRunner starts e's job runner, stopping it when the test ends. e's
// logs must not be read while it runs.
func (e plansEnv) startRunner(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.runner.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		e.runner.Wait()
	})
}

// waitEntered waits for the title call to begin.
func (h heldTitler) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("archive_title was never called")
	}
}

// waitTitled waits for the week's Supply run to have a title through GET
// and returns it.
func (e plansEnv) waitTitled(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var l supplyRunJSON
		callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", &l)
		if l.Title != nil {
			return *l.Title
		}
		if time.Now().After(deadline) {
			t.Fatal("the Supply run was never titled")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestPushPlanDoesNotWaitForTitle: Push to Calendar answers while the
// Supply run's title is still being written; the list reads untitled until
// the title job stores it.
func TestPushPlanDoesNotWaitForTitle(t *testing.T) {
	t.Parallel()
	held := newHeldTitler()
	e := newPlansRouterTitled(t, held.title)
	e.groundWeek(t)
	e.connectCalendar(t, true)
	e.startRunner(t)

	rec, body := callDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/push-calendar", "")
	if rec.Code != http.StatusOK || body["status"] != "synced" {
		t.Fatalf("push = %d %s", rec.Code, rec.Body)
	}
	run, _ := body["supply_run"].(map[string]any)
	if run["status"] != "generated" || run["error"] != nil {
		t.Errorf("supply_run = %v, want generated with no error", run)
	}
	titleJobID, _ := run["title_job_id"].(string)
	held.waitEntered(t)
	var l supplyRunJSON
	callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", &l)
	if l.Title != nil {
		t.Errorf("title = %q while archive_title is still running, want null", *l.Title)
	}

	close(held.release)
	wantTitleJobSucceeded(t, e, titleJobID)
	if got := e.waitTitled(t); got != "Chili and Friends" {
		t.Errorf("title = %q, want the title job's", got)
	}
}

// TestSupplyRunGenerateDoesNotWaitForTitle: POST generate answers with the
// list untitled while its title is still being written.
func TestSupplyRunGenerateDoesNotWaitForTitle(t *testing.T) {
	t.Parallel()
	held := newHeldTitler()
	e := newPlansRouterTitled(t, held.title)
	e.groundWeek(t)
	e.startRunner(t)

	var l supplyRunJSON
	status, body := callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", &l)
	if status != http.StatusOK {
		t.Fatalf("generate = %d %v", status, body)
	}
	if l.Title != nil {
		t.Errorf("title = %q while archive_title is still running, want null", *l.Title)
	}
	if !reflect.DeepEqual(l.render(), weekLines) {
		t.Errorf("generated = %v, want %v", l.render(), weekLines)
	}
	titleJobID, _ := body["title_job_id"].(string)
	held.waitEntered(t)

	close(held.release)
	wantTitleJobSucceeded(t, e, titleJobID)
	if got := e.waitTitled(t); got != "Chili and Friends" {
		t.Errorf("title = %q, want the title job's", got)
	}
	if _, body := callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", nil); body["title_job_id"] != nil {
		t.Errorf("remake title_job_id = %v, want null: the Supply run has its title", body["title_job_id"])
	}
}

// TestSupplyRunGenerateTitleJobFails: a title job that cannot title the
// Supply run is reported failed with title_failed by GET /jobs/{id}, and
// the week's next generation answers with a new title job.
func TestSupplyRunGenerateTitleJobFails(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)
	e.startRunner(t)

	_, body := callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", nil)
	first := titleJob(t, e, body["title_job_id"])
	job := waitJob(t, e.h, first["id"].(string))
	if job["status"] != "failed" || job["error"] != shopping.ReasonTitleFailed {
		t.Errorf("title job = %v, want failed %s", job, shopping.ReasonTitleFailed)
	}

	_, body = callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", nil)
	if second := titleJob(t, e, body["title_job_id"]); second["id"] == first["id"] {
		t.Errorf("retry title_job_id = %v, want a new job", second["id"])
	}
}

// titleJob returns the title_supply_run job id, as GET /jobs/{id} reports
// it; id must be a job id string.
func titleJob(t *testing.T, e plansEnv, id any) map[string]any {
	t.Helper()
	s, ok := id.(string)
	if !ok || s == "" {
		t.Fatalf("title_job_id = %v, want a job id", id)
	}
	rec, job := callDinners(t, e.h, http.MethodGet, "/api/v1/jobs/"+s, "")
	if rec.Code != http.StatusOK || job["kind"] != shopping.KindTitleSupplyRun {
		t.Fatalf("GET title job = %d %s, want a %s job", rec.Code, rec.Body, shopping.KindTitleSupplyRun)
	}
	return job
}

// wantTitleJobSucceeded waits for title job id to finish and fails t
// unless GET /jobs/{id} reports it succeeded with the title written.
func wantTitleJobSucceeded(t *testing.T, e plansEnv, id string) {
	t.Helper()
	titleJob(t, e, id)
	job := waitJob(t, e.h, id)
	result, _ := job["result"].(map[string]any)
	if job["status"] != "succeeded" || result["title"] != "Chili and Friends" {
		t.Errorf("title job = %v, want succeeded with the title", job)
	}
}

// TestFailedPushMakesNoSupplyRun: a push Calendar refused makes no Supply
// run.
func TestFailedPushMakesNoSupplyRun(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)

	if rec, _ := e.push(t, "2026-10-12"); rec.Code != http.StatusConflict {
		t.Fatalf("push unconnected = %d %s", rec.Code, rec.Body)
	}
	if status, body := callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", nil); status != http.StatusNotFound {
		t.Errorf("GET after a failed push = %d %v, want 404", status, body)
	}
}

// TestPushPlanSupplyRunFails: when the Supply run cannot be made after the
// push, the push stands (200, synced, on Calendar), the answer says the
// Supply run failed, the failure is logged as an error, and no Supply run
// is claimed.
func TestPushPlanSupplyRunFails(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE recipe_ingredients SET name = NULL WHERE name = 'onion'`); err != nil {
		t.Fatal(err)
	}
	e.connectCalendar(t, true)

	rec, body := e.push(t, "2026-10-12")
	if rec.Code != http.StatusOK || body["status"] != "synced" {
		t.Fatalf("push = %d %s, want 200 synced", rec.Code, rec.Body)
	}
	want := map[string]any{"status": "failed", "error": map[string]any{
		"code":      "supply_run_failed",
		"message":   "the plan is on Calendar, but its Supply run could not be made; make it again from the Supply run",
		"retryable": true,
	}, "title_job_id": nil}
	if !reflect.DeepEqual(body["supply_run"], want) {
		t.Errorf("supply_run = %v, want %v", body["supply_run"], want)
	}
	if n := len(e.google.Events(pushCalendar.ID)); n != 7 {
		t.Errorf("calendar has %d events, want 7", n)
	}
	p, err := e.svc.Get(t.Context(), mustLocalDate(t, "2026-10-12"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != plan.StatusSynced {
		t.Errorf("plan = %s after the push, want synced", p.Status)
	}
	if !strings.Contains(e.logs.String(), `"level":"ERROR","msg":"generate Supply run after Push to Calendar"`) ||
		!strings.Contains(e.logs.String(), "has no normalized name") {
		t.Errorf("logs = %s, want the failure logged as an error", e.logs)
	}
	if status, got := callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", nil); status != http.StatusNotFound {
		t.Errorf("GET after the failure = %d %v, want 404", status, got)
	}
}

// TestPushPlanEditedBeforeSupplyRun: a plan edit landing after the push
// is recorded but before the Supply run is made does not make the Supply
// run from the edited plan, which Calendar does not show. The push stands
// (200, on Calendar) and the answer says the Supply run was not made
// because the plan changed.
func TestPushPlanEditedBeforeSupplyRun(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)
	e.connectCalendar(t, true)
	// Once the push records Sunday's event, the household swaps Tuesday to
	// Pad Thai, as a swap does: committed before the Supply run is made.
	if _, err := e.st.Writer.ExecContext(t.Context(), `CREATE TRIGGER edit_after_push
		AFTER UPDATE OF calendar_event_id ON plan_days
		WHEN NEW.date = '2026-10-18' AND NEW.calendar_event_id IS NOT NULL
		BEGIN
			UPDATE plan_days SET dinner_id = '`+e.questLog+`', is_new = 1, calendar_event_id = NULL WHERE date = '2026-10-13';
			UPDATE week_plans SET status = CASE WHEN status = 'synced' THEN 'changed' ELSE status END, revision = revision + 1;
		END`); err != nil {
		t.Fatal(err)
	}

	rec, body := e.push(t, "2026-10-12")
	if rec.Code != http.StatusOK {
		t.Fatalf("push = %d %s, want 200", rec.Code, rec.Body)
	}
	want := map[string]any{"status": "failed", "error": map[string]any{
		"code":      "plan_changed",
		"message":   "the plan is on Calendar as pushed, but it was edited before its Supply run was made, so no Supply run was made from it; push again",
		"retryable": true,
	}, "title_job_id": nil}
	if !reflect.DeepEqual(body["supply_run"], want) {
		t.Errorf("supply_run = %v, want %v", body["supply_run"], want)
	}
	if n := len(e.google.Events(pushCalendar.ID)); n != 7 {
		t.Errorf("calendar has %d events, want 7", n)
	}
	if status, got := callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", nil); status != http.StatusNotFound {
		t.Errorf("GET after the edit = %d %v, want 404: no Supply run from the edited plan", status, got)
	}
	if !strings.Contains(e.logs.String(), `"level":"WARN","msg":"plan edited after Push to Calendar; Supply run not made"`) {
		t.Errorf("logs = %s, want the edit logged as a warning", e.logs)
	}
}

// TestPushPlanLeavesArchivedSupplyRun: pushing a week whose Supply run is
// in the Supply archive leaves it as it was and says so.
func TestPushPlanLeavesArchivedSupplyRun(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)
	callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", nil)
	e.archive(t)
	if _, err := e.svc.Swap(t.Context(), mustLocalDate(t, "2026-10-12"), "2026-10-13", e.questLog); err != nil {
		t.Fatal(err)
	}
	e.connectCalendar(t, true)

	rec, body := e.push(t, "2026-10-12")
	if rec.Code != http.StatusOK || body["status"] != "synced" {
		t.Fatalf("push = %d %s", rec.Code, rec.Body)
	}
	run, _ := body["supply_run"].(map[string]any)
	if apiErr, _ := run["error"].(map[string]any); run["status"] != "archived" || apiErr["code"] != "archived" || run["title_job_id"] != nil {
		t.Errorf("supply_run = %v, want archived", body["supply_run"])
	}
	var l supplyRunJSON
	callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", &l)
	if !reflect.DeepEqual(l.render(), weekLines) {
		t.Errorf("archived Supply run after push = %v, want unchanged", l.render())
	}
}

// archiveJSON is a GET /shopping/archive answer decoded with every field
// the API promises.
type archiveJSON struct {
	By      string `json:"by"`
	Periods []struct {
		Key     string `json:"key"`
		Heading string `json:"heading"`
		Posts   []struct {
			WeekStart   string   `json:"weekStart"`
			Label       string   `json:"label"`
			Title       *string  `json:"title"`
			DinnerCount int      `json:"dinnerCount"`
			ItemCount   int      `json:"itemCount"`
			OnHandCount int      `json:"onHandCount"`
			Dinners     []string `json:"dinners"`
			Current     bool     `json:"current"`
			Frozen      bool     `json:"frozen"`
		} `json:"posts"`
	} `json:"periods"`
}

// weekSupplyRun plans a new dinner named name, grounded in a recipe
// listing ings, on the Monday of the week starting monday, and generates
// the week's Supply run.
func (e plansEnv) weekSupplyRun(t *testing.T, monday store.LocalDate, name string, ings ...shopIng) supplyRunJSON {
	t.Helper()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	d, err := q.CreateDinner(t.Context(), db.CreateDinnerParams{
		ID: store.NewID(), Name: name, NormalizedName: harness.Normalize(name), Status: "regular", Origin: "regular",
		GroundingStatus: "manual", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	planID := store.NewID()
	if err := q.CreateWeekPlan(t.Context(), db.CreateWeekPlanParams{ID: planID, WeekStart: monday.String(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := q.PutPlanDay(t.Context(), db.PutPlanDayParams{ID: store.NewID(), PlanID: planID, Date: monday.String(),
		DinnerID: sql.NullString{String: d.ID, Valid: true}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	e.groundDay(t, monday.String(), ings...)
	var l supplyRunJSON
	if status, body := callShopping(t, e, http.MethodPost, shoppingPath+monday.String()+"/generate", "", &l); status != http.StatusOK {
		t.Fatalf("generate %s = %d %v", monday, status, body)
	}
	return l
}

// TestSupplyArchive freezes last week's Supply run and reads the Supply
// archive: posts newest first with exactly the promised fields, this
// week's marked current. The frozen list is served read-only.
func TestSupplyArchive(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	// The fixture's plan is for a fixed week; this test plans weeks
	// around today instead.
	if _, err := e.st.Writer.ExecContext(t.Context(), `DELETE FROM plan_days; DELETE FROM week_plans`); err != nil {
		t.Fatal(err)
	}
	zone, err := household.LoadZone("America/New_York") // the env's household zone
	if err != nil {
		t.Fatal(err)
	}
	thisWeek := household.WeekStart(household.Today(time.Now(), zone))
	y, m, d := thisWeek.Date()
	lastWeek := store.LocalDateOf(time.Date(y, m, d-7, 0, 0, 0, 0, time.UTC))

	last := e.weekSupplyRun(t, lastWeek, "Beef Stew", shopIng{"stewing beef", "2", "lb"}, shopIng{"carrots", "4", ""}, shopIng{"onion", "1", ""})
	onion := last.itemID(t, "onion")
	if status, body := callShopping(t, e, http.MethodPatch, shoppingPath+"items/"+onion, `{"needed":false}`, nil); status != http.StatusOK {
		t.Fatalf("PATCH = %d %v", status, body)
	}
	e.weekSupplyRun(t, thisWeek, "Lemon Chicken", shopIng{"chicken thighs", "6", ""}, shopIng{"lemon", "1", ""})
	if err := e.shop.Freeze(t.Context(), lastWeek); err != nil {
		t.Fatal(err)
	}
	if err := e.shop.Freeze(t.Context(), thisWeek); !errors.Is(err, shopping.ErrWeekNotEnded) {
		t.Fatalf("Freeze this week = %v, want ErrWeekNotEnded", err)
	}

	var got archiveJSON
	status, raw := callShopping(t, e, http.MethodGet, shoppingPath+"archive?by=week", "", &got)
	if status != http.StatusOK {
		t.Fatalf("GET archive = %d %v", status, raw)
	}
	if k := keys(raw); !reflect.DeepEqual(k, []string{"by", "periods"}) {
		t.Errorf("archive fields = %v", k)
	}
	period := raw["periods"].([]any)[0].(map[string]any)
	if k := keys(period); !reflect.DeepEqual(k, []string{"heading", "key", "posts"}) {
		t.Errorf("period fields = %v", k)
	}
	wantPost := []string{"current", "dinnerCount", "dinners", "frozen", "itemCount", "label", "onHandCount", "title", "weekStart"}
	if k := keys(period["posts"].([]any)[0].(map[string]any)); !reflect.DeepEqual(k, wantPost) {
		t.Errorf("post fields = %v, want %v", k, wantPost)
	}
	var posts []string
	for _, p := range got.Periods {
		if len(p.Posts) != 1 || p.Key != household.PeriodKey(mustLocalDate(t, p.Posts[0].WeekStart)) || p.Heading != p.Posts[0].Label {
			t.Errorf("period %+v, want one post under its week", p)
		}
		for _, post := range p.Posts {
			label, err := household.WeekLabel(mustLocalDate(t, post.WeekStart))
			if err != nil {
				t.Fatal(err)
			}
			if post.Label != label || post.Title != nil {
				t.Errorf("post %s: label %q title %v, want %q and null", post.WeekStart, post.Label, post.Title, label)
			}
			posts = append(posts, fmt.Sprintf("%s %d dinners %d items %d on hand %v current=%t frozen=%t",
				post.WeekStart, post.DinnerCount, post.ItemCount, post.OnHandCount, post.Dinners, post.Current, post.Frozen))
		}
	}
	want := []string{
		thisWeek.String() + " 1 dinners 2 items 0 on hand [Lemon Chicken] current=true frozen=false",
		lastWeek.String() + " 1 dinners 3 items 1 on hand [Beef Stew] current=false frozen=true",
	}
	if got.By != "week" || !reflect.DeepEqual(posts, want) {
		t.Errorf("archive by %s =\n  %s\nwant\n  %s", got.By, strings.Join(posts, "\n  "), strings.Join(want, "\n  "))
	}

	for _, q := range []string{"", "?by=", "?by=day", "?by=MONTH"} {
		status, body := callShopping(t, e, http.MethodGet, shoppingPath+"archive"+q, "", nil)
		if status != http.StatusBadRequest || errorCode(body) != "invalid_request" {
			t.Errorf("GET archive%s = %d %v, want 400 invalid_request", q, status, body)
		}
	}

	status, body := callShopping(t, e, http.MethodPatch, shoppingPath+"items/"+last.itemID(t, "carrots"), `{"needed":false}`, nil)
	if status != http.StatusConflict || errorCode(body) != "archived" {
		t.Errorf("PATCH after Freeze = %d %v, want 409 archived", status, body)
	}
	var frozen supplyRunJSON
	if status, body := callShopping(t, e, http.MethodGet, shoppingPath+lastWeek.String(), "", &frozen); status != http.StatusOK {
		t.Fatalf("GET frozen list = %d %v", status, body)
	}
	wantLines := []string{
		"Monday " + lastWeek.String() + " Beef Stew: stewing beef [2 lb] needed",
		"Monday " + lastWeek.String() + " Beef Stew: carrots [4] needed",
		"Monday " + lastWeek.String() + " Beef Stew: onion [1] have it",
	}
	if frozen.Status != "archived" || frozen.NeededCount != 2 || frozen.TotalCount != 3 || !reflect.DeepEqual(frozen.render(), wantLines) {
		t.Errorf("frozen list = %s %d/%d %v, want archived 2/3 %v", frozen.Status, frozen.NeededCount, frozen.TotalCount, frozen.render(), wantLines)
	}
}

// TestSupplyArchiveUnfrozenPastWeek: a past week's Supply run that
// week_rollover has not frozen is in the Supply archive as not frozen for
// as long as the Supply run route can still change it, and as frozen once
// it cannot.
func TestSupplyArchiveUnfrozenPastWeek(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	if _, err := e.st.Writer.ExecContext(t.Context(), `DELETE FROM plan_days; DELETE FROM week_plans`); err != nil {
		t.Fatal(err)
	}
	zone, err := household.LoadZone("America/New_York") // the env's household zone
	if err != nil {
		t.Fatal(err)
	}
	thisWeek := household.WeekStart(household.Today(time.Now(), zone))
	y, m, d := thisWeek.Date()
	lastWeek := store.LocalDateOf(time.Date(y, m, d-7, 0, 0, 0, 0, time.UTC))
	last := e.weekSupplyRun(t, lastWeek, "Beef Stew", shopIng{"stewing beef", "2", "lb"}, shopIng{"carrots", "4", ""})

	// post is last week's post in the Supply archive.
	post := func() (frozen bool) {
		t.Helper()
		var got archiveJSON
		if status, raw := callShopping(t, e, http.MethodGet, shoppingPath+"archive?by=week", "", &got); status != http.StatusOK {
			t.Fatalf("GET archive = %d %v", status, raw)
		}
		for _, p := range got.Periods {
			for _, post := range p.Posts {
				if post.WeekStart == lastWeek.String() {
					if post.Current {
						t.Errorf("last week's post is current")
					}
					return post.Frozen
				}
			}
		}
		t.Fatalf("archive %+v has no post for %s", got, lastWeek)
		return false
	}
	// change marks carrots and reports whether the Supply run route took it.
	change := func(needed bool) bool {
		t.Helper()
		status, body := callShopping(t, e, http.MethodPatch, shoppingPath+"items/"+last.itemID(t, "carrots"), fmt.Sprintf(`{"needed":%t}`, needed), nil)
		switch {
		case status == http.StatusOK:
			return true
		case status == http.StatusConflict && errorCode(body) == "archived":
			return false
		}
		t.Fatalf("PATCH = %d %v, want 200 or 409 archived", status, body)
		return false
	}

	if frozen, changed := post(), change(false); frozen || !changed {
		t.Errorf("unfrozen past week: post frozen=%t, PATCH changed=%t; want a post not frozen for a list that changes", frozen, changed)
	}
	if err := e.shop.Freeze(t.Context(), lastWeek); err != nil {
		t.Fatal(err)
	}
	if frozen, changed := post(), change(true); !frozen || changed {
		t.Errorf("frozen past week: post frozen=%t, PATCH changed=%t; want a frozen post for a list that cannot change", frozen, changed)
	}
}

// TestSupplyArchiveKeepsFrozenNames freezes last week's Supply run, then
// renames its dinner and this week's through the dinners API: the frozen
// post and list keep the name the dinner had when frozen, while this
// week's list, not frozen, shows the new name.
func TestSupplyArchiveKeepsFrozenNames(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	if _, err := e.st.Writer.ExecContext(t.Context(), `DELETE FROM plan_days; DELETE FROM week_plans`); err != nil {
		t.Fatal(err)
	}
	zone, err := household.LoadZone("America/New_York") // the env's household zone
	if err != nil {
		t.Fatal(err)
	}
	thisWeek := household.WeekStart(household.Today(time.Now(), zone))
	y, m, d := thisWeek.Date()
	lastWeek := store.LocalDateOf(time.Date(y, m, d-7, 0, 0, 0, 0, time.UTC))

	last := e.weekSupplyRun(t, lastWeek, "Beef Stew", shopIng{"stewing beef", "2", "lb"})
	current := e.weekSupplyRun(t, thisWeek, "Lemon Chicken", shopIng{"lemon", "1", ""})
	if err := e.shop.Freeze(t.Context(), lastWeek); err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{
		last.Groups[0].Dinner.ID:    "Sunday Beef Stew",
		current.Groups[0].Dinner.ID: "Lemon Garlic Chicken",
	} {
		if status, body := callShopping(t, e, http.MethodPatch, dinnersPath+"/"+id, `{"name":"`+name+`"}`, nil); status != http.StatusOK {
			t.Fatalf("PATCH dinner %s = %d %v", id, status, body)
		}
	}

	var got archiveJSON
	if status, raw := callShopping(t, e, http.MethodGet, shoppingPath+"archive?by=week", "", &got); status != http.StatusOK {
		t.Fatalf("GET archive = %d %v", status, raw)
	}
	var posts []string
	for _, p := range got.Periods {
		for _, post := range p.Posts {
			posts = append(posts, fmt.Sprintf("%s %v current=%t", post.WeekStart, post.Dinners, post.Current))
		}
	}
	want := []string{
		thisWeek.String() + " [Lemon Garlic Chicken] current=true",
		lastWeek.String() + " [Beef Stew] current=false",
	}
	if !reflect.DeepEqual(posts, want) {
		t.Errorf("archive after rename =\n  %s\nwant\n  %s", strings.Join(posts, "\n  "), strings.Join(want, "\n  "))
	}

	for _, tc := range []struct {
		week store.LocalDate
		want string
	}{
		{lastWeek, "Monday " + lastWeek.String() + " Beef Stew: stewing beef [2 lb] needed"},
		{thisWeek, "Monday " + thisWeek.String() + " Lemon Garlic Chicken: lemon [1] needed"},
	} {
		var l supplyRunJSON
		if status, body := callShopping(t, e, http.MethodGet, shoppingPath+tc.week.String(), "", &l); status != http.StatusOK {
			t.Fatalf("GET %s = %d %v", tc.week, status, body)
		}
		if got := l.render(); !reflect.DeepEqual(got, []string{tc.want}) {
			t.Errorf("list %s after rename = %v, want [%s]", tc.week, got, tc.want)
		}
	}
}

// TestSupplyRunSendChat posts the week's Supply run to Google Chat once,
// records sent_to_chat_at, answers a second send with the status as it
// is, and posts again only with resend.
func TestSupplyRunSendChat(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)
	var l supplyRunJSON
	callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", &l)
	callShopping(t, e, http.MethodPatch, shoppingPath+"items/"+l.itemID(t, "onion"), `{"needed":false}`, nil)
	send := shoppingPath + "2026-10-12/send-chat"

	if status, body := callShopping(t, e, http.MethodPost, send, `{}`, nil); status != http.StatusConflict || errorCode(body) != "not_configured" {
		t.Fatalf("send with no webhook = %d %v, want 409 not_configured", status, body)
	}
	if _, err := e.chatSvc.Put(t.Context(), chattest.Webhook, "Family"); err != nil {
		t.Fatal(err)
	}

	var sent supplyRunJSON
	if status, body := callShopping(t, e, http.MethodPost, send, `{}`, &sent); status != http.StatusOK {
		t.Fatalf("send = %d %v", status, body)
	}
	if sent.SentToChatAt == nil || sent.WeekStart != "2026-10-12" || !reflect.DeepEqual(sent.render(), []string{
		"Monday 2026-10-12 Beef Chili: ground beef [1 lb] needed",
		"Monday 2026-10-12 Beef Chili: kidney beans [2 cans] needed",
		"Monday 2026-10-12 Beef Chili: onion [1] have it",
		"Tuesday 2026-10-13 Regular 2026-10-13: spaghetti [500 g] needed",
		"Tuesday 2026-10-13 Regular 2026-10-13: basil [null] needed",
	}) {
		t.Errorf("send answer = %+v, want the Supply run with sent_to_chat_at", sent)
	}
	posts := e.chat.Posts()
	if len(posts) != 1 {
		t.Fatalf("Google Chat got %d posts, want 1", len(posts))
	}
	want := "Supply run · Week 42 · Oct 12 – 18\n\n*Beef Chili*\n• ground beef — 1 lb\n• kidney beans — 2 cans\n\n" +
		"*Regular 2026-10-13*\n• spaghetti — 500 g\n• basil\n\nOpen the Supply run →"
	if posts[0].Text() != want {
		t.Errorf("posted text = %q, want %q", posts[0].Text(), want)
	}

	var again supplyRunJSON
	if status, body := callShopping(t, e, http.MethodPost, send, `{}`, &again); status != http.StatusOK || again.SentToChatAt == nil || *again.SentToChatAt != *sent.SentToChatAt {
		t.Errorf("send again = %d %v, sent_to_chat_at %v, want 200 and %s", status, body, again.SentToChatAt, *sent.SentToChatAt)
	}
	if n := len(e.chat.Posts()); n != 1 {
		t.Errorf("send again without resend posted: %d posts, want 1", n)
	}
	var got supplyRunJSON
	callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", &got)
	if got.SentToChatAt == nil || *got.SentToChatAt != *sent.SentToChatAt {
		t.Errorf("GET sent_to_chat_at = %v, want %s", got.SentToChatAt, *sent.SentToChatAt)
	}

	var resent supplyRunJSON
	if status, body := callShopping(t, e, http.MethodPost, send, `{"resend":true}`, &resent); status != http.StatusOK || resent.SentToChatAt == nil {
		t.Fatalf("resend = %d %v", status, body)
	}
	if n := len(e.chat.Posts()); n != 2 {
		t.Errorf("resend: %d posts, want 2", n)
	}
}

func TestSupplyRunSendChatErrors(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.groundWeek(t)
	callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/generate", "", nil)
	if _, err := e.chatSvc.Put(t.Context(), chattest.Webhook, "Family"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, body string
		status           int
		code             string
	}{
		{"no body", "2026-10-12/send-chat", "", http.StatusBadRequest, "invalid_request"},
		{"resend not a boolean", "2026-10-12/send-chat", `{"resend":"yes"}`, http.StatusBadRequest, "invalid_request"},
		{"unknown field", "2026-10-12/send-chat", `{"force":true}`, http.StatusBadRequest, "invalid_request"},
		{"not a Monday", "2026-10-13/send-chat", `{}`, http.StatusBadRequest, "invalid_week"},
		{"no list", "2026-10-19/send-chat", `{}`, http.StatusNotFound, "not_found"},
	} {
		status, body := callShopping(t, e, http.MethodPost, shoppingPath+tc.path, tc.body, nil)
		if status != tc.status || errorCode(body) != tc.code {
			t.Errorf("%s = %d %v, want %d %s", tc.name, status, body, tc.status, tc.code)
		}
	}

	e.chat.SetStatus(http.StatusInternalServerError)
	status, body := callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/send-chat", `{}`, nil)
	if status != http.StatusBadGateway || errorCode(body) != "chat_refused" || body["error"].(map[string]any)["retryable"] != true {
		t.Errorf("send Google Chat refuses = %d %v, want 502 chat_refused, retryable", status, body)
	}
	e.chat.SetStatus(http.StatusNotFound)
	status, body = callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/send-chat", `{}`, nil)
	if status != http.StatusBadGateway || errorCode(body) != "chat_refused" || body["error"].(map[string]any)["retryable"] != false {
		t.Errorf("send Google Chat answers 404 = %d %v, want 502 chat_refused, not retryable", status, body)
	}
	var l supplyRunJSON
	callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", &l)
	if l.SentToChatAt != nil || l.ChatSendUnconfirmedAt != nil {
		t.Errorf("after refused sends sent_to_chat_at = %v, chat_send_unconfirmed_at = %v, want both null", l.SentToChatAt, l.ChatSendUnconfirmedAt)
	}
	e.chat.Close()
	status, body = callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/send-chat", `{}`, nil)
	if status != http.StatusBadGateway || errorCode(body) != "chat_unconfirmed" || body["error"].(map[string]any)["retryable"] != false {
		t.Errorf("send Google Chat does not answer = %d %v, want 502 chat_unconfirmed, not retryable", status, body)
	}
	callShopping(t, e, http.MethodGet, shoppingPath+"2026-10-12", "", &l)
	if l.SentToChatAt != nil || l.ChatSendUnconfirmedAt == nil {
		t.Errorf("after an unanswered send sent_to_chat_at = %v, chat_send_unconfirmed_at = %v, want null and set", l.SentToChatAt, l.ChatSendUnconfirmedAt)
	}

	e.archive(t)
	status, body = callShopping(t, e, http.MethodPost, shoppingPath+"2026-10-12/send-chat", `{"resend":true}`, nil)
	if status != http.StatusConflict || errorCode(body) != "archived" {
		t.Errorf("send an archived list = %d %v, want 409 archived", status, body)
	}
}
