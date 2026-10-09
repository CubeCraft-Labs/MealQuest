package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/recipes/recipestest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/suggest"
)

const suggestionsPath = "/api/v1/suggestions"

// newSuggestionsRouter returns a router whose suggestions service has an
// unstarted job runner, so batches stay pending, and a deck seeded with
// pending suggestions of dinners named names, in order, whose ids it
// returns.
func newSuggestionsRouter(t *testing.T, names ...string) (http.Handler, *store.Store, []string) {
	t.Helper()
	st := newTestStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	g := recipestest.NewGrounder(t, st, t.TempDir(), &recipestest.Search{}, &recipestest.Pages{}, &recipestest.Images{}, &llm.Fake{})
	fail := func(context.Context, string, skills.SideQuestsRequest) (skills.SideQuests, error) {
		return skills.SideQuests{}, errors.New("no model in this test")
	}
	svc, err := suggest.NewService(st, g, fail, runner, notifytest.New(t, st), logger)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t), newTestChat(t),
		newTestDinners(t), svc, newTestPlan(t), newTestShopping(t), newTestGoogle(t), newTestPush(t), newTestIdeas(t), runner, newTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}

	q := db.New(st.Writer)
	ctx := t.Context()
	now := store.Now().String()
	batch, err := q.CreateSuggestionBatch(ctx, db.CreateSuggestionBatchParams{ID: store.NewID(), Trigger: "manual", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.FinishSuggestionBatch(ctx, db.FinishSuggestionBatchParams{Status: "done", UpdatedAt: now, ID: batch.ID}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, name := range names {
		d, err := q.CreateDinner(ctx, db.CreateDinnerParams{
			ID: store.NewID(), Name: name, NormalizedName: harness.Normalize(name), Status: "suggested", Origin: "side_quest",
			Difficulty: sql.NullString{String: "easy", Valid: true}, GroundingStatus: "grounded", CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		id := store.NewID()
		if err := q.CreateSuggestion(ctx, db.CreateSuggestionParams{
			ID: id, DinnerID: d.ID, BatchID: batch.ID, Name: name, Reason: fmt.Sprintf("Reason %d", i), CitedRegularIds: "[]",
			CreatedAt: store.Now().String(), UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return h, st, ids
}

func TestSuggestionDeck(t *testing.T) {
	t.Parallel()
	h, _, ids := newSuggestionsRouter(t, "Pho", "Ramen")

	rec, body := callDinners(t, h, http.MethodGet, suggestionsPath+"/deck", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET deck = %d: %s", rec.Code, rec.Body)
	}
	list, _ := body["suggestions"].([]any)
	if len(list) != 2 {
		t.Fatalf("deck = %v, want 2 suggestions", body)
	}
	for i, name := range []string{"Pho", "Ramen"} {
		s := list[i].(map[string]any)
		if s["id"] != ids[i] || s["name"] != name || s["difficulty"] != "easy" || s["reason"] != fmt.Sprintf("Reason %d", i) || s["status"] != "pending" {
			t.Errorf("deck[%d] = %v, want %s", i, s, name)
		}
		if _, ok := s["photo_url"]; !ok {
			t.Errorf("deck[%d] has no photo_url key", i)
		}
	}
}

func TestSuggestionDecisions(t *testing.T) {
	t.Parallel()
	h, _, ids := newSuggestionsRouter(t, "Pho", "Ramen", "Laksa", "Bibimbap", "Katsu", "Tonkatsu", "Udon")

	for _, tc := range []struct {
		target       string
		status       int
		code         string
		sugStatus    string
		dinnerStatus string
	}{
		{ids[0] + "/accept", http.StatusOK, "", "accepted", "quest_log"},
		{ids[0] + "/accept", http.StatusOK, "", "accepted", "quest_log"},
		{ids[0] + "/decline", http.StatusConflict, "suggestion_decided", "", ""},
		{ids[1] + "/decline", http.StatusOK, "", "declined", "archived"},
		{ids[1] + "/decline", http.StatusOK, "", "declined", "archived"},
		{ids[1] + "/accept", http.StatusConflict, "suggestion_decided", "", ""},
		{"nope/accept", http.StatusNotFound, "not_found", "", ""},
		{"nope/decline", http.StatusNotFound, "not_found", "", ""},
	} {
		rec, body := callDinners(t, h, http.MethodPost, suggestionsPath+"/"+tc.target, "")
		if rec.Code != tc.status {
			t.Fatalf("POST %s = %d, want %d: %s", tc.target, rec.Code, tc.status, rec.Body)
		}
		if tc.code != "" {
			if errorCode(body) != tc.code {
				t.Errorf("POST %s error = %v, want %s", tc.target, body, tc.code)
			}
			continue
		}
		s, _ := body["suggestion"].(map[string]any)
		if s["status"] != tc.sugStatus || s["dinner_status"] != tc.dinnerStatus {
			t.Errorf("POST %s = %v, want %s with dinner %s", tc.target, body, tc.sugStatus, tc.dinnerStatus)
		}
		if body["replenish_job_id"] != nil {
			t.Errorf("POST %s replenished with 5 left: %v", tc.target, body["replenish_job_id"])
		}
	}

	// A third decision leaves 4: a replenish batch starts.
	rec, body := callDinners(t, h, http.MethodPost, suggestionsPath+"/"+ids[2]+"/decline", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("decline = %d: %s", rec.Code, rec.Body)
	}
	replenish, _ := body["replenish_job_id"].(string)
	if replenish == "" {
		t.Fatalf("decline leaving 4 = %v, want a replenish_job_id", body)
	}

	// A fourth decision joins the batch already pending and reports its job.
	rec, body = callDinners(t, h, http.MethodPost, suggestionsPath+"/"+ids[3]+"/accept", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("accept = %d: %s", rec.Code, rec.Body)
	}
	if body["replenish_job_id"] != replenish {
		t.Errorf("accept leaving 3 = %v, want replenish_job_id %s", body, replenish)
	}
}

func TestSuggestionDeckActiveBatch(t *testing.T) {
	t.Parallel()
	h, _, _ := newSuggestionsRouter(t, "Pho")

	rec, body := callDinners(t, h, http.MethodGet, suggestionsPath+"/deck", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET deck = %d: %s", rec.Code, rec.Body)
	}
	if active, ok := body["active_batch"]; !ok || active != nil {
		t.Fatalf("deck = %v, want active_batch null", body)
	}
	if failed, ok := body["failed_batch"]; !ok || failed != nil {
		t.Fatalf("deck = %v, want failed_batch null", body)
	}

	rec, started := callDinners(t, h, http.MethodPost, suggestionsPath+"/batches", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST batches = %d: %s", rec.Code, rec.Body)
	}
	rec, body = callDinners(t, h, http.MethodGet, suggestionsPath+"/deck", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET deck = %d: %s", rec.Code, rec.Body)
	}
	active, _ := body["active_batch"].(map[string]any)
	if active == nil || active["batch_id"] != started["batch_id"] || active["job_id"] != started["job_id"] || active["status"] != "pending" {
		t.Errorf("deck active_batch = %v, want the started batch %v", body["active_batch"], started)
	}
}

func TestPostSuggestionBatch(t *testing.T) {
	t.Parallel()
	h, st, _ := newSuggestionsRouter(t)

	rec, first := callDinners(t, h, http.MethodPost, suggestionsPath+"/batches", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST batches = %d: %s", rec.Code, rec.Body)
	}
	if first["batch_id"] == nil || first["job_id"] == nil || first["trigger"] != "manual" || first["status"] != "pending" {
		t.Errorf("POST batches = %v, want a pending manual batch with its job", first)
	}
	var kind string
	if err := st.Reader.QueryRow(`SELECT kind FROM jobs WHERE id = ?`, first["job_id"]).Scan(&kind); err != nil || kind != suggest.KindSuggestionBatch {
		t.Errorf("job kind = %q (%v), want %s", kind, err, suggest.KindSuggestionBatch)
	}

	rec, again := callDinners(t, h, http.MethodPost, suggestionsPath+"/batches", "")
	if rec.Code != http.StatusAccepted || again["batch_id"] != first["batch_id"] || again["job_id"] != first["job_id"] {
		t.Errorf("second POST batches = %d %v, want the pending batch %v", rec.Code, again, first)
	}
}

func TestSuggestionWritesNeedCSRF(t *testing.T) {
	t.Parallel()
	h, _, ids := newSuggestionsRouter(t, "Pho")
	for _, target := range []string{"/batches", "/" + ids[0] + "/accept", "/" + ids[0] + "/decline"} {
		req := httptest.NewRequest(http.MethodPost, suggestionsPath+target, nil)
		req.AddCookie(validSessionCookie)
		rec, body := serve(t, h, req)
		if rec.Code != http.StatusForbidden || errorCode(body) != "csrf" {
			t.Errorf("POST %s without CSRF = %d %v, want 403 csrf", target, rec.Code, body)
		}
	}
}
