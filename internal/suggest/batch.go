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
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
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
// Side quests given the household context, the deck and every declined
// name, then grounds them in the model's order, at most the number still
// wanted at a time, until BatchSize are kept or none are left. A Side quest
// no recipe grounds is dropped; any other grounding failure fails the
// batch, as does suggest_side_quests failing, with no Side quest made up
// instead. Each round's grounded Side quests are stored before the next
// round starts, so the deck fills as the batch goes. The batch ends done
// or failed; Side quests stored before a failure stay in the deck.
func (s *Service) runBatch(ctx context.Context, p BatchJob, publish func(batchResult) error) (batchResult, error) {
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
	res, err := s.fill(ctx, p.BatchID, publish)
	write := context.WithoutCancel(ctx)
	if err == nil {
		s.finishBatch(write, p.BatchID, BatchDone)
		return res, nil
	}
	s.finishBatch(write, p.BatchID, BatchFailed)
	if ctx.Err() != nil {
		return batchResult{}, ctx.Err()
	}
	var failure *jobs.Failure
	if errors.As(err, &failure) {
		// Only the code is logged: the error may quote a model's or
		// search provider's response.
		s.logger.InfoContext(ctx, "suggestion batch failed", "batch_id", p.BatchID, "reason", failure.Code)
	}
	return batchResult{}, err
}

// fill does runBatch's work, returning a *jobs.Failure for a failure the
// household may see.
func (s *Service) fill(ctx context.Context, batchID string, publish func(batchResult) error) (batchResult, error) {
	res := batchResult{BatchID: batchID, Stage: StageSuggesting, SuggestionIDs: []string{}}
	if err := publish(res); err != nil {
		return batchResult{}, err
	}
	in, err := s.request(ctx)
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
	for len(res.SuggestionIDs) < BatchSize && len(left) > 0 {
		round := left[:min(BatchSize-len(res.SuggestionIDs), len(left))]
		left = left[len(round):]
		grounded, err := s.ground(ctx, round)
		if err != nil {
			return batchResult{}, err
		}
		res.Dropped += len(round) - len(grounded)
		ids, err := s.store(ctx, batchID, grounded)
		if err != nil {
			return batchResult{}, err
		}
		res.Dropped += len(grounded) - len(ids)
		res.SuggestionIDs = append(res.SuggestionIDs, ids...)
		if err := publish(res); err != nil {
			return batchResult{}, err
		}
	}
	res.Stage = StageDone
	return res, nil
}

// request reads what suggest_side_quests is given: the household context,
// the deck's names and every declined name. A household with no Regulars
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
	declined, err := q.ListDeclinedSuggestionNames(ctx)
	if err != nil {
		return skills.SideQuestsRequest{}, fmt.Errorf("suggest: list declined names: %w", err)
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
// suggest_side_quests ran, a dinner it repeats was added or a Regular it
// cites renamed or archived.
func (s *Service) store(ctx context.Context, batchID string, quests []groundedQuest) ([]string, error) {
	ids := []string{}
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		existing, err := q.ListUnarchivedDinners(ctx)
		if err != nil {
			return fmt.Errorf("suggest: list dinners for dedupe: %w", err)
		}
		var names []string
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
				Reason:          gq.quest.Reason,
				CitedRegularIds: string(citedJSON),
				CreatedAt:       now,
				UpdatedAt:       now,
			}); err != nil {
				return fmt.Errorf("suggest: create suggestion: %w", err)
			}
			names = append(names, d.Name)
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
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
