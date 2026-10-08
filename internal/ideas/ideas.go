// Package ideas finds dinner ideas for Cook from what I have: from a list
// of ingredients on hand, ideas_from_ingredients proposes dinners, each is
// grounded with the recipe grounding pipeline (dropping those no recipe
// grounds), and the ideas kept are reported as a job's result, which fills
// as each idea grounds. A job that keeps no idea, or fails, shows no idea:
// nothing is made up instead (D-017). Ideas are not stored as dinners: the
// household acts on one later.
package ideas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
)

// KindFromIngredients is the job that finds ideas from ingredients.
const KindFromIngredients = "ideas_from_ingredients"

// QuickMinutes is the most minutes from prep to table of a Quick dinner
// not cooked in a crock pot.
const QuickMinutes = 30

// Job stages, as the job's result reports them.
const (
	StageSuggesting = "suggesting"
	StageGrounding  = "grounding"
	StageDone       = "done"
)

// Reasons the job failed, stored as its failure code. Any other failure,
// such as the database's, is the runner's internal_error.
const (
	// ReasonAIFailed: ideas_from_ingredients, or normalize_recipe while
	// grounding, failed; no idea is made up instead (D-017).
	ReasonAIFailed = "ai_failed"
	// ReasonSearchNotConfigured: recipe search is not set up.
	ReasonSearchNotConfigured = "search_not_configured"
	// ReasonSearchFailed: the search provider failed.
	ReasonSearchFailed = "search_failed"
	// ReasonNoIdeasFound: every idea was dropped, by
	// ideas_from_ingredients' rules, because no recipe grounds it, or, for
	// a Quick only request, because its grounded recipe is not a Quick
	// dinner.
	ReasonNoIdeasFound = "no_ideas_found"
)

// Reasons are every failure reason of the job.
var Reasons = []string{ReasonAIFailed, ReasonSearchNotConfigured, ReasonSearchFailed, ReasonNoIdeasFound}

// InvalidError is a request the household must correct, such as an empty
// ingredient list.
type InvalidError struct {
	Message string
}

func (e *InvalidError) Error() string { return e.Message }

// Generator runs ideas_from_ingredients on in; scope is
// skills.IdeasScope(in). In production it is skills.IdeasFromIngredients
// with the household's harness.Config.
type Generator func(ctx context.Context, scope string, in skills.IdeasRequest) (skills.Ideas, error)

// Service starts and runs idea jobs.
type Service struct {
	grounder *recipes.Grounder
	generate Generator
	logger   *slog.Logger
	job      *jobs.Kind[FromIngredientsJob]
}

// NewService returns a service that finds ideas with generate, grounds
// them with grounder and registers the KindFromIngredients job on runner,
// which must not have started yet. The job is started only by
// StartFromIngredients, which checks the ingredient list first.
func NewService(grounder *recipes.Grounder, generate Generator, runner *jobs.Runner, logger *slog.Logger) (*Service, error) {
	switch {
	case grounder == nil:
		return nil, errors.New("ideas: nil grounder")
	case generate == nil:
		return nil, errors.New("ideas: nil generator")
	case runner == nil:
		return nil, errors.New("ideas: nil job runner")
	case logger == nil:
		return nil, errors.New("ideas: nil logger")
	}
	s := &Service{grounder: grounder, generate: generate, logger: logger}
	kind, err := jobs.Register(runner, KindFromIngredients, s.run, Reasons...)
	if err != nil {
		return nil, err
	}
	if err := kind.ServiceOnly(); err != nil {
		return nil, err
	}
	if err := kind.FailedResult(withoutIdeas); err != nil {
		return nil, err
	}
	s.job = kind
	return s, nil
}

// FromIngredientsJob is the KindFromIngredients payload: the ingredients
// on hand, as skills.CleanIngredients returns them, and whether only
// Quick dinners are wanted.
type FromIngredientsJob struct {
	Ingredients []string `json:"ingredients"`
	QuickOnly   bool     `json:"quick_only"`
}

// Started is the job a request started.
type Started struct {
	JobID string `json:"jobId"`
}

// StartFromIngredients enqueues a KindFromIngredients job for ingredients
// and returns its id. An ingredient list skills.CleanIngredients refuses
// is an *InvalidError, and starts nothing.
func (s *Service) StartFromIngredients(ctx context.Context, ingredients []string, quickOnly bool) (Started, error) {
	ings, err := skills.CleanIngredients(ingredients)
	if err != nil {
		return Started{}, &InvalidError{Message: "ingredients: " + err.Error()}
	}
	id, err := s.job.Enqueue(ctx, FromIngredientsJob{Ingredients: ings, QuickOnly: quickOnly})
	if err != nil {
		return Started{}, fmt.Errorf("ideas: enqueue: %w", err)
	}
	return Started{JobID: id}, nil
}

// Idea is a grounded idea as the job's result shows it. Method,
// MinutesTotal and Difficulty are the grounded recipe's, never the
// model's estimates.
type Idea struct {
	Name string `json:"name"`
	// Uses are the household's ingredients the idea uses, as the household
	// wrote them and in its order.
	Uses []string `json:"uses"`
	// Missing are the ingredients the idea needs that the household does
	// not have.
	Missing []string `json:"missing"`
	// Summary is the line shown under the idea, e.g. "Uses 3 of 5 ·
	// missing: 1 lime" (see skills.IdeaSummary).
	Summary string `json:"summary"`
	// Method is stovetop, oven, crockpot, grill or other.
	Method string `json:"method"`
	// MinutesTotal is prep to table, null when the recipe does not say.
	MinutesTotal *int `json:"minutes_total"`
	// Difficulty is easy, medium or hard.
	Difficulty string `json:"difficulty"`
	// Quick says whether the idea is a Quick dinner (see IsQuick).
	Quick     bool   `json:"quick"`
	RecipeID  string `json:"recipe_id"`
	PhotoURL  string `json:"photo_url"`
	SourceURL string `json:"source_url"`
	SiteName  string `json:"site_name"`
}

// Result is the KindFromIngredients result, published as the job goes:
// its stage, the request, the ideas grounded so far in the model's order
// (an idea that grounds sooner than one before it is shown first, and the
// earlier one is put back in its place when it grounds),
// and how many ideas were dropped, by ideas_from_ingredients' rules,
// because no recipe grounds them, or, for a Quick only request, because
// the grounded recipe is not a Quick dinner.
type Result struct {
	Stage       string   `json:"stage"`
	Ingredients []string `json:"ingredients"`
	QuickOnly   bool     `json:"quick_only"`
	Ideas       []Idea   `json:"ideas"`
	Dropped     int      `json:"dropped"`
}

func (r Result) PublicJSON() ([]byte, error) { return json.Marshal(r) }

// IsQuick reports whether a recipe cooked by method in minutes (nil when
// unknown) is a Quick dinner: cooked in a crock pot, or 30 minutes or less
// from prep to table.
func IsQuick(method string, minutes *int) bool {
	return method == "crockpot" || (minutes != nil && *minutes <= QuickMinutes)
}

// run runs a KindFromIngredients job: it asks ideas_from_ingredients for
// skills.IdeasRequested ideas, then grounds them in the model's order, at
// most the number still wanted at a time, until skills.IdeasKept are kept
// or none are left. An idea no recipe grounds is dropped, and so, for a
// Quick only request, is one whose grounded recipe is not a Quick dinner;
// when every idea is dropped the job fails with ReasonNoIdeasFound. Any
// other grounding failure fails the job, as does ideas_from_ingredients
// failing, with no idea made up instead. The result is published at each
// stage and again as each idea is kept, so it fills as the ideas ground.
// Before the job fails, its result is published again with no ideas, to
// tidy the stored result. That write is not what keeps a failed job from
// showing ideas, since it may fail and a job interrupted by restart never
// makes it: withoutIdeas, applied whenever a failed job is read, is.
func (s *Service) run(ctx context.Context, p FromIngredientsJob, publish func(Result) error) (Result, error) {
	res, err := s.fill(ctx, p, publish)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, err
		}
		var failure *jobs.Failure
		if errors.As(err, &failure) {
			// Only the code is logged: the error may quote a model's or
			// search provider's response.
			s.logger.InfoContext(ctx, "ideas from ingredients failed", "reason", failure.Code)
		}
		if len(res.Ideas) > 0 {
			res.Ideas = []Idea{}
			if perr := publish(res); perr != nil {
				return Result{}, fmt.Errorf("ideas: clear failed job's ideas: %w", perr)
			}
		}
		return Result{}, err
	}
	return res, nil
}

// fill does run's work, returning a *jobs.Failure for a failure the
// household may see. On failure it returns the result last published with
// the error, so run can clear its ideas.
func (s *Service) fill(ctx context.Context, p FromIngredientsJob, publish func(Result) error) (Result, error) {
	res := Result{Stage: StageSuggesting, Ingredients: p.Ingredients, QuickOnly: p.QuickOnly, Ideas: []Idea{}}
	if err := publish(res); err != nil {
		return res, err
	}
	in := skills.IdeasRequest{Ingredients: p.Ingredients, QuickOnly: p.QuickOnly}
	out, err := s.generate(ctx, skills.IdeasScope(in), in)
	if err != nil {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, &jobs.Failure{Code: ReasonAIFailed}
	}
	res.Stage = StageGrounding
	res.Dropped = len(out.Dropped)
	if err := publish(res); err != nil {
		return res, err
	}
	// order[k] is the index in out.Ideas of res.Ideas[k], so each idea is
	// put in its place in the model's order as it grounds.
	var order []int
	for next := 0; len(res.Ideas) < skills.IdeasKept && next < len(out.Ideas); {
		first := next
		round := out.Ideas[first:min(first+skills.IdeasKept-len(res.Ideas), len(out.Ideas))]
		next += len(round)
		err := s.ground(ctx, round, func(i int, gr *recipes.Grounded) error {
			if gr == nil {
				res.Dropped++
				return nil
			}
			idea := ideaOf(round[i], *gr)
			if p.QuickOnly && !idea.Quick {
				res.Dropped++
				return nil
			}
			at := len(order)
			for at > 0 && order[at-1] > first+i {
				at--
			}
			order = slices.Insert(order, at, first+i)
			res.Ideas = slices.Insert(res.Ideas, at, idea)
			return publish(res)
		})
		if err != nil {
			return res, err
		}
	}
	if len(res.Ideas) == 0 {
		return res, &jobs.Failure{Code: ReasonNoIdeasFound}
	}
	res.Stage = StageDone
	return res, nil
}

// ground grounds ideas by their search queries and calls kept, one call at
// a time, as each idea grounds, with its index in ideas and its
// grounding, or nil when no recipe grounds it. Any other grounding
// failure, or kept failing, stops the groundings still running and is
// returned, as a *jobs.Failure when the household may see it.
func (s *Service) ground(ctx context.Context, ideas []skills.Idea, kept func(i int, gr *recipes.Grounded) error) error {
	groundCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	reqs := make([]recipes.GroundRequest, len(ideas))
	for i, idea := range ideas {
		reqs[i] = recipes.GroundRequest{DishName: idea.Name, Query: idea.SearchQuery}
	}
	var failed error
	s.grounder.GroundEach(groundCtx, reqs, func(i int, r recipes.GroundResult) {
		if failed != nil {
			return
		}
		var ungroundable *recipes.ErrUngroundable
		switch {
		case r.Err == nil:
			failed = kept(i, &r.Grounded)
		case errors.As(r.Err, &ungroundable):
			failed = kept(i, nil)
		case errors.Is(r.Err, recipes.ErrSearchNotConfigured):
			failed = &jobs.Failure{Code: ReasonSearchNotConfigured}
		case errors.Is(r.Err, recipes.ErrSearchFailed):
			failed = &jobs.Failure{Code: ReasonSearchFailed}
		case errors.Is(r.Err, recipes.ErrNormalizeFailed):
			failed = &jobs.Failure{Code: ReasonAIFailed}
		default:
			failed = fmt.Errorf("ideas: ground %q: %w", ideas[i].Name, r.Err)
		}
		if failed != nil {
			cancel()
		}
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	return failed
}

// withoutIdeas is a failed job's stored result as the job shows it: with no
// ideas, however the job failed, so a failed job never shows ideas kept
// before the failure.
func withoutIdeas(stored json.RawMessage) (json.RawMessage, error) {
	var res Result
	if err := json.Unmarshal(stored, &res); err != nil {
		return nil, fmt.Errorf("ideas: decode failed job's result: %w", err)
	}
	res.Ideas = []Idea{}
	return res.PublicJSON()
}

// ideaOf is idea as grounded by gr.
func ideaOf(idea skills.Idea, gr recipes.Grounded) Idea {
	return Idea{
		Name:         idea.Name,
		Uses:         idea.Uses,
		Missing:      idea.Missing,
		Summary:      idea.Summary,
		Method:       gr.Method,
		MinutesTotal: gr.MinutesTotal,
		Difficulty:   gr.Difficulty,
		Quick:        IsQuick(gr.Method, gr.MinutesTotal),
		RecipeID:     gr.RecipeID,
		PhotoURL:     dinners.PhotoURL(gr.PhotoID),
		SourceURL:    gr.SourceURL,
		SiteName:     gr.SiteName,
	}
}
