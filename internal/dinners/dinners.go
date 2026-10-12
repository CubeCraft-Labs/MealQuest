// Package dinners keeps the household's dinners: adding Regulars by name or
// recipe link, listing Regulars and the Quest log, editing, archiving and
// favoriting, and grounding each dinner in a recipe. Names are deduplicated with the harness's dinner-name rules,
// so the Regulars the household types and the Side quests the AI suggests
// are compared the same way.
package dinners

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Dinner statuses (the dinners.status column).
const (
	StatusSuggested = "suggested"
	StatusQuestLog  = "quest_log"
	StatusTried     = "tried"
	StatusRegular   = "regular"
	StatusArchived  = "archived"
)

// Limits on what a member may enter.
const (
	MaxNameChars   = 120
	MaxRecipeURL   = 2048
	MaxMinutes     = 1440 // a day; a crock-pot dinner fits
	photoListWidth = 400  // the stored photo size lists show
)

// Methods and Difficulties are the values the dinners table allows.
var (
	Methods      = []string{"stovetop", "oven", "crockpot", "grill", "other"}
	Difficulties = []string{"easy", "medium", "hard"}
)

// listable are the statuses List answers for.
var listable = []string{StatusRegular, StatusQuestLog}

// ErrNotFound is returned for an unknown dinner id.
var ErrNotFound = errors.New("dinner not found")

// Dinner is a dinner as the API shows it. It has no recipe_url: a pasted
// link may carry a personal token, so it stays in the database for
// grounding and never leaves the server.
type Dinner struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Status          string  `json:"status"`
	Origin          string  `json:"origin"`
	Method          *string `json:"method"`
	MinutesTotal    *int64  `json:"minutes_total"`
	Difficulty      *string `json:"difficulty"`
	Cuisine         *string `json:"cuisine"`
	IsFavorite      bool    `json:"is_favorite"`
	RecipeID        *string `json:"recipe_id"`
	PhotoURL        *string `json:"photo_url"`
	GroundingStatus string  `json:"grounding_status"`
	// GroundingError is why grounding failed, one of the Reason codes;
	// null unless GroundingStatus is failed.
	GroundingError *string `json:"grounding_error"`
	UpdatedBy      *string `json:"updated_by"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// NewRegular is a Regular to add: its Name, which the household always
// gives, and optionally the RecipeURL to ground it in. The name is never
// taken from the link: a pasted link may carry a personal token anywhere in
// it, and the name is shown in every dinner response.
type NewRegular struct {
	Name      *string
	RecipeURL *string
}

// Patch edits a dinner. Nil fields are left as they are.
type Patch struct {
	Name         *string
	Method       *string
	MinutesTotal *int
	Difficulty   *string
}

// Problem is one invalid field, named by its JSON key.
type Problem struct {
	Field  string
	Reason string
}

// ValidationError lists every invalid field of a request.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Field + ": " + p.Reason
	}
	return "invalid dinner: " + strings.Join(parts, "; ")
}

func invalid(field, reason string) *ValidationError {
	return &ValidationError{Problems: []Problem{{Field: field, Reason: reason}}}
}

// DuplicateError is returned when a new or renamed dinner repeats an
// unarchived one: its name is harness.Similar, or it has the same recipe
// link.
type DuplicateError struct {
	Existing Dinner
}

func (e *DuplicateError) Error() string {
	return fmt.Sprintf("%q is already a dinner (%s)", e.Existing.Name, e.Existing.Status)
}

// Service reads and changes dinners.
type Service struct {
	st        *store.Store
	grounder  *recipes.Grounder
	logger    *slog.Logger
	groundJob *jobs.Kind[GroundJob]
	// backfillJob enqueues KindBackfillPhotos jobs.
	backfillJob *jobs.Kind[BackfillJob]
	// wake is called after every committed change to a dinner's method or
	// minutes, which can move its crock-pot start reminder.
	wake func()

	// groundingsMu guards groundings: the KindGroundDinner jobs running,
	// by dinner id, so archiving or renaming a dinner can stop them.
	groundingsMu sync.Mutex
	groundings   map[string]map[*runningGrounding]struct{}

	// offerKey keys the candidate ids Candidates offers, so they are
	// opaque and stable for this process.
	offerKey []byte
	// offersMu guards offers and offerSearches. offers holds, by dinner
	// id, the candidates last offered by Candidates until they expire, so
	// a candidate id is resolved here and its URL never leaves the server.
	// offerSearches holds the Candidates searches running, by dinner id,
	// so clearing a dinner's offers keeps them from storing stale ones.
	offersMu      sync.Mutex
	offers        map[string]*offerSet
	offerSearches map[string]map[*offerSearch]struct{}
}

// NewService returns a service over st that grounds dinners with grounder,
// calls wake (the scheduler's Wake) after every committed change to a
// dinner's method or minutes, and registers the KindGroundDinner and
// KindBackfillPhotos jobs on runner, which must not have started yet.
func NewService(st *store.Store, grounder *recipes.Grounder, runner *jobs.Runner, wake func(), logger *slog.Logger) (*Service, error) {
	switch {
	case st == nil:
		return nil, errors.New("dinners: nil store")
	case grounder == nil:
		return nil, errors.New("dinners: nil grounder")
	case runner == nil:
		return nil, errors.New("dinners: nil job runner")
	case wake == nil:
		return nil, errors.New("dinners: nil wake")
	case logger == nil:
		return nil, errors.New("dinners: nil logger")
	}
	s := &Service{st: st, grounder: grounder, wake: wake, logger: logger, groundings: make(map[string]map[*runningGrounding]struct{}), offerKey: make([]byte, 32), offers: make(map[string]*offerSet), offerSearches: make(map[string]map[*offerSearch]struct{})}
	rand.Read(s.offerKey) // never fails: it crashes the program instead
	kind, err := jobs.Register(runner, KindGroundDinner, s.runGroundJob, Reasons...)
	if err != nil {
		return nil, err
	}
	s.groundJob = kind
	backfill, err := jobs.Register(runner, KindBackfillPhotos, s.runBackfillJob)
	if err != nil {
		return nil, err
	}
	if err := backfill.ServiceOnly(); err != nil {
		return nil, err
	}
	s.backfillJob = backfill
	return s, nil
}

// Created is a new Regular with the id of the job grounding it, which is
// nil when the job could not be enqueued and the dinner's grounding is
// failed instead.
type Created struct {
	Dinner
	GroundingJobID *string `json:"grounding_job_id"`
}

// CreateRegular adds a Regular named r.Name, keeping r.RecipeURL for
// grounding when given, with status and origin regular and grounding_status
// pending, recording updatedBy, and enqueues the KindGroundDinner job that grounds it. It returns without
// waiting for the job. A name repeating an unarchived dinner is a
// *DuplicateError. When the job cannot be enqueued, the stored dinner is
// returned with grounding_status failed (ReasonGroundingError) and no job
// id, so the household can fix its recipe by hand.
func (s *Service) CreateRegular(ctx context.Context, r NewRegular, updatedBy string) (Created, error) {
	d, err := s.createRegular(ctx, r, updatedBy)
	if err != nil {
		return Created{}, err
	}
	jobID, err := s.enqueueGrounding(ctx, d.ID, d.GroundingRevision)
	if err != nil {
		return Created{}, err
	}
	if jobID == nil {
		row, err := db.New(s.st.Reader).GetDinner(context.WithoutCancel(ctx), d.ID)
		if err != nil {
			return Created{}, fmt.Errorf("dinners: read %s: %w", d.ID, err)
		}
		return Created{Dinner: fromRow(row)}, nil
	}
	return Created{Dinner: fromRow(d), GroundingJobID: jobID}, nil
}

// enqueueGrounding enqueues the KindGroundDinner job for pending dinner id
// at grounding_revision revision and returns its id. When the job cannot be
// enqueued, the dinner's grounding is failed with ReasonGroundingError, so
// it is not left pending with no job, and the id is nil.
func (s *Service) enqueueGrounding(ctx context.Context, id string, revision int64) (*string, error) {
	jobID, err := s.groundJob.Enqueue(ctx, GroundJob{DinnerID: id, Revision: revision})
	if err == nil {
		return &jobID, nil
	}
	// The error is a database error, never a dependency's text.
	s.logger.ErrorContext(ctx, "dinner grounding not enqueued", "dinner_id", id, "error", err)
	if _, err := db.New(s.st.Writer).FailPendingDinnerGrounding(context.WithoutCancel(ctx), db.FailPendingDinnerGroundingParams{
		GroundingError:    sql.NullString{String: ReasonGroundingError, Valid: true},
		UpdatedAt:         store.Now().String(),
		ID:                id,
		GroundingRevision: revision,
	}); err != nil {
		return nil, fmt.Errorf("dinners: record failed grounding of %s: %w", id, err)
	}
	return nil, nil
}

func (s *Service) createRegular(ctx context.Context, r NewRegular, updatedBy string) (db.Dinner, error) {
	if updatedBy == "" {
		return db.Dinner{}, errors.New("dinners: create: no member to record as updated_by")
	}
	if r.Name == nil {
		return db.Dinner{}, invalid("name", "give the dinner's name, with or without a recipe_url")
	}
	name, err := checkName(*r.Name)
	if err != nil {
		return db.Dinner{}, err
	}
	var recipeURL sql.NullString
	if r.RecipeURL != nil {
		u, err := checkRecipeURL(*r.RecipeURL)
		if err != nil {
			return db.Dinner{}, err
		}
		recipeURL = sql.NullString{String: u.String(), Valid: true}
	}

	var row db.Dinner
	err = s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		if err := checkUnique(ctx, q, "", name, recipeURL); err != nil {
			return err
		}
		now := store.Now().String()
		var err error
		row, err = q.CreateDinner(ctx, db.CreateDinnerParams{
			ID:              store.NewID(),
			Name:            name,
			NormalizedName:  harness.Normalize(name),
			Status:          StatusRegular,
			Origin:          "regular",
			RecipeUrl:       recipeURL,
			GroundingStatus: "pending",
			UpdatedBy:       sql.NullString{String: updatedBy, Valid: true},
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		if err != nil {
			return fmt.Errorf("dinners: create: %w", err)
		}
		return nil
	})
	if err != nil {
		return db.Dinner{}, err
	}
	return row, nil
}

// IdeaOrigins are the origins of a dinner added from an idea: ingredients
// for Cook from what I have, quick for Quick dinners.
var IdeaOrigins = []string{"ingredients", "quick"}

// NewQuestLog is a dinner to add to the Quest log from an idea: its name,
// the recipe the idea was already grounded in, and its origin, one of
// IdeaOrigins.
type NewQuestLog struct {
	Name     string
	RecipeID string
	Origin   string
}

// AddToQuestLog adds n to the Quest log, recording updatedBy: a dinner
// with status quest_log and origin n.Origin, grounded in recipe n.RecipeID
// (its method, minutes, difficulty and photo), and returns it with
// created true. When an unarchived dinner already repeats it, by the same
// rule that refuses a duplicate Regular (a harness.Similar name, or the
// recipe's link), that dinner is returned as it is, with created false,
// so adding the same idea again never makes a second dinner: a Regular
// stays a Regular and a Quest log dinner stays in the Quest log. A
// repeated dinner still awaiting the household's choice (a suggested Side
// quest, or one tried and awaiting its Rotation check) is a
// *DuplicateError and changes nothing: adding it would make that choice
// for them. An unknown recipe is an error.
func (s *Service) AddToQuestLog(ctx context.Context, n NewQuestLog, updatedBy string) (d Dinner, created bool, err error) {
	if updatedBy == "" {
		return Dinner{}, false, errors.New("dinners: add to Quest log: no member to record as updated_by")
	}
	if !slices.Contains(IdeaOrigins, n.Origin) {
		return Dinner{}, false, fmt.Errorf("dinners: add to Quest log: origin %q is not one of %s", n.Origin, strings.Join(IdeaOrigins, ", "))
	}
	name, err := checkName(n.Name)
	if err != nil {
		return Dinner{}, false, err
	}
	var row db.Dinner
	err = s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		recipe, err := q.GetRecipe(ctx, n.RecipeID)
		if err != nil {
			return fmt.Errorf("dinners: add to Quest log: read recipe %s: %w", n.RecipeID, err)
		}
		existing, found, err := findRepeat(ctx, q, "", name, sql.NullString{String: recipe.SourceUrl, Valid: true})
		if err != nil {
			return err
		}
		if found {
			if existing.Status != StatusRegular && existing.Status != StatusQuestLog {
				return &DuplicateError{Existing: fromRow(existing)}
			}
			row = existing
			return nil
		}
		now := store.Now().String()
		row, err = q.CreateDinner(ctx, db.CreateDinnerParams{
			ID:              store.NewID(),
			Name:            name,
			NormalizedName:  harness.Normalize(name),
			Status:          StatusQuestLog,
			Origin:          n.Origin,
			Method:          recipe.Method,
			MinutesTotal:    recipe.MinutesTotal,
			Difficulty:      recipe.Difficulty,
			RecipeID:        sql.NullString{String: recipe.ID, Valid: true},
			PhotoID:         recipe.PhotoID,
			GroundingStatus: GroundingGrounded,
			UpdatedBy:       sql.NullString{String: updatedBy, Valid: true},
			CreatedAt:       now,
			UpdatedAt:       now,
		})
		if err != nil {
			return fmt.Errorf("dinners: add to Quest log: %w", err)
		}
		created = true
		return nil
	})
	if err != nil {
		return Dinner{}, false, err
	}
	return fromRow(row), created, nil
}

// List returns the dinners with status, which must be regular or
// quest_log, ordered by normalized name. The Quest log is the dinners
// accepted and not yet scheduled, so a quest_log dinner on the plan for the
// household's current week or a later one is left out of it.
func (s *Service) List(ctx context.Context, status string) ([]Dinner, error) {
	if !slices.Contains(listable, status) {
		return nil, invalid("status", "must be one of "+strings.Join(listable, ", "))
	}
	var rows []db.Dinner
	var err error
	if status == StatusQuestLog {
		rows, err = s.listQuestLog(ctx)
	} else {
		rows, err = db.New(s.st.Reader).ListDinnersByStatus(ctx, status)
	}
	if err != nil {
		return nil, fmt.Errorf("dinners: list %s: %w", status, err)
	}
	out := make([]Dinner, len(rows))
	for i, row := range rows {
		out[i] = fromRow(row)
	}
	return out, nil
}

// listQuestLog reads the Quest log in one read transaction, so the
// household's current week and the plans come from the same snapshot.
func (s *Service) listQuestLog(ctx context.Context) (rows []db.Dinner, err error) {
	tx, err := s.st.Reader.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin read: %w", err)
	}
	defer func() {
		err = errors.Join(err, tx.Rollback())
	}()
	q := db.New(tx)
	settings, err := q.GetHouseholdSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("read household settings: %w", err)
	}
	loc, err := household.LoadZone(settings.Timezone)
	if err != nil {
		return nil, fmt.Errorf("household settings: %w", err)
	}
	thisWeek := household.WeekStart(household.Today(time.Now(), loc))
	return q.ListUnplannedQuestLogDinners(ctx, thisWeek.String())
}

// Update applies p to dinner id, recording updatedBy. An invalid patch is a
// *ValidationError and a new name repeating another unarchived dinner a
// *DuplicateError; either changes nothing. Renaming a dinner raises its
// grounding_revision and drops the candidates offered for its old name:
// each was offered at an earlier revision, so none of them grounds it.
// Renaming a dinner still pending also enqueues a fresh KindGroundDinner
// job that grounds the new name, and clears its recipe_url when the
// normalized name changes: a job queued or running for the old name or
// link carries the old revision, so it can no longer write the dinner, and
// one running is stopped.
func (s *Service) Update(ctx context.Context, id string, p Patch, updatedBy string) (Dinner, error) {
	if updatedBy == "" {
		return Dinner{}, errors.New("dinners: update: no member to record as updated_by")
	}
	name, err := p.validate()
	if err != nil {
		return Dinner{}, err
	}

	var row db.Dinner
	var renamed, regrounding bool
	err = s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		cur, err := q.GetDinner(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("dinners: read %s: %w", id, err)
		}
		params := db.UpdateDinnerParams{
			ID:                cur.ID,
			Name:              cur.Name,
			NormalizedName:    cur.NormalizedName,
			Status:            cur.Status,
			Origin:            cur.Origin,
			Method:            cur.Method,
			MinutesTotal:      cur.MinutesTotal,
			Difficulty:        cur.Difficulty,
			Cuisine:           cur.Cuisine,
			RecipeID:          cur.RecipeID,
			RecipeUrl:         cur.RecipeUrl,
			PhotoID:           cur.PhotoID,
			GroundingStatus:   cur.GroundingStatus,
			GroundingRevision: cur.GroundingRevision,
			UpdatedBy:         sql.NullString{String: updatedBy, Valid: true},
			UpdatedAt:         store.Now().String(),
		}
		if p.Name != nil {
			if err := checkUnique(ctx, q, cur.ID, name, sql.NullString{}); err != nil {
				return err
			}
			params.Name = name
			params.NormalizedName = harness.Normalize(name)
			if name != cur.Name {
				params.GroundingRevision++
			}
			if cur.GroundingStatus == GroundingPending && params.NormalizedName != cur.NormalizedName {
				// The link was the old dinner's recipe; the new name is
				// grounded by name, and a link can be given by hand.
				params.RecipeUrl = sql.NullString{}
			}
		}
		if p.Method != nil {
			params.Method = sql.NullString{String: *p.Method, Valid: true}
		}
		if p.MinutesTotal != nil {
			params.MinutesTotal = sql.NullInt64{Int64: int64(*p.MinutesTotal), Valid: true}
		}
		if p.Difficulty != nil {
			params.Difficulty = sql.NullString{String: *p.Difficulty, Valid: true}
		}
		row, err = q.UpdateDinner(ctx, params)
		if err != nil {
			return fmt.Errorf("dinners: update %s: %w", id, err)
		}
		renamed = row.GroundingRevision != cur.GroundingRevision
		regrounding = renamed && cur.GroundingStatus == GroundingPending
		return nil
	})
	if err != nil {
		return Dinner{}, err
	}
	if p.Method != nil || p.MinutesTotal != nil {
		s.wake()
	}
	if renamed {
		updateCommitted()
		s.clearOffers(row.ID, row.GroundingRevision)
	}
	if regrounding {
		s.stopGroundings(row.ID, row.GroundingRevision)
		jobID, err := s.enqueueGrounding(ctx, row.ID, row.GroundingRevision)
		if err != nil {
			return Dinner{}, err
		}
		if jobID == nil {
			row, err = db.New(s.st.Reader).GetDinner(context.WithoutCancel(ctx), row.ID)
			if err != nil {
				return Dinner{}, fmt.Errorf("dinners: read %s: %w", id, err)
			}
		}
	}
	return fromRow(row), nil
}

// Archive sets dinner id's status to archived, recording updatedBy. The row
// is kept, so its name still counts for Side quest dedupe. A
// KindGroundDinner job running for it is stopped, and its candidates no
// longer ground it.
func (s *Service) Archive(ctx context.Context, id, updatedBy string) (Dinner, error) {
	if updatedBy == "" {
		return Dinner{}, errors.New("dinners: archive: no member to record as updated_by")
	}
	row, err := db.New(s.st.Writer).ArchiveDinner(ctx, db.ArchiveDinnerParams{
		ID:        id,
		UpdatedBy: sql.NullString{String: updatedBy, Valid: true},
		UpdatedAt: store.Now().String(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Dinner{}, ErrNotFound
	}
	if err != nil {
		return Dinner{}, fmt.Errorf("dinners: archive %s: %w", id, err)
	}
	s.stopGroundings(row.ID, math.MaxInt64)
	s.clearOffers(row.ID, math.MaxInt64)
	return fromRow(row), nil
}

// ToggleFavorite flips dinner id's is_favorite, recording updatedBy.
func (s *Service) ToggleFavorite(ctx context.Context, id, updatedBy string) (Dinner, error) {
	if updatedBy == "" {
		return Dinner{}, errors.New("dinners: favorite: no member to record as updated_by")
	}
	row, err := db.New(s.st.Writer).ToggleDinnerFavorite(ctx, db.ToggleDinnerFavoriteParams{
		ID:        id,
		UpdatedBy: sql.NullString{String: updatedBy, Valid: true},
		UpdatedAt: store.Now().String(),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Dinner{}, ErrNotFound
	}
	if err != nil {
		return Dinner{}, fmt.Errorf("dinners: favorite %s: %w", id, err)
	}
	return fromRow(row), nil
}

// validate checks every field of p and returns its trimmed name.
func (p Patch) validate() (string, error) {
	var problems []Problem
	var name string
	if p.Name == nil && p.Method == nil && p.MinutesTotal == nil && p.Difficulty == nil {
		return "", invalid("patch", "no fields to change")
	}
	if p.Name != nil {
		n, err := checkName(*p.Name)
		var verr *ValidationError
		if errors.As(err, &verr) {
			problems = append(problems, verr.Problems...)
		}
		name = n
	}
	if p.Method != nil && !slices.Contains(Methods, *p.Method) {
		problems = append(problems, Problem{"method", "must be one of " + strings.Join(Methods, ", ")})
	}
	if p.MinutesTotal != nil && (*p.MinutesTotal < 1 || *p.MinutesTotal > MaxMinutes) {
		problems = append(problems, Problem{"minutes_total", fmt.Sprintf("must be 1 to %d", MaxMinutes)})
	}
	if p.Difficulty != nil && !slices.Contains(Difficulties, *p.Difficulty) {
		problems = append(problems, Problem{"difficulty", "must be one of " + strings.Join(Difficulties, ", ")})
	}
	if problems != nil {
		return "", &ValidationError{Problems: problems}
	}
	return name, nil
}

// checkName returns raw trimmed, refusing a name that is empty, too long or
// has no words left once normalized (so it could not be deduplicated).
func checkName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", invalid("name", "must not be empty")
	case utf8.RuneCountInString(name) > MaxNameChars:
		return "", invalid("name", fmt.Sprintf("must be at most %d characters", MaxNameChars))
	case harness.Normalize(name) == "":
		return "", invalid("name", "must name a dish, not only filler words")
	}
	return name, nil
}

// checkRecipeURL parses raw as an absolute https:// link to a recipe page,
// the only kind recipe grounding fetches.
func checkRecipeURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > MaxRecipeURL {
		return nil, invalid("recipe_url", fmt.Sprintf("must be at most %d bytes", MaxRecipeURL))
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return nil, invalid("recipe_url", "is not a URL")
	case u.Scheme != "https" || u.Hostname() == "":
		return nil, invalid("recipe_url", "must be an https:// link")
	case u.User != nil:
		return nil, invalid("recipe_url", "must not carry a user name or password")
	}
	return u, nil
}

// checkUnique returns a *DuplicateError when name is harness.Similar to an
// unarchived dinner other than self, or recipeURL is one's recipe link: the
// source_url of the recipe it is grounded in, else the recipe_url still
// awaiting grounding.
func checkUnique(ctx context.Context, q *db.Queries, self, name string, recipeURL sql.NullString) error {
	row, found, err := findRepeat(ctx, q, self, name, recipeURL)
	if err != nil {
		return err
	}
	if found {
		return &DuplicateError{Existing: fromRow(row)}
	}
	return nil
}

// findRepeat returns the first unarchived dinner other than self that
// name or recipeURL repeats, as checkUnique decides it, and whether there
// is one.
func findRepeat(ctx context.Context, q *db.Queries, self, name string, recipeURL sql.NullString) (db.Dinner, bool, error) {
	rows, err := q.ListUnarchivedDinners(ctx)
	if err != nil {
		return db.Dinner{}, false, fmt.Errorf("dinners: list for dedupe: %w", err)
	}
	for _, row := range rows {
		if row.Dinner.ID == self {
			continue
		}
		link := row.Dinner.RecipeUrl
		if row.RecipeSourceUrl.Valid {
			link = row.RecipeSourceUrl
		}
		if harness.Similar(name, row.Dinner.Name) || (recipeURL.Valid && link == recipeURL) {
			return row.Dinner, true, nil
		}
	}
	return db.Dinner{}, false, nil
}

// PhotoURL is where the API serves a list-sized copy of photo id.
func PhotoURL(id string) string {
	return photoURL(id, photoListWidth)
}

// photoURL is where the API serves photo id at stored width size.
func photoURL(id string, size int) string {
	return fmt.Sprintf("/media/photos/%s-%d.jpg", id, size)
}

func fromRow(r db.Dinner) Dinner {
	d := Dinner{
		ID:              r.ID,
		Name:            r.Name,
		Status:          r.Status,
		Origin:          r.Origin,
		Method:          nullString(r.Method),
		Difficulty:      nullString(r.Difficulty),
		Cuisine:         nullString(r.Cuisine),
		IsFavorite:      r.IsFavorite == 1,
		RecipeID:        nullString(r.RecipeID),
		GroundingStatus: r.GroundingStatus,
		GroundingError:  nullString(r.GroundingError),
		UpdatedBy:       nullString(r.UpdatedBy),
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
	if r.MinutesTotal.Valid {
		d.MinutesTotal = &r.MinutesTotal.Int64
	}
	if r.PhotoID.Valid {
		u := PhotoURL(r.PhotoID.String)
		d.PhotoURL = &u
	}
	return d
}

func nullString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
