package recipes

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
)

// RankDepth is how many of the top search candidates Rank fetches and
// extracts.
const RankDepth = 5

var (
	// ErrNoIngredients rejects a candidate whose recipe lists no
	// ingredients.
	ErrNoIngredients = errors.New("recipes: recipe has no ingredients")
	// ErrNoImage rejects a candidate whose recipe has no image.
	ErrNoImage = errors.New("recipes: recipe has no image")
	// ErrNoUsableRecipe is returned when Rank ranks no candidate; it is
	// joined with each candidate's reason.
	ErrNoUsableRecipe = errors.New("recipes: no candidate holds a usable recipe")
	// ErrCandidatesUnranked is returned, alongside the ranked candidates,
	// when Rank ranks some candidates but not all; it is joined with each
	// unranked candidate's reason.
	ErrCandidatesUnranked = errors.New("recipes: some candidates were not ranked")
)

// PageFetcher fetches a candidate's page; *Fetcher satisfies it.
type PageFetcher interface {
	Fetch(ctx context.Context, rawURL string) (Page, error)
}

// Ranked is a candidate whose page held a usable recipe, with its score.
type Ranked struct {
	Candidate Candidate
	Recipe    Recipe
	// Score is the token similarity of the recipe's title to the dish name
	// (0 to 1, harness.Similarity) plus a quarter for each of ingredients,
	// image, steps and total minutes present (0 to 1).
	Score float64
}

// Rank fetches and extracts the first RankDepth candidates concurrently and
// returns those whose recipe is usable — it has ingredients and an image,
// and any total time it gives is positive — highest Score first; equal
// scores keep search order.
//
// Every candidate that is not ranked — its fetch failed, its page holds no
// valid recipe, or the recipe is not usable — is reported, never dropped
// silently: when some are ranked the error is
// ErrCandidatesUnranked, returned together with the ranked ones; when none
// are it is ErrNoUsableRecipe with no results. Either is joined with each
// unranked candidate's URL and reason. A cancelled ctx returns only its
// error.
func Rank(ctx context.Context, f PageFetcher, dish string, cands []Candidate) ([]Ranked, error) {
	return rank(ctx, dish, cands, func(ctx context.Context, c Candidate) (Recipe, error) {
		return fetchRecipe(ctx, f, c.URL)
	})
}

// rank is Rank with each candidate's recipe got by load rather than always
// fetched.
func rank(ctx context.Context, dish string, cands []Candidate, load func(context.Context, Candidate) (Recipe, error)) ([]Ranked, error) {
	if len(cands) > RankDepth {
		cands = cands[:RankDepth]
	}
	type outcome struct {
		ranked Ranked
		err    error
	}
	outcomes := make([]outcome, len(cands))
	var wg sync.WaitGroup
	for i, c := range cands {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := rankOne(ctx, load, dish, c)
			outcomes[i] = outcome{r, err}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var ranked []Ranked
	var reasons []error
	for i, o := range outcomes {
		if o.err != nil {
			reasons = append(reasons, fmt.Errorf("%s: %w", cands[i].URL, o.err))
			continue
		}
		ranked = append(ranked, o.ranked)
	}
	if len(ranked) == 0 {
		return nil, errors.Join(append([]error{ErrNoUsableRecipe}, reasons...)...)
	}
	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].Score > ranked[b].Score })
	if len(reasons) > 0 {
		return ranked, errors.Join(append([]error{ErrCandidatesUnranked}, reasons...)...)
	}
	return ranked, nil
}

// rankOne loads and scores one candidate's recipe, or says why it is
// rejected.
func rankOne(ctx context.Context, load func(context.Context, Candidate) (Recipe, error), dish string, c Candidate) (Ranked, error) {
	r, err := load(ctx, c)
	if err != nil {
		return Ranked{}, err
	}
	if err := usable(r); err != nil {
		return Ranked{}, err
	}
	return Ranked{Candidate: c, Recipe: r, Score: harness.Similarity(r.Title, dish) + completeness(r)}, nil
}

// fetchRecipe fetches the page at rawURL and extracts its recipe.
func fetchRecipe(ctx context.Context, f PageFetcher, rawURL string) (Recipe, error) {
	page, err := f.Fetch(ctx, rawURL)
	if err != nil {
		return Recipe{}, err
	}
	return Extract(page.Body, page.URL)
}

// usable says why r cannot ground a dish: it has no ingredients or no
// image, or a total time that is not positive.
func usable(r Recipe) error {
	switch {
	case len(r.Ingredients) == 0:
		return ErrNoIngredients
	case r.ImageURL == "":
		return ErrNoImage
	case r.TotalMinutes != nil && *r.TotalMinutes <= 0:
		return ErrTotalTimeNotPositive
	}
	return nil
}

// completeness is a quarter for each of ingredients, image, steps and total
// minutes the recipe has.
func completeness(r Recipe) float64 {
	score := 0.0
	for _, present := range []bool{len(r.Ingredients) > 0, r.ImageURL != "", len(r.Steps) > 0, r.TotalMinutes != nil} {
		if present {
			score += 0.25
		}
	}
	return score
}
