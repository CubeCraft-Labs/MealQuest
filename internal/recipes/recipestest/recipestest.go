// Package recipestest provides fakes for tests that ground dinners outside
// package recipes: a search, page and image fetchers serving fixtures, and
// a Grounder over them that normalizes through the real harness and
// normalize_recipe skill with a scripted model.
package recipestest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"sync"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Search is a recipes.SearchSource and recipes.SearchProvider answering
// each query from Results. Err, when set, fails every search. Gate, when
// set, makes every search wait for it to close.
type Search struct {
	Results map[string][]recipes.Candidate
	Err     error
	Gate    chan struct{}

	mu      sync.Mutex
	queries []string
}

func (s *Search) Provider(context.Context) (recipes.SearchProvider, error) { return s, nil }

func (s *Search) Search(ctx context.Context, query string, n int) ([]recipes.Candidate, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	if s.Gate != nil {
		select {
		case <-s.Gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.Err != nil {
		return nil, s.Err
	}
	c := s.Results[query]
	return c[:min(n, len(c))], nil
}

// Queries returns every query searched, in order.
func (s *Search) Queries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

// Pages is a recipes.PageFetcher serving Bodies by URL; any other URL is a
// 404. Gate, when set, makes every fetch wait for it to close.
type Pages struct {
	Bodies map[string][]byte
	Gate   chan struct{}
}

func (p *Pages) Fetch(ctx context.Context, rawURL string) (recipes.Page, error) {
	if p.Gate != nil {
		select {
		case <-p.Gate:
		case <-ctx.Done():
			return recipes.Page{}, ctx.Err()
		}
	}
	body, ok := p.Bodies[rawURL]
	if !ok {
		return recipes.Page{}, &recipes.StatusError{URL: rawURL, Status: 404}
	}
	return recipes.Page{URL: rawURL, ContentType: "text/html", Body: body}, nil
}

// Images is a recipes.ImageFetcher serving JPEGs by URL; any other URL is
// a 404.
type Images struct {
	JPEGs map[string][]byte
}

func (i *Images) FetchImage(_ context.Context, rawURL string) (recipes.Page, error) {
	body, ok := i.JPEGs[rawURL]
	if !ok {
		return recipes.Page{}, &recipes.StatusError{URL: rawURL, Status: 404}
	}
	return recipes.Page{URL: rawURL, ContentType: "image/jpeg", Body: body}, nil
}

// JPEG is a w×h JPEG whose pixels depend on seed, so different seeds give
// different photos.
func JPEG(t testing.TB, w, h int, seed uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), seed, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// RecipePage is a page whose schema.org Recipe is title, from site, with
// two ingredients, two steps, a total time of minutes and the image at
// imageURL.
func RecipePage(title, site, imageURL string, minutes int) []byte {
	return fmt.Appendf(nil, `<!DOCTYPE html><html><head>
<meta property="og:site_name" content=%q>
<script type="application/ld+json">{
	"@context": "https://schema.org",
	"@type": "Recipe",
	"name": %q,
	"image": %q,
	"recipeIngredient": ["1 onion", "2 cloves garlic, minced"],
	"recipeInstructions": ["Chop the onion.", "Fry with the garlic."],
	"totalTime": "PT%dM"
}</script></head><body></body></html>`, site, title, imageURL, minutes)
}

// NormalizedReply is a usable normalize_recipe reply for a RecipePage with
// method and minutes.
func NormalizedReply(method string, minutes int) llm.GenerateResult {
	js := fmt.Sprintf(`{"ingredients":[
		{"name":"onion","qty":"1","unit":"","note":""},
		{"name":"garlic","qty":"2","unit":"cloves","note":"minced"}
	],"method":%q,"difficulty":null,"minutes_total":%d}`, method, minutes)
	return llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}}
}

// NewGrounder returns a Grounder storing in st, with photos cached under
// dataDir, normalizing through the real harness with model.
func NewGrounder(t testing.TB, st *store.Store, dataDir string, search recipes.SearchSource, pages recipes.PageFetcher, images recipes.ImageFetcher, model llm.Provider) *recipes.Grounder {
	t.Helper()
	photos, err := recipes.NewPhotoCache(images, st, dataDir)
	if err != nil {
		t.Fatal(err)
	}
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
	g, err := recipes.NewGrounder(st, search, pages, photos, normalize)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
