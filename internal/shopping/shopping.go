// Package shopping keeps the household's Supply runs: each week's
// shopping list, generated from the week's plan and grouped by dinner, and
// the Supply archive they are frozen into.
//
// Generate makes a week's Supply run from its plan, and remakes it after
// the plan changes. It is deterministic and runs no model: an item is one
// ingredient of the dinner's recipe exactly as recipe grounding stored it
// (see Generate). The one model call is the Supply run's title, written by
// archive_title in a KindTitleSupplyRun job when the list is generated
// untitled (see Generate).
//
// Freeze moves a week's Supply run into the Supply archive once its week
// has ended, and Archive reads the archive back as posts grouped by week,
// month or year.
package shopping

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
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

// KindTitleSupplyRun is the job kind that titles a Supply run with
// archive_title. Only Generate and GeneratePushed enqueue it.
const KindTitleSupplyRun = "title_supply_run"

// ReasonTitleFailed is the failure code of a KindTitleSupplyRun job that
// stored no title: archive_title failed, or the Supply run was frozen or
// replaced first. The Supply run stays untitled.
const ReasonTitleFailed = "title_failed"

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

// ErrWeekNotEnded is a freeze of a Supply run whose week has not ended in
// the household's time zone.
var ErrWeekNotEnded = errors.New("the week has not ended")

// ErrSendUnconfirmed is a send to Google Chat whose outcome is not known:
// Google Chat did not answer, or accepted the post but recording it failed.
// The post may have been made, so the Supply run keeps the send as
// unconfirmed and is posted again only with resend.
var ErrSendUnconfirmed = errors.New("the shopping list may have reached Google Chat, but that could not be confirmed; check the space before sending again")

// ErrInvalidPeriod is an Archive grouping other than ByWeek, ByMonth and
// ByYear.
var ErrInvalidPeriod = errors.New("by must be week, month or year")

// Titler runs archive_title on in; scope names the week, its plan revision
// and its dinners.
// In production it is skills.TitleArchive with the household's
// harness.Config.
type Titler func(ctx context.Context, scope string, in skills.ArchiveTitleRequest) (skills.ArchiveTitle, error)

// Service generates Supply runs.
type Service struct {
	st       *store.Store
	title    Titler
	titleJob *jobs.Kind[TitleJob]
	notifier *notify.Service
	chat     *chat.Service
	logger   *slog.Logger
	// now is the clock weeks are ended and current on; tests replace it.
	now func() time.Time
	// titling orders the title writes against Freeze. A title job holds it
	// shared from reading the list to storing its title, and Freeze holds
	// it exclusively from reading the list to freezing it, so a freeze
	// waits for the title jobs in flight to store their titles or fail,
	// and no title job runs archive_title while a freeze is under way.
	titling sync.RWMutex
	// sending serializes SendToChat, so a week is posted to Google Chat
	// once however many sends race.
	sending sync.Mutex
}

// NewService returns a service over st that titles Supply runs with title
// in KindTitleSupplyRun jobs, which it registers on runner, tells the
// household through notifier, which must be over st, sends Supply runs to
// Google Chat through chatSvc, and logs to logger. runner must not have
// started yet.
func NewService(st *store.Store, title Titler, runner *jobs.Runner, notifier *notify.Service, chatSvc *chat.Service, logger *slog.Logger) (*Service, error) {
	switch {
	case st == nil:
		return nil, errors.New("shopping: nil store")
	case title == nil:
		return nil, errors.New("shopping: nil titler")
	case runner == nil:
		return nil, errors.New("shopping: nil job runner")
	case notifier == nil:
		return nil, errors.New("shopping: nil notifier")
	case chatSvc == nil:
		return nil, errors.New("shopping: nil chat service")
	case logger == nil:
		return nil, errors.New("shopping: nil logger")
	}
	s := &Service{st: st, title: title, notifier: notifier, chat: chatSvc, logger: logger, now: time.Now}
	kind, err := jobs.Register(runner, KindTitleSupplyRun, s.runTitleJob, ReasonTitleFailed)
	if err != nil {
		return nil, err
	}
	if err := kind.ServiceOnly(); err != nil {
		return nil, err
	}
	s.titleJob = kind
	return s, nil
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
	// ChatSendUnconfirmedAt is when a send to Google Chat whose outcome is
	// not known began: one under way, or one that may or may not have been
	// posted. nil when there is none.
	ChatSendUnconfirmedAt *string `json:"chat_send_unconfirmed_at"`
	Groups                []Group `json:"groups"`
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
	// TitleJobID is the KindTitleSupplyRun job titling the list; nil when
	// the list already has its title.
	TitleJobID *string `json:"title_job_id"`
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
// A list still untitled once it is stored, as a new list always is, gets a
// KindTitleSupplyRun job, enqueued in the same transaction, that titles it
// with archive_title from the names of the plan's dinners in day order.
// Generate returns without waiting for the job: the list is returned
// untitled, with the job's id. A title is written once: a remake keeps it
// and enqueues no job. Titling never fails the generation: a title that
// cannot be written leaves the list untitled, the failure is logged and
// the job failed with ReasonTitleFailed, and the next generation of the
// week, Generate or GeneratePushed, tries again.
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
	var out Generated
	notified := false
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
		now := store.Now().String()
		list, err := q.GetShoppingList(ctx, weekStart.String())
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("shopping: read list: %w", err)
		}
		created := err != nil
		switch {
		case created: // no list yet
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
		if created {
			ev := chat.ListReady(weekStart, out.List.Counts().Needed, len(groups))
			if err := s.notifier.EmitTx(ctx, tx, ev); err != nil {
				return fmt.Errorf("shopping: %w", err)
			}
			notified = true
		}
		if list.Title.Valid {
			return nil
		}
		id, err := s.titleJob.EnqueueTx(ctx, tx, TitleJob{
			ListID: list.ID, WeekStart: list.WeekStart, Revision: plan.Revision, Dinners: dinnerNames,
		})
		if err != nil {
			return fmt.Errorf("shopping: enqueue title: %w", err)
		}
		out.TitleJobID = &id
		return nil
	})
	if err != nil {
		return Generated{}, err
	}
	if out.TitleJobID != nil {
		s.titleJob.Wake()
	}
	if notified {
		s.notifier.Wake()
	}
	return out, nil
}

// TitleJob is a KindTitleSupplyRun job's payload: the untitled Supply run
// ListID of the week WeekStart, made from the plan at Revision, whose
// dinners in day order are Dinners.
type TitleJob struct {
	ListID    string   `json:"list_id"`
	WeekStart string   `json:"week_start"`
	Revision  int64    `json:"revision"`
	Dinners   []string `json:"dinners"`
}

// titleResult is a KindTitleSupplyRun job's result: the Supply run and the
// title it has.
type titleResult struct {
	ListID string `json:"list_id"`
	Title  string `json:"title"`
}

// PublicJSON returns the Supply run's id and title.
func (r titleResult) PublicJSON() ([]byte, error) { return json.Marshal(r) }

// runTitleJob titles the Supply run of p with archive_title and stores the
// title. A title that cannot be stored leaves the list untitled: the
// failure is logged and the job fails with ReasonTitleFailed.
func (s *Service) runTitleJob(ctx context.Context, p TitleJob, _ func(titleResult) error) (titleResult, error) {
	title, err := s.writeTitle(ctx, p)
	if err != nil {
		s.logger.WarnContext(ctx, "Supply run left untitled; its next generation titles it again",
			"week", p.WeekStart, "list", p.ListID, "error", err)
		return titleResult{}, &jobs.Failure{Code: ReasonTitleFailed}
	}
	return titleResult{ListID: p.ListID, Title: title}, nil
}

// writeTitle runs archive_title for p's Supply run and stores its title,
// as storeTitle does, while no freeze is under way: it waits for one to
// end first, and a freeze waits for it to end.
func (s *Service) writeTitle(ctx context.Context, p TitleJob) (string, error) {
	s.titling.RLock()
	defer s.titling.RUnlock()
	return s.storeTitle(ctx, p)
}

// storeTitle runs archive_title for p's Supply run and stores its title.
// When the list was titled by another job, before this one ran or
// meanwhile, that title stands and is returned. The caller holds titling.
func (s *Service) storeTitle(ctx context.Context, p TitleJob) (string, error) {
	q := db.New(s.st.Writer)
	title, err := s.currentTitle(ctx, q, p)
	if err != nil || title != "" {
		return title, err
	}
	out, err := s.title(ctx, titleScope(p), skills.ArchiveTitleRequest{Dinners: p.Dinners})
	if err != nil {
		return "", fmt.Errorf("shopping: title: %w", err)
	}
	write, cancel := jobs.FinalWriteContext(ctx)
	defer cancel()
	n, err := q.SetShoppingListTitle(write, db.SetShoppingListTitleParams{
		Title: sql.NullString{String: out.Title, Valid: true}, UpdatedAt: store.Now().String(), ID: p.ListID,
	})
	if err != nil {
		return "", fmt.Errorf("shopping: store title: %w", err)
	}
	if n == 1 {
		return out.Title, nil
	}
	title, err = s.currentTitle(write, q, p)
	if err == nil && title == "" {
		err = errors.New("shopping: store title: the Supply run is still untitled")
	}
	return title, err
}

// currentTitle returns the title of p's Supply run, or "" while it is
// untitled. A list frozen or replaced untitled is an error: it can no
// longer be titled.
func (s *Service) currentTitle(ctx context.Context, q *db.Queries, p TitleJob) (string, error) {
	list, err := q.GetShoppingList(ctx, p.WeekStart)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("shopping: title: read list: %w", err)
	}
	switch {
	case err == nil && list.ID == p.ListID && list.Title.Valid:
		return list.Title.String, nil
	case err != nil || list.ID != p.ListID || frozen(list):
		return "", errors.New("shopping: store title: the Supply run was frozen or replaced before its title was stored")
	}
	return "", nil
}

// titleScope is the archive_title scope of p: its week and plan revision,
// and a fingerprint of its dinners in day order, so runs share a title only
// when they title the same dinner names. A renamed dinner changes the
// names but not the plan revision.
func titleScope(p TitleJob) string {
	names, _ := json.Marshal(p.Dinners) // a []string always encodes
	return fmt.Sprintf("%s@%d/%x", p.WeekStart, p.Revision, sha256.Sum256(names))
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
	return readList(ctx, q, list)
}

// readList reads the groups and items of list through q.
func readList(ctx context.Context, q *db.Queries, list db.ShoppingList) (List, error) {
	rows, err := q.ListShoppingGroupDinners(ctx, list.ID)
	if err != nil {
		return List{}, fmt.Errorf("shopping: read groups of %s: %w", list.WeekStart, err)
	}
	items, err := q.ListShoppingItems(ctx, list.ID)
	if err != nil {
		return List{}, fmt.Errorf("shopping: read items of %s: %w", list.WeekStart, err)
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
		ID:                    list.ID,
		WeekStart:             list.WeekStart,
		PlanID:                list.PlanID,
		Title:                 nullString(list.Title),
		Status:                list.Status,
		SentToChatAt:          nullString(list.SentToChatAt),
		ChatSendUnconfirmedAt: nullString(list.ChatSendUnconfirmedAt),
		Groups:                groups,
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
