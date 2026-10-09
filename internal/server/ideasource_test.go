package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
)

// sourceCase is a recipe page an idea grounds in: its link and the site
// name its page gives (empty for none, so the host is stored instead).
type sourceCase struct {
	dish, link, site string
	// want is the idea's source_url and site_name; nil is null.
	wantURL, wantSite any
}

const (
	cleanSourceLink = "https://kitchen.example/recipes/chicken-curry"
	queryToken      = "query-token-abc123"
	queryHost       = "query-host-q1.example"
	userinfoToken   = "user-token-def456"
	userinfoHost    = "userinfo-host-u2.example"
)

// sourceCases are a clean link, credited by its page's name, and links
// carrying a token in the query and the userinfo whose pages name no site,
// so the stored name is the link's host.
var sourceCases = []sourceCase{
	{"Chicken Curry", cleanSourceLink, "Curry House", cleanSourceLink, "Curry House"},
	{"Beef Stew", "https://" + queryHost + "/stew?access_token=" + queryToken, "", nil, nil},
	{"Bean Chili", "https://" + userinfoToken + "@" + userinfoHost + "/chili", "", nil, nil},
}

// sourceSecrets are what no idea job response may contain.
var sourceSecrets = []string{queryToken, "access_token", queryHost, userinfoToken, userinfoHost}

// sourceFakes makes the search for each case's dish find its page and
// scripts the normalize_recipe reply grounding it takes.
func sourceFakes(t *testing.T, minutes int) (*llm.Fake, *llm.Fake, *recipestest.Search, *recipestest.Pages, *recipestest.Images) {
	t.Helper()
	model, normalize, search, pages, images := newQuickFakes()
	for i, c := range sourceCases {
		photo := fmt.Sprintf("https://img.example/%d.jpg", i)
		search.Results[c.dish] = []recipes.Candidate{{URL: c.link}}
		pages.Bodies[c.link] = recipestest.RecipePage(c.dish, c.site, photo, minutes)
		images.JPEGs[photo] = recipestest.JPEG(t, 400, 320, uint8(i+1))
		normalize.QueueGenerate(recipestest.NormalizedReply("stovetop", minutes))
	}
	return model, normalize, search, pages, images
}

// checkIdeaSources checks the finished job id: each idea shows its case's
// safe source, and the response holds nothing of a token-bearing link.
func checkIdeaSources(t *testing.T, h http.Handler, id string) {
	t.Helper()
	job := waitJob(t, h, id)
	if job["status"] != "succeeded" {
		t.Fatalf("GET job = %v", job)
	}
	rec, _ := callDinners(t, h, http.MethodGet, "/api/v1/jobs/"+id, "")
	for _, secret := range sourceSecrets {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("job response contains %q: %s", secret, rec.Body)
		}
	}
	list := job["result"].(map[string]any)["ideas"].([]any)
	if len(list) != len(sourceCases) {
		t.Fatalf("ideas = %v, want %d", list, len(sourceCases))
	}
	for i, c := range sourceCases {
		idea := list[i].(map[string]any)
		if idea["name"] != c.dish {
			t.Fatalf("idea %d = %v, want %s", i, idea, c.dish)
		}
		for key, want := range map[string]any{"source_url": c.wantURL, "site_name": c.wantSite} {
			got, ok := idea[key]
			if !ok || !reflect.DeepEqual(got, want) {
				t.Errorf("%s %s = %v (present %t), want %v", c.dish, key, got, ok, want)
			}
		}
		if idea["recipe_id"] == "" || idea["photo_url"] == "" {
			t.Errorf("idea %s = %v, want a recipe and photo", c.dish, idea)
		}
	}
}

// An ideas_from_ingredients job shows a clean link and its page's name,
// and neither a token-bearing link nor a name taken from its host.
func TestIdeasJobShowsOnlySafeSources(t *testing.T) {
	t.Parallel()
	model, normalize, search, pages, images := sourceFakes(t, 25)
	list := make([]string, len(sourceCases))
	for i, c := range sourceCases {
		list[i] = fmt.Sprintf(`{"name":%q,"uses":["rice"],"missing":[],"est_minutes":25,"method":"stovetop","difficulty":"easy","search_query":%q}`, c.dish, c.dish)
	}
	js := `{"ideas":[` + strings.Join(list, ",") + `]}`
	model.QueueGenerate(llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}})
	h, _, _ := newIdeasRouter(t, model, normalize, search, pages, images, true)
	checkIdeaSources(t, h, startIdeasJob(t, h))
}

// A quick_ideas job shows sources as an ideas_from_ingredients job does.
func TestQuickJobShowsOnlySafeSources(t *testing.T) {
	t.Parallel()
	model, normalize, search, pages, images := sourceFakes(t, 25)
	ideas := make([][3]any, len(sourceCases))
	for i, c := range sourceCases {
		ideas[i] = [3]any{c.dish, "stovetop", 20}
	}
	quickReply(model, ideas...)
	h, _, _ := newIdeasRouter(t, model, normalize, search, pages, images, true)
	rec, body := callDinners(t, h, http.MethodPost, quickPath, `{"kind": "under30", "exclude": []}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
	}
	checkIdeaSources(t, h, body["jobId"].(string))
}
