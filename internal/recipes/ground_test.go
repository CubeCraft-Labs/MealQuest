package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// fakeSearch is a SearchSource and SearchProvider answering each query
// from results, and failing a query in errs. With gate set, every Search
// reports its query on started and waits for gate to close.
type fakeSearch struct {
	results map[string][]Candidate
	errs    map[string]error
	started chan string
	gate    chan struct{}

	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
}

func (s *fakeSearch) Provider(context.Context) (SearchProvider, error) { return s, nil }

func (s *fakeSearch) Search(_ context.Context, query string, n int) ([]Candidate, error) {
	s.mu.Lock()
	s.calls++
	s.active++
	s.maxActive = max(s.maxActive, s.active)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if s.gate != nil {
		s.started <- query
		<-s.gate
	}
	if err := s.errs[query]; err != nil {
		return nil, err
	}
	c := s.results[query]
	return c[:min(n, len(c))], nil
}

func (s *fakeSearch) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (f *fakeFetcher) fetchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fetched)
}

// groundPage is a recipe page for name with two ingredients, two steps, a
// 40-minute total and the image at https://example.com{imagePath}.
func groundPage(name, imagePath string) []byte {
	return page(`<meta property="og:site_name" content="Example Kitchen">`, fmt.Sprintf(`{
		"@context": "https://schema.org",
		"@type": "Recipe",
		"name": %q,
		"image": "https://example.com%s",
		"recipeIngredient": ["1 onion", "2 cloves garlic, minced"],
		"recipeInstructions": ["Chop the onion.", "Fry with the garlic."],
		"totalTime": "PT40M",
		"recipeYield": "4"
	}`, name, imagePath))
}

// normalizedReply is a usable normalize_recipe reply for groundPage.
func normalizedReply() llm.GenerateResult {
	js := `{"ingredients":[
		{"name":"onion","qty":"1","unit":"","note":""},
		{"name":"garlic","qty":"2","unit":"cloves","note":"minced"}
	],"method":"stovetop","difficulty":null,"minutes_total":40}`
	return llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}}
}

// badReply is a normalize_recipe reply that fails validation.
func badReply() llm.GenerateResult {
	js := `{"ingredients":[],"method":"stovetop","difficulty":null,"minutes_total":40}`
	return llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}}
}

type groundEnv struct {
	g       *Grounder
	st      *store.Store
	dataDir string
	images  *photoServer
	pages   *fakeFetcher
	search  *fakeSearch
	llm     *llm.Fake
}

// newGroundEnv returns a Grounder over pages, images served at
// https://example.com, search and a fake model, normalizing through the
// real harness and normalize_recipe skill.
func newGroundEnv(t *testing.T, pages map[string][]byte, images map[string]photoFile, search *fakeSearch) *groundEnv {
	t.Helper()
	photos, st, dataDir, imgSrv := newTestPhotoCache(t, images)
	model := &llm.Fake{}
	guard, err := harness.NewGuard(harness.DefaultMaxCalls)
	if err != nil {
		t.Fatal(err)
	}
	cfg := harness.Config{
		Settings:    llm.Settings{Provider: "anthropic", Model: "claude-test"},
		NewProvider: func(llm.Settings, string) (llm.Provider, error) { return model, nil },
		Store:       st,
		Guard:       guard,
	}
	normalize := func(ctx context.Context, scope string, in skills.RecipeText) (skills.NormalizedRecipe, error) {
		return skills.NormalizeRecipe(ctx, cfg, scope, in)
	}
	fetcher := &fakeFetcher{pages: pages}
	g, err := NewGrounder(st, search, fetcher, photos, normalize)
	if err != nil {
		t.Fatal(err)
	}
	return &groundEnv{g: g, st: st, dataDir: dataDir, images: imgSrv, pages: fetcher, search: search, llm: model}
}

func jpegFile(t *testing.T, w, h int) photoFile {
	return photoFile{"image/jpeg", encodeJPEG(t, gradient(w, h))}
}

func countRows(t *testing.T, st *store.Store, table string) int {
	t.Helper()
	var n int
	if err := st.Reader.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestGroundHappyPath(t *testing.T) {
	t.Parallel()
	search := &fakeSearch{results: map[string][]Candidate{
		"chicken curry": {
			{URL: "https://a.example/missing"},
			{URL: "https://b.example/curry"},
		},
	}}
	env := newGroundEnv(t,
		map[string][]byte{"https://b.example/curry": groundPage("Chicken Curry", "/img/curry.jpg")},
		map[string]photoFile{"/img/curry.jpg": jpegFile(t, 800, 600)},
		search)
	env.llm.QueueGenerate(normalizedReply())

	gr, err := env.g.Ground(t.Context(), "Chicken Curry", "chicken curry")
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if gr.RecipeID == "" || gr.PhotoID == "" {
		t.Fatalf("Grounded = %+v, want recipe and photo ids", gr)
	}
	want := Grounded{
		RecipeID: gr.RecipeID, PhotoID: gr.PhotoID, Method: "stovetop", MinutesTotal: gr.MinutesTotal,
		Difficulty: "easy", SourceURL: "https://b.example/curry", SiteName: "Example Kitchen",
	}
	if gr != want || gr.MinutesTotal == nil || *gr.MinutesTotal != 40 {
		t.Errorf("Grounded = %+v (minutes %v), want %+v with 40 minutes", gr, gr.MinutesTotal, want)
	}

	q := db.New(env.st.Reader)
	rec, err := q.GetRecipe(t.Context(), gr.RecipeID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Title != "Chicken Curry" || rec.PhotoID.String != gr.PhotoID || rec.Method.String != "stovetop" ||
		rec.Difficulty.String != "easy" || rec.MinutesTotal.Int64 != 40 || rec.TotalMinutes.Int64 != 40 || rec.Yield.String != "4" {
		t.Errorf("recipe row = %+v", rec)
	}
	ings, err := q.ListRecipeIngredients(t.Context(), gr.RecipeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ings) != 2 || ings[1].RawText != "2 cloves garlic, minced" || ings[1].Name.String != "garlic" ||
		ings[1].Qty.String != "2" || ings[1].Unit.String != "cloves" || ings[1].Note.String != "minced" || ings[0].Unit.Valid {
		t.Errorf("ingredients = %+v", ings)
	}
	steps, err := q.ListRecipeSteps(t.Context(), gr.RecipeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].Text != "Chop the onion." || steps[1].Position != 1 {
		t.Errorf("steps = %+v", steps)
	}
	photo, err := q.GetPhoto(t.Context(), gr.PhotoID)
	if err != nil {
		t.Fatal(err)
	}
	if photo.SourceUrl != "https://example.com/img/curry.jpg" {
		t.Errorf("photo row = %+v", photo)
	}
	if _, err := os.Stat(filepath.Join(env.dataDir, filepath.FromSlash(photo.LocalPath))); err != nil {
		t.Errorf("photo file: %v", err)
	}
}

func TestGroundUngroundable(t *testing.T) {
	t.Parallel()
	search := &fakeSearch{results: map[string][]Candidate{
		"mystery stew": {
			{URL: "https://a.example/missing"},
			{URL: "https://b.example/no-recipe"},
			{URL: "https://c.example/tiny-photo"},
		},
	}}
	env := newGroundEnv(t,
		map[string][]byte{
			"https://b.example/no-recipe":  page(""),
			"https://c.example/tiny-photo": groundPage("Mystery Stew", "/img/tiny.jpg"),
		},
		map[string]photoFile{"/img/tiny.jpg": jpegFile(t, 200, 200)},
		search)

	_, err := env.g.Ground(t.Context(), "Mystery Stew", "mystery stew")
	var ungroundable *ErrUngroundable
	if !errors.As(err, &ungroundable) {
		t.Fatalf("Ground error = %v, want *ErrUngroundable", err)
	}
	if ungroundable.Target != "Mystery Stew" {
		t.Errorf("Target = %q", ungroundable.Target)
	}
	for _, reason := range []error{ErrCandidatesUnranked, ErrNoRecipe, ErrPhotoTooSmall} {
		if !errors.Is(err, reason) {
			t.Errorf("error %v does not report %v", err, reason)
		}
	}
	if n := len(env.llm.Requests()); n != 0 {
		t.Errorf("%d model calls, want none before a photo is cached", n)
	}
	for _, table := range []string{"recipes", "recipe_ingredients", "photos"} {
		if n := countRows(t, env.st, table); n != 0 {
			t.Errorf("%d %s rows for an ungroundable dish", n, table)
		}
	}
	if names := photoFiles(t, env.dataDir); len(names) != 0 {
		t.Errorf("photo files stored for an ungroundable dish: %v", names)
	}

	_, err = env.g.Ground(t.Context(), "Nothing At All", "nothing at all")
	if !errors.As(err, &ungroundable) || !errors.Is(err, ErrNoCandidates) {
		t.Errorf("Ground with no search results error = %v, want *ErrUngroundable with ErrNoCandidates", err)
	}
}

func TestGroundTriesNextCandidateAfterBadOutput(t *testing.T) {
	t.Parallel()
	search := &fakeSearch{results: map[string][]Candidate{
		"curry": {{URL: "https://a.example/curry"}, {URL: "https://b.example/curry"}},
	}}
	env := newGroundEnv(t,
		map[string][]byte{
			"https://a.example/curry": groundPage("Curry", "/img/a.jpg"),
			"https://b.example/curry": groundPage("Curry", "/img/b.jpg"),
		},
		map[string]photoFile{"/img/a.jpg": jpegFile(t, 640, 480), "/img/b.jpg": jpegFile(t, 480, 640)},
		search)
	// The first candidate's reply and repair are unusable; the second's is
	// fine.
	env.llm.QueueGenerate(badReply(), badReply(), normalizedReply())

	gr, err := env.g.Ground(t.Context(), "Curry", "curry")
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if gr.SourceURL != "https://b.example/curry" {
		t.Errorf("grounded in %s, want the second candidate", gr.SourceURL)
	}
	if n := countRows(t, env.st, "photos"); n != 1 {
		t.Errorf("%d photos rows, want only the grounded recipe's", n)
	}
}

func TestGroundSearchFailureIsNotUngroundable(t *testing.T) {
	t.Parallel()
	env := newGroundEnv(t, nil, nil, &fakeSearch{errs: map[string]error{"curry": ErrSearchRateLimited}})
	_, err := env.g.Ground(t.Context(), "Curry", "curry")
	var ungroundable *ErrUngroundable
	if !errors.Is(err, ErrSearchRateLimited) || errors.As(err, &ungroundable) {
		t.Errorf("Ground error = %v, want ErrSearchRateLimited and not *ErrUngroundable", err)
	}
}

func TestGroundReusesStoredRecipe(t *testing.T) {
	t.Parallel()
	search := &fakeSearch{results: map[string][]Candidate{
		"curry":         {{URL: "https://b.example/curry"}},
		"chicken curry": {{URL: "https://a.example/other"}, {URL: "https://b.example/curry"}},
	}}
	env := newGroundEnv(t,
		map[string][]byte{"https://b.example/curry": groundPage("Curry", "/img/curry.jpg")},
		map[string]photoFile{"/img/curry.jpg": jpegFile(t, 640, 480)},
		search)
	env.llm.QueueGenerate(normalizedReply())

	first, err := env.g.Ground(t.Context(), "Curry", "curry")
	if err != nil {
		t.Fatalf("first Ground: %v", err)
	}
	pages, images, calls := env.pages.fetchCount(), env.images.requests.Load(), len(env.llm.Requests())

	// The stored page is gone and the model has no reply left: only the
	// stored recipe can ground the dish.
	delete(env.pages.pages, "https://b.example/curry")
	again, err := env.g.Ground(t.Context(), "Chicken Curry", "chicken curry")
	if err != nil {
		t.Fatalf("second Ground: %v", err)
	}
	if again.RecipeID != first.RecipeID || again.PhotoID != first.PhotoID || again.Method != first.Method ||
		again.Difficulty != first.Difficulty || *again.MinutesTotal != *first.MinutesTotal || again.SiteName != first.SiteName {
		t.Errorf("reused grounding = %+v, want %+v", again, first)
	}
	if env.images.requests.Load() != images || len(env.llm.Requests()) != calls {
		t.Errorf("reuse fetched images %d→%d, model calls %d→%d; want none",
			images, env.images.requests.Load(), calls, len(env.llm.Requests()))
	}
	// Ranking fetches only the candidate not stored; the stored one ranks
	// on its stored recipe.
	env.pages.mu.Lock()
	fetched := append([]string(nil), env.pages.fetched[pages:]...)
	env.pages.mu.Unlock()
	if len(fetched) != 1 || fetched[0] != "https://a.example/other" {
		t.Errorf("reuse fetched %v, want only https://a.example/other", fetched)
	}
	pages = env.pages.fetchCount()

	byURL, err := env.g.GroundURL(t.Context(), "https://b.example/curry")
	if err != nil || byURL.RecipeID != first.RecipeID {
		t.Errorf("GroundURL of the stored page = %+v, %v; want the stored recipe", byURL, err)
	}
	if env.pages.fetchCount() != pages {
		t.Error("GroundURL refetched a stored page")
	}
	if n := countRows(t, env.st, "recipes"); n != 1 {
		t.Errorf("%d recipes rows, want 1", n)
	}
}

func TestGroundStoredRecipeDoesNotOutrankRelevantOne(t *testing.T) {
	t.Parallel()
	search := &fakeSearch{results: map[string][]Candidate{
		"brownies":      {{URL: "https://b.example/brownies"}},
		"chicken curry": {{URL: "https://b.example/brownies"}, {URL: "https://a.example/curry"}},
	}}
	env := newGroundEnv(t,
		map[string][]byte{
			"https://a.example/curry":    groundPage("Chicken Curry", "/img/curry.jpg"),
			"https://b.example/brownies": groundPage("Fudgy Brownies", "/img/brownies.jpg"),
		},
		map[string]photoFile{
			"/img/curry.jpg":    jpegFile(t, 640, 480),
			"/img/brownies.jpg": jpegFile(t, 640, 480),
		},
		search)
	env.llm.QueueGenerate(normalizedReply(), normalizedReply())

	brownies, err := env.g.Ground(t.Context(), "Brownies", "brownies")
	if err != nil {
		t.Fatalf("Ground brownies: %v", err)
	}

	// Search lists the unrelated stored recipe first and the relevant new
	// one second: ranking, not search order or storage, grounds the
	// relevant one.
	curry, err := env.g.Ground(t.Context(), "Chicken Curry", "chicken curry")
	if err != nil {
		t.Fatalf("Ground chicken curry: %v", err)
	}
	if curry.SourceURL != "https://a.example/curry" || curry.RecipeID == brownies.RecipeID {
		t.Errorf("grounded Chicken Curry in %s (recipe %s), want https://a.example/curry, not the stored brownies %s",
			curry.SourceURL, curry.RecipeID, brownies.RecipeID)
	}
	if n := countRows(t, env.st, "recipes"); n != 2 {
		t.Errorf("%d recipes rows, want 2", n)
	}
}

func TestGroundURL(t *testing.T) {
	t.Parallel()
	env := newGroundEnv(t,
		map[string][]byte{
			"https://b.example/pasted":   groundPage("Grandma's Slow Cooker Chili", "/img/chili.jpg"),
			"https://b.example/no-image": recipePage("Chili", true, false, true, true),
			"https://b.example/no-time": page("", `{"@type":"Recipe","name":"Chili","image":"https://example.com/img/chili.jpg",
				"recipeIngredient":["1 onion"],"totalTime":"PT0M"}`),
		},
		map[string]photoFile{"/img/chili.jpg": jpegFile(t, 640, 480)},
		&fakeSearch{})
	env.llm.QueueGenerate(normalizedReply())

	gr, err := env.g.GroundURL(t.Context(), "https://b.example/pasted")
	if err != nil {
		t.Fatalf("GroundURL: %v", err)
	}
	if gr.SourceURL != "https://b.example/pasted" || gr.Method != "crockpot" || gr.PhotoID == "" {
		t.Errorf("Grounded = %+v, want the pasted URL, crockpot from the title", gr)
	}
	if env.search.callCount() != 0 {
		t.Error("GroundURL searched")
	}

	for rawURL, reason := range map[string]error{
		"https://b.example/no-image": ErrNoImage,
		"https://b.example/no-time":  ErrTotalTimeNotPositive,
		"https://b.example/missing":  nil,
	} {
		_, err := env.g.GroundURL(t.Context(), rawURL)
		var ungroundable *ErrUngroundable
		if !errors.As(err, &ungroundable) || ungroundable.Target != rawURL {
			t.Errorf("GroundURL(%s) error = %v, want *ErrUngroundable", rawURL, err)
		}
		var status *StatusError
		if reason != nil && !errors.Is(err, reason) || reason == nil && !errors.As(err, &status) {
			t.Errorf("GroundURL(%s) error = %v, want reason %v", rawURL, err, reason)
		}
	}
}

func TestGroundManyIsolatesFailures(t *testing.T) {
	t.Parallel()
	search := &fakeSearch{
		results: map[string][]Candidate{
			"curry":   {{URL: "https://a.example/curry"}},
			"nothing": {{URL: "https://a.example/missing"}},
			"chili":   {{URL: "https://b.example/chili"}},
		},
		errs: map[string]error{"broken": ErrSearchRefused},
	}
	env := newGroundEnv(t,
		map[string][]byte{
			"https://a.example/curry": groundPage("Curry", "/img/curry.jpg"),
			"https://b.example/chili": groundPage("Chili", "/img/chili.jpg"),
		},
		map[string]photoFile{"/img/curry.jpg": jpegFile(t, 640, 480), "/img/chili.jpg": jpegFile(t, 480, 640)},
		search)
	env.llm.QueueGenerate(normalizedReply(), normalizedReply())

	results := env.g.GroundMany(t.Context(), []GroundRequest{
		{DishName: "Curry", Query: "curry"},
		{DishName: "Nothing", Query: "nothing"},
		{DishName: "Broken", Query: "broken"},
		{DishName: "Chili", Query: "chili"},
	})
	if len(results) != 4 {
		t.Fatalf("%d results, want 4", len(results))
	}
	if results[0].Err != nil || results[0].Grounded.SourceURL != "https://a.example/curry" {
		t.Errorf("curry = %+v", results[0])
	}
	var ungroundable *ErrUngroundable
	if !errors.As(results[1].Err, &ungroundable) {
		t.Errorf("nothing error = %v, want *ErrUngroundable", results[1].Err)
	}
	if !errors.Is(results[2].Err, ErrSearchRefused) {
		t.Errorf("broken error = %v, want ErrSearchRefused", results[2].Err)
	}
	if results[3].Err != nil || results[3].Grounded.SourceURL != "https://b.example/chili" {
		t.Errorf("chili = %+v", results[3])
	}
	if n := countRows(t, env.st, "recipes"); n != 2 {
		t.Errorf("%d recipes rows, want 2", n)
	}
}

func TestGroundManyLimitsConcurrency(t *testing.T) {
	t.Parallel()
	search := &fakeSearch{started: make(chan string, 5), gate: make(chan struct{})}
	env := newGroundEnv(t, nil, nil, search)
	reqs := make([]GroundRequest, 5)
	for i := range reqs {
		reqs[i] = GroundRequest{DishName: fmt.Sprintf("Dish %d", i), Query: fmt.Sprintf("dish %d", i)}
	}
	done := make(chan []GroundResult)
	go func() { done <- env.g.GroundMany(t.Context(), reqs) }()

	for range GroundConcurrency {
		select {
		case <-search.started:
		case <-time.After(5 * time.Second):
			t.Fatal("fewer than GroundConcurrency items started")
		}
	}
	select {
	case q := <-search.started:
		t.Fatalf("%s started while %d were in flight", q, GroundConcurrency)
	case <-time.After(100 * time.Millisecond):
	}
	close(search.gate)
	results := <-done
	for i, r := range results {
		if !errors.Is(r.Err, ErrNoCandidates) {
			t.Errorf("item %d error = %v, want ErrNoCandidates", i, r.Err)
		}
	}
	if search.maxActive != GroundConcurrency || search.callCount() != 5 {
		t.Errorf("max in flight %d of %d calls, want %d of 5", search.maxActive, search.callCount(), GroundConcurrency)
	}
}

func TestCandidatesOffersBestFirstUpToN(t *testing.T) {
	t.Parallel()
	var cands []Candidate
	pages := map[string][]byte{}
	for i, title := range []string{"Fish Tacos", "Fish Taco Bowls", "Baja Fish Tacos", "Tacos"} {
		u := fmt.Sprintf("https://site%d.example/tacos", i)
		cands = append(cands, Candidate{URL: u})
		pages[u] = groundPage(title, "/img/tacos.jpg")
	}
	search := &fakeSearch{results: map[string][]Candidate{"fish tacos": cands}}
	env := newGroundEnv(t, pages, nil, search)

	got, err := env.g.Candidates(t.Context(), "Fish Tacos", "fish tacos", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].URL != cands[0].URL || got[0].Title != "Fish Tacos" || got[0].SiteName != "Example Kitchen" ||
		got[0].PhotoID != "" || got[0].Minutes == nil || *got[0].Minutes != 40 {
		t.Fatalf("Candidates = %+v, want 3 with Fish Tacos first", got)
	}
	if env.images.requests.Load() != 0 || len(env.llm.Requests()) != 0 {
		t.Errorf("Candidates downloaded a photo or ran the model")
	}
}

func TestCandidatesSearchFailureWrapsErrSearchFailed(t *testing.T) {
	t.Parallel()
	search := &fakeSearch{errs: map[string]error{"stew": ErrSearchRateLimited}}
	env := newGroundEnv(t, nil, nil, search)
	_, err := env.g.Candidates(t.Context(), "Stew", "stew", 3)
	if !errors.Is(err, ErrSearchFailed) || !errors.Is(err, ErrSearchRateLimited) {
		t.Errorf("err = %v, want ErrSearchFailed wrapping ErrSearchRateLimited", err)
	}
}
