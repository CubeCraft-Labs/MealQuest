package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ideas"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
)

// sourceCase is a recipe page an idea grounds in: its link and the site
// name its page gives (empty for none, so no name is stored).
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
// so no name is stored and, the links being unsafe, none is shown.
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

// legacyIdea is an idea as the result builders stored it before sources
// were made safe: the grounded recipe's raw link and stored site name,
// which is the link's host when its page named no site.
func legacyIdea(name, link, site string) map[string]any {
	return map[string]any{
		"name": name, "method": "stovetop", "minutes_total": 25, "difficulty": "easy",
		"recipe_id": "01JLEGACY" + strings.ToUpper(strings.ReplaceAll(name, " ", "")), "photo_url": "/api/v1/photos/legacy",
		"source_url": link, "site_name": site,
	}
}

// A job of either idea kind whose result was stored before sources were
// made safe, and upgraded by migration 0020, is served with the sources a
// job stored now holds: neither a token-bearing link nor a name taken from
// its host.
func TestJobServesLegacyIdeaResultsWithOnlySafeSources(t *testing.T) {
	t.Parallel()
	model, normalize, search, pages, images := newQuickFakes()
	h, _, st := newIdeasRouter(t, model, normalize, search, pages, images, false)
	legacy := []any{
		legacyIdea("Chicken Curry", cleanSourceLink, "Curry House"),
		legacyIdea("Beef Stew", "https://"+queryHost+"/stew?access_token="+queryToken, queryHost),
		legacyIdea("Bean Chili", "https://"+userinfoToken+"@"+userinfoHost+"/chili", userinfoHost),
	}
	for kind, result := range map[string]map[string]any{
		ideas.KindFromIngredients: {"stage": "done", "ingredients": []string{"rice"}, "quick_only": false, "ideas": legacy, "dropped": 0},
		ideas.KindQuick:           {"stage": "done", "kind": "under30", "exclude": []string{}, "ideas": legacy, "dropped": 0},
	} {
		js, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		id := "legacy-" + kind
		if _, err := st.Writer.ExecContext(t.Context(),
			`INSERT INTO jobs (id, kind, status, payload, result, created_at, updated_at) VALUES (?, ?, 'succeeded', '{}', ?, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`,
			id, kind, string(js)); err != nil {
			t.Fatal(err)
		}
	}
	applySiteNameMigration(t, st)
	for kind := range map[string]bool{ideas.KindFromIngredients: true, ideas.KindQuick: true} {
		checkIdeaSources(t, h, "legacy-"+kind)
	}
}

// An idea job whose grounded recipe's stored link does not parse fails,
// even when no site name is stored, rather than show the idea without a
// source.
func TestIdeaJobFailsOnUnparseableSourceWithoutSiteName(t *testing.T) {
	t.Parallel()
	const badLink = "https://kitchen.example/%zz"
	model, normalize, search, pages, images := sourceFakes(t, 25)
	idea := fmt.Sprintf(`{"name":%q,"uses":["rice"],"missing":[],"est_minutes":25,"method":"stovetop","difficulty":"easy","search_query":%q}`, sourceCases[0].dish, sourceCases[0].dish)
	js := `{"ideas":[` + idea + `]}`
	for range 2 {
		model.QueueGenerate(llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}})
	}
	h, _, st := newIdeasRouter(t, model, normalize, search, pages, images, true)
	if job := waitJob(t, h, startIdeasJob(t, h)); job["status"] != "succeeded" {
		t.Fatalf("grounding job = %v", job)
	}
	// The recipe is now stored under badLink with no site name, and the
	// search finds it there.
	res, err := st.Writer.ExecContext(t.Context(), `UPDATE recipes SET source_url = ?, site_name = NULL WHERE source_url = ?`, badLink, cleanSourceLink)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("updated %d recipes (%v), want 1", n, err)
	}
	search.Results[sourceCases[0].dish] = []recipes.Candidate{{URL: badLink}}
	job := waitJob(t, h, startIdeasJob(t, h))
	assertFailedWithoutIdeas(t, job, jobs.CodeInternal)
}
