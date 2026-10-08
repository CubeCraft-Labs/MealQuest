package dinners

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
)

// Detail is a dinner with its recipe, as the recipe detail screen and
// Kitchen reading mode show it.
type Detail struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	Origin       string  `json:"origin"`
	Method       *string `json:"method"`
	MinutesTotal *int64  `json:"minutes_total"`
	Difficulty   *string `json:"difficulty"`
	Cuisine      *string `json:"cuisine"`
	IsFavorite   bool    `json:"is_favorite"`
	// PhotoURLs is the cached photo at each stored width, keyed by width
	// ("400", "1200"); null when the dinner has no photo.
	PhotoURLs map[string]string `json:"photo_urls"`
	// Ingredients and Steps are the recipe's, in recipe order; empty when
	// the dinner has no recipe.
	Ingredients []Ingredient `json:"ingredients"`
	Steps       []string     `json:"steps"`
	// Source is the site of the recipe page the dinner is grounded in;
	// null for a recipe entered by hand or a dinner with no recipe.
	Source          *Source `json:"source"`
	GroundingStatus string  `json:"grounding_status"`
	// GroundingError is why grounding failed, one of the Reason codes;
	// null unless GroundingStatus is failed.
	GroundingError *string `json:"grounding_error"`
	// WhyPicked is the reason of the suggestion that created the dinner
	// (see GetDinnerSuggestionReasons); null for a dinner no suggestion
	// created.
	WhyPicked *string `json:"why_picked"`
}

// Ingredient is one recipe line. RawText is the line as the recipe gives
// it; Name, Qty, Unit and Note are normalize_recipe's reading of it, each
// null when it has none, and Name, Qty and Unit always null for a recipe
// entered by hand (see IsManualRecipe).
type Ingredient struct {
	Name    *string `json:"name"`
	Qty     *string `json:"qty"`
	Unit    *string `json:"unit"`
	Note    *string `json:"note"`
	RawText string  `json:"raw_text"`
}

// Source is the site a recipe was fetched from. It has no url: the page's
// link, pasted or offered as a candidate, may carry a personal token, and
// no stored link can be shown to be free of one, so like a dinner's
// recipe_url and a candidate's page it never leaves the server. SiteName
// is null when the page names no site.
type Source struct {
	SiteName *string `json:"site_name"`
}

// Get returns dinner id with its recipe and the reason it was suggested,
// read in one read transaction. An unknown or archived dinner is
// ErrNotFound: an archived dinner has no detail to show.
func (s *Service) Get(ctx context.Context, id string) (d Detail, err error) {
	tx, err := s.st.Reader.BeginTx(ctx, nil)
	if err != nil {
		return Detail{}, fmt.Errorf("dinners: begin read: %w", err)
	}
	defer func() {
		err = errors.Join(err, tx.Rollback())
	}()
	q := db.New(tx)
	row, err := q.GetDinner(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, fmt.Errorf("dinners: read %s: %w", id, err)
	}
	if row.Status == StatusArchived {
		return Detail{}, ErrNotFound
	}
	d = Detail{
		ID:              row.ID,
		Name:            row.Name,
		Status:          row.Status,
		Origin:          row.Origin,
		Method:          nullString(row.Method),
		Difficulty:      nullString(row.Difficulty),
		Cuisine:         nullString(row.Cuisine),
		IsFavorite:      row.IsFavorite == 1,
		Ingredients:     []Ingredient{},
		Steps:           []string{},
		GroundingStatus: row.GroundingStatus,
		GroundingError:  nullString(row.GroundingError),
	}
	if row.MinutesTotal.Valid {
		d.MinutesTotal = &row.MinutesTotal.Int64
	}
	if row.PhotoID.Valid {
		d.PhotoURLs = make(map[string]string, len(recipes.PhotoSizes))
		for _, size := range recipes.PhotoSizes {
			d.PhotoURLs[strconv.Itoa(size)] = photoURL(row.PhotoID.String, size)
		}
	}
	if (row.GroundingStatus == GroundingGrounded || row.GroundingStatus == GroundingManual) && !row.RecipeID.Valid {
		return Detail{}, fmt.Errorf("dinners: %s is %s with no recipe", id, row.GroundingStatus)
	}
	if row.RecipeID.Valid {
		if err := readRecipe(ctx, q, row.RecipeID.String, &d); err != nil {
			return Detail{}, fmt.Errorf("dinners: read recipe of %s: %w", id, err)
		}
	}
	reasons, err := q.GetDinnerSuggestionReasons(ctx, id)
	if err != nil {
		return Detail{}, fmt.Errorf("dinners: read suggestion reason of %s: %w", id, err)
	}
	switch len(reasons) {
	case 0:
	case 1:
		d.WhyPicked = &reasons[0]
	default:
		// Nothing records which of them created the dinner, and any
		// one picked would be a guess.
		return Detail{}, fmt.Errorf("dinners: %s has %d suggestions that could have created it", id, len(reasons))
	}
	return d, nil
}

// readRecipe sets d's Source, Ingredients and Steps from recipe recipeID.
func readRecipe(ctx context.Context, q *db.Queries, recipeID string, d *Detail) error {
	rec, err := q.GetRecipe(ctx, recipeID)
	if err != nil {
		return fmt.Errorf("recipe %s: %w", recipeID, err)
	}
	if !IsManualRecipe(rec.SourceUrl) {
		d.Source = &Source{SiteName: nullString(rec.SiteName)}
	}
	ings, err := q.ListRecipeIngredients(ctx, recipeID)
	if err != nil {
		return fmt.Errorf("ingredients: %w", err)
	}
	for i, ing := range ings {
		if i > 0 && ing.Position == ings[i-1].Position {
			return duplicatePosition("ingredient", ing.Position)
		}
		d.Ingredients = append(d.Ingredients, Ingredient{
			Name:    nullString(ing.Name),
			Qty:     nullString(ing.Qty),
			Unit:    nullString(ing.Unit),
			Note:    nullString(ing.Note),
			RawText: ing.RawText,
		})
	}
	steps, err := q.ListRecipeSteps(ctx, recipeID)
	if err != nil {
		return fmt.Errorf("steps: %w", err)
	}
	for i, step := range steps {
		if i > 0 && step.Position == steps[i-1].Position {
			return duplicatePosition("step", step.Position)
		}
		d.Steps = append(d.Steps, step.Text)
	}
	return nil
}

// duplicatePosition is the error for two recipe items of kind at position.
// Position is the only record of recipe order, and the id that breaks the
// tie says nothing about it, so a recipe with a duplicate is refused rather
// than shown in an order nobody can trust.
func duplicatePosition(kind string, position int64) error {
	return fmt.Errorf("two %ss at position %d: recipe order is unknown", kind, position)
}
