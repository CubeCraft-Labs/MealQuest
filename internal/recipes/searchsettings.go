package recipes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/secretbox"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const (
	// SearchTestQuery is what TestSearch searches for.
	SearchTestQuery = "chicken tikka masala"
	// MinBraveKeyLength is the shortest Brave API key accepted, so its last
	// four characters never give away most of it.
	MinBraveKeyLength = 8
)

// ErrSearchNotConfigured is returned when no recipe search provider has
// been chosen yet.
var ErrSearchNotConfigured = errors.New("recipes: recipe search is not configured")

// SearchSettings is the household's recipe search configuration as the API
// shows it. The Brave API key is never part of it: only whether one is
// stored and its last four characters.
type SearchSettings struct {
	// Provider is "searxng" or "brave", nil until first chosen.
	Provider *string `json:"provider"`
	// BaseURL is the saved SearXNG instance's URL, nil when not set.
	BaseURL *string `json:"base_url"`
	// DefaultBaseURL is the deployment's SearXNG URL
	// (MEALQUEST_SEARXNG_URL), for the client to suggest while BaseURL is
	// not set. It is never saved by itself: only a PATCH of base_url saves
	// a URL. Nil when the deployment names none.
	DefaultBaseURL *string `json:"default_base_url"`
	APIKeySet      bool    `json:"api_key_set"`
	APIKeyLast4    *string `json:"api_key_last4"`
	// UpdatedAt is nil until the settings are first saved.
	UpdatedAt *string `json:"updated_at"`
}

// SearchSettingsPatch changes the settings. A nil field is left as it is;
// an empty BaseURL or APIKey removes the stored one.
type SearchSettingsPatch struct {
	Provider *string `json:"provider"`
	BaseURL  *string `json:"base_url"`
	// APIKey is the Brave API key in plain text. It is stored encrypted
	// and never returned.
	APIKey *string `json:"api_key"`
}

// SearchSettingsProblem is one invalid field of a SearchSettingsPatch,
// named by its JSON key.
type SearchSettingsProblem struct {
	Field  string
	Reason string
}

// SearchSettingsError lists every problem with a SearchSettingsPatch, or
// with the settings it would leave.
type SearchSettingsError struct {
	Problems []SearchSettingsProblem
}

func (e *SearchSettingsError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Field + ": " + p.Reason
	}
	return "invalid recipe search settings: " + strings.Join(parts, "; ")
}

// SearchSettingsService reads and changes the recipe_search_settings row
// and builds the search provider it names. Brave API keys are sealed with
// its secret box.
type SearchSettingsService struct {
	st  *store.Store
	box *secretbox.Box
	// defaultBaseURL is SearchSettings.DefaultBaseURL, "" for none.
	defaultBaseURL string
	// newProvider is NewSearchProvider; tests point Brave elsewhere.
	newProvider func(db.RecipeSearchSetting, *secretbox.Box) (SearchProvider, error)
}

// NewSearchSettingsService returns the service over st, sealing keys with
// box. defaultBaseURL is the deployment's SearXNG URL, "" for none; a set
// one must be a valid SearXNG URL.
func NewSearchSettingsService(st *store.Store, box *secretbox.Box, defaultBaseURL string) (*SearchSettingsService, error) {
	if st == nil {
		return nil, errors.New("recipes: nil store")
	}
	if box == nil {
		return nil, errors.New("recipes: nil secret box")
	}
	if defaultBaseURL != "" {
		if _, err := NewSearXNG(defaultBaseURL); err != nil {
			return nil, fmt.Errorf("recipes: default SearXNG URL: %s", strings.TrimPrefix(err.Error(), "recipes: "))
		}
	}
	return &SearchSettingsService{st: st, box: box, defaultBaseURL: defaultBaseURL, newProvider: NewSearchProvider}, nil
}

// Get returns the settings; before any are saved every field but
// DefaultBaseURL is unset.
func (s *SearchSettingsService) Get(ctx context.Context) (SearchSettings, error) {
	row, err := db.New(s.st.Reader).GetRecipeSearchSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return SearchSettings{DefaultBaseURL: s.defaultBase()}, nil
	}
	if err != nil {
		return SearchSettings{}, fmt.Errorf("recipes: read recipe search settings: %w", err)
	}
	return s.settingsFrom(row), nil
}

func (s *SearchSettingsService) defaultBase() *string {
	if s.defaultBaseURL == "" {
		return nil
	}
	u := s.defaultBaseURL
	return &u
}

// Update applies patch and returns the settings it leaves. The first update
// must choose a provider. The result must be usable: SearXNG needs a base
// URL and Brave an API key. Every problem is reported in a
// *SearchSettingsError.
func (s *SearchSettingsService) Update(ctx context.Context, patch SearchSettingsPatch) (SearchSettings, error) {
	var saved db.RecipeSearchSetting
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		row, err := q.GetRecipeSearchSettings(ctx)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("recipes: read recipe search settings: %w", err)
		}
		next, err := s.apply(row, exists, patch)
		if err != nil {
			return err
		}
		now := store.Now().String()
		created := now
		if exists {
			created = row.CreatedAt
		}
		saved, err = q.UpsertRecipeSearchSettings(ctx, db.UpsertRecipeSearchSettingsParams{
			Provider:    next.Provider,
			BaseUrl:     next.BaseUrl,
			ApiKeyCt:    next.ApiKeyCt,
			ApiKeyLast4: next.ApiKeyLast4,
			CreatedAt:   created,
			UpdatedAt:   now,
		})
		if err != nil {
			return fmt.Errorf("recipes: save recipe search settings: %w", err)
		}
		return nil
	})
	if err != nil {
		return SearchSettings{}, err
	}
	return s.settingsFrom(saved), nil
}

// apply returns row with patch applied, validated.
func (s *SearchSettingsService) apply(row db.RecipeSearchSetting, exists bool, patch SearchSettingsPatch) (db.RecipeSearchSetting, error) {
	var problems []SearchSettingsProblem
	if patch.Provider != nil {
		switch *patch.Provider {
		case "searxng", "brave":
			row.Provider = *patch.Provider
		default:
			problems = append(problems, SearchSettingsProblem{"provider", `must be "searxng" or "brave"`})
		}
	} else if !exists {
		problems = append(problems, SearchSettingsProblem{"provider", "is required until recipe search is configured"})
	}
	if patch.BaseURL != nil {
		row.BaseUrl = sql.NullString{}
		if u := *patch.BaseURL; u != "" {
			if _, err := NewSearXNG(u); err != nil {
				problems = append(problems, SearchSettingsProblem{"base_url", strings.TrimPrefix(err.Error(), "recipes: ")})
			} else {
				row.BaseUrl = sql.NullString{String: u, Valid: true}
			}
		}
	}
	if patch.APIKey != nil {
		row.ApiKeyCt, row.ApiKeyLast4 = nil, sql.NullString{}
		if key := *patch.APIKey; key != "" {
			// The key is never echoed in a problem.
			if _, err := NewBrave(key); err != nil {
				problems = append(problems, SearchSettingsProblem{"api_key", "has a character not allowed in a header"})
			} else if len(key) < MinBraveKeyLength {
				problems = append(problems, SearchSettingsProblem{"api_key", fmt.Sprintf("must be at least %d characters", MinBraveKeyLength)})
			} else {
				ct, err := s.box.Encrypt([]byte(key))
				if err != nil {
					return db.RecipeSearchSetting{}, fmt.Errorf("recipes: seal Brave API key: %w", err)
				}
				row.ApiKeyCt = ct
				row.ApiKeyLast4 = sql.NullString{String: key[len(key)-4:], Valid: true}
			}
		}
	}
	if len(problems) == 0 {
		switch {
		case row.Provider == "searxng" && !row.BaseUrl.Valid:
			problems = append(problems, SearchSettingsProblem{"base_url", "is required for SearXNG"})
		case row.Provider == "brave" && len(row.ApiKeyCt) == 0:
			problems = append(problems, SearchSettingsProblem{"api_key", "is required for Brave"})
		}
	}
	if len(problems) > 0 {
		return db.RecipeSearchSetting{}, &SearchSettingsError{Problems: problems}
	}
	return row, nil
}

// Provider returns the search provider the settings name, or
// ErrSearchNotConfigured.
func (s *SearchSettingsService) Provider(ctx context.Context) (SearchProvider, error) {
	row, err := db.New(s.st.Reader).GetRecipeSearchSettings(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrSearchNotConfigured
	}
	if err != nil {
		return nil, fmt.Errorf("recipes: read recipe search settings: %w", err)
	}
	return s.newProvider(row, s.box)
}

// TestSearch searches for SearchTestQuery with the configured provider and
// returns how many candidates it found, up to MaxSearchResults.
func (s *SearchSettingsService) TestSearch(ctx context.Context) (int, error) {
	p, err := s.Provider(ctx)
	if err != nil {
		return 0, err
	}
	cands, err := p.Search(ctx, SearchTestQuery, MaxSearchResults)
	if err != nil {
		return 0, err
	}
	return len(cands), nil
}

func (s *SearchSettingsService) settingsFrom(row db.RecipeSearchSetting) SearchSettings {
	provider, updated := row.Provider, row.UpdatedAt
	return SearchSettings{
		Provider:       &provider,
		BaseURL:        nullString(row.BaseUrl),
		DefaultBaseURL: s.defaultBase(),
		APIKeySet:      len(row.ApiKeyCt) > 0,
		APIKeyLast4:    nullString(row.ApiKeyLast4),
		UpdatedAt:      &updated,
	}
}

func nullString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
