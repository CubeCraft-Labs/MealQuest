package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const plansPath = "/api/v1/plans/"

// plansEnv is a router whose plan service has an unstarted job runner, so
// drafts stay queued, over a store holding a draft plan of the week of
// 2026-10-12: Beef Chili (crock pot, 240 minutes) on Monday and a Regular
// on every other day. questLog is the id of a Quest log dinner on no plan.
type plansEnv struct {
	h        http.Handler
	st       *store.Store
	svc      *plan.Service
	questLog string
}

func newPlansRouter(t *testing.T) plansEnv {
	t.Helper()
	st := newTestStore(t)
	ctx := t.Context()
	if err := household.SeedSettings(ctx, st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	fail := func(context.Context, string, skills.WeekRequest) (skills.Week, error) {
		return skills.Week{}, errors.New("no model in this test")
	}
	svc, err := plan.NewService(st, fail, runner, logger)
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewRouter(logger, "dev", nil, stubAuth{}, stubSessions{}, newTestSettings(t), newTestSearch(t), newTestAI(t),
		newTestDinners(t), newTestSuggest(t), svc, runner, newTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(fixtureIndex)}})
	if err != nil {
		t.Fatal(err)
	}

	q := db.New(st.Writer)
	now := store.Now().String()
	dinner := func(name, status, method string, minutes int64) string {
		d, err := q.CreateDinner(ctx, db.CreateDinnerParams{
			ID: store.NewID(), Name: name, NormalizedName: harness.Normalize(name), Status: status, Origin: "regular",
			Method:          sql.NullString{String: method, Valid: method != ""},
			MinutesTotal:    sql.NullInt64{Int64: minutes, Valid: minutes > 0},
			GroundingStatus: "manual", CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	planID := store.NewID()
	if err := q.CreateWeekPlan(ctx, db.CreateWeekPlanParams{ID: planID, WeekStart: "2026-10-12",
		AiNote: sql.NullString{String: "Mostly Regulars.", Valid: true}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i, date := range []string{"2026-10-12", "2026-10-13", "2026-10-14", "2026-10-15", "2026-10-16", "2026-10-17", "2026-10-18"} {
		id := dinner("Regular "+date, "regular", "stovetop", 30)
		if i == 0 {
			id = dinner("Beef Chili", "regular", "crockpot", 240)
		}
		if err := q.PutPlanDay(ctx, db.PutPlanDayParams{ID: store.NewID(), PlanID: planID, Date: date,
			DinnerID: sql.NullString{String: id, Valid: true}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	return plansEnv{h: h, st: st, svc: svc, questLog: dinner("Pad Thai", "quest_log", "stovetop", 35)}
}

func (e plansEnv) setStatus(t *testing.T, status string) {
	t.Helper()
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE week_plans SET status = ?`, status); err != nil {
		t.Fatal(err)
	}
}

func TestGetPlan(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)

	rec, body := callDinners(t, e.h, http.MethodGet, plansPath+"2026-10-12", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET plan = %d: %s", rec.Code, rec.Body)
	}
	if body["status"] != "draft" || body["week_start"] != "2026-10-12" || body["week_label"] != "Week 42 · Oct 12 – 18" ||
		body["ai_note"] != "Mostly Regulars." || body["calendar_synced_at"] != nil {
		t.Errorf("plan = %v", body)
	}
	days, _ := body["days"].([]any)
	if len(days) != 7 {
		t.Fatalf("days = %v, want 7", body["days"])
	}
	monday := days[0].(map[string]any)
	if monday["date"] != "2026-10-12" || monday["is_new"] != false || monday["start_by"] != "2026-10-12T13:30:00-04:00" {
		t.Errorf("Monday = %v", monday)
	}
	d := monday["dinner"].(map[string]any)
	for _, key := range []string{"id", "name", "photo", "minutes", "method", "origin"} {
		if _, ok := d[key]; !ok {
			t.Errorf("dinner has no %s: %v", key, d)
		}
	}
	if d["name"] != "Beef Chili" || d["minutes"] != 240.0 || d["method"] != "crockpot" || d["origin"] != "regular" || d["photo"] != nil {
		t.Errorf("Monday dinner = %v", d)
	}
	if tuesday := days[1].(map[string]any); tuesday["start_by"] != nil {
		t.Errorf("Tuesday start_by = %v, want null for a stovetop dinner", tuesday["start_by"])
	}

	rec, body = callDinners(t, e.h, http.MethodGet, plansPath+"2026-10-19", "")
	if rec.Code != http.StatusNotFound || errorCode(body) != "not_found" {
		t.Errorf("GET week with no plan = %d %v, want 404 not_found", rec.Code, body)
	}
}

func TestPlanWeekStartMustBeMonday(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)

	for _, tc := range []struct{ method, target, body string }{
		{http.MethodGet, plansPath + "2026-10-13", ""},
		{http.MethodGet, plansPath + "not-a-date", ""},
		{http.MethodGet, plansPath + "2026-02-30", ""},
		{http.MethodPost, plansPath + "2027-02-29/draft", "{}"},
		{http.MethodPost, plansPath + "2026-10-18/draft", "{}"},
		{http.MethodPut, plansPath + "2026-10-14/days/2026-10-14", `{"dinner_id":"` + e.questLog + `"}`},
	} {
		rec, body := callDinners(t, e.h, tc.method, tc.target, tc.body)
		if rec.Code != http.StatusBadRequest || errorCode(body) != "invalid_week" {
			t.Errorf("%s %s = %d %v, want 400 invalid_week", tc.method, tc.target, rec.Code, body)
		}
	}
}

func TestPostPlanDraft(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)

	rec, body := callDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", "{}")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST draft = %d: %s", rec.Code, rec.Body)
	}
	jobID, _ := body["jobId"].(string)
	if jobID == "" || len(body) != 1 {
		t.Fatalf("POST draft = %v, want only a jobId", body)
	}
	rec, job := callDinners(t, e.h, http.MethodGet, "/api/v1/jobs/"+jobID, "")
	if rec.Code != http.StatusOK || job["kind"] != plan.KindDraftWeek || job["status"] != "queued" {
		t.Errorf("GET job = %d %v, want a queued %s job", rec.Code, job, plan.KindDraftWeek)
	}
	// A second draft of the week joins the queued one.
	rec, body = callDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", `{"confirm":false}`)
	if rec.Code != http.StatusAccepted || body["jobId"] != jobID {
		t.Errorf("second POST draft = %d %v, want 202 with job %s", rec.Code, body, jobID)
	}
	// A week with no plan needs no confirm.
	rec, body = callDinners(t, e.h, http.MethodPost, plansPath+"2026-10-19/draft", "{}")
	if rec.Code != http.StatusAccepted || body["jobId"] == jobID {
		t.Errorf("POST draft of a new week = %d %v, want 202 with its own job", rec.Code, body)
	}

	// A confirmed draft does not join the unconfirmed one and enqueues
	// nothing.
	rec, body = callDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", `{"confirm":true}`)
	if rec.Code != http.StatusConflict || errorCode(body) != "draft_running" {
		t.Errorf("confirmed POST draft = %d %v, want 409 draft_running", rec.Code, body)
	}
	var queued int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM jobs WHERE kind = ?`, plan.KindDraftWeek).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 2 {
		t.Errorf("%d draft jobs, want one per week", queued)
	}

	rec, body = callDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", `{"confirm":"yes"}`)
	if rec.Code != http.StatusBadRequest || errorCode(body) != "invalid_request" {
		t.Errorf("POST draft with a bad confirm = %d %v, want 400", rec.Code, body)
	}
}

// TestDraftWeekNotEnqueuedThroughJobs checks POST /api/v1/jobs refuses a
// draft_week job, so a confirmed draft cannot run beside the week's
// unconfirmed one: POST /plans/{weekStart}/draft is the only way to start
// a draft.
func TestDraftWeekNotEnqueuedThroughJobs(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)

	rec, _ := callDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", "{}")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST draft = %d: %s", rec.Code, rec.Body)
	}
	rec, body := callDinners(t, e.h, http.MethodPost, "/api/v1/jobs",
		`{"kind":"`+plan.KindDraftWeek+`","payload":{"week_start":"2026-10-12","confirm":true}}`)
	if rec.Code != http.StatusBadRequest || errorCode(body) != "service_only_kind" {
		t.Errorf("POST job %s = %d %v, want 400 service_only_kind", plan.KindDraftWeek, rec.Code, body)
	}
	var queued int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM jobs WHERE kind = ?`, plan.KindDraftWeek).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Errorf("%d draft jobs, want only the one POST draft enqueued", queued)
	}
}

func TestSimultaneousPostPlanDraftsShareOneJob(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)

	const n = 10
	recs := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			recs[i] = httptest.NewRecorder()
			e.h.ServeHTTP(recs[i], signedIn(http.MethodPost, plansPath+"2026-10-12/draft", strings.NewReader("{}")))
		})
	}
	wg.Wait()
	jobIDs := map[any]int{}
	for i, rec := range recs {
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusAccepted {
			t.Fatalf("POST draft %d = %d %s, want 202", i, rec.Code, rec.Body)
		}
		jobIDs[body["jobId"]]++
	}
	var queued int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM jobs WHERE kind = ?`, plan.KindDraftWeek).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if len(jobIDs) != 1 || jobIDs[nil] != 0 || queued != 1 {
		t.Errorf("jobIds = %v, %d draft jobs; want one jobId for all %d drafts", jobIDs, queued, n)
	}
}

func TestPostPlanDraftOverSyncedPlanNeedsConfirm(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"synced", "changed"} {
		e := newPlansRouter(t)
		e.setStatus(t, status)
		for _, b := range []string{"{}", `{"confirm":false}`} {
			rec, body := callDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", b)
			if rec.Code != http.StatusConflict || errorCode(body) != "confirm_required" {
				t.Errorf("%s plan: POST draft %s = %d %v, want 409 confirm_required", status, b, rec.Code, body)
			}
		}
		rec, body := callDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", `{"confirm":true}`)
		if rec.Code != http.StatusAccepted || body["jobId"] == nil {
			t.Errorf("%s plan: POST draft with confirm = %d %v, want 202", status, rec.Code, body)
		}
	}
}

func TestPutPlanDay(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.setStatus(t, "synced")

	rec, body := callDinners(t, e.h, http.MethodPut, plansPath+"2026-10-12/days/2026-10-14", `{"dinner_id":"`+e.questLog+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT day = %d: %s", rec.Code, rec.Body)
	}
	if body["status"] != "changed" {
		t.Errorf("status = %v, want a swapped synced plan changed", body["status"])
	}
	wednesday := body["days"].([]any)[2].(map[string]any)
	if wednesday["date"] != "2026-10-14" || wednesday["is_new"] != true || wednesday["dinner"].(map[string]any)["name"] != "Pad Thai" {
		t.Errorf("Wednesday = %v, want Pad Thai, new", wednesday)
	}

	for _, tc := range []struct {
		target, body string
		status       int
		code         string
	}{
		{"2026-10-12/days/2026-10-14", `{}`, http.StatusBadRequest, "invalid_request"},
		{"2026-10-12/days/2026-10-14", `{"dinner_id":7}`, http.StatusBadRequest, "invalid_request"},
		{"2026-10-12/days/2026-10-14", `{"dinner_id":"x","extra":1}`, http.StatusBadRequest, "invalid_request"},
		{"2026-10-12/days/2026-10-20", `{"dinner_id":"` + e.questLog + `"}`, http.StatusBadRequest, "invalid_date"},
		{"2026-10-12/days/2026-10-32", `{"dinner_id":"` + e.questLog + `"}`, http.StatusBadRequest, "invalid_date"},
		{"2026-10-12/days/2026-10-14", `{"dinner_id":"no-such-dinner"}`, http.StatusNotFound, "not_found"},
		{"2026-10-19/days/2026-10-20", `{"dinner_id":"` + e.questLog + `"}`, http.StatusNotFound, "not_found"},
		{"2026-10-12/days/2026-10-15", `{"dinner_id":"` + e.questLog + `"}`, http.StatusConflict, "dinner_already_planned"},
	} {
		rec, body := callDinners(t, e.h, http.MethodPut, plansPath+tc.target, tc.body)
		if rec.Code != tc.status || errorCode(body) != tc.code {
			t.Errorf("PUT %s %s = %d %v, want %d %s", tc.target, tc.body, rec.Code, body, tc.status, tc.code)
		}
	}
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE dinners SET status = 'archived' WHERE id = ?`, e.questLog); err != nil {
		t.Fatal(err)
	}
	rec, body = callDinners(t, e.h, http.MethodPut, plansPath+"2026-10-12/days/2026-10-15", `{"dinner_id":"`+e.questLog+`"}`)
	if rec.Code != http.StatusConflict || errorCode(body) != "dinner_not_plannable" {
		t.Errorf("PUT archived dinner = %d %v, want 409 dinner_not_plannable", rec.Code, body)
	}
}
