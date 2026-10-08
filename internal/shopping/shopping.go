// Package shopping keeps the household's Supply runs: each week's
// shopping list, generated from the week's plan and grouped by dinner, and
// the Supply archive they are frozen into.
//
// Generate makes a week's Supply run from its plan, and remakes it after
// the plan changes. It is deterministic and runs no model: an item is one
// ingredient of the dinner's recipe exactly as recipe grounding stored it
// (see Generate). The one model call is the Supply run's title, written by
// archive_title when the list is generated untitled (see Generate).
package shopping

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
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

// ErrPlanChanged is a generation from a pushed plan the household has
// edited since it was pushed: the Supply run would not match Calendar, so
// none is made, and pushing the plan again makes it.
var ErrPlanChanged = errors.New("the plan changed after it was pushed to Calendar; push again")

// ErrFrozen is a generation of, or a change to an item of, a Supply run
// frozen into the Supply archive, which is read-only.
var ErrFrozen = errors.New("the week's Supply run is in the Supply archive and can no longer change")

// ErrListNotFound is a read of a week with no Supply run.
var ErrListNotFound = errors.New("the week has no Supply run")

// ErrItemNotFound is a change to an item on no Supply run.
var ErrItemNotFound = errors.New("no Supply run has that item")

// Titler runs archive_title on in; scope names the week and its dinners.
// In production it is skills.TitleArchive with the household's
// harness.Config.
type Titler func(ctx context.Context, scope string, in skills.ArchiveTitleRequest) (skills.ArchiveTitle, error)

// Service generates Supply runs.
type Service struct {
	st     *store.Store
	title  Titler
	logger *slog.Logger
}

// NewService returns a service over st that titles Supply runs with title
// and logs to logger.
func NewService(st *store.Store, title Titler, logger *slog.Logger) (*Service, error) {
	switch {
	case st == nil:
		return nil, errors.New("shopping: nil store")
	case title == nil:
		return nil, errors.New("shopping: nil titler")
	case logger == nil:
		return nil, errors.New("shopping: nil logger")
	}
	return &Service{st: st, title: title, logger: logger}, nil
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
	// SentToChatAt is when the list was sent to Google Chat; nil until it
	// is.
	SentToChatAt *string `json:"sent_to_chat_at"`
	Groups       []Group `json:"groups"`
}

// Counts are how many items of a Supply run are still needed and how many
// it has in all.
type Counts struct {
	Needed int `json:"needed_count"`
	Total  int `json:"total_count"`
}

// Counts counts l's items.
func (l List) Counts() Counts {
	var c Counts
	for _, g := range l.Groups {
		for _, it := range g.Items {
			c.Total++
			if it.Needed {
				c.Needed++
			}
		}
	}
	return c
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
// A list still untitled once it is stored, as a new list always is, is
// then titled by archive_title from the names of the plan's dinners in day
// order, outside the transaction. A title is written once: a remake keeps
// it. Titling never fails the generation: a title that cannot be written
// leaves the list untitled, as it is returned, the failure is logged, and
// the next generation of the week, Generate or GeneratePushed, tries again.
//
// A week with no plan is ErrPlanNotFound; a list frozen into the Supply
// archive is ErrFrozen. A fetched recipe's ingredient without a name is an
// error: grounding always names one.
func (s *Service) Generate(ctx context.Context, weekStart store.LocalDate) (Generated, error) {
	return s.generate(ctx, weekStart, func(db.WeekPlan) error { return nil })
}

// GeneratePushed makes the Supply run of the week starting weekStart as
// Generate does, from the plan a Push to Calendar pushed at revision. A
// plan edited since (another revision) is ErrPlanChanged, and the Supply
// run is left as it was.
func (s *Service) GeneratePushed(ctx context.Context, weekStart store.LocalDate, revision int64) (Generated, error) {
	return s.generate(ctx, weekStart, func(plan db.WeekPlan) error {
		if plan.Revision != revision {
			return ErrPlanChanged
		}
		return nil
	})
}

// generate makes the Supply run of the week starting weekStart from the
// week's plan, as Generate says, once check accepts that plan, in the same
// transaction.
func (s *Service) generate(ctx context.Context, weekStart store.LocalDate, check func(db.WeekPlan) error) (Generated, error) {
	if _, err := household.WeekDays(weekStart); err != nil {
		return Generated{}, fmt.Errorf("shopping: generate: %w", err)
	}
	var (
		out      Generated
		revision int64
		names    []string // the plan's dinners, in day order
	)
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		plan, err := q.GetWeekPlan(ctx, weekStart.String())
		if errors.Is(err, sql.ErrNoRows) {
			return ErrPlanNotFound
		}
		if err != nil {
			return fmt.Errorf("shopping: read plan: %w", err)
		}
		if err := check(plan); err != nil {
			return err
		}
		want, skipped, dinnerNames, err := wanted(ctx, q, plan.ID)
		if err != nil {
			return err
		}
		revision, names = plan.Revision, dinnerNames
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
		case frozen(list):
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
		out = Generated{List: listOf(list, groups), Skipped: skipped}
		return nil
	})
	if err != nil {
		return Generated{}, err
	}
	if out.List.Title == nil {
		s.titleList(ctx, &out.List, revision, names)
	}
	return out, nil
}

// titleList titles l, an untitled Supply run made from dinners, the names
// of its plan's dinners in day order at plan revision, with archive_title,
// and stores the title on it. A title that cannot be written leaves l
// untitled; the failure is logged, never returned.
func (s *Service) titleList(ctx context.Context, l *List, revision int64, dinners []string) {
	title, err := s.writeTitle(ctx, l, revision, dinners)
	if err != nil {
		s.logger.WarnContext(ctx, "Supply run left untitled; its next generation titles it again",
			"week", l.WeekStart, "list", l.ID, "error", err)
		return
	}
	l.Title = &title
}

// writeTitle runs archive_title for l and stores its title. When l was
// titled by another generation meanwhile, that title stands and is
// returned.
func (s *Service) writeTitle(ctx context.Context, l *List, revision int64, dinners []string) (string, error) {
	scope := fmt.Sprintf("%s@%d", l.WeekStart, revision)
	out, err := s.title(ctx, scope, skills.ArchiveTitleRequest{Dinners: dinners})
	if err != nil {
		return "", fmt.Errorf("shopping: title: %w", err)
	}
	q := db.New(s.st.Writer)
	n, err := q.SetShoppingListTitle(ctx, db.SetShoppingListTitleParams{
		Title: sql.NullString{String: out.Title, Valid: true}, UpdatedAt: store.Now().String(), ID: l.ID,
	})
	if err != nil {
		return "", fmt.Errorf("shopping: store title: %w", err)
	}
	if n == 1 {
		return out.Title, nil
	}
	list, err := q.GetShoppingList(ctx, l.WeekStart)
	if err != nil {
		return "", fmt.Errorf("shopping: store title: read list: %w", err)
	}
	if list.ID != l.ID || !list.Title.Valid {
		return "", errors.New("shopping: store title: the Supply run was frozen or replaced before its title was stored")
	}
	return list.Title.String, nil
}

// Get returns the Supply run of the week starting weekStart, as stored: a
// week with none is ErrListNotFound.
func (s *Service) Get(ctx context.Context, weekStart store.LocalDate) (l List, err error) {
	if _, err := household.WeekDays(weekStart); err != nil {
		return List{}, fmt.Errorf("shopping: get: %w", err)
	}
	tx, err := s.st.Reader.BeginTx(ctx, nil)
	if err != nil {
		return List{}, fmt.Errorf("shopping: begin read: %w", err)
	}
	defer func() {
		err = errors.Join(err, tx.Rollback())
	}()
	q := db.New(tx)
	list, err := q.GetShoppingList(ctx, weekStart.String())
	if errors.Is(err, sql.ErrNoRows) {
		return List{}, ErrListNotFound
	}
	if err != nil {
		return List{}, fmt.Errorf("shopping: read list: %w", err)
	}
	rows, err := q.ListShoppingGroupDinners(ctx, list.ID)
	if err != nil {
		return List{}, fmt.Errorf("shopping: read groups: %w", err)
	}
	items, err := q.ListShoppingItems(ctx, list.ID)
	if err != nil {
		return List{}, fmt.Errorf("shopping: read items: %w", err)
	}
	byGroup := make(map[string][]Item, len(rows))
	for _, r := range items {
		it := r.ShoppingItem
		byGroup[it.GroupID] = append(byGroup[it.GroupID], Item{
			ID: it.ID, Name: it.Name, Qty: nullString(it.Qty), Needed: it.Needed == 1, Position: int(it.Position),
		})
	}
	groups := make([]Group, 0, len(rows))
	for _, r := range rows {
		g := r.ShoppingGroup
		groups = append(groups, Group{
			ID: g.ID, DinnerID: g.DinnerID, DinnerName: r.DinnerName, Date: g.Date, Position: int(g.Position),
			Items: byGroup[g.ID],
		})
	}
	return listOf(list, groups), nil
}

// SetNeeded marks item itemID needed, or "have it" when needed is false,
// and returns its Supply run's counts after the change. An item on no
// Supply run is ErrItemNotFound; one on a list frozen into the Supply
// archive is ErrFrozen, and is left as it was.
func (s *Service) SetNeeded(ctx context.Context, itemID string, needed bool) (Counts, error) {
	var c Counts
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		row, err := q.GetShoppingItemList(ctx, itemID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrItemNotFound
		}
		if err != nil {
			return fmt.Errorf("shopping: read list of item %s: %w", itemID, err)
		}
		if frozen(row.ShoppingList) {
			return ErrFrozen
		}
		flag := int64(0)
		if needed {
			flag = 1
		}
		if err := q.SetShoppingItemNeeded(ctx, db.SetShoppingItemNeededParams{
			Needed: flag, UpdatedAt: store.Now().String(), ID: itemID,
		}); err != nil {
			return fmt.Errorf("shopping: mark item %s: %w", itemID, err)
		}
		items, err := q.ListShoppingItems(ctx, row.ShoppingList.ID)
		if err != nil {
			return fmt.Errorf("shopping: read items: %w", err)
		}
		for _, r := range items {
			c.Total++
			if r.ShoppingItem.Needed == 1 {
				c.Needed++
			}
		}
		return nil
	})
	if err != nil {
		return Counts{}, err
	}
	return c, nil
}

// frozen reports whether list is frozen into the Supply archive.
func frozen(list db.ShoppingList) bool {
	return list.FrozenAt.Valid || list.Status == StatusArchived
}

// listOf is list with groups.
func listOf(list db.ShoppingList, groups []Group) List {
	return List{
		ID:           list.ID,
		WeekStart:    list.WeekStart,
		PlanID:       list.PlanID,
		Title:        nullString(list.Title),
		Status:       list.Status,
		SentToChatAt: nullString(list.SentToChatAt),
		Groups:       groups,
	}
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
// groups the list must have, by date, the days that get none, and the
// names of the plan's dinners in day order.
func wanted(ctx context.Context, q *db.Queries, planID string) ([]wantGroup, []SkippedDay, []string, error) {
	days, err := q.ListPlanDays(ctx, planID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("shopping: read plan days: %w", err)
	}
	var (
		groups  []wantGroup
		skipped []SkippedDay
		names   []string
	)
	for _, day := range days {
		d := day.Dinner
		names = append(names, d.Name)
		skip := SkippedDay{Date: day.Date, DinnerID: d.ID, DinnerName: d.Name}
		if !d.RecipeID.Valid {
			skip.Reason = SkipNoRecipe
			skipped = append(skipped, skip)
			continue
		}
		recipe, err := q.GetRecipe(ctx, d.RecipeID.String)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("shopping: read recipe %s of dinner %s: %w", d.RecipeID.String, d.ID, err)
		}
		ings, err := q.ListRecipeIngredients(ctx, recipe.ID)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("shopping: read ingredients of recipe %s: %w", recipe.ID, err)
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
				return nil, nil, nil, fmt.Errorf("shopping: recipe %s: %w", recipe.ID, err)
			}
			g.items = append(g.items, it)
		}
		groups = append(groups, g)
	}
	return groups, skipped, names, nil
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
