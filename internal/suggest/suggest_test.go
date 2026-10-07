package suggest_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/dinners"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/suggest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const member = "member-1"

type env struct {
	svc    *suggest.Service
	st     *store.Store
	runner *jobs.Runner
	search *recipestest.Search
	pages  *recipestest.Pages
	images *recipestest.Images
	// model answers normalize_recipe while grounding; side answers
	// suggest_side_quests.
	model *llm.Fake
	side  *llm.Fake
	// regulars are the Regulars' ids by name.
	regulars map[string]string
	grounder *recipes.Grounder

	// logs collects the service's and job runner's log output.
	logs *logBuffer

	mu     sync.Mutex
	inputs []skills.SideQuestsRequest
	// stub, when set, answers in place of suggest_side_quests.
	stub func(in skills.SideQuestsRequest) skills.SideQuests
}

// newEnv returns a suggestions service over a migrated store with the
// Regulars Beef Chili and Chicken Fajitas and an unstarted job runner. It
// suggests through the real harness and suggest_side_quests with the
// scripted e.side, and grounds with fake search, pages and images and the
// scripted e.model.
func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := t.Context()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx, st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := household.SeedSettings(ctx, st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	e := &env{
		st:       st,
		search:   &recipestest.Search{Results: map[string][]recipes.Candidate{}},
		pages:    &recipestest.Pages{Bodies: map[string][]byte{}},
		images:   &recipestest.Images{JPEGs: map[string][]byte{}},
		model:    &llm.Fake{},
		side:     &llm.Fake{},
		regulars: map[string]string{},
		logs:     &logBuffer{},
	}
	for _, name := range []string{"Beef Chili", "Chicken Fajitas"} {
		e.regulars[name] = e.dinner(t, name, "regular")
	}
	g := recipestest.NewGrounder(t, st, dataDir, e.search, e.pages, e.images, e.model)
	e.grounder = g
	guard, err := harness.NewGuard(harness.DefaultMaxCalls)
	if err != nil {
		t.Fatal(err)
	}
	cfg := harness.Config{
		Settings:    llm.Settings{Provider: "anthropic", Model: "claude-test", Temperature: 0.7},
		NewProvider: func(llm.Settings, string) (llm.Provider, error) { return e.side, nil },
		Store:       st,
		Guard:       guard,
	}
	suggester := func(ctx context.Context, scope string, in skills.SideQuestsRequest) (skills.SideQuests, error) {
		e.mu.Lock()
		e.inputs = append(e.inputs, in)
		stub := e.stub
		e.mu.Unlock()
		if stub != nil {
			return stub(in), nil
		}
		return skills.SuggestSideQuests(ctx, cfg, scope, in)
	}
	logger := slog.New(slog.NewTextHandler(e.logs, nil))
	e.runner, err = jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	e.svc, err = suggest.NewService(st, g, suggester, e.runner, logger)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// logBuffer collects log output written by concurrent goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// start starts the env's job runner, stopping it when the test ends.
func (e *env) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.runner.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		e.runner.Wait()
	})
}

// dinner stores a dinner named name with status and returns its id.
func (e *env) dinner(t *testing.T, name, status string) string {
	t.Helper()
	now := store.Now().String()
	origin := "regular"
	if status == "suggested" {
		origin = "side_quest"
	}
	d, err := db.New(e.st.Writer).CreateDinner(t.Context(), db.CreateDinnerParams{
		ID:              store.NewID(),
		Name:            name,
		NormalizedName:  harness.Normalize(name),
		Status:          status,
		Origin:          origin,
		GroundingStatus: "manual",
		CreatedAt:       now,
		UpdatedAt:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d.ID
}

// seedDeck stores a finished batch holding pending suggestions of
// dinners named names, in order, and returns their ids.
func (e *env) seedDeck(t *testing.T, names ...string) []string {
	t.Helper()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	b, err := q.CreateSuggestionBatch(t.Context(), db.CreateSuggestionBatchParams{ID: store.NewID(), Trigger: "manual", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.FinishSuggestionBatch(t.Context(), db.FinishSuggestionBatchParams{Status: "done", UpdatedAt: now, ID: b.ID}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, name := range names {
		id := store.NewID()
		if err := q.CreateSuggestion(t.Context(), db.CreateSuggestionParams{
			ID:              id,
			DinnerID:        e.dinner(t, name, "suggested"),
			BatchID:         b.ID,
			Name:            name,
			Reason:          "Like your Beef Chili.",
			CitedRegularIds: fmt.Sprintf("[%q]", e.regulars["Beef Chili"]),
			CreatedAt:       store.Now().String(),
			UpdatedAt:       now,
		}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// recipe makes the search for name find one page that grounds it, and
// scripts the normalize_recipe reply grounding it takes.
func (e *env) recipe(t *testing.T, name string) {
	t.Helper()
	slug := strings.ReplaceAll(strings.ToLower(name), " ", "-")
	page, photo := "https://r.example/"+slug, "https://r.example/"+slug+".jpg"
	e.search.Results[name] = []recipes.Candidate{{URL: page}}
	e.pages.Bodies[page] = recipestest.RecipePage(name, "R Kitchen", photo, 35)
	e.images.JPEGs[photo] = recipestest.JPEG(t, 400, 320, uint8(len(e.images.JPEGs)+1))
	e.model.QueueGenerate(recipestest.NormalizedReply("stovetop", 35))
}

// sideQuest renders one Side quest of a suggest_side_quests reply whose
// search query is its name.
func sideQuest(name, reason string, cited ...string) string {
	c, _ := json.Marshal(cited)
	r, _ := json.Marshal(reason)
	return fmt.Sprintf(`{"name":%q,"cuisine":"Fusion","method":"stovetop","est_minutes":35,"difficulty":"easy","reason":%s,"cited_regulars":%s,"search_query":%q}`,
		name, r, c, name)
}

// reply scripts one suggest_side_quests reply holding quests.
func (e *env) reply(quests ...string) {
	js := `{"suggestions":[` + strings.Join(quests, ",") + `]}`
	e.side.QueueGenerate(llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}})
}

// waitJob waits for job id to finish and returns it.
func (e *env) waitJob(t *testing.T, id string) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		job, err := e.runner.Get(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == jobs.StatusSucceeded || job.Status == jobs.StatusFailed {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s still %s", id, job.Status)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type batchResult struct {
	BatchID       string   `json:"batch_id"`
	Stage         string   `json:"stage"`
	SuggestionIDs []string `json:"suggestion_ids"`
	Dropped       int      `json:"dropped"`
}

// runBatch starts a manual batch, waits for it to succeed and returns its
// result.
func (e *env) runBatch(t *testing.T) batchResult {
	t.Helper()
	b, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Created {
		t.Fatalf("batch %s was already active", b.ID)
	}
	job := e.waitJob(t, b.JobID)
	if job.Status != jobs.StatusSucceeded {
		t.Fatalf("batch job %s, error %v", job.Status, *job.Error)
	}
	var res batchResult
	if err := json.Unmarshal(job.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.BatchID != b.ID || res.Stage != suggest.StageDone {
		t.Fatalf("batch result = %+v, want batch %s done", res, b.ID)
	}
	if got := e.batchStatus(t, b.ID); got != suggest.BatchDone {
		t.Fatalf("batch status = %s, want done", got)
	}
	return res
}

func (e *env) batchStatus(t *testing.T, id string) string {
	t.Helper()
	var status string
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT status FROM suggestion_batches WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func (e *env) activeBatches(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM suggestion_batches WHERE status IN ('pending', 'running')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *env) dinnerStatus(t *testing.T, id string) string {
	t.Helper()
	row, err := db.New(e.st.Reader).GetDinner(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row.Status
}

// deckNames returns the names Deck lists, in order.
func (e *env) deckNames(t *testing.T) []string {
	t.Helper()
	deck, err := e.svc.Deck(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, s := range deck.Suggestions {
		names = append(names, s.Name)
	}
	return names
}

func TestBatchStoresGroundedSideQuests(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.recipe(t, "Chicken Shawarma Bowls")
	e.recipe(t, "Chili Verde")
	e.reply(
		sideQuest("Chicken Shawarma Bowls", "Spiced chicken, like your Chicken Fajitas.", "Chicken Fajitas"),
		sideQuest("Mystery Stew", "Hearty like your Beef Chili.", "Beef Chili"), // no recipe grounds it
		sideQuest("Chili Verde", "Your Beef Chili gone green.", "Beef Chili"),
		sideQuest("Lamb Tagine", "Slow-cooked like your Pot Roast.", "Pot Roast"), // cites no Regular
	)

	res := e.runBatch(t)
	if len(res.SuggestionIDs) != 2 || res.Dropped != 2 {
		t.Fatalf("result = %+v, want 2 suggestions and 2 dropped", res)
	}
	if g, _, _ := e.model.Remaining(); g != 0 {
		t.Errorf("%d normalize_recipe replies unused", g)
	}
	if q := e.search.Queries(); slices.Contains(q, "Lamb Tagine") {
		t.Errorf("searched %q for a Side quest the skill dropped", q)
	}

	deck, err := e.svc.Deck(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(deck.Suggestions) != 2 {
		t.Fatalf("deck has %d suggestions, want 2", len(deck.Suggestions))
	}
	want := []struct{ name, reason, regular string }{
		{"Chicken Shawarma Bowls", "Spiced chicken, like your Chicken Fajitas.", "Chicken Fajitas"},
		{"Chili Verde", "Your Beef Chili gone green.", "Beef Chili"},
	}
	for i, w := range want {
		s := deck.Suggestions[i]
		if s.ID != res.SuggestionIDs[i] || s.Name != w.name || s.Reason != w.reason || s.Status != suggest.StatusPending {
			t.Errorf("deck[%d] = %+v, want %s pending with reason %q", i, s, w.name, w.reason)
		}
		if !slices.Equal(s.CitedRegularIDs, []string{e.regulars[w.regular]}) {
			t.Errorf("deck[%d] cites %v, want %s's id", i, s.CitedRegularIDs, w.regular)
		}
		if s.PhotoURL == nil || !strings.HasPrefix(*s.PhotoURL, "/media/photos/") || s.Difficulty == nil {
			t.Errorf("deck[%d] photo %v, difficulty %v; want both", i, s.PhotoURL, s.Difficulty)
		}
		row, err := db.New(e.st.Reader).GetDinner(t.Context(), s.DinnerID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != "suggested" || row.Origin != "side_quest" || row.GroundingStatus != "grounded" || !row.RecipeID.Valid || row.Cuisine.String != "Fusion" {
			t.Errorf("dinner %s = %+v, want suggested side_quest grounded in a recipe", w.name, row)
		}
	}
	var dinners int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM dinners WHERE name IN ('Mystery Stew', 'Lamb Tagine')`).Scan(&dinners); err != nil {
		t.Fatal(err)
	}
	if dinners != 0 {
		t.Errorf("%d dropped Side quests stored as dinners, want none", dinners)
	}
}

func TestBatchKeepsAtMostBatchSize(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	var quests []string
	for i := range suggest.BatchSize + 2 {
		name := fmt.Sprintf("Fajita Dish %c", 'A'+i)
		e.search.Results[name] = nil // ungroundable unless recipe is called
		quests = append(quests, sideQuest(name, "Like your Chicken Fajitas.", "Chicken Fajitas"))
	}
	// The first round grounds BatchSize Side quests; one fails, so the
	// second round grounds one more, and the last is never grounded.
	for i := range suggest.BatchSize + 1 {
		if i != 1 {
			e.recipe(t, fmt.Sprintf("Fajita Dish %c", 'A'+i))
		}
	}
	e.reply(quests...)

	res := e.runBatch(t)
	if len(res.SuggestionIDs) != suggest.BatchSize {
		t.Fatalf("kept %d, want %d", len(res.SuggestionIDs), suggest.BatchSize)
	}
	if slices.Contains(e.search.Queries(), fmt.Sprintf("Fajita Dish %c", 'A'+suggest.BatchSize+1)) {
		t.Error("grounded a Side quest after the batch was full")
	}
	names := e.deckNames(t)
	if names[0] != "Fajita Dish A" || names[1] != "Fajita Dish C" || names[len(names)-1] != fmt.Sprintf("Fajita Dish %c", 'A'+suggest.BatchSize) {
		t.Errorf("deck = %v, want the model's order without B", names)
	}
}

func TestDeckIsOldestFirst(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.recipe(t, "Chicken Shawarma Bowls")
	e.recipe(t, "Chili Verde")
	e.reply(
		sideQuest("Chicken Shawarma Bowls", "Spiced chicken, like your Chicken Fajitas.", "Chicken Fajitas"),
		sideQuest("Chili Verde", "Your Beef Chili gone green.", "Beef Chili"),
	)
	e.runBatch(t)
	e.recipe(t, "Beef Bulgogi")
	e.reply(sideQuest("Beef Bulgogi", "Sweet beef for Beef Chili fans.", "Beef Chili"))
	e.runBatch(t)

	want := []string{"Chicken Shawarma Bowls", "Chili Verde", "Beef Bulgogi"}
	if got := e.deckNames(t); !slices.Equal(got, want) {
		t.Errorf("deck = %v, want %v", got, want)
	}
	// The second batch was told the first one's Side quests are pending.
	if got := e.inputs[1].Pending; !slices.Equal(got, want[:2]) {
		t.Errorf("second batch pending = %v, want %v", got, want[:2])
	}
}

func TestDeckReportsTheActiveBatch(t *testing.T) {
	e := newEnv(t) // runner not started: the batch stays pending
	e.seedDeck(t, "Pho")
	deck, err := e.svc.Deck(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if deck.ActiveBatch != nil {
		t.Fatalf("active batch = %+v with none pending or running, want nil", deck.ActiveBatch)
	}
	b, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	deck, err = e.svc.Deck(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(deck.Suggestions) != 1 {
		t.Errorf("deck has %d suggestions, want 1", len(deck.Suggestions))
	}
	want := suggest.Batch{ID: b.ID, JobID: b.JobID, Trigger: suggest.TriggerManual, Status: suggest.BatchPending}
	if deck.ActiveBatch == nil || *deck.ActiveBatch != want {
		t.Errorf("active batch = %+v, want %+v", deck.ActiveBatch, want)
	}
}

func TestDeckFailsBatchInterruptedByRestart(t *testing.T) {
	e := newEnv(t)
	e.seedDeck(t, "Pho")
	b, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	// A previous process claimed the job and started the batch, then
	// stopped before recording its end.
	now := store.Now().String()
	if _, err := db.New(e.st.Writer).ClaimNextJob(t.Context(), sql.NullString{String: now, Valid: true}); err != nil {
		t.Fatal(err)
	}
	if n, err := db.New(e.st.Writer).StartSuggestionBatch(t.Context(), db.StartSuggestionBatchParams{UpdatedAt: now, ID: b.ID}); err != nil || n != 1 {
		t.Fatalf("start batch: %d rows, %v", n, err)
	}
	e.start(t) // fails the running job as interrupted
	if job := e.waitJob(t, b.JobID); job.Status != jobs.StatusFailed {
		t.Fatalf("job status = %s, want failed", job.Status)
	}

	deck, err := e.svc.Deck(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if deck.ActiveBatch != nil {
		t.Errorf("active batch = %+v after its job failed, want nil", deck.ActiveBatch)
	}
	if len(deck.Suggestions) != 1 {
		t.Errorf("deck has %d suggestions, want 1", len(deck.Suggestions))
	}
	if got := e.batchStatus(t, b.ID); got != suggest.BatchFailed {
		t.Errorf("interrupted batch status = %s, want failed", got)
	}
}

func TestDeckLeavesOutDinnersNoLongerSuggested(t *testing.T) {
	e := newEnv(t)
	ids := e.seedDeck(t, "Pho", "Ramen")
	sug, err := e.svc.Deck(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.New(e.st.Writer).ArchiveDinner(t.Context(), db.ArchiveDinnerParams{ID: sug.Suggestions[0].DinnerID, UpdatedAt: store.Now().String()}); err != nil {
		t.Fatal(err)
	}
	if got := e.deckNames(t); !slices.Equal(got, []string{"Ramen"}) {
		t.Errorf("deck = %v, want [Ramen]", got)
	}
	if _, err := e.svc.Accept(t.Context(), ids[0], member); !errors.Is(err, suggest.ErrDinnerNotSuggested) {
		t.Errorf("accept a suggestion whose dinner was archived: %v, want ErrDinnerNotSuggested", err)
	}
}

func TestAcceptIsIdempotent(t *testing.T) {
	e := newEnv(t)
	ids := e.seedDeck(t, "Pho", "Ramen", "Laksa", "Bibimbap", "Katsu", "Tonkatsu")

	first, err := e.svc.Accept(t.Context(), ids[0], member)
	if err != nil {
		t.Fatal(err)
	}
	s := first.Suggestion
	if s.Status != suggest.StatusAccepted || s.DinnerStatus != "quest_log" || s.DecidedAt == nil {
		t.Fatalf("accepted = %+v, want accepted with its dinner in the Quest log", s)
	}
	row, err := db.New(e.st.Reader).GetDinner(t.Context(), s.DinnerID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != "quest_log" || row.UpdatedBy.String != member {
		t.Errorf("dinner status %s, updated_by %q; want quest_log by %s", row.Status, row.UpdatedBy.String, member)
	}

	again, err := e.svc.Accept(t.Context(), ids[0], "member-2")
	if err != nil {
		t.Fatal(err)
	}
	if *again.Suggestion.DecidedAt != *s.DecidedAt || again.ReplenishJobID != nil {
		t.Errorf("repeat accept = %+v, want the first decision unchanged", again)
	}
	if row, _ := db.New(e.st.Reader).GetDinner(t.Context(), s.DinnerID); row.UpdatedBy.String != member {
		t.Errorf("repeat accept recorded updated_by %q", row.UpdatedBy.String)
	}

	var decided *suggest.DecidedError
	if _, err := e.svc.Decline(t.Context(), ids[0], member); !errors.As(err, &decided) || decided.Status != suggest.StatusAccepted {
		t.Fatalf("decline an accepted suggestion: %v, want *DecidedError accepted", err)
	}
	if got := e.dinnerStatus(t, s.DinnerID); got != "quest_log" {
		t.Errorf("dinner status after refused decline = %s, want quest_log", got)
	}
	if got := e.deckNames(t); slices.Contains(got, "Pho") {
		t.Errorf("deck %v still holds the accepted suggestion", got)
	}
}

func TestDeclineIsIdempotent(t *testing.T) {
	e := newEnv(t)
	ids := e.seedDeck(t, "Pho", "Ramen", "Laksa", "Bibimbap", "Katsu", "Tonkatsu")

	first, err := e.svc.Decline(t.Context(), ids[1], member)
	if err != nil {
		t.Fatal(err)
	}
	s := first.Suggestion
	if s.Status != suggest.StatusDeclined || s.DinnerStatus != "archived" || s.DecidedAt == nil {
		t.Fatalf("declined = %+v, want declined with its dinner archived", s)
	}
	again, err := e.svc.Decline(t.Context(), ids[1], member)
	if err != nil {
		t.Fatal(err)
	}
	if *again.Suggestion.DecidedAt != *s.DecidedAt || again.ReplenishJobID != nil {
		t.Errorf("repeat decline = %+v, want the first decision unchanged", again)
	}
	var decided *suggest.DecidedError
	if _, err := e.svc.Accept(t.Context(), ids[1], member); !errors.As(err, &decided) || decided.Status != suggest.StatusDeclined {
		t.Fatalf("accept a declined suggestion: %v, want *DecidedError declined", err)
	}
	if got := e.dinnerStatus(t, s.DinnerID); got != "archived" {
		t.Errorf("dinner status after refused accept = %s, want archived", got)
	}
	names, err := db.New(e.st.Reader).ListDeclinedSuggestionNames(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(names, []string{"Ramen"}) {
		t.Errorf("declined names = %v, want [Ramen]", names)
	}
}

func TestDecideUnknownSuggestion(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Accept(t.Context(), "nope", member); !errors.Is(err, suggest.ErrNotFound) {
		t.Errorf("accept unknown: %v, want ErrNotFound", err)
	}
	if _, err := e.svc.Decline(t.Context(), "nope", member); !errors.Is(err, suggest.ErrNotFound) {
		t.Errorf("decline unknown: %v, want ErrNotFound", err)
	}
}

func TestReplenishWhenDeckFallsBelowFive(t *testing.T) {
	e := newEnv(t) // runner not started: a replenish batch stays pending
	ids := e.seedDeck(t, "Pho", "Ramen", "Laksa", "Bibimbap", "Katsu", "Tonkatsu")

	d, err := e.svc.Accept(t.Context(), ids[0], member) // 5 left
	if err != nil {
		t.Fatal(err)
	}
	if d.ReplenishJobID != nil || e.activeBatches(t) != 0 {
		t.Fatalf("replenished with 5 left: job %v, %d active batches", d.ReplenishJobID, e.activeBatches(t))
	}

	d, err = e.svc.Decline(t.Context(), ids[1], member) // 4 left
	if err != nil {
		t.Fatal(err)
	}
	if d.ReplenishJobID == nil {
		t.Fatal("no replenish batch with 4 left")
	}
	job, err := e.runner.Get(t.Context(), *d.ReplenishJobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != suggest.KindSuggestionBatch || job.Status != jobs.StatusQueued {
		t.Errorf("replenish job = %+v, want a queued %s", job, suggest.KindSuggestionBatch)
	}
	var trigger string
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT trigger FROM suggestion_batches WHERE status = 'pending'`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	if trigger != suggest.TriggerReplenish {
		t.Errorf("batch trigger = %s, want replenish", trigger)
	}
}

func TestReplenishIsSingleFlight(t *testing.T) {
	e := newEnv(t) // runner not started: the replenish batch stays pending
	ids := e.seedDeck(t, "Pho", "Ramen", "Laksa")

	first, err := e.svc.Decline(t.Context(), ids[0], member)
	if err != nil {
		t.Fatal(err)
	}
	if first.ReplenishJobID == nil {
		t.Fatal("no replenish batch with 2 left")
	}
	second, err := e.svc.Accept(t.Context(), ids[1], member)
	if err != nil {
		t.Fatal(err)
	}
	// The second decision joins the pending batch and reports its job, so a
	// client that did not start it can follow it.
	if second.ReplenishJobID == nil || *second.ReplenishJobID != *first.ReplenishJobID {
		t.Errorf("second decision replenish job = %v, want the pending batch's job %s", second.ReplenishJobID, *first.ReplenishJobID)
	}
	manual, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if manual.Created || manual.JobID != *first.ReplenishJobID || manual.Trigger != suggest.TriggerReplenish {
		t.Errorf("manual batch = %+v, want the pending replenish batch with job %s", manual, *first.ReplenishJobID)
	}
	if n := e.activeBatches(t); n != 1 {
		t.Errorf("%d active batches, want 1", n)
	}
	var jobsQueued int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM jobs WHERE kind = ?`, suggest.KindSuggestionBatch).Scan(&jobsQueued); err != nil {
		t.Fatal(err)
	}
	if jobsQueued != 1 {
		t.Errorf("%d batch jobs, want 1", jobsQueued)
	}
}

func TestStartBatchIsSingleFlightUnderConcurrency(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	batches := make([]suggest.Batch, 8)
	for i := range batches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := e.svc.StartBatch(context.Background(), suggest.TriggerManual)
			if err != nil {
				t.Error(err)
			}
			batches[i] = b
		}()
	}
	wg.Wait()
	created := 0
	for _, b := range batches {
		if b.Created {
			created++
		}
		if b.ID != batches[0].ID || b.JobID != batches[0].JobID || b.JobID == "" {
			t.Errorf("batch %+v, want %s with job %s", b, batches[0].ID, batches[0].JobID)
		}
	}
	if created != 1 {
		t.Errorf("%d calls created a batch, want 1", created)
	}
}

func TestLaterBatchNeverResuggestsDeclined(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	shawarma := sideQuest("Chicken Shawarma Bowls", "Spiced chicken, like your Chicken Fajitas.", "Chicken Fajitas")
	e.recipe(t, "Chicken Shawarma Bowls")
	e.reply(shawarma)
	res := e.runBatch(t)

	// Passing it leaves the deck empty, so a replenish batch runs; the
	// model suggests the passed dish again, respelled, with a new one.
	e.recipe(t, "Beef Ragu")
	e.reply(
		sideQuest("chicken shawarma bowls!", "Spiced chicken, like your Chicken Fajitas.", "Chicken Fajitas"),
		sideQuest("Beef Ragu", "Slow beef like your Beef Chili.", "Beef Chili"),
	)
	d, err := e.svc.Decline(t.Context(), res.SuggestionIDs[0], member)
	if err != nil {
		t.Fatal(err)
	}
	if d.ReplenishJobID == nil {
		t.Fatal("passing the last Side quest started no replenish batch")
	}
	job := e.waitJob(t, *d.ReplenishJobID)
	if job.Status != jobs.StatusSucceeded {
		t.Fatalf("replenish job %s: %v", job.Status, *job.Error)
	}
	var replenished batchResult
	if err := json.Unmarshal(job.Result, &replenished); err != nil {
		t.Fatal(err)
	}
	if len(replenished.SuggestionIDs) != 1 || replenished.Dropped != 1 {
		t.Errorf("replenish result = %+v, want 1 kept and the passed dish dropped", replenished)
	}
	if got := e.deckNames(t); !slices.Equal(got, []string{"Beef Ragu"}) {
		t.Errorf("deck = %v, want [Beef Ragu]", got)
	}
	e.mu.Lock()
	in := e.inputs[1]
	e.mu.Unlock()
	if !slices.Equal(in.Declined, []string{"Chicken Shawarma Bowls"}) || !slices.Equal(in.Context.Passed, []string{"Chicken Shawarma Bowls"}) {
		t.Errorf("replenish batch declined %v, passed %v; want the passed dish in both", in.Declined, in.Context.Passed)
	}
}

// TestBatchNeverStoresDishPassedWhileRunning passes a Side quest after a
// batch has read its input but before it stores, with suggest_side_quests
// answering from that input: the batch must not store the passed dish.
func TestBatchNeverStoresDishPassedWhileRunning(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	ids := e.seedDeck(t, "Chicken Shawarma Bowls")
	e.recipe(t, "Chicken Shawarma Bowls")
	e.recipe(t, "Beef Ragu")
	held, release := make(chan struct{}), make(chan struct{})
	stub := func(skills.SideQuestsRequest) skills.SideQuests {
		close(held)
		<-release
		quest := func(name, cited string) skills.SideQuest {
			return skills.SideQuest{Name: name, Cuisine: "Fusion", Method: "stovetop", EstMinutes: 35, Difficulty: "easy",
				Reason: "Like your " + cited + ".", CitedRegulars: []string{cited}, SearchQuery: name}
		}
		return skills.SideQuests{Suggestions: []skills.SideQuest{
			quest("Chicken Shawarma Bowls", "Chicken Fajitas"),
			quest("Beef Ragu", "Beef Chili"),
		}}
	}
	e.mu.Lock()
	e.stub = stub
	e.mu.Unlock()

	b, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	<-held
	if _, err := e.svc.Decline(t.Context(), ids[0], member); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	job := e.waitJob(t, b.JobID)
	if job.Status != jobs.StatusSucceeded {
		t.Fatalf("batch job %s: %v", job.Status, *job.Error)
	}
	var res batchResult
	if err := json.Unmarshal(job.Result, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.SuggestionIDs) != 1 || res.Dropped != 1 {
		t.Errorf("batch result = %+v, want 1 kept and the passed dish dropped", res)
	}
	if got := e.deckNames(t); !slices.Equal(got, []string{"Beef Ragu"}) {
		t.Errorf("deck = %v, want [Beef Ragu]", got)
	}
}

// TestLaterBatchNeverResuggestsRenamedPass passes a Side quest, renames
// its archived dinner, and checks the next batch still refuses the name it
// was suggested under.
func TestLaterBatchNeverResuggestsRenamedPass(t *testing.T) {
	e := newEnv(t) // runner not started: the replenish batch waits for the rename
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	dinnerSvc, err := dinners.NewService(e.st, e.grounder, e.runner, logger)
	if err != nil {
		t.Fatal(err)
	}
	ids := e.seedDeck(t, "Chicken Shawarma Bowls")
	d, err := e.svc.Decline(t.Context(), ids[0], member)
	if err != nil {
		t.Fatal(err)
	}
	if d.ReplenishJobID == nil {
		t.Fatal("passing the last Side quest started no replenish batch")
	}
	renamed := "Leftover Wrap"
	if _, err := dinnerSvc.Update(t.Context(), d.Suggestion.DinnerID, dinners.Patch{Name: &renamed}, member); err != nil {
		t.Fatal(err)
	}

	// The respelled passed dish would ground, so only the declined names
	// keep it out of the deck.
	e.recipe(t, "chicken shawarma bowls!")
	e.recipe(t, "Beef Ragu")
	e.reply(
		sideQuest("chicken shawarma bowls!", "Spiced chicken, like your Chicken Fajitas.", "Chicken Fajitas"),
		sideQuest("Beef Ragu", "Slow beef like your Beef Chili.", "Beef Chili"),
	)
	e.start(t)
	job := e.waitJob(t, *d.ReplenishJobID)
	if job.Status != jobs.StatusSucceeded {
		t.Fatalf("replenish job %s: %v", job.Status, *job.Error)
	}
	if got := e.deckNames(t); !slices.Equal(got, []string{"Beef Ragu"}) {
		t.Errorf("deck = %v, want [Beef Ragu]", got)
	}
	e.mu.Lock()
	in := e.inputs[0]
	e.mu.Unlock()
	if !slices.Equal(in.Declined, []string{"Chicken Shawarma Bowls"}) || !slices.Equal(in.Context.Passed, []string{"Chicken Shawarma Bowls"}) {
		t.Errorf("replenish batch declined %v, passed %v; want the name it was suggested under in both", in.Declined, in.Context.Passed)
	}
}

// TestDecisionNeverCommitsWithoutItsReplenish makes the replenish batch's
// job fail to store: the decision must not be recorded either, so the
// retry decides again and starts the replenish.
func TestDecisionNeverCommitsWithoutItsReplenish(t *testing.T) {
	e := newEnv(t) // runner not started: the replenish batch stays pending
	ids := e.seedDeck(t, "Pho", "Ramen", "Laksa", "Bibimbap", "Katsu")
	if _, err := e.st.Writer.ExecContext(t.Context(), `CREATE TRIGGER refuse_jobs BEFORE INSERT ON jobs BEGIN SELECT RAISE(ABORT, 'jobs refused'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Decline(t.Context(), ids[0], member); err == nil {
		t.Fatal("decline succeeded with no replenish job stored")
	}
	if got := e.deckNames(t); len(got) != 5 {
		t.Errorf("deck after the failed decline = %v, want all 5 still pending", got)
	}
	if n := e.activeBatches(t); n != 0 {
		t.Errorf("%d active batches after the failed decline, want 0", n)
	}

	if _, err := e.st.Writer.ExecContext(t.Context(), `DROP TRIGGER refuse_jobs`); err != nil {
		t.Fatal(err)
	}
	d, err := e.svc.Decline(t.Context(), ids[0], member)
	if err != nil {
		t.Fatal(err)
	}
	if d.Suggestion.Status != suggest.StatusDeclined || d.ReplenishJobID == nil {
		t.Fatalf("retried decline = %+v, want declined with a replenish job", d)
	}
	job, err := e.runner.Get(t.Context(), *d.ReplenishJobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Kind != suggest.KindSuggestionBatch || job.Status != jobs.StatusQueued {
		t.Errorf("replenish job = %+v, want a queued %s", job, suggest.KindSuggestionBatch)
	}
	if n := e.activeBatches(t); n != 1 {
		t.Errorf("%d active batches, want 1", n)
	}
}

func TestBatchFailsWhenAIFails(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.side.QueueGenerate(llm.GenerateResult{Err: errors.New("provider down")})

	b, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	job := e.waitJob(t, b.JobID)
	if job.Status != jobs.StatusFailed || *job.Error != suggest.ReasonAIFailed {
		t.Fatalf("job = %s %v, want failed %s", job.Status, job.Error, suggest.ReasonAIFailed)
	}
	if got := e.batchStatus(t, b.ID); got != suggest.BatchFailed {
		t.Errorf("batch status = %s, want failed", got)
	}
	if got := e.deckNames(t); len(got) != 0 {
		t.Errorf("deck = %v after a failed batch, want empty", got)
	}
	// A failed batch does not block the next one.
	next, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Created || next.ID == b.ID {
		t.Errorf("next batch = %+v, want a new one", next)
	}
}

func TestBatchFailsWhenSearchFails(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.search.Err = errors.New("search down")
	e.reply(sideQuest("Chili Verde", "Your Beef Chili gone green.", "Beef Chili"))

	b, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	job := e.waitJob(t, b.JobID)
	if job.Status != jobs.StatusFailed || *job.Error != suggest.ReasonSearchFailed {
		t.Fatalf("job = %s %v, want failed %s", job.Status, job.Error, suggest.ReasonSearchFailed)
	}
	if got := e.deckNames(t); len(got) != 0 {
		t.Errorf("deck = %v, want empty", got)
	}
}

func TestBatchFailsWithNoRegulars(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	for _, id := range e.regulars {
		if _, err := db.New(e.st.Writer).ArchiveDinner(t.Context(), db.ArchiveDinnerParams{ID: id, UpdatedAt: store.Now().String()}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	job := e.waitJob(t, b.JobID)
	if job.Status != jobs.StatusFailed || *job.Error != suggest.ReasonNoRegulars {
		t.Fatalf("job = %s %v, want failed %s", job.Status, job.Error, suggest.ReasonNoRegulars)
	}
	if reqs := e.side.Requests(); len(reqs) != 0 {
		t.Errorf("model called %d times with no Regulars", len(reqs))
	}
}

func TestStartBatchFailsOrphanedBatch(t *testing.T) {
	e := newEnv(t)
	now := store.Now().String()
	orphan, err := db.New(e.st.Writer).CreateSuggestionBatch(t.Context(), db.CreateSuggestionBatchParams{ID: store.NewID(), Trigger: "manual", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Created || b.ID == orphan.ID {
		t.Fatalf("batch = %+v, want a new one past the orphan", b)
	}
	if got := e.batchStatus(t, orphan.ID); got != suggest.BatchFailed {
		t.Errorf("orphan status = %s, want failed", got)
	}
	// A batch whose job is still queued is not an orphan.
	again, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.ID != b.ID || e.batchStatus(t, b.ID) != suggest.BatchPending {
		t.Errorf("second batch = %+v, status %s; want batch %s still pending", again, e.batchStatus(t, b.ID), b.ID)
	}
}

func TestBatchJobFailsWhenRecordingItsEndFails(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.recipe(t, "Chili Verde")
	e.reply(sideQuest("Chili Verde", "Your Beef Chili gone green.", "Beef Chili"))
	if _, err := e.st.Writer.ExecContext(t.Context(), `CREATE TRIGGER refuse_done BEFORE UPDATE OF status ON suggestion_batches WHEN NEW.status = 'done' BEGIN SELECT RAISE(ABORT, 'done refused'); END`); err != nil {
		t.Fatal(err)
	}

	b, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	job := e.waitJob(t, b.JobID)
	if job.Status != jobs.StatusFailed {
		t.Fatalf("job %s with its batch %s, want failed", job.Status, e.batchStatus(t, b.ID))
	}

	if _, err := e.st.Writer.ExecContext(t.Context(), `DROP TRIGGER refuse_done`); err != nil {
		t.Fatal(err)
	}
	next, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Created || next.ID == b.ID {
		t.Errorf("next batch = %+v, want a new one", next)
	}
	if got := e.batchStatus(t, b.ID); got != suggest.BatchFailed {
		t.Errorf("batch status = %s with its job failed, want failed", got)
	}
}

func TestBatchRecoversWhenRecordingItsEndAndItsJobsEndFail(t *testing.T) {
	e := newEnv(t)
	e.start(t)
	e.recipe(t, "Chili Verde")
	e.reply(sideQuest("Chili Verde", "Your Beef Chili gone green.", "Beef Chili"))
	for _, trigger := range []string{
		`CREATE TRIGGER refuse_done BEFORE UPDATE OF status ON suggestion_batches WHEN NEW.status = 'done' BEGIN SELECT RAISE(ABORT, 'done refused'); END`,
		`CREATE TRIGGER refuse_job_end BEFORE UPDATE OF status ON jobs WHEN NEW.status IN ('succeeded', 'failed') BEGIN SELECT RAISE(ABORT, 'job end refused'); END`,
	} {
		if _, err := e.st.Writer.ExecContext(t.Context(), trigger); err != nil {
			t.Fatal(err)
		}
	}

	b, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(e.logs.String(), "record job failure") {
		if time.Now().After(deadline) {
			t.Fatalf("job end never recorded; batch %s", e.batchStatus(t, b.ID))
		}
		time.Sleep(5 * time.Millisecond)
	}
	// While neither end is recorded the batch is still the active one.
	again, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if again.Created || again.ID != b.ID {
		t.Fatalf("batch = %+v while batch %s is running, want it", again, b.ID)
	}

	// The database recovers; the service is not restarted.
	for _, trigger := range []string{"refuse_done", "refuse_job_end"} {
		if _, err := e.st.Writer.ExecContext(t.Context(), "DROP TRIGGER "+trigger); err != nil {
			t.Fatal(err)
		}
	}
	job := e.waitJob(t, b.JobID)
	if job.Status != jobs.StatusFailed {
		t.Fatalf("job %s, want failed", job.Status)
	}
	next, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Created || next.ID == b.ID {
		t.Errorf("next batch = %+v, want a new one", next)
	}
	if got := e.batchStatus(t, b.ID); got != suggest.BatchFailed {
		t.Errorf("batch status = %s with its job failed, want failed", got)
	}
}

func TestBlockedBatchEndDoesNotHoldShutdown(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := e.runner.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	e.search.Gate = make(chan struct{})
	e.search.Err = errors.New("search down")
	e.reply(sideQuest("Chili Verde", "Your Beef Chili gone green.", "Beef Chili"))

	b, err := e.svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(e.search.Queries()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("batch never searched")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Hold the only writer connection, so recording the batch's end
	// waits for it once the search fails.
	conn, err := e.st.Writer.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	waits := e.st.Writer.Stats().WaitCount
	close(e.search.Gate)
	for e.st.Writer.Stats().WaitCount == waits {
		if time.Now().After(deadline) {
			conn.Close()
			t.Fatal("batch end never waited for the writer")
		}
		time.Sleep(5 * time.Millisecond)
	}

	go func() {
		e.runner.Wait()
		close(stopped)
	}()
	cancel()
	// Each final write's attempt ends after jobs.FinalWriteTimeout; well
	// past that, the worker is waiting for the writer with no bound.
	select {
	case <-stopped:
	case <-time.After(jobs.FinalWriteTimeout + 10*time.Second):
		conn.Close()
		<-stopped
		t.Fatal("worker waited for a held writer through shutdown")
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if got := e.batchStatus(t, b.ID); got != suggest.BatchRunning {
		t.Fatalf("after shutdown batch status = %s, want left running", got)
	}

	// The next process fails the job as interrupted, and the batch no
	// longer blocks the next one.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	next, err := jobs.NewRunner(e.st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	refuse := func(context.Context, string, skills.SideQuestsRequest) (skills.SideQuests, error) {
		return skills.SideQuests{}, errors.New("not wanted")
	}
	svc, err := suggest.NewService(e.st, e.grounder, refuse, next, logger)
	if err != nil {
		t.Fatal(err)
	}
	nextCtx, nextCancel := context.WithCancel(context.Background())
	if err := next.Start(nextCtx); err != nil {
		nextCancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		nextCancel()
		next.Wait()
	})
	job, err := next.Get(t.Context(), b.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobs.StatusFailed || job.Error == nil || *job.Error != jobs.CodeInterrupted {
		t.Fatalf("interrupted job = %+v, want failed with %q", job, jobs.CodeInterrupted)
	}
	again, err := svc.StartBatch(t.Context(), suggest.TriggerManual)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Created || again.ID == b.ID {
		t.Errorf("next batch = %+v, want a new one", again)
	}
	if got := e.batchStatus(t, b.ID); got != suggest.BatchFailed {
		t.Errorf("interrupted batch status = %s, want failed", got)
	}
}

func TestStartBatchRefusesUnknownTrigger(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.StartBatch(t.Context(), "whenever"); err == nil {
		t.Fatal("unknown trigger accepted")
	}
}
