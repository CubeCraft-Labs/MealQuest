package recipes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

var _ PageFetcher = (*Fetcher)(nil)

// fakeFetcher serves fixture pages by URL and records what was fetched.
type fakeFetcher struct {
	pages map[string][]byte

	mu      sync.Mutex
	fetched []string
}

func (f *fakeFetcher) Fetch(_ context.Context, rawURL string) (Page, error) {
	f.mu.Lock()
	f.fetched = append(f.fetched, rawURL)
	f.mu.Unlock()
	body, ok := f.pages[rawURL]
	if !ok {
		return Page{}, &StatusError{URL: rawURL, Status: 404}
	}
	return Page{URL: rawURL, ContentType: "text/html", Body: body}, nil
}

// recipePage returns a page whose Recipe has the given name and, as asked,
// ingredients, an image, steps and a total time.
func recipePage(name string, ingredients, image, steps, minutes bool) []byte {
	ld := fmt.Sprintf(`{"@context":"https://schema.org","@type":"Recipe","name":%q`, name)
	if ingredients {
		ld += `,"recipeIngredient":["1 onion","2 cloves garlic"]`
	}
	if image {
		ld += `,"image":"https://img.example.com/dish.jpg"`
	}
	if steps {
		ld += `,"recipeInstructions":["Chop.","Cook."]`
	}
	if minutes {
		ld += `,"totalTime":"PT40M"`
	}
	return page("", ld+"}")
}

func TestRankOrder(t *testing.T) {
	f := &fakeFetcher{pages: map[string][]byte{
		"https://a.example/thai":    recipePage("Thai Green Curry", true, true, true, true),
		"https://b.example/noimg":   recipePage("Chicken Curry", true, false, true, true),
		"https://c.example/easy":    recipePage("Easy Chicken Curry", true, true, true, false),
		"https://d.example/noingr":  recipePage("Chicken Curry", false, true, true, true),
		"https://e.example/best":    recipePage("Chicken Curry", true, true, true, true),
		"https://f.example/toodeep": recipePage("Chicken Curry", true, true, true, true),
	}}
	cands := []Candidate{
		{URL: "https://a.example/thai", Title: "Thai green curry"},
		{URL: "https://b.example/noimg"},
		{URL: "https://c.example/easy"},
		{URL: "https://d.example/noingr"},
		{URL: "https://e.example/best"},
		{URL: "https://f.example/toodeep"},
	}
	got, err := Rank(context.Background(), f, "Chicken curry", cands)
	if !errors.Is(err, ErrCandidatesUnranked) || !errors.Is(err, ErrNoImage) || !errors.Is(err, ErrNoIngredients) {
		t.Errorf("err = %v, want ErrCandidatesUnranked with ErrNoImage and ErrNoIngredients", err)
	}
	if errors.Is(err, ErrNoUsableRecipe) {
		t.Errorf("err = %v, want not ErrNoUsableRecipe when some are ranked", err)
	}
	want := []struct {
		url   string
		score float64
	}{
		// Same words, every field: 1 + 1.
		{"https://e.example/best", 2},
		// "Easy" is a filler word, so the title matches; no total time.
		{"https://c.example/easy", 1.75},
		// Shares one of four words: 0.25 + 1.
		{"https://a.example/thai", 1.25},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d ranked, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Candidate.URL != w.url || got[i].Score != w.score {
			t.Errorf("rank %d = %s (%.2f), want %s (%.2f)", i, got[i].Candidate.URL, got[i].Score, w.url, w.score)
		}
	}
	if got[2].Recipe.Title != "Thai Green Curry" || got[2].Candidate.Title != "Thai green curry" {
		t.Errorf("ranked entry lost its recipe or candidate: %+v", got[2])
	}
	for _, u := range f.fetched {
		if u == "https://f.example/toodeep" {
			t.Errorf("fetched a candidate past RankDepth")
		}
	}
	if len(f.fetched) != RankDepth {
		t.Errorf("fetched %d pages, want %d", len(f.fetched), RankDepth)
	}
}

func TestRankTiesKeepSearchOrder(t *testing.T) {
	f := &fakeFetcher{pages: map[string][]byte{
		"https://a.example/1": recipePage("Beef Stew", true, true, true, true),
		"https://b.example/2": recipePage("Beef Stew", true, true, true, true),
		"https://c.example/3": recipePage("Beef Stew", true, true, true, true),
	}}
	cands := []Candidate{{URL: "https://a.example/1"}, {URL: "https://b.example/2"}, {URL: "https://c.example/3"}}
	got, err := Rank(context.Background(), f, "beef stew", cands)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cands {
		if got[i].Candidate.URL != c.URL {
			t.Errorf("rank %d = %s, want %s", i, got[i].Candidate.URL, c.URL)
		}
	}
}

// TestRankReportsFetchFailure checks a failed fetch is reported with the
// candidates that were ranked, never swallowed.
func TestRankReportsFetchFailure(t *testing.T) {
	f := &fakeFetcher{pages: map[string][]byte{
		"https://a.example/1": recipePage("Beef Stew", true, true, true, true),
	}}
	cands := []Candidate{{URL: "https://a.example/1"}, {URL: "https://b.example/missing"}}
	got, err := Rank(context.Background(), f, "beef stew", cands)
	if len(got) != 1 || got[0].Candidate.URL != "https://a.example/1" {
		t.Errorf("ranked = %+v, want only https://a.example/1", got)
	}
	if !errors.Is(err, ErrCandidatesUnranked) {
		t.Errorf("err = %v, want ErrCandidatesUnranked", err)
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 404 || se.URL != "https://b.example/missing" {
		t.Errorf("err = %v, want the 404 StatusError for https://b.example/missing", err)
	}
	if err != nil && !strings.Contains(err.Error(), "https://b.example/missing") {
		t.Errorf("err = %v, want it to name the failed candidate", err)
	}
}

func TestRankAllRejected(t *testing.T) {
	f := &fakeFetcher{pages: map[string][]byte{
		"https://a.example/noimg":  recipePage("Chicken Curry", true, false, true, true),
		"https://b.example/noingr": recipePage("Chicken Curry", false, true, true, true),
		"https://c.example/blog":   page(""),
	}}
	cands := []Candidate{
		{URL: "https://a.example/noimg"},
		{URL: "https://b.example/noingr"},
		{URL: "https://c.example/blog"},
		{URL: "https://d.example/missing"},
	}
	_, err := Rank(context.Background(), f, "chicken curry", cands)
	for _, want := range []error{ErrNoUsableRecipe, ErrNoImage, ErrNoIngredients, ErrNoRecipe} {
		if !errors.Is(err, want) {
			t.Errorf("err = %v, want it to wrap %v", err, want)
		}
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 404 {
		t.Errorf("err = %v, want the 404 StatusError", err)
	}
}

func TestRankNoCandidates(t *testing.T) {
	if _, err := Rank(context.Background(), &fakeFetcher{}, "chicken curry", nil); !errors.Is(err, ErrNoUsableRecipe) {
		t.Errorf("err = %v, want ErrNoUsableRecipe", err)
	}
}

func TestRankContextCancelled(t *testing.T) {
	f := &fakeFetcher{pages: map[string][]byte{"https://a.example/1": recipePage("Beef Stew", true, true, true, true)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Rank(ctx, f, "beef stew", []Candidate{{URL: "https://a.example/1"}}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestRankOverHTTP ranks fixture candidates served over HTTPS through the
// SSRF-safe Fetcher: every host, a subdomain the test server's certificate
// covers, resolves to a public test address the test
// dialer routes to one httptest server, which serves pages by host and path.
func TestRankOverHTTP(t *testing.T) {
	t.Parallel()
	pages := map[string][]byte{
		"best.example.com/curry":  recipePage("Chicken Curry", true, true, true, true),
		"easy.example.com/curry":  recipePage("Easy Chicken Curry", true, true, true, false),
		"thai.example.com/curry":  recipePage("Thai Green Curry", true, true, true, true),
		"noimg.example.com/curry": recipePage("Chicken Curry", true, false, true, true),
	}
	env := newTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.Host+r.URL.Path]
		if !ok {
			http.Error(w, "broken", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write(body)
	}), map[string][][]string{
		"best.example.com":   {{publicIP}},
		"easy.example.com":   {{publicIP}},
		"thai.example.com":   {{publicIP}},
		"noimg.example.com":  {{publicIP}},
		"broken.example.com": {{publicIP}},
	})
	cands := []Candidate{
		{URL: "https://thai.example.com/curry"},
		{URL: "https://broken.example.com/curry"},
		{URL: "https://easy.example.com/curry"},
		{URL: "https://noimg.example.com/curry"},
		{URL: "https://best.example.com/curry"},
	}

	got, err := Rank(t.Context(), env.f, "Chicken curry", cands)

	want := []struct {
		url   string
		score float64
	}{
		{"https://best.example.com/curry", 2},
		{"https://easy.example.com/curry", 1.75},
		{"https://thai.example.com/curry", 1.25},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d ranked, want %d: %+v (err %v)", len(got), len(want), got, err)
	}
	for i, w := range want {
		if got[i].Candidate.URL != w.url || got[i].Score != w.score {
			t.Errorf("rank %d = %s (%.2f), want %s (%.2f)", i, got[i].Candidate.URL, got[i].Score, w.url, w.score)
		}
	}
	if got[0].Recipe.Title != "Chicken Curry" || got[0].Recipe.ImageURL != "https://img.example.com/dish.jpg" {
		t.Errorf("top recipe = %+v", got[0].Recipe)
	}
	if !errors.Is(err, ErrCandidatesUnranked) || !errors.Is(err, ErrNoImage) {
		t.Errorf("err = %v, want ErrCandidatesUnranked with ErrNoImage", err)
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusInternalServerError {
		t.Errorf("err = %v, want the 500 StatusError for broken.example.com", err)
	}
	if n := len(env.dials()); n != len(cands) {
		t.Errorf("dialed %d times, want %d", n, len(cands))
	}
}
