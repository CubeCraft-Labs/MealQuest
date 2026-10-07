// Package suggest keeps the household's Side quest deck: batches of new
// dinners the AI suggests, each grounded in a recipe, and the household's
// Try it / Pass decision on each. A batch runs as a job: suggest_side_quests
// proposes Side quests, each is grounded with the recipe grounding pipeline
// (dropping those no recipe grounds), and every one kept is stored as a
// dinner with status suggested and origin side_quest and a pending
// suggestion. Only one batch is pending or running at a time.
package suggest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// KindSuggestionBatch is the job that runs a suggestion batch.
const KindSuggestionBatch = "suggestion_batch"

const (
	// BatchSize is how many Side quests a batch keeps at most.
	BatchSize = 5
	// ReplenishBelow is the deck size under which a decision starts a
	// replenish batch.
	ReplenishBelow = 5
	// ContextBudget is the character budget of the household context
	// suggest_side_quests is given (see harness.BuildContext).
	ContextBudget = 16000
)

// Batch triggers (the suggestion_batches.trigger column).
const (
	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"
	TriggerReplenish = "replenish"
)

// Batch statuses (the suggestion_batches.status column).
const (
	BatchPending = "pending"
	BatchRunning = "running"
	BatchDone    = "done"
	BatchFailed  = "failed"
)

// Suggestion statuses (the suggestions.status column).
const (
	StatusPending  = "pending"
	StatusAccepted = "accepted"
	StatusDeclined = "declined"
)

// Reasons a batch failed, stored as the KindSuggestionBatch job's failure
// code. Any other failure, such as the database's, is the runner's
// internal_error.
const (
	// ReasonNoRegulars: the household has no Regulars for Side quests to
	// be based on.
	ReasonNoRegulars = "no_regulars"
	// ReasonAIFailed: suggest_side_quests, or normalize_recipe while
	// grounding, failed; no Side quest is made up instead (D-017).
	ReasonAIFailed = "ai_failed"
	// ReasonSearchNotConfigured: recipe search is not set up.
	ReasonSearchNotConfigured = "search_not_configured"
	// ReasonSearchFailed: the search provider failed.
	ReasonSearchFailed = "search_failed"
)

// Reasons are every batch failure reason.
var Reasons = []string{ReasonNoRegulars, ReasonAIFailed, ReasonSearchNotConfigured, ReasonSearchFailed}

var triggers = []string{TriggerScheduled, TriggerManual, TriggerReplenish}

// ErrNotFound is returned for an unknown suggestion id.
var ErrNotFound = errors.New("suggestion not found")

// ErrDinnerNotSuggested is a decision on a pending suggestion whose dinner
// was archived or moved out of the deck some other way.
var ErrDinnerNotSuggested = errors.New("the suggestion's dinner is no longer suggested")

// DecidedError is a decision on a suggestion already decided the other
// way.
type DecidedError struct {
	// Status is the suggestion's decision: accepted or declined.
	Status string
}

func (e *DecidedError) Error() string {
	return "the suggestion was already " + e.Status
}

// Suggester runs suggest_side_quests on in; scope names the batch. In
// production it is skills.SuggestSideQuests with the household's
// harness.Config.
type Suggester func(ctx context.Context, scope string, in skills.SideQuestsRequest) (skills.SideQuests, error)

// Service runs suggestion batches and records decisions.
type Service struct {
	st       *store.Store
	grounder *recipes.Grounder
	suggest  Suggester
	logger   *slog.Logger
	batchJob *jobs.Kind[BatchJob]
}

// NewService returns a service over st that suggests with suggest, grounds
// with grounder and registers the KindSuggestionBatch job on runner, which
// must not have started yet.
func NewService(st *store.Store, grounder *recipes.Grounder, suggest Suggester, runner *jobs.Runner, logger *slog.Logger) (*Service, error) {
	switch {
	case st == nil:
		return nil, errors.New("suggest: nil store")
	case grounder == nil:
		return nil, errors.New("suggest: nil grounder")
	case suggest == nil:
		return nil, errors.New("suggest: nil suggester")
	case runner == nil:
		return nil, errors.New("suggest: nil job runner")
	case logger == nil:
		return nil, errors.New("suggest: nil logger")
	}
	s := &Service{st: st, grounder: grounder, suggest: suggest, logger: logger}
	kind, err := jobs.Register(runner, KindSuggestionBatch, s.runBatch, Reasons...)
	if err != nil {
		return nil, err
	}
	s.batchJob = kind
	return s, nil
}

// Batch is a suggestion batch with the id of the job running it.
type Batch struct {
	ID      string `json:"batch_id"`
	JobID   string `json:"job_id"`
	Trigger string `json:"trigger"`
	Status  string `json:"status"`
	// Created says whether this call created the batch rather than found
	// it already pending or running.
	Created bool `json:"-"`
}

// StartBatch creates a batch with trigger and enqueues the
// KindSuggestionBatch job that runs it, unless a batch is already pending
// or running, which it returns instead with Created false.
func (s *Service) StartBatch(ctx context.Context, trigger string) (Batch, error) {
	if !slices.Contains(triggers, trigger) {
		return Batch{}, fmt.Errorf("suggest: unknown batch trigger %q", trigger)
	}
	var b Batch
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		b, err = s.startBatch(ctx, tx, trigger)
		return err
	})
	if err != nil {
		return Batch{}, err
	}
	if b.Created {
		s.batchJob.Wake()
	}
	return b, nil
}

// startBatch does StartBatch's work in tx: the batch and the job that runs
// it are committed together, so a batch is never seen without its job. A
// batch still pending or running whose job is no longer queued or running
// is failed first, so a batch whose job ended without recording its end
// (the process stopped, or the write failed) never blocks a later one. The
// caller wakes the job runner once tx has committed and Created is true.
func (s *Service) startBatch(ctx context.Context, tx *sql.Tx, trigger string) (Batch, error) {
	q := db.New(tx)
	n, err := q.FailOrphanedSuggestionBatches(ctx, db.FailOrphanedSuggestionBatchesParams{
		UpdatedAt: store.Now().String(),
		Kind:      KindSuggestionBatch,
	})
	if err != nil {
		return Batch{}, fmt.Errorf("suggest: fail orphaned batches: %w", err)
	}
	if n > 0 {
		s.logger.WarnContext(ctx, "failed suggestion batches whose job ended", "count", n)
	}
	cur, err := q.GetActiveSuggestionBatch(ctx)
	if err == nil {
		jobID, err := q.GetSuggestionBatchJobID(ctx, db.GetSuggestionBatchJobIDParams{Kind: KindSuggestionBatch, BatchID: cur.ID})
		if err != nil {
			return Batch{}, fmt.Errorf("suggest: read job of batch %s: %w", cur.ID, err)
		}
		return Batch{ID: cur.ID, JobID: jobID, Trigger: cur.Trigger, Status: cur.Status}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Batch{}, fmt.Errorf("suggest: read active batch: %w", err)
	}
	now := store.Now().String()
	row, err := q.CreateSuggestionBatch(ctx, db.CreateSuggestionBatchParams{
		ID:        store.NewID(),
		Trigger:   trigger,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return Batch{}, fmt.Errorf("suggest: create batch: %w", err)
	}
	jobID, err := s.batchJob.EnqueueTx(ctx, tx, BatchJob{BatchID: row.ID})
	if err != nil {
		return Batch{}, fmt.Errorf("suggest: enqueue batch %s: %w", row.ID, err)
	}
	return Batch{ID: row.ID, JobID: jobID, Trigger: row.Trigger, Status: row.Status, Created: true}, nil
}

// finishBatch records running batch id as status. Its job's handler
// returns any error, so the job never ends succeeded with the batch still
// running; the runner keeps only a code, so the error is logged too.
func (s *Service) finishBatch(ctx context.Context, id, status string) error {
	n, err := db.New(s.st.Writer).FinishSuggestionBatch(ctx, db.FinishSuggestionBatchParams{
		Status:    status,
		UpdatedAt: store.Now().String(),
		ID:        id,
	})
	if err == nil && n != 1 {
		err = errors.New("batch is not running")
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "record suggestion batch status", "batch_id", id, "status", status, "error", err)
		return fmt.Errorf("suggest: record batch %s %s: %w", id, status, err)
	}
	return nil
}

// Suggestion is a Side quest suggestion as the API shows it, with its
// dinner.
type Suggestion struct {
	ID           string  `json:"id"`
	DinnerID     string  `json:"dinner_id"`
	Name         string  `json:"name"`
	PhotoURL     *string `json:"photo_url"`
	Difficulty   *string `json:"difficulty"`
	Method       *string `json:"method"`
	MinutesTotal *int64  `json:"minutes_total"`
	Cuisine      *string `json:"cuisine"`
	// Reason is the "Why this?".
	Reason          string   `json:"reason"`
	CitedRegularIDs []string `json:"cited_regular_ids"`
	Status          string   `json:"status"`
	DinnerStatus    string   `json:"dinner_status"`
	DecidedAt       *string  `json:"decided_at"`
	CreatedAt       string   `json:"created_at"`
}

// Deck returns the pending suggestions whose dinner is still suggested,
// oldest first.
func (s *Service) Deck(ctx context.Context) ([]Suggestion, error) {
	rows, err := db.New(s.st.Reader).ListDeck(ctx)
	if err != nil {
		return nil, fmt.Errorf("suggest: list deck: %w", err)
	}
	out := make([]Suggestion, len(rows))
	for i, row := range rows {
		out[i], err = fromRow(row.Suggestion, row.Dinner)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Decision is a suggestion once decided, with the id of the replenish
// batch's job when the decision started one.
type Decision struct {
	Suggestion     Suggestion `json:"suggestion"`
	ReplenishJobID *string    `json:"replenish_job_id"`
}

// Accept records Try it on suggestion id, moving its dinner to the Quest
// log and recording updatedBy on it.
func (s *Service) Accept(ctx context.Context, id, updatedBy string) (Decision, error) {
	return s.decide(ctx, id, StatusAccepted, dinners.StatusQuestLog, updatedBy)
}

// Decline records Pass on suggestion id, archiving its dinner and
// recording updatedBy on it. The suggestion keeps the name its dish was
// suggested under, whatever the dinner is later renamed, and every later
// batch reads it as declined, so the dish is never suggested again.
func (s *Service) Decline(ctx context.Context, id, updatedBy string) (Decision, error) {
	return s.decide(ctx, id, StatusDeclined, dinners.StatusArchived, updatedBy)
}

// decide records decision on suggestion id and moves its dinner to
// dinnerStatus. Repeating a decision changes nothing and returns the
// suggestion as it is; the opposite decision is a *DecidedError. A
// decision that leaves the deck under ReplenishBelow starts a replenish
// batch unless one is already pending or running, in the decision's own
// transaction: the decision is never recorded without the replenish it
// calls for, and a failure to start the batch records neither.
func (s *Service) decide(ctx context.Context, id, decision, dinnerStatus, updatedBy string) (Decision, error) {
	if updatedBy == "" {
		return Decision{}, errors.New("suggest: decide: no member to record as updated_by")
	}
	var row db.GetSuggestionRow
	var replenish *Batch
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		cur, err := q.GetSuggestion(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("suggest: read suggestion %s: %w", id, err)
		}
		switch cur.Suggestion.Status {
		case decision:
			row = cur
			return nil
		case StatusPending:
		default:
			return &DecidedError{Status: cur.Suggestion.Status}
		}
		if cur.Dinner.Status != dinners.StatusSuggested {
			return ErrDinnerNotSuggested
		}
		now := store.Now().String()
		if _, err := q.DecideSuggestion(ctx, db.DecideSuggestionParams{
			Status: decision,
			Now:    sql.NullString{String: now, Valid: true},
			ID:     id,
		}); err != nil {
			return fmt.Errorf("suggest: decide %s: %w", id, err)
		}
		if _, err := q.MoveSuggestedDinner(ctx, db.MoveSuggestedDinnerParams{
			Status:    dinnerStatus,
			UpdatedBy: sql.NullString{String: updatedBy, Valid: true},
			UpdatedAt: now,
			ID:        cur.Dinner.ID,
		}); err != nil {
			return fmt.Errorf("suggest: move dinner %s: %w", cur.Dinner.ID, err)
		}
		row, err = q.GetSuggestion(ctx, id)
		if err != nil {
			return fmt.Errorf("suggest: read suggestion %s: %w", id, err)
		}
		left, err := q.CountDeck(ctx)
		if err != nil {
			return fmt.Errorf("suggest: count deck: %w", err)
		}
		if left >= ReplenishBelow {
			return nil
		}
		b, err := s.startBatch(ctx, tx, TriggerReplenish)
		if err != nil {
			return err
		}
		replenish = &b
		return nil
	})
	if err != nil {
		return Decision{}, err
	}
	created := replenish != nil && replenish.Created
	if created {
		s.batchJob.Wake()
	}
	sug, err := fromRow(row.Suggestion, row.Dinner)
	if err != nil {
		return Decision{}, err
	}
	d := Decision{Suggestion: sug}
	if created {
		d.ReplenishJobID = &replenish.JobID
	}
	return d, nil
}

func fromRow(sg db.Suggestion, d db.Dinner) (Suggestion, error) {
	out := Suggestion{
		ID:           sg.ID,
		DinnerID:     d.ID,
		Name:         d.Name,
		Difficulty:   nullString(d.Difficulty),
		Method:       nullString(d.Method),
		Cuisine:      nullString(d.Cuisine),
		Reason:       sg.Reason,
		Status:       sg.Status,
		DinnerStatus: d.Status,
		DecidedAt:    nullString(sg.DecidedAt),
		CreatedAt:    sg.CreatedAt,
	}
	if err := json.Unmarshal([]byte(sg.CitedRegularIds), &out.CitedRegularIDs); err != nil || out.CitedRegularIDs == nil {
		return Suggestion{}, fmt.Errorf("suggest: suggestion %s cited_regular_ids is not a JSON array: %q", sg.ID, sg.CitedRegularIds)
	}
	if d.MinutesTotal.Valid {
		out.MinutesTotal = &d.MinutesTotal.Int64
	}
	if d.PhotoID.Valid {
		u := dinners.PhotoURL(d.PhotoID.String)
		out.PhotoURL = &u
	}
	return out, nil
}

func nullString(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
