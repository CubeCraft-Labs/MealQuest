package dinners

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// KindGroundDinner is the job that grounds a new Regular in a recipe.
const KindGroundDinner = "ground_dinner"

// Grounding statuses (the dinners.grounding_status column).
const (
	GroundingPending  = "pending"
	GroundingGrounded = "grounded"
	GroundingFailed   = "failed"
	GroundingManual   = "manual"
)

// Reasons a dinner's background grounding failed, stored in
// dinners.grounding_error and as the KindGroundDinner job's failure code.
const (
	// ReasonNoRecipe: no page the search found holds a usable recipe.
	ReasonNoRecipe = "no_recipe_found"
	// ReasonRecipeUnusable: the dinner's recipe_url holds no usable recipe.
	ReasonRecipeUnusable = "recipe_unusable"
	// ReasonSearchNotConfigured: recipe search is not set up.
	ReasonSearchNotConfigured = "search_not_configured"
	// ReasonSearchFailed: the search provider failed.
	ReasonSearchFailed = "search_failed"
	// ReasonAIFailed: normalize_recipe's model provider could not be built
	// or called.
	ReasonAIFailed = "ai_failed"
	// ReasonInterrupted: the server stopped while grounding.
	ReasonInterrupted = "interrupted"
	// ReasonGroundingError: any other failure, such as the database's.
	ReasonGroundingError = "grounding_error"
)

// Reasons are every grounding failure reason.
var Reasons = []string{
	ReasonNoRecipe, ReasonRecipeUnusable, ReasonSearchNotConfigured, ReasonSearchFailed,
	ReasonAIFailed, ReasonInterrupted, ReasonGroundingError,
}

// Limits on candidates and on a recipe entered by hand.
const (
	MaxCandidates     = 3
	MaxManualLines    = 100
	MaxManualLineChar = 500
	// manualSourcePrefix starts the source_url of a recipe entered by hand,
	// followed by the recipe's id; it is never a fetchable page.
	manualSourcePrefix = "mealquest:manual:"
)

// GroundTimeout bounds grounding a candidate or link by hand, which runs in
// the request: the page fetch (recipes.Timeout), its photo download and
// normalize_recipe (harness.DefaultTimeout), with some slack.
const GroundTimeout = 90 * time.Second

// groundTimeout is GroundTimeout; tests shorten it.
var groundTimeout = GroundTimeout

// ErrGroundTimeout is returned by Ground when grounding a candidate or link
// runs past GroundTimeout; the dinner is left as it was.
var ErrGroundTimeout = errors.New("dinners: grounding the recipe took too long")

// ErrGroundFailed wraps any unexpected failure grounding a dinner by hand.
// Its text may quote the link and dependency errors, so it is never
// logged.
var ErrGroundFailed = errors.New("dinners: grounding the recipe failed")

// ErrArchived is grounding an archived dinner by hand.
var ErrArchived = errors.New("dinner archived")

// ErrGroundConflict is a candidate or link grounded for a dinner that was
// renamed or grounded some other way while it was grounding.
var ErrGroundConflict = errors.New("dinner changed while its recipe was grounding")

// GroundJob is the KindGroundDinner payload: the dinner to ground and its
// grounding_revision when the job was enqueued.
type GroundJob struct {
	DinnerID string `json:"dinner_id"`
	Revision int64  `json:"revision"`
}

// groundResult is the KindGroundDinner result: the dinner's grounding
// status once the job ends. It is not grounded by this job when the dinner
// was grounded by hand before the job finished, renamed (a later job
// grounds it) or archived; the job then reports the status it found.
type groundResult struct {
	DinnerID        string `json:"dinner_id"`
	GroundingStatus string `json:"grounding_status"`
}

func (r groundResult) PublicJSON() ([]byte, error) { return json.Marshal(r) }

// runGroundJob grounds a pending dinner as its creation asked: its
// recipe_url with GroundURL, else its name with Ground. Success records
// the recipe and photo with grounding_status grounded, and the recipe's
// method, minutes and difficulty where a member has not set them; failure
// records grounding_status failed with its Reason, which is also the job's
// failure code. A dinner archived, no longer pending, or at a
// grounding_revision other than p.Revision is not grounded at all, and
// either result is recorded only while the dinner is still unarchived,
// pending and at p.Revision: a dinner grounded by hand meanwhile is left as
// it is, and one renamed or archived meanwhile is left pending — for the
// job its rename enqueued, or for none. Archiving or renaming the dinner
// stops the grounding where it is, so no further search, fetch, photo
// download or model call is made for it.
func (s *Service) runGroundJob(ctx context.Context, p GroundJob, _ func(groundResult) error) (groundResult, error) {
	// Tracked before the dinner is read: an archive or rename committed
	// after the read finds this job to stop.
	groundCtx, done := s.trackGrounding(ctx, p.DinnerID, p.Revision)
	defer done()
	row, err := db.New(s.st.Reader).GetDinner(ctx, p.DinnerID)
	if err != nil {
		return groundResult{}, fmt.Errorf("dinners: ground: read %s: %w", p.DinnerID, err)
	}
	if row.Status == StatusArchived || row.GroundingStatus != GroundingPending || row.GroundingRevision != p.Revision {
		return groundResult{DinnerID: row.ID, GroundingStatus: row.GroundingStatus}, nil
	}

	var gr recipes.Grounded
	if row.RecipeUrl.Valid {
		gr, err = s.grounder.GroundURL(groundCtx, row.RecipeUrl.String)
	} else {
		gr, err = s.grounder.Ground(groundCtx, row.Name, row.Name)
	}
	if err != nil && ctx.Err() == nil && errors.Is(context.Cause(groundCtx), errGroundingStopped) {
		return s.currentGrounding(ctx, row.ID)
	}
	if err != nil {
		reason := reasonFor(err, row.RecipeUrl.Valid)
		if ctx.Err() != nil {
			reason = ReasonInterrupted
		}
		// Only the reason is logged: the error may quote a dependency's
		// response, which jobs never store, return or log.
		s.logger.InfoContext(ctx, "dinner grounding failed", "dinner_id", row.ID, "reason", reason)
		write := context.WithoutCancel(ctx)
		if _, failErr := db.New(s.st.Writer).FailPendingDinnerGrounding(write, db.FailPendingDinnerGroundingParams{
			GroundingError:    sql.NullString{String: reason, Valid: true},
			UpdatedAt:         store.Now().String(),
			ID:                row.ID,
			GroundingRevision: p.Revision,
		}); failErr != nil {
			return groundResult{}, fmt.Errorf("dinners: ground: record failure of %s: %w", row.ID, failErr)
		}
		if ctx.Err() != nil {
			return groundResult{}, ctx.Err()
		}
		return groundResult{}, &jobs.Failure{Code: reason}
	}

	n, err := db.New(s.st.Writer).GroundPendingDinner(ctx, db.GroundPendingDinnerParams{
		RecipeID:          sql.NullString{String: gr.RecipeID, Valid: true},
		PhotoID:           sql.NullString{String: gr.PhotoID, Valid: true},
		Method:            sql.NullString{String: gr.Method, Valid: true},
		MinutesTotal:      nullInt(gr.MinutesTotal),
		Difficulty:        sql.NullString{String: gr.Difficulty, Valid: true},
		UpdatedAt:         store.Now().String(),
		ID:                row.ID,
		GroundingRevision: p.Revision,
	})
	if err != nil {
		return groundResult{}, fmt.Errorf("dinners: ground: record %s: %w", row.ID, err)
	}
	if n == 0 {
		return s.currentGrounding(ctx, row.ID)
	}
	return groundResult{DinnerID: row.ID, GroundingStatus: GroundingGrounded}, nil
}

// currentGrounding is the KindGroundDinner result of a job that did not
// ground dinner id: the grounding status the dinner has now.
func (s *Service) currentGrounding(ctx context.Context, id string) (groundResult, error) {
	cur, err := db.New(s.st.Reader).GetDinner(ctx, id)
	if err != nil {
		return groundResult{}, fmt.Errorf("dinners: ground: read %s: %w", id, err)
	}
	return groundResult{DinnerID: cur.ID, GroundingStatus: cur.GroundingStatus}, nil
}

// errGroundingStopped is the cause a running KindGroundDinner job's
// grounding is cancelled with when its dinner is archived or renamed.
var errGroundingStopped = errors.New("dinners: grounding stopped: dinner archived or renamed")

// runningGrounding is a KindGroundDinner job running for a dinner at
// revision, with the cancel of its grounding.
type runningGrounding struct {
	revision int64
	cancel   context.CancelCauseFunc
}

// trackGrounding returns a context for grounding dinnerID at revision that
// stopGroundings cancels, and the func to call when the job ends.
func (s *Service) trackGrounding(ctx context.Context, dinnerID string, revision int64) (context.Context, func()) {
	groundCtx, cancel := context.WithCancelCause(ctx)
	r := &runningGrounding{revision: revision, cancel: cancel}
	s.groundingsMu.Lock()
	if s.groundings[dinnerID] == nil {
		s.groundings[dinnerID] = make(map[*runningGrounding]struct{})
	}
	s.groundings[dinnerID][r] = struct{}{}
	s.groundingsMu.Unlock()
	return groundCtx, func() {
		s.groundingsMu.Lock()
		delete(s.groundings[dinnerID], r)
		if len(s.groundings[dinnerID]) == 0 {
			delete(s.groundings, dinnerID)
		}
		s.groundingsMu.Unlock()
		cancel(nil)
	}
}

// stopGroundings cancels, with errGroundingStopped, the grounding of every
// KindGroundDinner job running for dinnerID at a revision below below. Call
// it once the change that makes those jobs stale is committed.
func (s *Service) stopGroundings(dinnerID string, below int64) {
	s.groundingsMu.Lock()
	defer s.groundingsMu.Unlock()
	for r := range s.groundings[dinnerID] {
		if r.revision < below {
			r.cancel(errGroundingStopped)
		}
	}
}

// FailInterruptedGroundings fails, with ReasonInterrupted, every unarchived
// dinner still pending whose KindGroundDinner job is no longer queued or
// running: a previous process stopped before enqueuing the job or before
// its handler recorded a result. An archived dinner is left pending, since
// archiving, not a restart, stopped its grounding. Call it once at boot, after the job runner's
// Start has failed the jobs that process left running, so no dinner stays
// pending with nothing left to ground it. It returns how many it failed.
func (s *Service) FailInterruptedGroundings(ctx context.Context) (int64, error) {
	n, err := db.New(s.st.Writer).FailOrphanedPendingDinnerGroundings(ctx, db.FailOrphanedPendingDinnerGroundingsParams{
		GroundingError: sql.NullString{String: ReasonInterrupted, Valid: true},
		UpdatedAt:      store.Now().String(),
		Kind:           KindGroundDinner,
	})
	if err != nil {
		return 0, fmt.Errorf("dinners: fail interrupted groundings: %w", err)
	}
	if n > 0 {
		s.logger.WarnContext(ctx, "failed dinner groundings interrupted by restart", "count", n)
	}
	return n, nil
}

// reasonFor is the Reason for a grounding error; fromURL says whether the
// dinner's recipe_url was grounded rather than its name.
func reasonFor(err error, fromURL bool) string {
	var ungroundable *recipes.ErrUngroundable
	switch {
	case errors.As(err, &ungroundable) && fromURL:
		return ReasonRecipeUnusable
	case errors.As(err, &ungroundable):
		return ReasonNoRecipe
	case errors.Is(err, recipes.ErrSearchNotConfigured):
		return ReasonSearchNotConfigured
	case errors.Is(err, recipes.ErrSearchFailed):
		return ReasonSearchFailed
	case errors.Is(err, recipes.ErrNormalizeFailed):
		return ReasonAIFailed
	}
	return ReasonGroundingError
}

// Candidate is a recipe page offered to ground a dinner in.
type Candidate struct {
	// CandidateID is an opaque id for the page, resolved by the server:
	// Fix.CandidateID grounds it. The page's URL is never returned, as it
	// may carry a token.
	CandidateID string `json:"candidate_id"`
	Title       string `json:"title"`
	SiteName    string `json:"site_name"`
	// PhotoURL is the cached photo of a page already grounded, else null.
	PhotoURL *string `json:"photo_url"`
	Minutes  *int    `json:"minutes"`
}

// Candidates searches for dinner id's name and returns up to MaxCandidates
// recipe pages to ground it in, best first; the list may be empty. They
// replace the dinner's earlier candidates: one not offered again no longer
// grounds it. A search that fails wraps recipes.ErrSearchFailed.
func (s *Service) Candidates(ctx context.Context, id string) ([]Candidate, error) {
	row, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	offers, err := s.grounder.Candidates(ctx, row.Name, row.Name, MaxCandidates)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, len(offers))
	urls := make(map[string]string, len(offers))
	for i, o := range offers {
		cid := s.candidateID(row.ID, o.URL)
		urls[cid] = o.URL
		out[i] = Candidate{CandidateID: cid, Title: o.Title, SiteName: o.SiteName, Minutes: o.Minutes}
		if o.PhotoID != "" {
			u := PhotoURL(o.PhotoID)
			out[i].PhotoURL = &u
		}
	}
	s.offersMu.Lock()
	s.offers[row.ID] = urls
	s.offersMu.Unlock()
	return out, nil
}

// candidateID is the opaque id of the page at rawURL as a candidate for
// dinner id: the same page is offered under the same id until the service
// restarts, and the id reveals nothing of the URL.
func (s *Service) candidateID(id, rawURL string) string {
	mac := hmac.New(sha256.New, s.offerKey)
	mac.Write([]byte(id + "\x00" + rawURL))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// candidateURL is the page URL of candidate cid last offered for dinner id.
// A cid not among the dinner's candidates last offered since the service
// started, or whose page is not a valid recipe link, is a
// *ValidationError.
func (s *Service) candidateURL(id, cid string) (string, error) {
	s.offersMu.Lock()
	raw, ok := s.offers[id][cid]
	s.offersMu.Unlock()
	if ok {
		if u, err := checkRecipeURL(raw); err == nil {
			return u.String(), nil
		}
	}
	return "", invalid("candidate_id", "is not a candidate from recipe-candidates; reload the candidates")
}

// Fix grounds a dinner by hand: exactly one of a candidate from
// Candidates, a pasted recipe link or a recipe typed in.
type Fix struct {
	CandidateID *string
	RecipeURL   *string
	Manual      *ManualRecipe
}

// ManualRecipe is a recipe typed in by the household.
type ManualRecipe struct {
	// Ingredients are the ingredient lines, at least one.
	Ingredients []string
	// Steps are the instructions, in order; there may be none.
	Steps   []string
	Minutes *int
	Method  *string
}

// Ground grounds dinner id as f says, recording updatedBy. A candidate or
// link is grounded with recipes.Grounder.GroundURL and sets
// grounding_status grounded, with the recipe's photo, method, minutes and
// difficulty; one a member changed while it was grounding is kept. A
// manual recipe is stored as typed — no model runs — and sets
// grounding_status manual with its method and minutes; it has no photo,
// and the dinner keeps its difficulty. An invalid f is a *ValidationError
// and an archived dinner ErrArchived. A failure grounding a candidate or
// link, or recording it on the dinner, wraps ErrGroundFailed; a page that
// cannot ground the dinner is also a *recipes.ErrUngroundable. A dinner
// renamed, archived or grounded some other way while a candidate or link
// was grounding is ErrGroundConflict or ErrArchived. A missing dinner is
// ErrNotFound; any other failure, by candidate, link or manual recipe,
// wraps ErrGroundFailed. None of these changes the dinner.
func (s *Service) Ground(ctx context.Context, id string, f Fix, updatedBy string) (Dinner, error) {
	if updatedBy == "" {
		return Dinner{}, errors.New("dinners: ground: no member to record as updated_by")
	}
	link, manual, err := f.validate()
	if err != nil {
		return Dinner{}, err
	}
	if manual != nil {
		return s.groundManual(ctx, id, *manual, updatedBy)
	}
	cur, err := s.get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Dinner{}, err
	}
	if err != nil {
		return Dinner{}, fmt.Errorf("%w: dinner %s: %w", ErrGroundFailed, id, err)
	}
	if cur.Status == StatusArchived {
		return Dinner{}, ErrArchived
	}
	if f.CandidateID != nil {
		if link, err = s.candidateURL(cur.ID, *f.CandidateID); err != nil {
			return Dinner{}, err
		}
	}
	groundCtx, cancel := context.WithTimeout(ctx, groundTimeout)
	defer cancel()
	gr, err := s.grounder.GroundURL(groundCtx, link)
	if err != nil {
		if ctx.Err() == nil && errors.Is(groundCtx.Err(), context.DeadlineExceeded) {
			return Dinner{}, ErrGroundTimeout
		}
		return Dinner{}, fmt.Errorf("%w: dinner %s: %w", ErrGroundFailed, id, err)
	}
	row, err := db.New(s.st.Writer).GroundDinner(ctx, db.GroundDinnerParams{
		RecipeID:                  sql.NullString{String: gr.RecipeID, Valid: true},
		PhotoID:                   sql.NullString{String: gr.PhotoID, Valid: true},
		Method:                    sql.NullString{String: gr.Method, Valid: true},
		MinutesTotal:              nullInt(gr.MinutesTotal),
		Difficulty:                sql.NullString{String: gr.Difficulty, Valid: true},
		GroundingStatus:           GroundingGrounded,
		UpdatedBy:                 sql.NullString{String: updatedBy, Valid: true},
		UpdatedAt:                 store.Now().String(),
		ID:                        cur.ID,
		ObservedName:              cur.Name,
		ObservedNormalizedName:    cur.NormalizedName,
		ObservedGroundingStatus:   cur.GroundingStatus,
		ObservedGroundingRevision: cur.GroundingRevision,
		ObservedRecipeID:          cur.RecipeID,
		ObservedMethod:            cur.Method,
		ObservedMinutesTotal:      cur.MinutesTotal,
		ObservedDifficulty:        cur.Difficulty,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Dinner{}, s.groundRejected(ctx, id)
	}
	if err != nil {
		return Dinner{}, fmt.Errorf("%w: dinner %s: record: %w", ErrGroundFailed, id, err)
	}
	s.offersMu.Lock()
	delete(s.offers, cur.ID)
	s.offersMu.Unlock()
	return fromRow(row), nil
}

// groundRejected is the error for dinner id when it was not as observed
// before grounding: ErrNotFound, ErrArchived or ErrGroundConflict. Failing
// to read it wraps ErrGroundFailed.
func (s *Service) groundRejected(ctx context.Context, id string) error {
	cur, err := s.get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("%w: dinner %s: %w", ErrGroundFailed, id, err)
	}
	if cur.Status == StatusArchived {
		return ErrArchived
	}
	return ErrGroundConflict
}

// groundManual stores m as dinner id's recipe, with its ingredients and
// steps, and records it on the dinner, all in one transaction. A missing
// or archived dinner is ErrNotFound or ErrArchived; any other failure
// wraps ErrGroundFailed.
func (s *Service) groundManual(ctx context.Context, id string, m ManualRecipe, updatedBy string) (Dinner, error) {
	var row db.Dinner
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		cur, err := q.GetDinner(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("dinners: read %s: %w", id, err)
		}
		if cur.Status == StatusArchived {
			return ErrArchived
		}
		ld, err := json.Marshal(manualJSONLD{
			Context:      "https://schema.org",
			Type:         "Recipe",
			Name:         cur.Name,
			Ingredients:  m.Ingredients,
			Instructions: m.Steps,
			TotalTime:    fmt.Sprintf("PT%dM", *m.Minutes),
		})
		if err != nil {
			return fmt.Errorf("dinners: encode manual recipe: %w", err)
		}
		now := store.Now().String()
		recipeID := store.NewID()
		minutes := sql.NullInt64{Int64: int64(*m.Minutes), Valid: true}
		method := sql.NullString{String: *m.Method, Valid: true}
		rec, err := q.CreateManualRecipe(ctx, db.CreateManualRecipeParams{
			ID:           recipeID,
			SourceUrl:    manualSourcePrefix + recipeID,
			Title:        cur.Name,
			TotalMinutes: minutes,
			RawJsonld:    string(ld),
			Method:       method,
			MinutesTotal: minutes,
			FetchedAt:    now,
			CreatedAt:    now,
			UpdatedAt:    now,
		})
		if err != nil {
			return fmt.Errorf("dinners: store manual recipe: %w", err)
		}
		for i, line := range m.Ingredients {
			if err := q.CreateRecipeIngredient(ctx, db.CreateRecipeIngredientParams{
				ID:        store.NewID(),
				RecipeID:  rec.ID,
				Position:  int64(i),
				RawText:   line,
				CreatedAt: now,
				UpdatedAt: now,
			}); err != nil {
				return fmt.Errorf("dinners: store manual ingredient %d: %w", i+1, err)
			}
		}
		for i, text := range m.Steps {
			if err := q.CreateRecipeStep(ctx, db.CreateRecipeStepParams{
				ID:        store.NewID(),
				RecipeID:  rec.ID,
				Position:  int64(i),
				Text:      text,
				CreatedAt: now,
				UpdatedAt: now,
			}); err != nil {
				return fmt.Errorf("dinners: store manual step %d: %w", i+1, err)
			}
		}
		row, err = q.GroundDinner(ctx, db.GroundDinnerParams{
			RecipeID:                  sql.NullString{String: rec.ID, Valid: true},
			Method:                    method,
			MinutesTotal:              minutes,
			Difficulty:                cur.Difficulty,
			GroundingStatus:           GroundingManual,
			UpdatedBy:                 sql.NullString{String: updatedBy, Valid: true},
			UpdatedAt:                 now,
			ID:                        cur.ID,
			ObservedName:              cur.Name,
			ObservedNormalizedName:    cur.NormalizedName,
			ObservedGroundingStatus:   cur.GroundingStatus,
			ObservedGroundingRevision: cur.GroundingRevision,
			ObservedRecipeID:          cur.RecipeID,
			ObservedMethod:            cur.Method,
			ObservedMinutesTotal:      cur.MinutesTotal,
			ObservedDifficulty:        cur.Difficulty,
		})
		if err != nil {
			return fmt.Errorf("dinners: ground %s: %w", id, err)
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrArchived) {
		return Dinner{}, err
	}
	if err != nil {
		return Dinner{}, fmt.Errorf("%w: dinner %s: %w", ErrGroundFailed, id, err)
	}
	return fromRow(row), nil
}

// manualJSONLD is the schema.org Recipe stored as a manual recipe's
// raw_jsonld, so it reads like a fetched page's.
type manualJSONLD struct {
	Context      string   `json:"@context"`
	Type         string   `json:"@type"`
	Name         string   `json:"name"`
	Ingredients  []string `json:"recipeIngredient"`
	Instructions []string `json:"recipeInstructions"`
	TotalTime    string   `json:"totalTime"`
}

// validate checks f and returns the link to ground, or the manual recipe
// with its lines trimmed.
func (f Fix) validate() (link string, manual *ManualRecipe, err error) {
	given := 0
	for _, set := range []bool{f.CandidateID != nil, f.RecipeURL != nil, f.Manual != nil} {
		if set {
			given++
		}
	}
	if given != 1 {
		return "", nil, invalid("ground", "give exactly one of candidate_id, recipe_url and manual")
	}
	switch {
	case f.CandidateID != nil:
		// Ground resolves the candidate to its page.
		return "", nil, nil
	case f.RecipeURL != nil:
		u, err := checkRecipeURL(*f.RecipeURL)
		if err != nil {
			return "", nil, err
		}
		return u.String(), nil, nil
	}
	m, err := f.Manual.validate()
	if err != nil {
		return "", nil, err
	}
	return "", &m, nil
}

// validate checks every field of m and returns it with its lines trimmed.
func (m ManualRecipe) validate() (ManualRecipe, error) {
	var problems []Problem
	ings, ingProblem := checkLines(m.Ingredients, 1)
	if ingProblem != "" {
		problems = append(problems, Problem{"manual.ingredients", ingProblem})
	}
	steps, stepProblem := checkLines(m.Steps, 0)
	if stepProblem != "" {
		problems = append(problems, Problem{"manual.steps", stepProblem})
	}
	if m.Minutes == nil || *m.Minutes < 1 || *m.Minutes > MaxMinutes {
		problems = append(problems, Problem{"manual.minutes", fmt.Sprintf("must be 1 to %d", MaxMinutes)})
	}
	if m.Method == nil || !slices.Contains(Methods, *m.Method) {
		problems = append(problems, Problem{"manual.method", "must be one of " + strings.Join(Methods, ", ")})
	}
	if problems != nil {
		return ManualRecipe{}, &ValidationError{Problems: problems}
	}
	return ManualRecipe{Ingredients: ings, Steps: steps, Minutes: m.Minutes, Method: m.Method}, nil
}

// checkLines returns lines trimmed, or why they are refused: fewer than
// least, more than MaxManualLines, or one blank or over MaxManualLineChar.
func checkLines(lines []string, least int) ([]string, string) {
	if len(lines) < least {
		return nil, fmt.Sprintf("must have at least %d line", least)
	}
	if len(lines) > MaxManualLines {
		return nil, fmt.Sprintf("must have at most %d lines", MaxManualLines)
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			return nil, fmt.Sprintf("line %d is blank", i+1)
		case utf8.RuneCountInString(line) > MaxManualLineChar:
			return nil, fmt.Sprintf("line %d is over %d characters", i+1, MaxManualLineChar)
		}
		out[i] = line
	}
	return out, ""
}

// get reads dinner id, or ErrNotFound.
func (s *Service) get(ctx context.Context, id string) (db.Dinner, error) {
	row, err := db.New(s.st.Reader).GetDinner(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Dinner{}, ErrNotFound
	}
	if err != nil {
		return db.Dinner{}, fmt.Errorf("dinners: read %s: %w", id, err)
	}
	return row, nil
}

func nullInt(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}
