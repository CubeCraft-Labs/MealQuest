package recipes

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
)

const searxngBody = `{
	"query": "chicken curry recipe",
	"results": [
		{"url": "https://www.bbcgoodfood.com/recipes/easy-chicken-curry", "title": "Easy chicken curry recipe | BBC Good Food", "content": "Make our easy chicken curry &amp; rice.", "engine": "duckduckgo"},
		{"url": "https://www.recipetineats.com/chicken-curry/", "title": "Chicken Curry", "content": "A proper curry."},
		{"url": "https://example.com/third", "title": "Third", "content": ""}
	],
	"number_of_results": 0
}`

const braveBody = `{
	"type": "search",
	"web": {
		"type": "search",
		"results": [
			{"url": "https://www.bbcgoodfood.com/recipes/easy-chicken-curry", "title": "Easy <strong>chicken curry</strong> recipe", "description": "Make our <strong>easy</strong> chicken curry &amp; rice.", "language": "en"},
			{"url": "https://www.recipetineats.com/chicken-curry/", "title": "Chicken Curry", "description": "A proper curry."}
		]
	}
}`

func newSearXNGServer(t *testing.T, status int, body string, got *http.Request) *SearXNG {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			*got = *r.Clone(context.Background())
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	s, err := NewSearXNG(srv.URL + "/searx")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newBraveServer(t *testing.T, key string, status int, body string, got *http.Request) *Brave {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got != nil {
			*got = *r.Clone(context.Background())
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	b, err := NewBrave(key)
	if err != nil {
		t.Fatal(err)
	}
	b.endpoint = srv.URL + "/res/v1/web/search"
	return b
}

func TestSearXNGSearch(t *testing.T) {
	var req http.Request
	s := newSearXNGServer(t, http.StatusOK, searxngBody, &req)
	got, err := s.Search(context.Background(), "chicken curry", 2)
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Path != "/searx/search" {
		t.Errorf("path = %q, want /searx/search", req.URL.Path)
	}
	q := req.URL.Query()
	if q.Get("q") != "chicken curry recipe" || q.Get("format") != "json" || q.Get("categories") != "general" {
		t.Errorf("query = %v", q)
	}
	if req.Header.Get("User-Agent") != UserAgent {
		t.Errorf("User-Agent = %q", req.Header.Get("User-Agent"))
	}
	want := []Candidate{
		{URL: "https://www.bbcgoodfood.com/recipes/easy-chicken-curry", Title: "Easy chicken curry recipe | BBC Good Food", Snippet: "Make our easy chicken curry & rice."},
		{URL: "https://www.recipetineats.com/chicken-curry/", Title: "Chicken Curry", Snippet: "A proper curry."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("candidates =\n%#v\nwant\n%#v", got, want)
	}
}

func TestBraveSearch(t *testing.T) {
	var req http.Request
	b := newBraveServer(t, "brave-key-123", http.StatusOK, braveBody, &req)
	got, err := b.Search(context.Background(), "Chicken curry RECIPE", 5)
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("X-Subscription-Token") != "brave-key-123" {
		t.Errorf("X-Subscription-Token = %q", req.Header.Get("X-Subscription-Token"))
	}
	if req.Header.Get("Accept") != "application/json" {
		t.Errorf("Accept = %q", req.Header.Get("Accept"))
	}
	q := req.URL.Query()
	if q.Get("q") != "Chicken curry RECIPE" || q.Get("count") != "5" {
		t.Errorf("query = %v", q)
	}
	want := []Candidate{
		{URL: "https://www.bbcgoodfood.com/recipes/easy-chicken-curry", Title: "Easy chicken curry recipe", Snippet: "Make our easy chicken curry & rice."},
		{URL: "https://www.recipetineats.com/chicken-curry/", Title: "Chicken Curry", Snippet: "A proper curry."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("candidates =\n%#v\nwant\n%#v", got, want)
	}
}

func TestSearchQuery(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"chicken curry", "chicken curry recipe"},
		{"  chicken curry  ", "chicken curry recipe"},
		{"chicken curry recipe", "chicken curry recipe"},
		{"Recipe for chicken curry", "Recipe for chicken curry"},
		{"recipes for curry", "recipes for curry recipe"},
	} {
		got, err := searchQuery(tc.in, 3)
		if err != nil || got != tc.want {
			t.Errorf("searchQuery(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		q string
		n int
	}{{"", 3}, {"   ", 3}, {"curry", 0}, {"curry", MaxSearchResults + 1}} {
		if _, err := searchQuery(tc.q, tc.n); err == nil {
			t.Errorf("searchQuery(%q, %d) succeeded, want error", tc.q, tc.n)
		}
	}
}

func TestSearchErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{"unauthorized", http.StatusUnauthorized, `{}`, func(err error) bool { return errors.Is(err, ErrSearchRefused) }},
		{"forbidden", http.StatusForbidden, `{}`, func(err error) bool { return errors.Is(err, ErrSearchRefused) }},
		{"rate limited", http.StatusTooManyRequests, `{}`, func(err error) bool { return errors.Is(err, ErrSearchRateLimited) }},
		{"server error", http.StatusBadGateway, `{}`, func(err error) bool {
			var se *SearchStatusError
			return errors.As(err, &se) && se.Status == http.StatusBadGateway
		}},
		{"redirect not followed", http.StatusFound, ``, func(err error) bool {
			var se *SearchStatusError
			return errors.As(err, &se) && se.Status == http.StatusFound
		}},
		{"not JSON", http.StatusOK, `<html>`, func(err error) bool { return errors.Is(err, ErrSearchResponse) }},
		{"result without URL", http.StatusOK, `{"results":[{"title":"x"}],"web":{"results":[{"title":"x"}]}}`, func(err error) bool { return errors.Is(err, ErrSearchResponse) }},
		{"relative URL", http.StatusOK, `{"results":[{"url":"/x"}],"web":{"results":[{"url":"/x"}]}}`, func(err error) bool { return errors.Is(err, ErrSearchResponse) }},
		{"body too large", http.StatusOK, `{"results":[],"pad":"` + strings.Repeat("x", MaxSearchBodyBytes) + `"}`, func(err error) bool { return errors.Is(err, ErrSearchResponse) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			providers := map[string]SearchProvider{
				"SearXNG": newSearXNGServer(t, tc.status, tc.body, nil),
				"Brave":   newBraveServer(t, "secret-key", tc.status, tc.body, nil),
			}
			for name, p := range providers {
				_, err := p.Search(context.Background(), "curry", 3)
				if err == nil || !tc.check(err) {
					t.Errorf("%s: err = %v", name, err)
				}
				if err != nil && strings.Contains(err.Error(), "secret-key") {
					t.Errorf("%s: error leaks the API key: %v", name, err)
				}
			}
		})
	}
}

func TestSearchContextCancelled(t *testing.T) {
	s := newSearXNGServer(t, http.StatusOK, searxngBody, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(ctx, "curry", 3); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestSearchUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	s, err := NewSearXNG(url)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Search(context.Background(), "curry", 3)
	if err == nil || !strings.Contains(err.Error(), "SearXNG search") {
		t.Errorf("err = %v", err)
	}
}

func TestNewSearXNGRejects(t *testing.T) {
	for _, u := range []string{"", "searxng:8080", "ftp://searxng", "http://", "http://u:p@searxng", "http://searxng?x=1", "http://searxng#f"} {
		if _, err := NewSearXNG(u); err == nil {
			t.Errorf("NewSearXNG(%q) succeeded, want error", u)
		}
	}
	if _, err := NewSearXNG("http://searxng:8080"); err != nil {
		t.Errorf("internal URL rejected: %v", err)
	}
}

func TestNewBraveRejects(t *testing.T) {
	for _, k := range []string{"", "with space", "line\nbreak"} {
		if _, err := NewBrave(k); err == nil {
			t.Errorf("NewBrave(%q) succeeded, want error", k)
		}
	}
}

func TestNewSearchProvider(t *testing.T) {
	raw := make([]byte, secretbox.KeySize)
	rand.Read(raw)
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := box.Encrypt([]byte("brave-key"))
	if err != nil {
		t.Fatal(err)
	}

	p, err := NewSearchProvider(db.RecipeSearchSetting{Provider: "brave", ApiKeyCt: ct}, box, "")
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := p.(*Brave); !ok || b.key != "brave-key" || b.endpoint != BraveEndpoint {
		t.Errorf("provider = %#v, want Brave with decrypted key", p)
	}

	p, err = NewSearchProvider(db.RecipeSearchSetting{Provider: "searxng"}, box, "http://searxng:8080")
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := p.(*SearXNG); !ok || s.endpoint.String() != "http://searxng:8080/search" {
		t.Errorf("provider = %#v, want SearXNG at http://searxng:8080/search", p)
	}

	for name, tc := range map[string]struct {
		s   db.RecipeSearchSetting
		url string
	}{
		"brave without key":   {db.RecipeSearchSetting{Provider: "brave"}, ""},
		"brave bad key":       {db.RecipeSearchSetting{Provider: "brave", ApiKeyCt: []byte("not a token at all, far too plain")}, ""},
		"searxng without URL": {db.RecipeSearchSetting{Provider: "searxng"}, ""},
		"unknown provider":    {db.RecipeSearchSetting{Provider: "google"}, ""},
	} {
		if _, err := NewSearchProvider(tc.s, box, tc.url); err == nil {
			t.Errorf("%s: succeeded, want error", name)
		}
	}
}
