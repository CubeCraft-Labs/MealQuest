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
	// ErrNoUsableRecipe is returned when Rank rejects every candidate; it
	// wraps each candidate's reason.
	ErrNoUsableRecipe = errors.New("recipes: no candidate holds a usable recipe")
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
// returns those whose recipe has ingredients and an image, highest Score
// first; equal scores keep search order. When every candidate is rejected
// the error is ErrNoUsableRecipe joined with each rejection's reason.
func Rank(ctx context.Context, f PageFetcher, dish string, cands []Candidate) ([]Ranked, error) {
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
			r, err := rankOne(ctx, f, dish, c)
			outcomes[i] = outcome{r, err}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var ranked []Ranked
	reasons := []error{ErrNoUsableRecipe}
	for i, o := range outcomes {
		if o.err != nil {
			reasons = append(reasons, fmt.Errorf("%s: %w", cands[i].URL, o.err))
			continue
		}
		ranked = append(ranked, o.ranked)
	}
	if len(ranked) == 0 {
		return nil, errors.Join(reasons...)
	}
	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].Score > ranked[b].Score })
	return ranked, nil
}

// rankOne fetches, extracts and scores one candidate, or says why it is
// rejected.
func rankOne(ctx context.Context, f PageFetcher, dish string, c Candidate) (Ranked, error) {
	page, err := f.Fetch(ctx, c.URL)
	if err != nil {
		return Ranked{}, err
	}
	r, err := Extract(page.Body, page.URL)
	if err != nil {
		return Ranked{}, err
	}
	if len(r.Ingredients) == 0 {
		return Ranked{}, ErrNoIngredients
	}
	if r.ImageURL == "" {
		return Ranked{}, ErrNoImage
	}
	return Ranked{Candidate: c, Recipe: r, Score: harness.Similarity(r.Title, dish) + completeness(r)}, nil
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
