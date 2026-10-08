// Package shopping keeps the household's Supply runs: each week's
// shopping list, generated from the week's plan and grouped by dinner, and
// the Supply archive they are frozen into.
//
// Generate makes a week's Supply run from its plan, and remakes it after
// the plan changes. It is deterministic and runs no model: an item is one
// ingredient of the dinner's recipe exactly as recipe grounding stored it
// (see Generate).
package shopping

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// List statuses (the shopping_lists.status column).
const (
	StatusCurrent  = "current"
	StatusArchived = "archived"
)

// Reasons a plan day has no group on its Supply run.
const (
	// SkipNoRecipe: the day's dinner has no recipe, as while its grounding
	// is pending or after it failed.
	SkipNoRecipe = "no_recipe"
	// SkipNoIngredients: the day's dinner's recipe lists no ingredients.
	SkipNoIngredients = "no_ingredients"
)

// ErrPlanNotFound is a generation for a week with no plan.
var ErrPlanNotFound = errors.New("the week has no plan")

// ErrFrozen is a generation for a week whose Supply run is frozen into the
// Supply archive, which is read-only.
var ErrFrozen = errors.New("the week's Supply run is in the Supply archive and can no longer change")

// Service generates Supply runs.
type Service struct {
	st *store.Store
}

// NewService returns a service over st.
func NewService(st *store.Store) (*Service, error) {
	if st == nil {
		return nil, errors.New("shopping: nil store")
	}
	return &Service{st: st}, nil
}

// List is a Supply run.
type List struct {
	ID string `json:"id"`
	// WeekStart is the Monday of the list's week, YYYY-MM-DD.
	WeekStart string `json:"week_start"`
	PlanID    string `json:"plan_id"`
	// Title is the AI-written post title; nil until it is written, when
	// the week label stands in for it.
	Title  *string `json:"title"`
	Status string  `json:"status"`
	Groups []Group `json:"groups"`
}

// Group is the items of one plan day's dinner.
type Group struct {
	ID         string `json:"id"`
	DinnerID   string `json:"dinner_id"`
	DinnerName string `json:"dinner_name"`
	// Date is the plan day, household-local YYYY-MM-DD.
	Date     string `json:"date"`
	Position int    `json:"position"`
	Items    []Item `json:"items"`
}

// Item is one ingredient to buy.
type Item struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Qty is the quantity as shown, such as "2 cups" or "500 g"; nil when
	// the recipe gives none.
	Qty *string `json:"qty"`
	// Needed is false once the household marks the item "have it".
	Needed   bool `json:"needed"`
	Position int  `json:"position"`
}

// SkippedDay is a plan day with no group on the list, and why.
type SkippedDay struct {
	Date       string `json:"date"`
	DinnerID   string `json:"dinner_id"`
	DinnerName string `json:"dinner_name"`
	// Reason is SkipNoRecipe or SkipNoIngredients.
	Reason string `json:"reason"`
}

// Generated is the outcome of Generate.
type Generated struct {
	List List `json:"list"`
	// Skipped are the plan days whose dinner has no ingredients to shop
	// for, by date.
	Skipped []SkippedDay `json:"skipped"`
}

// Generate makes the Supply run of the week starting weekStart from the
// week's plan, creating it untitled the first time and remaking it after
// that, in one transaction.
//
// The list has one group per plan day with a dinner, by date, holding one
// item per ingredient of the dinner's recipe, by the ingredient's
// position. A fetched recipe's ingredients are normalized when it is
// grounded (normalize_recipe): the item is the ingredient's name, and its
// quantity is qty and unit joined by a space ("2 cups"), either alone when
// the other is missing, or nil when both are. A recipe entered by hand is
// stored as typed, unnormalized: each item is its line as typed, with no
// quantity. A day whose dinner has no recipe or whose recipe has no
// ingredients gets no group and is reported in Skipped.
//
// Remaking keeps what the household marked: an item is the same item as
// before when its dinner and name are the same, wherever the dinner and
// the ingredient now are, so it keeps its id and needed flag. When a
// dinner lists a name more than once, its first such item matches the
// first one before, the second the second, and so on. Any other item is
// new and needed; an item no longer on the plan is removed, and with it a
// group no longer on the plan.
//
// A week with no plan is ErrPlanNotFound; a list frozen into the Supply
// archive is ErrFrozen. A fetched recipe's ingredient without a name is an
// error: grounding always names one.
func (s *Service) Generate(ctx context.Context, weekStart store.LocalDate) (Generated, error) {
	if _, err := household.WeekDays(weekStart); err != nil {
		return Generated{}, fmt.Errorf("shopping: generate: %w", err)
	}
	var out Generated
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		plan, err := q.GetWeekPlan(ctx, weekStart.String())
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPlanNotFound
		}
		if err != nil {
			return fmt.Errorf("shopping: read plan: %w", err)
		}
		want, skipped, err := wanted(ctx, q, plan.ID)
		if err != nil {
			return err
		}
		now := store.Now().String()
		list, err := q.GetShoppingList(ctx, weekStart.String())
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("shopping: read list: %w", err)
		}
		switch {
		case err != nil: // no list yet
			list = db.ShoppingList{ID: store.NewID(), WeekStart: weekStart.String(), PlanID: plan.ID, Status: StatusCurrent}
			err = q.CreateShoppingList(ctx, db.CreateShoppingListParams{
				ID: list.ID, WeekStart: list.WeekStart, PlanID: list.PlanID, CreatedAt: now, UpdatedAt: now,
			})
		case list.FrozenAt.Valid || list.Status == StatusArchived:
			return ErrFrozen
		default:
			list.PlanID = plan.ID
			err = q.RegenerateShoppingList(ctx, db.RegenerateShoppingListParams{PlanID: plan.ID, UpdatedAt: now, ID: list.ID})
		}
		if err != nil {
			return fmt.Errorf("shopping: store list: %w", err)
		}
		groups, err := reconcile(ctx, q, list.ID, want, now)
		if err != nil {
			return err
		}
		out = Generated{
			List: List{
				ID:        list.ID,
				WeekStart: list.WeekStart,
				PlanID:    list.PlanID,
				Title:     nullString(list.Title),
				Status:    list.Status,
				Groups:    groups,
			},
			Skipped: skipped,
		}
		return nil
	})
	if err != nil {
		return Generated{}, err
	}
	return out, nil
}

// wantGroup is a group the plan asks for, before it is stored.
type wantGroup struct {
	date, dinnerID, dinnerName string
	items                      []wantItem
}

type wantItem struct {
	name string
	qty  sql.NullString
}

// wanted reads plan planID's days and their dinners' ingredients: the
// groups the list must have, by date, and the days that get none.
func wanted(ctx context.Context, q *db.Queries, planID string) ([]wantGroup, []SkippedDay, error) {
	days, err := q.ListPlanDays(ctx, planID)
	if err != nil {
		return nil, nil, fmt.Errorf("shopping: read plan days: %w", err)
	}
	var (
		groups  []wantGroup
		skipped []SkippedDay
	)
	for _, day := range days {
		d := day.Dinner
		skip := SkippedDay{Date: day.Date, DinnerID: d.ID, DinnerName: d.Name}
		if !d.RecipeID.Valid {
			skip.Reason = SkipNoRecipe
			skipped = append(skipped, skip)
			continue
		}
		recipe, err := q.GetRecipe(ctx, d.RecipeID.String)
		if err != nil {
			return nil, nil, fmt.Errorf("shopping: read recipe %s of dinner %s: %w", d.RecipeID.String, d.ID, err)
		}
		ings, err := q.ListRecipeIngredients(ctx, recipe.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("shopping: read ingredients of recipe %s: %w", recipe.ID, err)
		}
		if len(ings) == 0 {
			skip.Reason = SkipNoIngredients
			skipped = append(skipped, skip)
			continue
		}
		manual := dinners.IsManualRecipe(recipe.SourceUrl)
		g := wantGroup{date: day.Date, dinnerID: d.ID, dinnerName: d.Name}
		for _, ing := range ings {
			it, err := item(ing, manual)
			if err != nil {
				return nil, nil, fmt.Errorf("shopping: recipe %s: %w", recipe.ID, err)
			}
			g.items = append(g.items, it)
		}
		groups = append(groups, g)
	}
	return groups, skipped, nil
}

// item is the item for ingredient ing of a recipe entered by hand
// (manual) or fetched.
func item(ing db.RecipeIngredient, manual bool) (wantItem, error) {
	if manual {
		return wantItem{name: ing.RawText}, nil
	}
	if !ing.Name.Valid || ing.Name.String == "" {
		return wantItem{}, fmt.Errorf("ingredient %d (%q) has no normalized name", ing.Position+1, ing.RawText)
	}
	var parts []string
	for _, p := range []sql.NullString{ing.Qty, ing.Unit} {
		if p.Valid && p.String != "" {
			parts = append(parts, p.String)
		}
	}
	qty := strings.Join(parts, " ")
	return wantItem{name: ing.Name.String, qty: sql.NullString{String: qty, Valid: qty != ""}}, nil
}

// itemKey is what makes an item the same item across a remake.
type itemKey struct {
	dinnerID, name string
}

// groupKey is what makes a group the same group across a remake.
type groupKey struct {
	date, dinnerID string
}

// reconcile makes the groups and items of list listID those of want, as
// Generate says, and returns them.
func reconcile(ctx context.Context, q *db.Queries, listID string, want []wantGroup, now string) ([]Group, error) {
	oldGroups, err := q.ListShoppingGroups(ctx, listID)
	if err != nil {
		return nil, fmt.Errorf("shopping: read groups: %w", err)
	}
	oldItems, err := q.ListShoppingItems(ctx, listID)
	if err != nil {
		return nil, fmt.Errorf("shopping: read items: %w", err)
	}
	groupsByKey := make(map[groupKey]db.ShoppingGroup, len(oldGroups))
	for _, g := range oldGroups {
		groupsByKey[groupKey{g.Date, g.DinnerID}] = g
	}
	// itemsByKey holds each key's items in list order, so the nth item a
	// dinner lists under a name matches its nth one before.
	itemsByKey := make(map[itemKey][]db.ShoppingItem)
	for _, r := range oldItems {
		k := itemKey{r.DinnerID, r.ShoppingItem.Name}
		itemsByKey[k] = append(itemsByKey[k], r.ShoppingItem)
	}
	kept := make(map[string]bool) // ids of the groups and items kept

	groups := make([]Group, 0, len(want))
	for gi, w := range want {
		g := Group{DinnerID: w.dinnerID, DinnerName: w.dinnerName, Date: w.date, Position: gi}
		if old, ok := groupsByKey[groupKey{w.date, w.dinnerID}]; ok {
			g.ID = old.ID
			kept[old.ID] = true
			if err := q.MoveShoppingGroup(ctx, db.MoveShoppingGroupParams{Position: int64(gi), UpdatedAt: now, ID: g.ID}); err != nil {
				return nil, fmt.Errorf("shopping: move group %s: %w", g.ID, err)
			}
		} else {
			g.ID = store.NewID()
			if err := q.CreateShoppingGroup(ctx, db.CreateShoppingGroupParams{
				ID: g.ID, ListID: listID, DinnerID: w.dinnerID, Date: w.date, Position: int64(gi),
				CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return nil, fmt.Errorf("shopping: store group of %s: %w", w.date, err)
			}
		}
		for ii, wi := range w.items {
			it := Item{Name: wi.name, Qty: nullString(wi.qty), Needed: true, Position: ii}
			k := itemKey{w.dinnerID, wi.name}
			if same := itemsByKey[k]; len(same) > 0 {
				old := same[0]
				itemsByKey[k] = same[1:]
				it.ID, it.Needed = old.ID, old.Needed == 1
				kept[old.ID] = true
				if err := q.MoveShoppingItem(ctx, db.MoveShoppingItemParams{
					GroupID: g.ID, Qty: wi.qty, Position: int64(ii), UpdatedAt: now, ID: it.ID,
				}); err != nil {
					return nil, fmt.Errorf("shopping: move item %s: %w", it.ID, err)
				}
			} else {
				it.ID = store.NewID()
				if err := q.CreateShoppingItem(ctx, db.CreateShoppingItemParams{
					ID: it.ID, GroupID: g.ID, Name: wi.name, Qty: wi.qty, Position: int64(ii),
					CreatedAt: now, UpdatedAt: now,
				}); err != nil {
					return nil, fmt.Errorf("shopping: store item %q of %s: %w", wi.name, w.date, err)
				}
			}
			g.Items = append(g.Items, it)
		}
		groups = append(groups, g)
	}

	// Items first: a group is removed only once it is empty.
	for _, r := range oldItems {
		if !kept[r.ShoppingItem.ID] {
			if err := q.DeleteShoppingItem(ctx, r.ShoppingItem.ID); err != nil {
				return nil, fmt.Errorf("shopping: remove item %s: %w", r.ShoppingItem.ID, err)
			}
		}
	}
	for _, g := range oldGroups {
		if !kept[g.ID] {
			if err := q.DeleteShoppingGroup(ctx, g.ID); err != nil {
				return nil, fmt.Errorf("shopping: remove group %s: %w", g.ID, err)
			}
		}
	}
	return groups, nil
}

func nullString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
