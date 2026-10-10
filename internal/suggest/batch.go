package suggest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// BatchJob is the KindSuggestionBatch payload: the batch to run.
type BatchJob struct {
	BatchID string `json:"batch_id"`
	// Week is the YYYY-MM-DD Monday of the ISO week a scheduled batch is
	// for, which keys its new_suggestions; empty for any other trigger.
	Week string `json:"week,omitempty"`
}

// Batch stages, as the job's result reports them.
const (
	StageSuggesting = "suggesting"
	StageGrounding  = "grounding"
	StageDone       = "done"
)

// batchResult is the KindSuggestionBatch result, published as the batch
// goes: its stage, the suggestions stored so far, and how many Side
// quests were dropped, by suggest_side_quests' rules, because no recipe
// grounds them, or because they repeat a dinner added meanwhile.
type batchResult struct {
	BatchID       string   `json:"batch_id"`
	Stage         string   `json:"stage"`
	SuggestionIDs []string `json:"suggestion_ids"`
	Dropped       int      `json:"dropped"`
}

func (r batchResult) PublicJSON() ([]byte, error) { return json.Marshal(r) }

// runBatch runs batch p.BatchID: it asks suggest_side_quests for BatchSize
// Side quests given the household context, the deck and every passed
// name, then grounds them in the model's order, at most the number still
// wanted at a time, until BatchSize are kept or none are left. A Side quest
// no recipe grounds is dropped; any other grounding failure fails the
// batch, as does suggest_side_quests failing, with no Side quest made up
// instead. The batch ends done or failed. When recording the batch's end
// fails, so does the job; when it times out at shutdown, the job is left
// running and the batch with it, for the next start to fail.
//
// When a batch stores its Side quests depends on whether it notifies (see
// noticeOf). A scheduled or manual batch notifies: it holds every round's
// grounded Side quests and, once the last round is done, stores them all
// in one transaction with one new_suggestions counting and naming every
// Side quest stored. So the household is told of exactly what the batch
// added, once; and a batch that fails at any round, including a later
// round's normalize_recipe, stores nothing and sends nothing, leaving no
// Side quest in the deck that was never announced. A replenish batch,
// which tops up the deck as the household swipes and never notifies,
// stores each round's Side quests before the next round starts, so the
// deck fills as it goes; those stored before a failure stay in the deck.
// See store.
func (s *Service) runBatch(ctx context.Context, p BatchJob, publish func(batchResult) error) (batchResult, error) {
	batch, err := db.New(s.st.Reader).GetSuggestionBatch(ctx, p.BatchID)
	if err != nil {
		return batchResult{}, fmt.Errorf("suggest: read batch %s: %w", p.BatchID, err)
	}
	n, err := db.New(s.st.Writer).StartSuggestionBatch(ctx, db.StartSuggestionBatchParams{
		UpdatedAt: store.Now().String(),
		ID:        p.BatchID,
	})
	if err != nil {
		return batchResult{}, fmt.Errorf("suggest: start batch %s: %w", p.BatchID, err)
	}
	if n == 0 {
		return batchResult{}, fmt.Errorf("suggest: batch %s is not pending", p.BatchID)
	}
	res, err := s.fill(ctx, batch.Trigger, p, publish)
	if err == nil {
		if err := s.finishBatch(ctx, p.BatchID, BatchDone); err != nil {
			return batchResult{}, err
		}
		return res, nil
	}
	finishErr := s.finishBatch(ctx, p.BatchID, BatchFailed)
	if ctx.Err() != nil {
		return batchResult{}, errors.Join(ctx.Err(), finishErr)
	}
	var failure *jobs.Failure
	if errors.As(err, &failure) {
		// Only the code is logged: the error may quote a model's or
		// search provider's response.
		s.logger.InfoContext(ctx, "suggestion batch failed", "batch_id", p.BatchID, "reason", failure.Code)
	}
	return batchResult{}, errors.Join(err, finishErr)
}

// fill does runBatch's work for job of a batch with trigger, returning a
// *jobs.Failure for a failure the household may see. A batch with a notice
// is stored once, when its last round is done; any other batch round by
// round.
func (s *Service) fill(ctx context.Context, trigger string, job BatchJob, publish func(batchResult) error) (batchResult, error) {
	batchID := job.BatchID
	res := batchResult{BatchID: batchID, Stage: StageSuggesting, SuggestionIDs: []string{}}
	if err := publish(res); err != nil {
		return batchResult{}, err
	}
	in, err := s.request(ctx)
	if err != nil {
		return batchResult{}, err
	}
	note, err := noticeOf(trigger, job, in.Context.Today)
	if err != nil {
		return batchResult{}, err
	}
	out, err := s.suggest(ctx, batchID, in)
	if err != nil {
		if ctx.Err() != nil {
			return batchResult{}, ctx.Err()
		}
		return batchResult{}, &jobs.Failure{Code: ReasonAIFailed}
	}
	res.Stage = StageGrounding
	res.Dropped = len(out.Dropped)
	if err := publish(res); err != nil {
		return batchResult{}, err
	}
	left := out.Suggestions
	// held are a notifying batch's grounded Side quests, not yet stored.
	var held []groundedQuest
	for len(res.SuggestionIDs)+len(held) < BatchSize && len(left) > 0 {
		round := left[:min(BatchSize-len(res.SuggestionIDs)-len(held), len(left))]
		left = left[len(round):]
		grounded, err := s.ground(ctx, round)
		if err != nil {
			return batchResult{}, err
		}
		res.Dropped += len(round) - len(grounded)
		if note != nil {
			held = append(held, grounded...)
		} else if err := s.storeInto(ctx, &res, nil, grounded); err != nil {
			return batchResult{}, err
		}
		if err := publish(res); err != nil {
			return batchResult{}, err
		}
	}
	if note != nil {
		if err := s.storeInto(ctx, &res, note, held); err != nil {
			return batchResult{}, err
		}
	}
	res.Stage = StageDone
	return res, nil
}

// storeInto stores quests for res's batch with note (see store) and adds
// the suggestions stored, and the Side quests left out, to res.
func (s *Service) storeInto(ctx context.Context, res *batchResult, note *notice, quests []groundedQuest) error {
	ids, err := s.store(ctx, res.BatchID, note, quests)
	if err != nil {
		return err
	}
	res.Dropped += len(quests) - len(ids)
	res.SuggestionIDs = append(res.SuggestionIDs, ids...)
	return nil
}

// notice is how a batch's new_suggestions is keyed: by id, as
// new_suggestions:{id}, for the week starting on week.
type notice struct {
	id   string
	week store.LocalDate
}

// noticeOf returns the notice of job, run for a batch with trigger on
// household-local date today, or nil for a replenish batch, which never
// notifies. A scheduled batch is keyed by the ISO week it was scheduled
// for ("2026-W41"), not the week it happens to run in, so each week's
// schedule tells the household once even when its batch runs late; a
// manual batch is keyed by its batch id, for the week of today. A job
// whose week does not fit its trigger is an error.
func noticeOf(trigger string, job BatchJob, today store.LocalDate) (*notice, error) {
	if trigger != TriggerScheduled {
		if job.Week != "" {
			return nil, fmt.Errorf("suggest: %s batch %s has a week", trigger, job.BatchID)
		}
		if trigger == TriggerReplenish {
			return nil, nil
		}
		return &notice{id: job.BatchID, week: household.WeekStart(today)}, nil
	}
	week, err := store.ParseLocalDate(job.Week)
	if err != nil {
		return nil, fmt.Errorf("suggest: scheduled batch %s week: %w", job.BatchID, err)
	}
	if household.WeekStart(week) != week {
		return nil, fmt.Errorf("suggest: scheduled batch %s week %s is not a Monday", job.BatchID, week)
	}
	return &notice{id: household.PeriodKey(week), week: week}, nil
}

// request reads what suggest_side_quests is given: the household context,
// the deck's names and every passed name (ListPassedNames). A household with no Regulars
// fails with ReasonNoRegulars.
func (s *Service) request(ctx context.Context) (skills.SideQuestsRequest, error) {
	hctx, err := harness.BuildContext(ctx, s.st, time.Now(), ContextBudget)
	if err != nil {
		return skills.SideQuestsRequest{}, fmt.Errorf("suggest: %w", err)
	}
	if len(hctx.Regulars) == 0 {
		return skills.SideQuestsRequest{}, &jobs.Failure{Code: ReasonNoRegulars}
	}
	q := db.New(s.st.Reader)
	deck, err := q.ListDeck(ctx)
	if err != nil {
		return skills.SideQuestsRequest{}, fmt.Errorf("suggest: list deck: %w", err)
	}
	pending := []string{}
	for _, row := range deck {
		pending = append(pending, row.Dinner.Name)
	}
	declined, err := q.ListPassedNames(ctx)
	if err != nil {
		return skills.SideQuestsRequest{}, fmt.Errorf("suggest: list passed names: %w", err)
	}
	return skills.SideQuestsRequest{Context: hctx, Needed: BatchSize, Pending: pending, Declined: declined}, nil
}

// groundedQuest is a Side quest with the recipe that grounds it.
type groundedQuest struct {
	quest    skills.SideQuest
	grounded recipes.Grounded
}

// ground grounds quests by their search queries and returns those a
// recipe grounds, in order. A Side quest no recipe grounds is left out;
// any other failure fails the batch.
func (s *Service) ground(ctx context.Context, quests []skills.SideQuest) ([]groundedQuest, error) {
	reqs := make([]recipes.GroundRequest, len(quests))
	for i, q := range quests {
		reqs[i] = recipes.GroundRequest{DishName: q.Name, Query: q.SearchQuery}
	}
	results := s.grounder.GroundMany(ctx, reqs)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []groundedQuest
	for i, r := range results {
		var ungroundable *recipes.ErrUngroundable
		switch {
		case r.Err == nil:
			out = append(out, groundedQuest{quest: quests[i], grounded: r.Grounded})
		case errors.As(r.Err, &ungroundable):
		case errors.Is(r.Err, recipes.ErrSearchNotConfigured):
			return nil, &jobs.Failure{Code: ReasonSearchNotConfigured}
		case errors.Is(r.Err, recipes.ErrSearchFailed):
			return nil, &jobs.Failure{Code: ReasonSearchFailed}
		case errors.Is(r.Err, recipes.ErrNormalizeFailed):
			return nil, &jobs.Failure{Code: ReasonAIFailed}
		default:
			return nil, fmt.Errorf("suggest: ground %q: %w", quests[i].Name, r.Err)
		}
	}
	return out, nil
}

// store stores each grounded Side quest of batchID as a dinner with status
// suggested and origin side_quest, grounded in its recipe, and a pending
// suggestion citing its Regulars by id, all in one transaction, and
// returns the suggestion ids. A Side quest is left out when, since
// suggest_side_quests ran, a dinner it repeats was added, a suggestion it
// repeats passed or a Rotation check dropped, or a Regular it cites renamed
// or archived. Passed names are read in the same transaction as the store,
// so a Pass or drop committed before it is never missed.
//
// When note is not nil and a Side quest is stored, new_suggestions keyed by
// note, counting and naming those stored, is emitted in the same
// transaction, so the Side quests and their notification commit together
// or not at all: a notification that cannot be stored fails the store, and
// the batch with it. A notifying batch calls store once, with all its
// Side quests, so the count is the batch's (see runBatch). This is the
// only place new_suggestions is emitted.
func (s *Service) store(ctx context.Context, batchID string, note *notice, quests []groundedQuest) ([]string, error) {
	ids := []string{}
	var stored []string
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		existing, err := q.ListUnarchivedDinners(ctx)
		if err != nil {
			return fmt.Errorf("suggest: list dinners for dedupe: %w", err)
		}
		names, err := q.ListPassedNames(ctx)
		if err != nil {
			return fmt.Errorf("suggest: list passed names for dedupe: %w", err)
		}
		regulars := map[string][]string{}
		for _, row := range existing {
			names = append(names, row.Dinner.Name)
			if row.Dinner.Status == dinners.StatusRegular {
				regulars[row.Dinner.Name] = append(regulars[row.Dinner.Name], row.Dinner.ID)
			}
		}
		for _, gq := range quests {
			cited, ok := citedIDs(gq.quest.CitedRegulars, regulars)
			if !ok || repeats(gq.quest.Name, names) {
				s.logger.InfoContext(ctx, "suggested dinner dropped: changed while grounding", "batch_id", batchID)
				continue
			}
			citedJSON, err := json.Marshal(cited)
			if err != nil {
				return fmt.Errorf("suggest: encode cited Regulars: %w", err)
			}
			now := store.Now().String()
			gr := gq.grounded
			d, err := q.CreateDinner(ctx, db.CreateDinnerParams{
				ID:              store.NewID(),
				Name:            gq.quest.Name,
				NormalizedName:  harness.Normalize(gq.quest.Name),
				Status:          dinners.StatusSuggested,
				Origin:          "side_quest",
				Method:          sql.NullString{String: gr.Method, Valid: true},
				MinutesTotal:    nullInt(gr.MinutesTotal),
				Difficulty:      sql.NullString{String: gr.Difficulty, Valid: true},
				Cuisine:         sql.NullString{String: gq.quest.Cuisine, Valid: true},
				RecipeID:        sql.NullString{String: gr.RecipeID, Valid: true},
				PhotoID:         sql.NullString{String: gr.PhotoID, Valid: true},
				GroundingStatus: dinners.GroundingGrounded,
				CreatedAt:       now,
				UpdatedAt:       now,
			})
			if err != nil {
				return fmt.Errorf("suggest: create dinner: %w", err)
			}
			id := store.NewID()
			if err := q.CreateSuggestion(ctx, db.CreateSuggestionParams{
				ID:              id,
				DinnerID:        d.ID,
				BatchID:         batchID,
				Name:            d.Name,
				Reason:          gq.quest.Reason,
				CitedRegularIds: string(citedJSON),
				CreatedAt:       now,
				UpdatedAt:       now,
			}); err != nil {
				return fmt.Errorf("suggest: create suggestion: %w", err)
			}
			names = append(names, d.Name)
			ids = append(ids, id)
			stored = append(stored, d.Name)
		}
		if note == nil || len(stored) == 0 {
			return nil
		}
		return s.notifier.EmitTx(ctx, tx, chat.NewSuggestions(note.id, note.week, stored))
	})
	if err != nil {
		return nil, err
	}
	if note != nil && len(stored) > 0 {
		s.notifier.Wake()
	}
	return ids, nil
}

// citedIDs returns the ids of the Regulars named cited, false when one
// names no Regular or more than one.
func citedIDs(cited []string, regulars map[string][]string) ([]string, bool) {
	ids := make([]string, len(cited))
	for i, name := range cited {
		if len(regulars[name]) != 1 {
			return nil, false
		}
		ids[i] = regulars[name][0]
	}
	return ids, true
}

// repeats reports whether name is harness.Similar to any of names.
func repeats(name string, names []string) bool {
	for _, n := range names {
		if harness.Similar(name, n) {
			return true
		}
	}
	return false
}

func nullInt(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}
