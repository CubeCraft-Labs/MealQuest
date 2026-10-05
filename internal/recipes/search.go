package recipes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
)

const (
	// MaxSearchResults is the most candidates one search may ask for; it is
	// Brave's largest page.
	MaxSearchResults = 20
	// SearchTimeout bounds one search request, reading the body included.
	SearchTimeout = 10 * time.Second
	// MaxSearchBodyBytes caps a search response body. A larger one is an
	// error, never truncated.
	MaxSearchBodyBytes = 1 << 20
	// BraveEndpoint is the Brave Web Search API.
	BraveEndpoint = "https://api.search.brave.com/res/v1/web/search"
)

var (
	// ErrSearchRefused is returned for HTTP 401 or 403: Brave rejected the
	// API key, or SearXNG does not allow format=json.
	ErrSearchRefused = errors.New("recipes: search provider refused the request")
	// ErrSearchRateLimited is returned for HTTP 429.
	ErrSearchRateLimited = errors.New("recipes: search provider rate limit reached")
	// ErrSearchResponse is returned for a response body that is not the
	// provider's JSON shape, is over MaxSearchBodyBytes, or holds a result
	// without an absolute http(s) URL.
	ErrSearchResponse = errors.New("recipes: malformed search response")
)

// SearchStatusError is returned for any other non-2xx search response,
// redirects included: a search endpoint is called directly and never
// followed elsewhere.
type SearchStatusError struct {
	Provider string
	Status   int
}

func (e *SearchStatusError) Error() string {
	return fmt.Sprintf("recipes: %s search: HTTP %d", e.Provider, e.Status)
}

// Candidate is one search result: a page that may hold a recipe.
type Candidate struct {
	URL     string
	Title   string
	Snippet string
}

// SearchProvider finds recipe pages on the web.
type SearchProvider interface {
	// Search returns up to n candidates for query, best first. The query
	// gets " recipe" appended unless it already has the word. n must be
	// 1 to MaxSearchResults.
	Search(ctx context.Context, query string, n int) ([]Candidate, error)
}

// NewSearchProvider returns the provider s names. SearXNG is called at
// searxngURL, the configured MEALQUEST_SEARXNG_URL; Brave is called with the
// API key in s.ApiKeyCt, decrypted by box.
func NewSearchProvider(s db.RecipeSearchSetting, box *secretbox.Box, searxngURL string) (SearchProvider, error) {
	switch s.Provider {
	case "searxng":
		return NewSearXNG(searxngURL)
	case "brave":
		if len(s.ApiKeyCt) == 0 {
			return nil, errors.New("recipes: Brave search has no API key")
		}
		key, err := box.Decrypt(s.ApiKeyCt)
		if err != nil {
			return nil, fmt.Errorf("recipes: Brave API key: %w", err)
		}
		return NewBrave(string(key))
	default:
		return nil, fmt.Errorf("recipes: unknown search provider %q", s.Provider)
	}
}

// SearXNG searches through a SearXNG instance. Its base URL is a configured
// endpoint and may be on a private network, so it is called directly rather
// than through the public-only Fetcher.
type SearXNG struct {
	endpoint *url.URL
	client   *http.Client
}

// NewSearXNG returns a SearXNG provider for the instance at baseURL, an
// absolute http:// or https:// URL without credentials, query or fragment.
func NewSearXNG(baseURL string) (*SearXNG, error) {
	if baseURL == "" {
		return nil, errors.New("recipes: SearXNG URL is not set")
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("recipes: SearXNG URL must be an absolute http:// or https:// URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("recipes: SearXNG URL must not have credentials, a query or a fragment")
	}
	return &SearXNG{endpoint: u.JoinPath("search"), client: newSearchClient()}, nil
}

// Search implements SearchProvider with
// {base}/search?q=…&format=json&categories=general.
func (s *SearXNG) Search(ctx context.Context, query string, n int) ([]Candidate, error) {
	q, err := searchQuery(query, n)
	if err != nil {
		return nil, err
	}
	u := *s.endpoint
	u.RawQuery = url.Values{"q": {q}, "format": {"json"}, "categories": {"general"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("recipes: build SearXNG request: %w", err)
	}
	var body struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := doSearch(s.client, req, "SearXNG", &body); err != nil {
		return nil, err
	}
	var out []Candidate
	for _, r := range body.Results {
		out = append(out, Candidate{URL: r.URL, Title: r.Title, Snippet: r.Content})
	}
	return candidates("SearXNG", out, n)
}

// Brave searches through the Brave Web Search API.
type Brave struct {
	key      string
	endpoint string
	client   *http.Client
}

// NewBrave returns a Brave provider that authenticates with key, the
// decrypted API key.
func NewBrave(key string) (*Brave, error) {
	if key == "" {
		return nil, errors.New("recipes: Brave API key is empty")
	}
	for i := 0; i < len(key); i++ {
		if key[i] < 0x21 || key[i] > 0x7e {
			// The key is never echoed.
			return nil, errors.New("recipes: Brave API key has a character not allowed in a header")
		}
	}
	return &Brave{key: key, endpoint: BraveEndpoint, client: newSearchClient()}, nil
}

// Search implements SearchProvider with GET BraveEndpoint?q=…&count=n.
func (b *Brave) Search(ctx context.Context, query string, n int) ([]Candidate, error) {
	q, err := searchQuery(query, n)
	if err != nil {
		return nil, err
	}
	u := b.endpoint + "?" + url.Values{"q": {q}, "count": {strconv.Itoa(n)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("recipes: build Brave request: %w", err)
	}
	req.Header.Set("X-Subscription-Token", b.key)
	var body struct {
		Web struct {
			Results []struct {
				URL         string `json:"url"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := doSearch(b.client, req, "Brave", &body); err != nil {
		return nil, err
	}
	var out []Candidate
	for _, r := range body.Web.Results {
		out = append(out, Candidate{URL: r.URL, Title: r.Title, Snippet: r.Description})
	}
	return candidates("Brave", out, n)
}

func newSearchClient() *http.Client {
	return &http.Client{
		Timeout: SearchTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// searchQuery validates n and returns query with " recipe" appended unless
// one of its words already is "recipe", in any case.
func searchQuery(query string, n int) (string, error) {
	if n < 1 || n > MaxSearchResults {
		return "", fmt.Errorf("recipes: search result count %d is outside 1-%d", n, MaxSearchResults)
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return "", errors.New("recipes: search query is empty")
	}
	for _, w := range strings.Fields(q) {
		if strings.EqualFold(w, "recipe") {
			return q, nil
		}
	}
	return q + " recipe", nil
}

// doSearch sends req and decodes a 2xx JSON body into v, mapping failures to
// the search errors.
func doSearch(client *http.Client, req *http.Request, provider string, v any) error {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := client.Do(req)
	if err != nil {
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("recipes: %s search: %w", provider, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %s HTTP %d", ErrSearchRefused, provider, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s", ErrSearchRateLimited, provider)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return &SearchStatusError{Provider: provider, Status: resp.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxSearchBodyBytes+1))
	if err != nil {
		return fmt.Errorf("recipes: %s search: read body: %w", provider, err)
	}
	if len(data) > MaxSearchBodyBytes {
		return fmt.Errorf("%w: %s body is over %d bytes", ErrSearchResponse, provider, MaxSearchBodyBytes)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrSearchResponse, provider, err)
	}
	return nil
}

// candidates cleans results — HTML tags and entities, which both providers
// leave in titles and snippets, are removed — checks every URL is absolute
// http(s) and keeps the first n.
func candidates(provider string, results []Candidate, n int) ([]Candidate, error) {
	if len(results) > n {
		results = results[:n]
	}
	out := make([]Candidate, 0, len(results))
	for i, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("%w: %s result %d has no absolute http(s) URL", ErrSearchResponse, provider, i)
		}
		out = append(out, Candidate{URL: r.URL, Title: stripTags(r.Title), Snippet: stripTags(r.Snippet)})
	}
	return out, nil
}

// stripTags drops HTML tags such as Brave's <strong> highlights, then
// decodes entities and trims space.
func stripTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>' && inTag:
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return cleanText(b.String())
}
