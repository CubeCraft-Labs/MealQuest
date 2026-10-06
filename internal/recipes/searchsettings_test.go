package recipes

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const testBraveKey = "BSA-secret-brave-key-9f3c"

func newTestSearchSettings(t *testing.T) *SearchSettingsService {
	t.Helper()
	return newTestSearchSettingsWithDefault(t, "")
}

// newTestSearchSettingsWithDefault is newTestSearchSettings with
// defaultBaseURL as the deployment's SearXNG URL.
func newTestSearchSettingsWithDefault(t *testing.T, defaultBaseURL string) *SearchSettingsService {
	t.Helper()
	st, err := store.Open(t.Context(), t.TempDir())
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
	key := make([]byte, secretbox.KeySize)
	rand.Read(key)
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewSearchSettingsService(st, box, defaultBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func ptr(s string) *string { return &s }

// noKey fails the test when v, marshalled as the API would, holds the
// Brave key.
func noKey(t *testing.T, what string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), testBraveKey) || strings.Contains(string(b), testBraveKey[:12]) {
		t.Errorf("%s holds the Brave key: %s", what, b)
	}
}

func TestSearchSettingsUpdate(t *testing.T) {
	t.Parallel()
	svc := newTestSearchSettings(t)
	got, err := svc.Get(t.Context())
	if err != nil || got != (SearchSettings{}) {
		t.Fatalf("Get before any update = %+v, %v; want every field unset", got, err)
	}
	if _, err := svc.Provider(t.Context()); !errors.Is(err, ErrSearchNotConfigured) {
		t.Errorf("Provider before any update error = %v, want ErrSearchNotConfigured", err)
	}

	for name, patch := range map[string]SearchSettingsPatch{
		"no provider":         {BaseURL: ptr("http://searxng:8080")},
		"unknown provider":    {Provider: ptr("google")},
		"searxng without URL": {Provider: ptr("searxng")},
		"bad URL":             {Provider: ptr("searxng"), BaseURL: ptr("ftp://searxng")},
		"brave without key":   {Provider: ptr("brave")},
		"short key":           {Provider: ptr("brave"), APIKey: ptr("abc12")},
		"key with space":      {Provider: ptr("brave"), APIKey: ptr("abc 12345678")},
	} {
		var invalid *SearchSettingsError
		if _, err := svc.Update(t.Context(), patch); !errors.As(err, &invalid) {
			t.Errorf("%s: error = %v, want *SearchSettingsError", name, err)
		}
	}

	got, err = svc.Update(t.Context(), SearchSettingsPatch{Provider: ptr("searxng"), BaseURL: ptr("http://searxng:8080")})
	if err != nil {
		t.Fatal(err)
	}
	if *got.Provider != "searxng" || *got.BaseURL != "http://searxng:8080" || got.APIKeySet || got.APIKeyLast4 != nil || got.UpdatedAt == nil {
		t.Errorf("SearXNG settings = %+v", got)
	}

	got, err = svc.Update(t.Context(), SearchSettingsPatch{Provider: ptr("brave"), APIKey: ptr(testBraveKey)})
	if err != nil {
		t.Fatal(err)
	}
	if *got.Provider != "brave" || !got.APIKeySet || *got.APIKeyLast4 != "9f3c" || *got.BaseURL != "http://searxng:8080" {
		t.Errorf("Brave settings = %+v", got)
	}
	noKey(t, "Update result", got)
	got, err = svc.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	noKey(t, "Get result", got)
	row, err := db.New(svc.st.Reader).GetRecipeSearchSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(row.ApiKeyCt), testBraveKey) {
		t.Error("Brave key stored in plain text")
	}

	if _, err := svc.Update(t.Context(), SearchSettingsPatch{APIKey: ptr("")}); err == nil {
		t.Error("removing the key while Brave is chosen succeeded")
	}
	got, err = svc.Update(t.Context(), SearchSettingsPatch{Provider: ptr("searxng"), APIKey: ptr("")})
	if err != nil || got.APIKeySet || got.APIKeyLast4 != nil {
		t.Errorf("switch to SearXNG removing the key = %+v, %v", got, err)
	}
}

func TestSearchSettingsDefaultBaseURL(t *testing.T) {
	t.Parallel()
	svc := newTestSearchSettingsWithDefault(t, "http://searxng:8080")
	got, err := svc.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultBaseURL == nil || *got.DefaultBaseURL != "http://searxng:8080" || got.BaseURL != nil || got.Provider != nil {
		t.Errorf("Get before any update = %+v, want only default_base_url set", got)
	}
	if _, err := svc.Provider(t.Context()); !errors.Is(err, ErrSearchNotConfigured) {
		t.Errorf("Provider with only a default error = %v, want ErrSearchNotConfigured", err)
	}

	// The default is a suggestion: SearXNG still needs a saved URL.
	if _, err := svc.Update(t.Context(), SearchSettingsPatch{Provider: ptr("searxng")}); err == nil {
		t.Error("choosing SearXNG without a URL succeeded with a default set")
	}

	got, err = svc.Update(t.Context(), SearchSettingsPatch{Provider: ptr("searxng"), BaseURL: ptr("https://search.example")})
	if err != nil {
		t.Fatal(err)
	}
	if *got.BaseURL != "https://search.example" || got.DefaultBaseURL == nil || *got.DefaultBaseURL != "http://searxng:8080" {
		t.Errorf("Update result = %+v, want the saved URL beside the default", got)
	}
	got, err = svc.Get(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if *got.BaseURL != "https://search.example" {
		t.Errorf("Get base_url = %q, want the saved URL, not the default", *got.BaseURL)
	}
	row, err := db.New(svc.st.Reader).GetRecipeSearchSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if row.BaseUrl.String != "https://search.example" {
		t.Errorf("stored base_url = %q", row.BaseUrl.String)
	}
}

func TestNewSearchSettingsServiceRejectsBadDefault(t *testing.T) {
	t.Parallel()
	st, err := store.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	key := make([]byte, secretbox.KeySize)
	rand.Read(key)
	box, err := secretbox.New(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"searxng:8080", "ftp://searxng", "http://user:pw@searxng:8080"} {
		if _, err := NewSearchSettingsService(st, box, u); err == nil {
			t.Errorf("NewSearchSettingsService(%q) succeeded", u)
		} else if strings.Contains(err.Error(), "pw") {
			t.Errorf("error for %q echoes the URL: %v", u, err)
		}
	}
}

func TestSearchSettingsTestSearch(t *testing.T) {
	t.Parallel()
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != SearchTestQuery+" recipe" {
			t.Errorf("SearXNG query = %q", r.URL.Query().Get("q"))
		}
		w.Write([]byte(`{"results":[{"url":"https://a.example/1"},{"url":"https://b.example/2"}]}`))
	}))
	t.Cleanup(searx.Close)
	svc := newTestSearchSettings(t)
	if _, err := svc.TestSearch(t.Context()); !errors.Is(err, ErrSearchNotConfigured) {
		t.Errorf("TestSearch unconfigured error = %v, want ErrSearchNotConfigured", err)
	}
	if _, err := svc.Update(t.Context(), SearchSettingsPatch{Provider: ptr("searxng"), BaseURL: ptr(searx.URL)}); err != nil {
		t.Fatal(err)
	}
	if n, err := svc.TestSearch(t.Context()); err != nil || n != 2 {
		t.Errorf("TestSearch = %d, %v; want 2", n, err)
	}

	// Brave refuses the key; the error must not carry it.
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != testBraveKey {
			t.Errorf("Brave token = %q", r.Header.Get("X-Subscription-Token"))
		}
		http.Error(w, "bad key "+testBraveKey, http.StatusUnauthorized)
	}))
	t.Cleanup(brave.Close)
	svc.newProvider = func(s db.RecipeSearchSetting, box *secretbox.Box) (SearchProvider, error) {
		p, err := NewSearchProvider(s, box)
		if b, ok := p.(*Brave); ok {
			b.endpoint = brave.URL
		}
		return p, err
	}
	if _, err := svc.Update(t.Context(), SearchSettingsPatch{Provider: ptr("brave"), APIKey: ptr(testBraveKey)}); err != nil {
		t.Fatal(err)
	}
	_, err := svc.TestSearch(t.Context())
	if !errors.Is(err, ErrSearchRefused) {
		t.Fatalf("TestSearch error = %v, want ErrSearchRefused", err)
	}
	if strings.Contains(err.Error(), testBraveKey) {
		t.Errorf("TestSearch error holds the Brave key: %v", err)
	}
}
