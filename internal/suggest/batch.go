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
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// BatchJob is the KindSuggestionBatch payload: the batch to run.
type BatchJob struct {
	BatchID string `json:"batch_id"`
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
// instead. Each round's grounded Side quests are stored before the next
// round starts, so the deck fills as the batch goes. The batch ends done
// or failed; Side quests stored before a failure stay in the deck. When
// recording the batch's end fails, so does the job; when it times out at
// shutdown, the job is left running and the batch with it, for the next
// start to fail.
//
// A scheduled or manual batch that ends done having added Side quests to
// the deck emits new_suggestions, keyed by the batch, in the transaction
// that records it done. A replenish batch, which tops up the deck as the
// household swipes, never notifies. A notification that cannot be stored
// fails the done write with it, as any failure recording the batch's end
// does: the job fails, and its Side quests stay in the deck.
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
	f, err := s.fill(ctx, p.BatchID, publish)
	if err == nil {
		var ev *notify.Event
		if batch.Trigger != TriggerReplenish && len(f.names) > 0 {
			e := chat.NewSuggestions(p.BatchID, household.WeekStart(f.today), f.names)
			ev = &e
		}
		if err := s.finishBatch(ctx, p.BatchID, BatchDone, ev); err != nil {
			return batchResult{}, err
		}
		return f.res, nil
	}
	finishErr := s.finishBatch(ctx, p.BatchID, BatchFailed, nil)
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

// filled is what fill did: the batch's result, the names of the Side
// quests it added to the deck, and the household's date it ran on.
type filled struct {
	res   batchResult
	names []string
	today store.LocalDate
}

// fill does runBatch's work, returning a *jobs.Failure for a failure the
// household may see.
func (s *Service) fill(ctx context.Context, batchID string, publish func(batchResult) error) (filled, error) {
	res := batchResult{BatchID: batchID, Stage: StageSuggesting, SuggestionIDs: []string{}}
	if err := publish(res); err != nil {
		return filled{}, err
	}
	in, err := s.request(ctx)
	if err != nil {
		return filled{}, err
	}
	out, err := s.suggest(ctx, batchID, in)
	if err != nil {
		if ctx.Err() != nil {
			return filled{}, ctx.Err()
		}
		return filled{}, &jobs.Failure{Code: ReasonAIFailed}
	}
	res.Stage = StageGrounding
	res.Dropped = len(out.Dropped)
	if err := publish(res); err != nil {
		return filled{}, err
	}
	var names []string
	left := out.Suggestions
	for len(res.SuggestionIDs) < BatchSize && len(left) > 0 {
		round := left[:min(BatchSize-len(res.SuggestionIDs), len(left))]
		left = left[len(round):]
		grounded, err := s.ground(ctx, round)
		if err != nil {
			return filled{}, err
		}
		res.Dropped += len(round) - len(grounded)
		ids, stored, err := s.store(ctx, batchID, grounded)
		if err != nil {
			return filled{}, err
		}
		res.Dropped += len(grounded) - len(ids)
		res.SuggestionIDs = append(res.SuggestionIDs, ids...)
		names = append(names, stored...)
		if err := publish(res); err != nil {
			return filled{}, err
		}
	}
	res.Stage = StageDone
	return filled{res: res, names: names, today: in.Context.Today}, nil
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
// so a Pass or drop committed before it is never missed. The names stored
// are returned with the ids.
func (s *Service) store(ctx context.Context, batchID string, quests []groundedQuest) ([]string, []string, error) {
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
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return ids, stored, nil
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
