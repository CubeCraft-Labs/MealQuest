package ideastest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server/srvtest"
)

const quickPath = "/api/v1/ideas/quick"

// quickRecipe makes the search for name find a page titled title with a
// total time of minutes, and scripts the normalize_recipe reply grounding
// it takes. "Slow Cooker" in title grounds the method as crockpot.
func quickRecipe(t *testing.T, normalize *llm.Fake, search *recipestest.Search, pages *recipestest.Pages, images *recipestest.Images, name, title string, minutes int) {
	t.Helper()
	slug := strings.ReplaceAll(strings.ToLower(name), " ", "-")
	page, photo := "https://r.example/"+slug, "https://r.example/"+slug+".jpg"
	search.Results[name] = []recipes.Candidate{{URL: page}}
	pages.Bodies[page] = recipestest.RecipePage(title, "R Kitchen", photo, minutes)
	images.JPEGs[photo] = recipestest.JPEG(t, 400, 320, uint8(len(images.JPEGs)+1))
	normalize.QueueGenerate(recipestest.NormalizedReply("stovetop", minutes))
}

// quickReply scripts one quick_ideas reply holding the ideas named, each
// searched by its name.
func quickReply(model *llm.Fake, ideas ...[3]any) {
	list := make([]string, len(ideas))
	for i, idea := range ideas {
		list[i] = fmt.Sprintf(`{"name":%q,"est_minutes":%d,"method":%q,"difficulty":"medium","search_query":%q}`, idea[0], idea[2], idea[1], idea[0])
	}
	js := `{"ideas":[` + strings.Join(list, ",") + `]}`
	model.QueueGenerate(llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}})
}

func newQuickFakes() (*llm.Fake, *llm.Fake, *recipestest.Search, *recipestest.Pages, *recipestest.Images) {
	return &llm.Fake{}, &llm.Fake{}, &recipestest.Search{Results: map[string][]recipes.Candidate{}},
		&recipestest.Pages{Bodies: map[string][]byte{}}, &recipestest.Images{JPEGs: map[string][]byte{}}
}

func TestPostIdeasQuickRefusesBadRequests(t *testing.T) {
	t.Parallel()
	h, _, _ := newIdeasRouter(t, &llm.Fake{}, &llm.Fake{}, &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, false)

	for _, tc := range []struct{ body, message string }{
		{`{"exclude": []}`, "kind: required"},
		{`{"kind": "crockpot"}`, "exclude: required"},
		{`{"kind": "", "exclude": []}`, `kind: kind "" is not one of crockpot, under30`},
		{`{"kind": "quick", "exclude": []}`, `kind: kind "quick" is not one of crockpot, under30`},
		{`{"kind": "Crockpot", "exclude": []}`, `kind: kind "Crockpot" is not one of crockpot, under30`},
		{`{"kind": "under30", "exclude": ["Beef Stew", " "]}`, "exclude: excluded name 2 is blank"},
		{`{"kind": "under30", "exclude": ["Beef Stew", "beef-stew"]}`, `exclude: excluded name 2, "beef-stew", repeats "Beef Stew"`},
		{`{"kind": 30, "exclude": []}`, "kind: want a JSON string"},
		{`{"kind": "under30", "exclude": [], "extra": 1}`, `invalid request body: json: unknown field "extra"`},
	} {
		rec, body := srvtest.CallDinners(t, h, http.MethodPost, quickPath, tc.body)
		if rec.Code != http.StatusBadRequest || srvtest.ErrorCode(body) != "invalid_request" {
			t.Errorf("POST %s = %d %v, want 400 invalid_request", tc.body, rec.Code, body)
			continue
		}
		if msg := body["error"].(map[string]any)["message"]; msg != tc.message {
			t.Errorf("POST %s message = %q, want %q", tc.body, msg, tc.message)
		}
	}
}

// The job answers 202 at once and its result fills idea by idea: one
// idea is shown while another still grounds. The kind is checked against
// the grounded recipe and excluded names never come back.
func TestPostIdeasQuickRunsTheJobWithProgressiveResults(t *testing.T) {
	t.Parallel()
	model, normalize, search, pages, images := newQuickFakes()
	quickRecipe(t, normalize, search, pages, images, "Chicken Fried Rice", "Chicken Fried Rice", 25)
	quickRecipe(t, normalize, search, pages, images, "Beef Stroganoff", "Beef Stroganoff", 45)
	quickRecipe(t, normalize, search, pages, images, "Garlic Shrimp Pasta", "Garlic Shrimp Pasta", 30)
	gate := make(chan struct{})
	search.Gates = map[string]chan struct{}{"Garlic Shrimp Pasta": gate}
	quickReply(model,
		[3]any{"Easy Shrimp Tacos", "stovetop", 15},
		[3]any{"Chicken Fried Rice", "stovetop", 20},
		[3]any{"Beef Stroganoff", "stovetop", 25},
		[3]any{"Garlic Shrimp Pasta", "stovetop", 20},
	)
	h, _, _ := newIdeasRouter(t, model, normalize, search, pages, images, true)

	rec, body := srvtest.CallDinners(t, h, http.MethodPost, quickPath, `{"kind": "under30", "exclude": ["Shrimp Tacos"]}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	id, _ := body["jobId"].(string)
	if id == "" || len(body) != 1 {
		t.Fatalf("POST answered %v, want only a jobId", body)
	}

	// While Garlic Shrimp Pasta still grounds, Chicken Fried Rice shows.
	deadline := time.Now().Add(jobWait)
	for {
		_, job := srvtest.CallDinners(t, h, http.MethodGet, "/api/v1/jobs/"+id, "")
		if res, _ := job["result"].(map[string]any); res != nil {
			if list, _ := res["ideas"].([]any); len(list) == 1 {
				if job["status"] != "running" || res["stage"] != "grounding" || list[0].(map[string]any)["name"] != "Chicken Fried Rice" {
					t.Fatalf("partial job = %v", job)
				}
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("no idea was shown while another still grounded")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)

	job := srvtest.WaitJob(t, h, id)
	if job["status"] != "succeeded" || job["kind"] != "quick_ideas" {
		t.Fatalf("GET job = %v", job)
	}
	res := job["result"].(map[string]any)
	if res["stage"] != "done" || res["kind"] != "under30" || res["dropped"] != 2.0 {
		t.Fatalf("result = %v, want done with the excluded and the 45-minute ideas dropped", res)
	}
	if ex, _ := json.Marshal(res["exclude"]); string(ex) != `["Shrimp Tacos"]` {
		t.Errorf("result exclude = %s", ex)
	}
	var names []string
	for _, i := range res["ideas"].([]any) {
		names = append(names, i.(map[string]any)["name"].(string))
	}
	if want := []string{"Chicken Fried Rice", "Garlic Shrimp Pasta"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("ideas = %q, want %q", names, want)
	}
	got := res["ideas"].([]any)[1].(map[string]any)
	for key, want := range map[string]any{
		"method":        "stovetop",
		"minutes_total": 30.0,
		"site_name":     "R Kitchen",
		"source_url":    "https://r.example/garlic-shrimp-pasta",
	} {
		if got[key] != want {
			t.Errorf("idea %s = %v, want %v", key, got[key], want)
		}
	}
	if got["recipe_id"] == "" || got["photo_url"] == "" || got["difficulty"] == nil {
		t.Errorf("idea = %v, want a recipe, photo and difficulty", got)
	}
}

func TestFailedQuickJobShowsNoIdeas(t *testing.T) {
	t.Parallel()
	model, normalize, search, pages, images := newQuickFakes()
	quickRecipe(t, normalize, search, pages, images, "Chicken Chili", "Slow Cooker Chicken Chili", 240)
	search.Fail = map[string]error{"Lentil Soup": errors.New("boom")}
	ideas := [][3]any{{"Chicken Chili", "crockpot", 240}}
	for i := range 5 {
		ideas = append(ideas, [3]any{fmt.Sprintf("Mystery Stew %d", i+1), "crockpot", 300})
	}
	quickReply(model, append(ideas, [3]any{"Lentil Soup", "crockpot", 300})...)
	h, _, _ := newIdeasRouter(t, model, normalize, search, pages, images, true)
	rec, body := srvtest.CallDinners(t, h, http.MethodPost, quickPath, `{"kind": "crockpot", "exclude": []}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	assertFailedWithoutIdeas(t, srvtest.WaitJob(t, h, body["jobId"].(string)), "search_failed")
}

func TestJobsEndpointRefusesQuickKind(t *testing.T) {
	t.Parallel()
	h, _, _ := newIdeasRouter(t, &llm.Fake{}, &llm.Fake{}, &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, false)
	rec, body := srvtest.CallDinners(t, h, http.MethodPost, "/api/v1/jobs", `{"kind": "quick_ideas", "payload": {"kind": "crockpot", "exclude": []}}`)
	if rec.Code != http.StatusBadRequest || srvtest.ErrorCode(body) != "service_only_kind" {
		t.Fatalf("POST /jobs = %d %v, want 400 service_only_kind", rec.Code, body)
	}
}
