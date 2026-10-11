package ideas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
)

// quickIdea renders one idea of a quick_ideas reply whose search query is
// its name.
func quickIdea(name, method string, minutes int) string {
	return fmt.Sprintf(`{"name":%q,"est_minutes":%d,"method":%q,"difficulty":"easy","search_query":%q}`,
		name, minutes, method, name)
}

// quickRecorder collects every result a quick job publishes.
type quickRecorder struct {
	mu        sync.Mutex
	published []QuickResult
}

func (r *quickRecorder) latest() (QuickResult, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.published) == 0 {
		return QuickResult{}, false
	}
	return r.published[len(r.published)-1], true
}

func (r *quickRecorder) publish(res QuickResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	res.Ideas = append([]QuickIdea(nil), res.Ideas...)
	r.published = append(r.published, res)
	return nil
}

// runQuick runs the quick job's handler directly, recording each publish.
func (e *env) runQuick(t *testing.T, kind string, exclude ...string) (QuickResult, []QuickResult, error) {
	t.Helper()
	names, err := skills.CleanExclude(exclude)
	if err != nil {
		t.Fatal(err)
	}
	var rec quickRecorder
	res, err := e.svc.runQuick(t.Context(), QuickJob{Kind: kind, Exclude: names}, rec.publish)
	return res, rec.published, err
}

func quickNames(list []QuickIdea) []string {
	out := []string{}
	for _, i := range list {
		out = append(out, i.Name)
	}
	return out
}

func TestUnder30DropsIdeasOverThirtyMinutes(t *testing.T) {
	e := newEnv(t)
	// The model estimates every idea under 30 minutes; the grounded
	// recipes decide.
	e.recipe(t, "Chicken Fried Rice", "Chicken Fried Rice", 25)
	e.recipe(t, "Beef Stroganoff", "Beef Stroganoff", 45)
	e.recipe(t, "Shrimp Scampi", "Shrimp Scampi", 30)
	e.recipe(t, "Chicken Chili", "Slow Cooker Chicken Chili", 240)
	e.reply(
		quickIdea("Chicken Fried Rice", "stovetop", 20),
		quickIdea("Beef Stroganoff", "stovetop", 25),
		quickIdea("Shrimp Scampi", "stovetop", 20),
		quickIdea("Chicken Chili", "crockpot", 25),
	)
	res, _, err := e.runQuick(t, skills.QuickUnder30)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := quickNames(res.Ideas), []string{"Chicken Fried Rice", "Shrimp Scampi"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ideas = %q, want %q: the 45-minute and the crock pot ideas dropped", got, want)
	}
	if res.Dropped != 2 || res.Stage != StageDone || res.Kind != skills.QuickUnder30 {
		t.Fatalf("result = %+v, want done with 2 dropped", res)
	}
	for _, i := range res.Ideas {
		if i.MinutesTotal == nil || *i.MinutesTotal > QuickMinutes || i.RecipeID == "" || i.PhotoURL == "" {
			t.Errorf("idea %+v is not a grounded dinner of 30 minutes or less", i)
		}
	}
}

func TestCrockpotDropsStovetopIdeas(t *testing.T) {
	e := newEnv(t)
	// The model calls every idea a crock pot dinner; only the recipes
	// cooked in a slow cooker are.
	e.recipe(t, "Pulled Pork", "Slow Cooker Pulled Pork", 480)
	e.recipe(t, "Beef Stew", "Beef Stew", 150)
	e.recipe(t, "Chicken Tortilla Soup", "Crock Pot Chicken Tortilla Soup", 375)
	e.recipe(t, "Chicken Fried Rice", "Chicken Fried Rice", 25)
	e.reply(
		quickIdea("Pulled Pork", "crockpot", 480),
		quickIdea("Beef Stew", "crockpot", 360),
		quickIdea("Chicken Tortilla Soup", "crockpot", 375),
		quickIdea("Chicken Fried Rice", "crockpot", 240),
	)
	res, _, err := e.runQuick(t, skills.QuickCrockpot)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := quickNames(res.Ideas), []string{"Pulled Pork", "Chicken Tortilla Soup"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ideas = %q, want %q: the stovetop ideas dropped", got, want)
	}
	if res.Dropped != 2 {
		t.Fatalf("dropped %d, want 2", res.Dropped)
	}
	for _, i := range res.Ideas {
		if i.Method != "crockpot" {
			t.Errorf("idea %q method %s, want the grounded crockpot", i.Name, i.Method)
		}
	}
	if m := res.Ideas[0].MinutesTotal; m == nil || *m != 480 {
		t.Errorf("Pulled Pork minutes_total = %v, want the recipe's 480", m)
	}
}

func TestExcludedNamesAreNeverReturned(t *testing.T) {
	e := newEnv(t)
	// Every idea has a recipe that would ground it as a Quick dinner.
	for _, name := range []string{"Easy Chicken Fried Rice", "shrimp-tacos", "Garlic Shrimp Pasta"} {
		e.recipe(t, name, name, 20)
	}
	e.reply(
		quickIdea("Easy Chicken Fried Rice", "stovetop", 20),
		quickIdea("shrimp-tacos", "stovetop", 15),
		quickIdea("Garlic Shrimp Pasta", "stovetop", 25),
	)
	res, _, err := e.runQuick(t, skills.QuickUnder30, "Chicken Fried Rice", "Shrimp Tacos")
	if err != nil {
		t.Fatal(err)
	}
	if got := quickNames(res.Ideas); !reflect.DeepEqual(got, []string{"Garlic Shrimp Pasta"}) || res.Dropped != 2 {
		t.Fatalf("ideas = %q dropped %d, want only Garlic Shrimp Pasta with 2 dropped", got, res.Dropped)
	}
	if got := e.search.Queries(); !reflect.DeepEqual(got, []string{"Garlic Shrimp Pasta"}) {
		t.Errorf("searched %q, want the excluded names never grounded", got)
	}
	if !reflect.DeepEqual(res.Exclude, []string{"Chicken Fried Rice", "Shrimp Tacos"}) {
		t.Errorf("result exclude = %q", res.Exclude)
	}
	// The model was told what was already shown.
	if system := e.model.Requests()[0].System; !strings.Contains(system, "### Already shown\n- Chicken Fried Rice\n- Shrimp Tacos") {
		t.Errorf("prompt does not list the names already shown:\n%s", system)
	}
}

func TestQuickPageStopsAtSixAndShowsFewerHonestly(t *testing.T) {
	t.Run("twelve ideas, six shown", func(t *testing.T) {
		e := newEnv(t)
		var reply []string
		for i := range skills.QuickIdeasRequested {
			name := fmt.Sprintf("Skillet Dish %c", 'A'+i)
			e.recipe(t, name, name, 20)
			reply = append(reply, quickIdea(name, "stovetop", 20))
		}
		e.reply(reply...)
		res, _, err := e.runQuick(t, skills.QuickUnder30)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Ideas) != skills.QuickIdeasKept || res.Dropped != 0 {
			t.Fatalf("ideas = %q dropped %d, want %d", quickNames(res.Ideas), res.Dropped, skills.QuickIdeasKept)
		}
		if got := e.search.Queries(); len(got) != skills.QuickIdeasKept {
			t.Errorf("searched %d ideas, want only the %d a page holds", len(got), skills.QuickIdeasKept)
		}
	})
	t.Run("over-generated ideas replace dropped ones", func(t *testing.T) {
		e := newEnv(t)
		var reply []string
		// The first six ground too slow, so the next six are grounded to
		// fill the page; only three of those are quick.
		for i := range skills.QuickIdeasRequested {
			name := fmt.Sprintf("Skillet Dish %c", 'A'+i)
			minutes := 20
			if i < 6 || i%2 == 1 {
				minutes = 50
			}
			e.recipe(t, name, name, minutes)
			reply = append(reply, quickIdea(name, "stovetop", 20))
		}
		e.reply(reply...)
		res, _, err := e.runQuick(t, skills.QuickUnder30)
		if err != nil {
			t.Fatal(err)
		}
		// Fewer than six survive: the page shows three, never padded.
		if got, want := quickNames(res.Ideas), []string{"Skillet Dish G", "Skillet Dish I", "Skillet Dish K"}; !reflect.DeepEqual(got, want) || res.Dropped != 9 || res.Stage != StageDone {
			t.Fatalf("result %s ideas %q dropped %d, want done with %q and 9 dropped", res.Stage, got, res.Dropped, want)
		}
	})
}

func TestQuickResultFillsAsEachIdeaGrounds(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"Chicken Fried Rice", "Shrimp Scampi", "Garlic Shrimp Pasta"} {
		e.recipe(t, name, name, 25)
	}
	// The first idea's search waits, so the two after it ground first.
	gate := make(chan struct{})
	e.search.Gates = map[string]chan struct{}{"Chicken Fried Rice": gate}
	e.reply(
		quickIdea("Chicken Fried Rice", "stovetop", 20),
		quickIdea("Shrimp Scampi", "stovetop", 20),
		quickIdea("Garlic Shrimp Pasta", "stovetop", 25),
	)
	var rec quickRecorder
	type outcome struct {
		res QuickResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := e.svc.runQuick(t.Context(), QuickJob{Kind: skills.QuickUnder30, Exclude: []string{}}, rec.publish)
		done <- outcome{res, err}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if latest, ok := rec.latest(); ok && len(latest.Ideas) == 2 {
			if got := quickNames(latest.Ideas); !reflect.DeepEqual(got, []string{"Shrimp Scampi", "Garlic Shrimp Pasta"}) {
				t.Fatalf("while the first idea grounds, ideas = %q, want the two grounded", got)
			}
			if latest.Stage != StageGrounding {
				t.Fatalf("stage = %s, want grounding", latest.Stage)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the ideas that grounded were not shown while the first idea was still grounding")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate)
	out := <-done
	if out.err != nil {
		t.Fatal(out.err)
	}
	want := []string{"Chicken Fried Rice", "Shrimp Scampi", "Garlic Shrimp Pasta"}
	if got := quickNames(out.res.Ideas); !reflect.DeepEqual(got, want) || out.res.Stage != StageDone {
		t.Fatalf("result = %s %q, want done with %q in the model's order", out.res.Stage, got, want)
	}

	var stages []string
	var counts []int
	for _, p := range rec.published {
		stages = append(stages, p.Stage)
		counts = append(counts, len(p.Ideas))
	}
	if !reflect.DeepEqual(stages, []string{StageSuggesting, StageGrounding, StageGrounding, StageGrounding, StageGrounding}) || !reflect.DeepEqual(counts, []int{0, 0, 1, 2, 3}) {
		t.Fatalf("published stages %q with %v ideas, want suggesting, then grounding with 0, 1, 2, 3", stages, counts)
	}
}

func TestQuickFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, e *env)
		code  string
	}{
		{"ai fails", func(t *testing.T, e *env) { e.fail = errors.New("provider down") }, ReasonAIFailed},
		{"no idea is of the kind", func(t *testing.T, e *env) {
			e.recipe(t, "Beef Stew", "Beef Stew", 150)
			e.reply(quickIdea("Beef Stew", "crockpot", 360))
		}, ReasonNoIdeasFound},
		{"every idea already shown", func(t *testing.T, e *env) {
			e.reply(quickIdea("Pulled Pork", "crockpot", 480))
		}, ReasonNoIdeasFound},
		{"a later round fails after ideas were shown", func(t *testing.T, e *env) {
			e.recipe(t, "Chicken Chili", "Slow Cooker Chicken Chili", 240)
			e.recipe(t, "Beef Stew", "Beef Stew", 150)
			e.search.Fail = map[string]error{"Lentil Soup": errors.New("boom")}
			reply := []string{quickIdea("Chicken Chili", "crockpot", 240)}
			for i := range 5 {
				name := fmt.Sprintf("Mystery Stew %d", i+1)
				reply = append(reply, quickIdea(name, "crockpot", 300))
			}
			e.reply(append(reply, quickIdea("Lentil Soup", "crockpot", 300))...)
		}, ReasonSearchFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(t, e)
			_, published, err := e.runQuick(t, skills.QuickCrockpot, "Pulled Pork")
			var failure *jobs.Failure
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("err = %v, want failure %s", err, tc.code)
			}
			if last := published[len(published)-1]; len(last.Ideas) != 0 || last.Stage == StageDone {
				t.Fatalf("last publish of a failed job = %+v, want no ideas", last)
			}
		})
	}
}

func TestStartQuickRunsTheJob(t *testing.T) {
	e := newEnv(t)
	e.recipe(t, "Pulled Pork", "Slow Cooker Pulled Pork", 480)
	e.reply(quickIdea("Pulled Pork", "crockpot", 480))
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.runner.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		e.runner.Wait()
	})

	for _, tc := range []struct {
		kind    string
		exclude []string
		want    string
	}{
		{"", nil, `kind: kind "" is not one of crockpot, under30`},
		{"quick", nil, `kind: kind "quick" is not one of crockpot, under30`},
		{skills.QuickCrockpot, []string{"Beef Stew", "beef stew"}, `exclude: excluded name 2, "beef stew", repeats "Beef Stew"`},
	} {
		_, err := e.svc.StartQuick(t.Context(), tc.kind, tc.exclude)
		var invalid *InvalidError
		if !errors.As(err, &invalid) || invalid.Message != tc.want {
			t.Errorf("StartQuick(%q, %q) = %v, want %q", tc.kind, tc.exclude, err, tc.want)
		}
	}

	started, err := e.svc.StartQuick(t.Context(), skills.QuickCrockpot, []string{" Beef  Stew "})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	var job jobs.Job
	for {
		job, err = e.runner.Get(t.Context(), started.JobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == jobs.StatusSucceeded || job.Status == jobs.StatusFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job still %s", job.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if job.Status != jobs.StatusSucceeded || job.Kind != KindQuick {
		t.Fatalf("job = %+v", job)
	}
	var res QuickResult
	if err := json.Unmarshal(job.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Stage != StageDone || res.Kind != skills.QuickCrockpot || !reflect.DeepEqual(res.Exclude, []string{"Beef Stew"}) || len(res.Ideas) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if got := res.Ideas[0]; got.Name != "Pulled Pork" || got.Method != "crockpot" || got.SiteName == nil || *got.SiteName != "R Kitchen" {
		t.Errorf("idea = %+v", got)
	}
}

func TestOfQuickKind(t *testing.T) {
	m := func(n int) *int { return &n }
	for _, tc := range []struct {
		kind, method string
		minutes      *int
		want         bool
	}{
		{skills.QuickCrockpot, "crockpot", m(480), true},
		{skills.QuickCrockpot, "crockpot", nil, true},
		{skills.QuickCrockpot, "stovetop", m(20), false},
		{skills.QuickUnder30, "stovetop", m(30), true},
		{skills.QuickUnder30, "oven", m(31), false},
		{skills.QuickUnder30, "stovetop", nil, false},
		{skills.QuickUnder30, "crockpot", m(240), false},
		{"quick", "crockpot", m(20), false},
	} {
		if got := OfQuickKind(tc.kind, tc.method, tc.minutes); got != tc.want {
			t.Errorf("OfQuickKind(%s, %s, %v) = %v, want %v", tc.kind, tc.method, tc.minutes, got, tc.want)
		}
	}
	if !slices.Equal(skills.QuickKinds, []string{skills.QuickCrockpot, skills.QuickUnder30}) {
		t.Errorf("kinds = %q", skills.QuickKinds)
	}
}
