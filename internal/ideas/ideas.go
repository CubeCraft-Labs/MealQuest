// Package ideas finds dinner ideas for Cook from what I have and Quick
// dinners: from a list of ingredients on hand, ideas_from_ingredients
// proposes dinners, and for a kind of Quick dinner, quick_ideas does; each
// is grounded with the recipe grounding pipeline (dropping those no recipe
// grounds, and Quick dinners whose grounded recipe is not of the kind), and
// the ideas kept are reported as a job's result, which fills as each idea
// grounds. A job that keeps no idea, or fails, shows no idea: nothing is
// made up instead (D-017). Ideas are not stored as dinners: the household
// acts on one later.
package ideas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Job kinds.
const (
	// KindFromIngredients is the job that finds ideas from ingredients.
	KindFromIngredients = "ideas_from_ingredients"
	// KindQuick is the job that finds a page of Quick dinner ideas.
	KindQuick = "quick_ideas"
)

// ContextBudget is the character budget of the household context
// quick_ideas is given (see harness.BuildContext).
const ContextBudget = 16000

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
	// ReasonAIFailed: ideas_from_ingredients or quick_ideas, or
	// normalize_recipe while grounding, failed; no idea is made up instead
	// (D-017).
	ReasonAIFailed = "ai_failed"
	// ReasonSearchNotConfigured: recipe search is not set up.
	ReasonSearchNotConfigured = "search_not_configured"
	// ReasonSearchFailed: the search provider failed.
	ReasonSearchFailed = "search_failed"
	// ReasonNoIdeasFound: every idea was dropped, by the skill's rules,
	// because no recipe grounds it, or because its grounded recipe is not
	// a Quick dinner (a Quick only request) or not of the kind asked for (a
	// Quick dinners request).
	ReasonNoIdeasFound = "no_ideas_found"
)

// Reasons are every failure reason of both jobs.
var Reasons = []string{ReasonAIFailed, ReasonSearchNotConfigured, ReasonSearchFailed, ReasonNoIdeasFound}

// InvalidError is a request the household must correct, such as an empty
// ingredient list or an unknown kind of Quick dinner.
type InvalidError struct {
	Message string
}

func (e *InvalidError) Error() string { return e.Message }

// Generator runs ideas_from_ingredients on in; scope is
// skills.IdeasScope(in). In production it is skills.IdeasFromIngredients
// with the household's harness.Config.
type Generator func(ctx context.Context, scope string, in skills.IdeasRequest) (skills.Ideas, error)

// QuickGenerator runs quick_ideas on in; scope is skills.QuickIdeasScope(in).
// In production it is skills.QuickIdeas with the household's
// harness.Config.
type QuickGenerator func(ctx context.Context, scope string, in skills.QuickIdeasRequest) (skills.QuickIdeaList, error)

// Service starts and runs idea jobs.
type Service struct {
	st       *store.Store
	grounder *recipes.Grounder
	runner   *jobs.Runner
	generate Generator
	quick    QuickGenerator
	logger   *slog.Logger
	job      *jobs.Kind[FromIngredientsJob]
	quickJob *jobs.Kind[QuickJob]
}

// NewService returns a service that reads the household context from st,
// finds ideas with generate and Quick dinner ideas with quick, grounds
// them with grounder and registers the KindFromIngredients and KindQuick
// jobs on runner, which must not have started yet. The jobs are started
// only by StartFromIngredients and StartQuick, which check the request
// first.
func NewService(st *store.Store, grounder *recipes.Grounder, generate Generator, quick QuickGenerator, runner *jobs.Runner, logger *slog.Logger) (*Service, error) {
	switch {
	case st == nil:
		return nil, errors.New("ideas: nil store")
	case grounder == nil:
		return nil, errors.New("ideas: nil grounder")
	case generate == nil:
		return nil, errors.New("ideas: nil generator")
	case quick == nil:
		return nil, errors.New("ideas: nil quick generator")
	case runner == nil:
		return nil, errors.New("ideas: nil job runner")
	case logger == nil:
		return nil, errors.New("ideas: nil logger")
	}
	s := &Service{st: st, grounder: grounder, runner: runner, generate: generate, quick: quick, logger: logger}
	var err error
	if s.job, err = register(runner, KindFromIngredients, s.run, withoutIdeas[Result]); err != nil {
		return nil, err
	}
	if s.quickJob, err = register(runner, KindQuick, s.runQuick, withoutIdeas[QuickResult]); err != nil {
		return nil, err
	}
	return s, nil
}

// register registers the service-only job name, run by run, whose failed
// jobs show their result through failed.
func register[P any, R jobs.Public](runner *jobs.Runner, name string, run jobs.Handler[P, R], failed func(json.RawMessage) (json.RawMessage, error)) (*jobs.Kind[P], error) {
	kind, err := jobs.Register(runner, name, run, Reasons...)
	if err != nil {
		return nil, err
	}
	if err := kind.ServiceOnly(); err != nil {
		return nil, err
	}
	if err := kind.FailedResult(failed); err != nil {
		return nil, err
	}
	return kind, nil
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
// Before the job fails, its result is published again with no ideas (see
// finish).
func (s *Service) run(ctx context.Context, p FromIngredientsJob, publish func(Result) error) (Result, error) {
	res, err := s.fill(ctx, p, publish)
	return finish(ctx, s.logger, "ideas from ingredients", res, err, publish)
}

// shownResult is a job result that shows ideas.
type shownResult[R any] interface {
	jobs.Public
	// shown is how many ideas the result shows.
	shown() int
	// cleared is the result showing no ideas.
	cleared() R
}

func (r Result) shown() int { return len(r.Ideas) }

func (r Result) cleared() Result {
	r.Ideas = []Idea{}
	return r
}

// finish ends a job whose work returned res and err. A job that failed
// has its result published again with no ideas, to tidy the stored
// result. That write is not what keeps a failed job from showing ideas,
// since it may fail and a job interrupted by restart never makes it:
// withoutIdeas, applied whenever a failed job is read, is.
func finish[R shownResult[R]](ctx context.Context, logger *slog.Logger, what string, res R, err error, publish func(R) error) (R, error) {
	var zero R
	if err == nil {
		return res, nil
	}
	if ctx.Err() != nil {
		return zero, err
	}
	var failure *jobs.Failure
	if errors.As(err, &failure) {
		// Only the code is logged: the error may quote a model's or
		// search provider's response.
		logger.InfoContext(ctx, what+" failed", "reason", failure.Code)
	}
	if res.shown() > 0 {
		if perr := publish(res.cleared()); perr != nil {
			return zero, fmt.Errorf("ideas: clear failed job's ideas: %w", perr)
		}
	}
	return zero, err
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
	reqs := make([]recipes.GroundRequest, len(out.Ideas))
	for i, idea := range out.Ideas {
		reqs[i] = recipes.GroundRequest{DishName: idea.Name, Query: idea.SearchQuery}
	}
	err = keepGrounded(ctx, s.grounder, reqs, skills.IdeasKept, &res.Ideas, &res.Dropped, func(i int, gr recipes.Grounded) (Idea, bool) {
		idea := ideaOf(out.Ideas[i], gr)
		return idea, !p.QuickOnly || idea.Quick
	}, func() error { return publish(res) })
	if err != nil {
		return res, err
	}
	if len(res.Ideas) == 0 {
		return res, &jobs.Failure{Code: ReasonNoIdeasFound}
	}
	res.Stage = StageDone
	return res, nil
}

// keepGrounded grounds reqs with g in their order, at most the number
// still wanted at a time, until want ideas are shown or none are left. As
// each grounds, keep makes its idea from request i's grounding and says
// whether it is kept: a kept idea is put in *shown in its place in reqs'
// order (an idea that grounds sooner than one before it is shown first,
// and the earlier one is put back in its place when it grounds) and
// publish is called. An idea no recipe grounds, or that keep refuses,
// counts in *dropped. Any other grounding failure, or publish failing, is
// returned (see ground).
func keepGrounded[T any](ctx context.Context, g *recipes.Grounder, reqs []recipes.GroundRequest, want int, shown *[]T, dropped *int, keep func(i int, gr recipes.Grounded) (T, bool), publish func() error) error {
	// order[k] is the index in reqs of (*shown)[k], so each idea is put in
	// its place in reqs' order as it grounds.
	var order []int
	for next := 0; len(*shown) < want && next < len(reqs); {
		first := next
		round := reqs[first:min(first+want-len(*shown), len(reqs))]
		next += len(round)
		err := ground(ctx, g, round, func(i int, gr *recipes.Grounded) error {
			if gr == nil {
				*dropped++
				return nil
			}
			idea, ok := keep(first+i, *gr)
			if !ok {
				*dropped++
				return nil
			}
			at := len(order)
			for at > 0 && order[at-1] > first+i {
				at--
			}
			order = slices.Insert(order, at, first+i)
			*shown = slices.Insert(*shown, at, idea)
			return publish()
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// ground grounds reqs with g and calls kept, one call at a time, as each
// request grounds, with its index in reqs and its grounding, or nil when
// no recipe grounds it. Any other grounding failure, or kept failing,
// stops the groundings still running and is returned, as a *jobs.Failure
// when the household may see it.
func ground(ctx context.Context, g *recipes.Grounder, reqs []recipes.GroundRequest, kept func(i int, gr *recipes.Grounded) error) error {
	groundCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var failed error
	g.GroundEach(groundCtx, reqs, func(i int, r recipes.GroundResult) {
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
			failed = fmt.Errorf("ideas: ground %q: %w", reqs[i].DishName, r.Err)
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
func withoutIdeas[R shownResult[R]](stored json.RawMessage) (json.RawMessage, error) {
	var res R
	if err := json.Unmarshal(stored, &res); err != nil {
		return nil, fmt.Errorf("ideas: decode failed job's result: %w", err)
	}
	return res.cleared().PublicJSON()
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

// QuickJob is the KindQuick payload: the kind of Quick dinner wanted,
// skills.QuickCrockpot or skills.QuickUnder30, and the names already shown
// to the household, as skills.CleanExclude returns them.
type QuickJob struct {
	Kind    string   `json:"kind"`
	Exclude []string `json:"exclude"`
}

// StartQuick enqueues a KindQuick job for a page of Quick dinner ideas of
// kind, none repeating a name in exclude, and returns its id. A kind
// skills.CheckQuickKind refuses, with no default, or an exclude list
// skills.CleanExclude refuses, is an *InvalidError, and starts nothing.
func (s *Service) StartQuick(ctx context.Context, kind string, exclude []string) (Started, error) {
	if err := skills.CheckQuickKind(kind); err != nil {
		return Started{}, &InvalidError{Message: "kind: " + err.Error()}
	}
	names, err := skills.CleanExclude(exclude)
	if err != nil {
		return Started{}, &InvalidError{Message: "exclude: " + err.Error()}
	}
	id, err := s.quickJob.Enqueue(ctx, QuickJob{Kind: kind, Exclude: names})
	if err != nil {
		return Started{}, fmt.Errorf("ideas: enqueue: %w", err)
	}
	return Started{JobID: id}, nil
}

// QuickIdea is a grounded Quick dinner idea as the job's result shows it.
// Method, MinutesTotal and Difficulty are the grounded recipe's, never the
// model's estimates, and always make it of the kind asked for (see
// OfQuickKind).
type QuickIdea struct {
	Name string `json:"name"`
	// Method is stovetop, oven, crockpot, grill or other: crockpot for
	// skills.QuickCrockpot.
	Method string `json:"method"`
	// MinutesTotal is prep to table, null when the recipe does not say:
	// never null, and at most QuickMinutes, for skills.QuickUnder30.
	MinutesTotal *int `json:"minutes_total"`
	// Difficulty is easy, medium or hard.
	Difficulty string `json:"difficulty"`
	RecipeID   string `json:"recipe_id"`
	PhotoURL   string `json:"photo_url"`
	SourceURL  string `json:"source_url"`
	SiteName   string `json:"site_name"`
}

// QuickResult is the KindQuick result, published as the job goes: its
// stage, the request, the ideas grounded so far in the model's order (as
// in Result), and how many ideas were dropped, by quick_ideas' rules
// (repeating a name already shown or an earlier idea), because no recipe
// grounds them, or because the grounded recipe is not of the kind.
type QuickResult struct {
	Stage   string      `json:"stage"`
	Kind    string      `json:"kind"`
	Exclude []string    `json:"exclude"`
	Ideas   []QuickIdea `json:"ideas"`
	Dropped int         `json:"dropped"`
}

func (r QuickResult) PublicJSON() ([]byte, error) { return json.Marshal(r) }

func (r QuickResult) shown() int { return len(r.Ideas) }

func (r QuickResult) cleared() QuickResult {
	r.Ideas = []QuickIdea{}
	return r
}

// OfQuickKind reports whether a recipe cooked by method in minutes (nil
// when unknown) is a Quick dinner of kind: for skills.QuickCrockpot,
// cooked in a crock pot (normalize_recipe writes a slow cooker as
// crockpot); for skills.QuickUnder30, known to be 30 minutes or less from
// prep to table. Any other kind is none.
func OfQuickKind(kind, method string, minutes *int) bool {
	switch kind {
	case skills.QuickCrockpot:
		return method == "crockpot"
	case skills.QuickUnder30:
		return minutes != nil && *minutes <= QuickMinutes
	}
	return false
}

// runQuick runs a KindQuick job: from the household context, it asks
// quick_ideas for skills.QuickIdeasRequested ideas of the kind, more than
// a page holds, then grounds them in the model's order, at most the number
// still wanted at a time, until skills.QuickIdeasKept are kept or none are
// left. An idea no recipe grounds is dropped, and so is one whose grounded
// recipe is not of the kind (OfQuickKind): the model's estimate never
// keeps an idea. When fewer than skills.QuickIdeasKept survive, the page
// shows only those: fewer is honest, and it is never padded with an idea
// not of the kind. When every idea is dropped the job fails with
// ReasonNoIdeasFound. Any other grounding failure fails the job, as does
// quick_ideas failing, with no idea made up instead. The result is
// published at each stage and again as each idea is kept, and a failed job
// shows no ideas, as for KindFromIngredients (see finish).
func (s *Service) runQuick(ctx context.Context, p QuickJob, publish func(QuickResult) error) (QuickResult, error) {
	res, err := s.fillQuick(ctx, p, publish)
	return finish(ctx, s.logger, "quick ideas", res, err, publish)
}

// fillQuick does runQuick's work as fill does run's.
func (s *Service) fillQuick(ctx context.Context, p QuickJob, publish func(QuickResult) error) (QuickResult, error) {
	res := QuickResult{Stage: StageSuggesting, Kind: p.Kind, Exclude: p.Exclude, Ideas: []QuickIdea{}}
	if err := publish(res); err != nil {
		return res, err
	}
	hctx, err := harness.BuildContext(ctx, s.st, time.Now(), ContextBudget)
	if err != nil {
		return res, fmt.Errorf("ideas: %w", err)
	}
	in := skills.QuickIdeasRequest{Context: hctx, Kind: p.Kind, Exclude: p.Exclude}
	out, err := s.quick(ctx, skills.QuickIdeasScope(in), in)
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
	reqs := make([]recipes.GroundRequest, len(out.Ideas))
	for i, idea := range out.Ideas {
		reqs[i] = recipes.GroundRequest{DishName: idea.Name, Query: idea.SearchQuery}
	}
	err = keepGrounded(ctx, s.grounder, reqs, skills.QuickIdeasKept, &res.Ideas, &res.Dropped, func(i int, gr recipes.Grounded) (QuickIdea, bool) {
		return quickIdeaOf(out.Ideas[i], gr), OfQuickKind(p.Kind, gr.Method, gr.MinutesTotal)
	}, func() error { return publish(res) })
	if err != nil {
		return res, err
	}
	if len(res.Ideas) == 0 {
		return res, &jobs.Failure{Code: ReasonNoIdeasFound}
	}
	res.Stage = StageDone
	return res, nil
}

// quickIdeaOf is idea as grounded by gr.
func quickIdeaOf(idea skills.QuickIdea, gr recipes.Grounded) QuickIdea {
	return QuickIdea{
		Name:         idea.Name,
		Method:       gr.Method,
		MinutesTotal: gr.MinutesTotal,
		Difficulty:   gr.Difficulty,
		RecipeID:     gr.RecipeID,
		PhotoURL:     dinners.PhotoURL(gr.PhotoID),
		SourceURL:    gr.SourceURL,
		SiteName:     gr.SiteName,
	}
}

// Origins of a dinner made from an idea (the dinners.origin column).
const (
	// OriginIngredients is an idea from Cook from what I have.
	OriginIngredients = "ingredients"
	// OriginQuick is an idea from Quick dinners.
	OriginQuick = "quick"
)

// ErrIdeaNotFound is an idea the household cannot act on: an unknown job,
// a job that does not find ideas, a failed job (which shows no ideas), or
// an index past the job's ideas.
var ErrIdeaNotFound = errors.New("idea not found")

// ErrIdeasNotReady is an idea of a job still queued or running. Its ideas
// may still move: an idea that grounds later is put back in its place
// before those shown already, so an index names an idea only once the job
// has succeeded.
var ErrIdeasNotReady = errors.New("the ideas are still being found; act on one when the job has succeeded")

// Chosen is the idea the household acts on: its name, the recipe it was
// grounded in, and the origin of a dinner made from it.
type Chosen struct {
	Name     string
	RecipeID string
	Origin   string
}

// Idea returns idea i, counted from 0, of the succeeded job jobID, as its
// stored result shows it: a dinner made from it uses the recipe it was
// grounded in, never grounding it again. A job still queued or running is
// ErrIdeasNotReady; any idea not shown by a succeeded KindFromIngredients
// or KindQuick job is ErrIdeaNotFound.
func (s *Service) Idea(ctx context.Context, jobID string, i int) (Chosen, error) {
	job, err := s.runner.Get(ctx, jobID)
	if errors.Is(err, jobs.ErrNotFound) {
		return Chosen{}, ErrIdeaNotFound
	}
	if err != nil {
		return Chosen{}, fmt.Errorf("ideas: read job: %w", err)
	}
	if job.Kind != KindFromIngredients && job.Kind != KindQuick {
		return Chosen{}, ErrIdeaNotFound
	}
	switch job.Status {
	case jobs.StatusQueued, jobs.StatusRunning:
		return Chosen{}, ErrIdeasNotReady
	case jobs.StatusSucceeded:
	default:
		return Chosen{}, ErrIdeaNotFound
	}
	if job.Kind == KindQuick {
		var res QuickResult
		if err := json.Unmarshal(job.Result, &res); err != nil {
			return Chosen{}, fmt.Errorf("ideas: decode %s result: %w", job.Kind, err)
		}
		if i < 0 || i >= len(res.Ideas) {
			return Chosen{}, ErrIdeaNotFound
		}
		return Chosen{Name: res.Ideas[i].Name, RecipeID: res.Ideas[i].RecipeID, Origin: OriginQuick}, nil
	}
	var res Result
	if err := json.Unmarshal(job.Result, &res); err != nil {
		return Chosen{}, fmt.Errorf("ideas: decode %s result: %w", job.Kind, err)
	}
	if i < 0 || i >= len(res.Ideas) {
		return Chosen{}, ErrIdeaNotFound
	}
	return Chosen{Name: res.Ideas[i].Name, RecipeID: res.Ideas[i].RecipeID, Origin: OriginIngredients}, nil
}
