package recipes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// GroundConcurrency is how many items GroundMany grounds at once.
const GroundConcurrency = 3

var (
	// ErrNoCandidates is a reason a dish is ungroundable: the search found
	// no pages at all.
	ErrNoCandidates = errors.New("recipes: search found no candidates")
	// ErrTotalTimeNotPositive rejects a recipe whose page gives a total
	// time of zero minutes, which normalize_recipe refuses.
	ErrTotalTimeNotPositive = errors.New("recipes: recipe total time is not positive")
	// ErrSearchFailed wraps every failure to search: the search provider
	// could not be built (ErrSearchNotConfigured among them) or its search
	// failed.
	ErrSearchFailed = errors.New("recipes: recipe search failed")
	// ErrNormalizeFailed wraps a normalize_recipe failure that is not the
	// recipe's: the model provider could not be built or called.
	ErrNormalizeFailed = errors.New("recipes: normalize_recipe failed")
)

// ErrUngroundable is returned when no recipe page could ground a dish or a
// pasted URL. Reasons says why each page tried was rejected, or why there
// were none; errors.Is matches any of them.
type ErrUngroundable struct {
	// Target is the dish name or the URL.
	Target  string
	Reasons []error
}

func (e *ErrUngroundable) Error() string {
	parts := make([]string, len(e.Reasons))
	for i, r := range e.Reasons {
		parts[i] = r.Error()
	}
	return fmt.Sprintf("recipes: cannot ground %q: %s", e.Target, strings.Join(parts, "; "))
}

func (e *ErrUngroundable) Unwrap() []error { return e.Reasons }

// Grounded is a recipe stored for a dish: the recipe and its cached photo,
// with normalize_recipe's method, minutes and difficulty.
type Grounded struct {
	RecipeID string `json:"recipe_id"`
	PhotoID  string `json:"photo_id"`
	// Method is stovetop, oven, crockpot, grill or other.
	Method string `json:"method"`
	// MinutesTotal is prep to table, nil when unknown.
	MinutesTotal *int `json:"minutes_total"`
	// Difficulty is easy, medium or hard.
	Difficulty string `json:"difficulty"`
	SourceURL  string `json:"source_url"`
	SiteName   string `json:"site_name"`
}

// SearchSource returns the household's current search provider;
// *SearchSettingsService satisfies it.
type SearchSource interface {
	Provider(ctx context.Context) (SearchProvider, error)
}

// Normalizer runs normalize_recipe on a recipe; scope is its source URL. In
// production it is skills.NormalizeRecipe with the household's
// harness.Config.
type Normalizer func(ctx context.Context, scope string, in skills.RecipeText) (skills.NormalizedRecipe, error)

// Grounder grounds dishes in recipe pages: it searches, fetches, extracts
// and ranks candidates, caches the best one's photo, normalizes its
// ingredients and stores it all. It is safe for concurrent use.
type Grounder struct {
	st        *store.Store
	search    SearchSource
	pages     PageFetcher
	photos    *PhotoCache
	normalize Normalizer
}

// NewGrounder returns a Grounder storing recipes in st, searching with
// search, fetching pages with pages, caching photos with photos and
// normalizing with normalize.
func NewGrounder(st *store.Store, search SearchSource, pages PageFetcher, photos *PhotoCache, normalize Normalizer) (*Grounder, error) {
	switch {
	case st == nil:
		return nil, errors.New("recipes: nil store")
	case search == nil:
		return nil, errors.New("recipes: nil search source")
	case pages == nil:
		return nil, errors.New("recipes: nil page fetcher")
	case photos == nil:
		return nil, errors.New("recipes: nil photo cache")
	case normalize == nil:
		return nil, errors.New("recipes: nil normalizer")
	}
	return &Grounder{st: st, search: search, pages: pages, photos: photos, normalize: normalize}, nil
}

// Ground searches for query and grounds dishName in the best recipe found.
// The candidates are ranked as Rank does, except that a candidate already
// stored is scored on its stored recipe and never fetched. They are tried
// best first: a stored candidate is returned as it is, and otherwise the
// first whose photo can be cached and whose ingredients normalize is stored
// — the recipe, its ingredients, steps and photo in one transaction — and
// returned. A stored recipe is reused only when it ranks above every
// candidate that grounds.
//
// When no candidate grounds the dish the error is an *ErrUngroundable with
// each reason. A failure that is not the candidates' — the search, the
// model provider, the database — is returned as it is.
func (g *Grounder) Ground(ctx context.Context, dishName, query string) (Grounded, error) {
	if strings.TrimSpace(dishName) == "" {
		return Grounded{}, errors.New("recipes: ground: dish name is empty")
	}
	found, err := g.searchRanked(ctx, dishName, query)
	if err != nil {
		return Grounded{}, fmt.Errorf("recipes: ground %q: %w", dishName, err)
	}
	var reasons []error
	if found.rankErr != nil {
		reasons = append(reasons, found.rankErr)
	}
	for _, r := range found.ranked {
		if sr, ok := found.stored[r.Candidate.URL]; ok {
			return sr.grounded, nil
		}
		gr, reason, err := g.ground(ctx, r.Candidate.URL, r.Recipe)
		if err != nil {
			return Grounded{}, fmt.Errorf("recipes: ground %q: %w", dishName, err)
		}
		if reason == nil {
			return gr, nil
		}
		reasons = append(reasons, fmt.Errorf("%s: %w", r.Candidate.URL, reason))
	}
	return Grounded{}, &ErrUngroundable{Target: dishName, Reasons: reasons}
}

// rankedSearch is a search's candidates ranked: the ranked ones best first,
// those already stored by URL, and why any were not ranked.
type rankedSearch struct {
	ranked []Ranked
	stored map[string]storedRecipe
	// rankErr is Rank's error, or ErrNoCandidates when the search found
	// nothing.
	rankErr error
}

// searchRanked searches for query and ranks the top RankDepth candidates
// for dishName as Ground describes. A search, database or context failure
// is its error; candidates that do not rank are only rankErr's.
func (g *Grounder) searchRanked(ctx context.Context, dishName, query string) (rankedSearch, error) {
	provider, err := g.search.Provider(ctx)
	if err != nil {
		return rankedSearch{}, fmt.Errorf("%w: %w", ErrSearchFailed, err)
	}
	cands, err := provider.Search(ctx, query, RankDepth)
	if err != nil {
		return rankedSearch{}, fmt.Errorf("%w: %w", ErrSearchFailed, err)
	}
	if len(cands) == 0 {
		return rankedSearch{rankErr: ErrNoCandidates}, nil
	}
	cands = cands[:min(len(cands), RankDepth)]
	stored := make(map[string]storedRecipe)
	for _, c := range cands {
		sr, ok, err := g.storedRecipe(ctx, c.URL)
		if err != nil {
			return rankedSearch{}, err
		}
		if ok {
			stored[c.URL] = sr
		}
	}
	ranked, rankErr := rank(ctx, dishName, cands, func(ctx context.Context, c Candidate) (Recipe, error) {
		if sr, ok := stored[c.URL]; ok {
			return sr.recipe, nil
		}
		return fetchRecipe(ctx, g.pages, c.URL)
	})
	if err := ctx.Err(); err != nil {
		return rankedSearch{}, err
	}
	return rankedSearch{ranked: ranked, stored: stored, rankErr: rankErr}, nil
}

// Offer is a ranked candidate offered for the household to ground a dish
// in, with GroundURL on its URL.
type Offer struct {
	URL      string
	Title    string
	SiteName string
	// PhotoID is the cached photo of a candidate already stored, "" for
	// one that is not: a candidate's photo is cached only when it grounds.
	PhotoID string
	// Minutes is a stored candidate's normalized minutes_total, else its
	// page's total time; nil when unknown.
	Minutes *int
}

// Candidates searches for query and returns up to n of the candidates
// Ground would try for dishName, best first. It fetches and extracts pages
// but downloads no photo and runs no model. Candidates that hold no usable
// recipe are not offered, so the list may be empty; a search that fails is
// an error wrapping ErrSearchFailed.
func (g *Grounder) Candidates(ctx context.Context, dishName, query string, n int) ([]Offer, error) {
	if strings.TrimSpace(dishName) == "" {
		return nil, errors.New("recipes: candidates: dish name is empty")
	}
	if n < 1 {
		return nil, fmt.Errorf("recipes: candidates: n is %d, want at least 1", n)
	}
	found, err := g.searchRanked(ctx, dishName, query)
	if err != nil {
		return nil, fmt.Errorf("recipes: candidates for %q: %w", dishName, err)
	}
	offers := make([]Offer, 0, min(n, len(found.ranked)))
	for _, r := range found.ranked[:min(n, len(found.ranked))] {
		o := Offer{URL: r.Candidate.URL, Title: r.Recipe.Title, SiteName: r.Recipe.SiteName, Minutes: r.Recipe.TotalMinutes}
		if sr, ok := found.stored[r.Candidate.URL]; ok {
			o.PhotoID = sr.grounded.PhotoID
			o.Minutes = sr.grounded.MinutesTotal
		}
		offers = append(offers, o)
	}
	return offers, nil
}

// GroundURL grounds the recipe on the page at rawURL, a pasted URL, as
// Ground does a candidate. An already stored rawURL is returned without
// fetching it. A page that cannot be fetched, holds no usable recipe, or
// whose photo or ingredients fail is an *ErrUngroundable.
func (g *Grounder) GroundURL(ctx context.Context, rawURL string) (Grounded, error) {
	if gr, ok, err := g.stored(ctx, rawURL); err != nil || ok {
		return gr, err
	}
	ungroundable := func(err error) (Grounded, error) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Grounded{}, ctxErr
		}
		return Grounded{}, &ErrUngroundable{Target: rawURL, Reasons: []error{err}}
	}
	page, err := g.pages.Fetch(ctx, rawURL)
	if err != nil {
		return ungroundable(err)
	}
	r, err := Extract(page.Body, page.URL)
	if err != nil {
		return ungroundable(err)
	}
	if err := usable(r); err != nil {
		return ungroundable(err)
	}
	gr, reason, err := g.ground(ctx, rawURL, r)
	if err != nil {
		return Grounded{}, fmt.Errorf("recipes: ground pasted link: %w", err)
	}
	if reason != nil {
		return ungroundable(reason)
	}
	return gr, nil
}

// GroundRequest is one dish for GroundMany.
type GroundRequest struct {
	DishName string
	Query    string
}

// GroundResult is one GroundMany outcome: Grounded, or Err when the item
// failed.
type GroundResult struct {
	Grounded Grounded
	Err      error
}

// GroundMany grounds each request with Ground, GroundConcurrency at a time,
// and returns their results in request order. One item's failure does not
// affect the others.
func (g *Grounder) GroundMany(ctx context.Context, reqs []GroundRequest) []GroundResult {
	results := make([]GroundResult, len(reqs))
	slots := make(chan struct{}, GroundConcurrency)
	var wg sync.WaitGroup
	for i, req := range reqs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				results[i] = GroundResult{Err: ctx.Err()}
				return
			}
			defer func() { <-slots }()
			gr, err := g.Ground(ctx, req.DishName, req.Query)
			results[i] = GroundResult{Grounded: gr, Err: err}
		}()
	}
	wg.Wait()
	return results
}

// stored returns the grounding of the recipe stored for sourceURL, and
// false when there is none.
func (g *Grounder) stored(ctx context.Context, sourceURL string) (Grounded, bool, error) {
	row, ok, err := g.storedRow(ctx, sourceURL)
	if err != nil || !ok {
		return Grounded{}, false, err
	}
	return groundedFrom(row), true, nil
}

// storedRow returns the recipe row stored for sourceURL, and false when
// there is none.
func (g *Grounder) storedRow(ctx context.Context, sourceURL string) (db.Recipe, bool, error) {
	row, err := db.New(g.st.Reader).GetRecipeBySourceURL(ctx, sourceURL)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Recipe{}, false, nil
	}
	if err != nil {
		return db.Recipe{}, false, fmt.Errorf("recipes: look up recipe %s: %w", sourceURL, err)
	}
	if !row.PhotoID.Valid || !row.Method.Valid || !row.Difficulty.Valid {
		return db.Recipe{}, false, fmt.Errorf("recipes: stored recipe %s for %s has no photo, method or difficulty", row.ID, sourceURL)
	}
	return row, true, nil
}

// storedRecipe is a recipe already stored: its grounding, and the recipe
// rebuilt from what was stored so it can be ranked without its page.
type storedRecipe struct {
	grounded Grounded
	recipe   Recipe
}

// storedRecipe returns the recipe stored for sourceURL with its photo,
// ingredients and steps, and false when there is none.
func (g *Grounder) storedRecipe(ctx context.Context, sourceURL string) (storedRecipe, bool, error) {
	row, ok, err := g.storedRow(ctx, sourceURL)
	if err != nil || !ok {
		return storedRecipe{}, false, err
	}
	gr := groundedFrom(row)
	q := db.New(g.st.Reader)
	photo, err := q.GetPhoto(ctx, gr.PhotoID)
	if err != nil {
		return storedRecipe{}, false, fmt.Errorf("recipes: load photo %s of recipe %s: %w", gr.PhotoID, gr.RecipeID, err)
	}
	ings, err := q.ListRecipeIngredients(ctx, gr.RecipeID)
	if err != nil {
		return storedRecipe{}, false, fmt.Errorf("recipes: load ingredients of recipe %s: %w", gr.RecipeID, err)
	}
	steps, err := q.ListRecipeSteps(ctx, gr.RecipeID)
	if err != nil {
		return storedRecipe{}, false, fmt.Errorf("recipes: load steps of recipe %s: %w", gr.RecipeID, err)
	}
	r := Recipe{
		Title:        row.Title,
		SiteName:     row.SiteName.String,
		ImageURL:     photo.SourceUrl,
		PrepMinutes:  intFrom(row.PrepMinutes),
		CookMinutes:  intFrom(row.CookMinutes),
		TotalMinutes: intFrom(row.TotalMinutes),
		Yield:        row.Yield.String,
		JSONLD:       json.RawMessage(row.RawJsonld),
	}
	for _, ing := range ings {
		r.Ingredients = append(r.Ingredients, ing.RawText)
	}
	for _, st := range steps {
		r.Steps = append(r.Steps, st.Text)
	}
	return storedRecipe{grounded: gr, recipe: r}, true, nil
}

// ground downloads r's photo, normalizes it and stores it as sourceURL's
// recipe. A problem with this recipe is returned as reason, so the caller
// can try another; err is a failure that is not the recipe's.
//
// The order is fixed: the photo is downloaded and validated first, so a
// recipe without a usable photo never costs a normalize_recipe call; then
// normalize_recipe runs; then save records the photo (CachePhoto) and the
// recipe in one transaction.
func (g *Grounder) ground(ctx context.Context, sourceURL string, r Recipe) (gr Grounded, reason, err error) {
	photo, err := g.photos.DownloadPhoto(ctx, r.ImageURL)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Grounded{}, nil, ctxErr
		}
		return Grounded{}, fmt.Errorf("photo: %w", err), nil
	}
	norm, err := g.normalize(ctx, sourceURL, skills.RecipeText{
		Title:        r.Title,
		Ingredients:  r.Ingredients,
		Steps:        r.Steps,
		PrepMinutes:  r.PrepMinutes,
		CookMinutes:  r.CookMinutes,
		TotalMinutes: r.TotalMinutes,
	})
	if errors.Is(err, llm.ErrBadOutput) {
		return Grounded{}, fmt.Errorf("normalize: %w", err), nil
	}
	if err != nil {
		return Grounded{}, nil, fmt.Errorf("%w: %w", ErrNormalizeFailed, err)
	}
	if len(norm.Ingredients) != len(r.Ingredients) {
		return Grounded{}, nil, fmt.Errorf("normalize: %d ingredients for %d lines", len(norm.Ingredients), len(r.Ingredients))
	}
	gr, err = g.save(ctx, sourceURL, r, photo, norm)
	return gr, nil, err
}

// save stores the recipe, its ingredients and steps and its photo in one
// transaction. A photo's files written for a transaction that does not
// commit are removed.
func (g *Grounder) save(ctx context.Context, sourceURL string, r Recipe, p DownloadedPhoto, norm skills.NormalizedRecipe) (Grounded, error) {
	var (
		row     db.Recipe
		photo   db.Photo
		created bool
	)
	err := g.st.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		photo, created, err = g.photos.CachePhoto(ctx, tx, p)
		if err != nil {
			return err
		}
		q := db.New(tx)
		now := store.Now().String()
		row, err = q.UpsertRecipe(ctx, db.UpsertRecipeParams{
			ID:           store.NewID(),
			SourceUrl:    sourceURL,
			SiteName:     sql.NullString{String: r.SiteName, Valid: r.SiteName != ""},
			Title:        r.Title,
			TotalMinutes: nullInt(r.TotalMinutes),
			PrepMinutes:  nullInt(r.PrepMinutes),
			CookMinutes:  nullInt(r.CookMinutes),
			Yield:        sql.NullString{String: r.Yield, Valid: r.Yield != ""},
			RawJsonld:    string(r.JSONLD),
			PhotoID:      sql.NullString{String: photo.ID, Valid: true},
			Method:       sql.NullString{String: norm.Method, Valid: true},
			Difficulty:   sql.NullString{String: norm.Difficulty, Valid: true},
			MinutesTotal: nullInt(norm.MinutesTotal),
			FetchedAt:    now,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
		if err != nil {
			return fmt.Errorf("recipes: store recipe: %w", err)
		}
		if err := q.DeleteRecipeIngredients(ctx, row.ID); err != nil {
			return fmt.Errorf("recipes: replace ingredients: %w", err)
		}
		if err := q.DeleteRecipeSteps(ctx, row.ID); err != nil {
			return fmt.Errorf("recipes: replace steps: %w", err)
		}
		for i, line := range r.Ingredients {
			ing := norm.Ingredients[i]
			err := q.CreateRecipeIngredient(ctx, db.CreateRecipeIngredientParams{
				ID:        store.NewID(),
				RecipeID:  row.ID,
				Position:  int64(i),
				RawText:   line,
				Name:      sql.NullString{String: ing.Name, Valid: true},
				Qty:       sql.NullString{String: ing.Qty, Valid: ing.Qty != ""},
				Unit:      sql.NullString{String: ing.Unit, Valid: ing.Unit != ""},
				Note:      sql.NullString{String: ing.Note, Valid: ing.Note != ""},
				CreatedAt: now,
				UpdatedAt: now,
			})
			if err != nil {
				return fmt.Errorf("recipes: store ingredient %d: %w", i+1, err)
			}
		}
		for i, text := range r.Steps {
			err := q.CreateRecipeStep(ctx, db.CreateRecipeStepParams{
				ID:        store.NewID(),
				RecipeID:  row.ID,
				Position:  int64(i),
				Text:      text,
				CreatedAt: now,
				UpdatedAt: now,
			})
			if err != nil {
				return fmt.Errorf("recipes: store step %d: %w", i+1, err)
			}
		}
		return nil
	})
	if err != nil {
		if created {
			err = errors.Join(err, g.photos.RemovePhotoFiles(photo.ID))
		}
		return Grounded{}, err
	}
	return groundedFrom(row), nil
}

func groundedFrom(row db.Recipe) Grounded {
	gr := Grounded{
		RecipeID:   row.ID,
		PhotoID:    row.PhotoID.String,
		Method:     row.Method.String,
		Difficulty: row.Difficulty.String,
		SourceURL:  row.SourceUrl,
		SiteName:   row.SiteName.String,
	}
	if row.MinutesTotal.Valid {
		m := int(row.MinutesTotal.Int64)
		gr.MinutesTotal = &m
	}
	return gr
}

func intFrom(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

func nullInt(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}
