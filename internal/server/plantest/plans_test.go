package plantest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/skills"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/chat/chattest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/gcal/gcaltest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/notify/notifytest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/server/srvtest"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
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
	shop     *shopping.Service
	runner   *jobs.Runner
	questLog string
	// google is the fake Google the plan service pushes to; it starts
	// unconnected.
	google *gcaltest.Google
	gcal   *gcal.Service
	// chat is the fake Google Chat endpoint chatSvc posts to; no webhook
	// is stored until a test stores one.
	chat    *chattest.Chat
	chatSvc *chat.Service
	// logs holds every log line the router writes.
	logs *bytes.Buffer
}

func newPlansRouter(t *testing.T) plansEnv {
	t.Helper()
	return newPlansRouterTitled(t, srvtest.Untitled)
}

// newPlansRouterTitled is newPlansRouter whose Supply runs are titled by
// title.
func newPlansRouterTitled(t *testing.T, title shopping.Titler) plansEnv {
	t.Helper()
	st := srvtest.NewTestStore(t)
	ctx := t.Context()
	if err := household.SeedSettings(ctx, st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	runner, err := jobs.NewRunner(st, logger, 1)
	if err != nil {
		t.Fatal(err)
	}
	fail := func(context.Context, string, skills.WeekRequest) (skills.Week, error) {
		return skills.Week{}, errors.New("no model in this test")
	}
	pub := &url.URL{Scheme: "https", Host: "mealquest.example"}
	google := gcaltest.New(t, "https://mealquest.example"+gcal.CallbackPath)
	googleSvc, err := gcal.NewService(st, srvtest.NewTestBox(t), gcal.Config{
		ClientID:     gcaltest.ClientID,
		ClientSecret: gcaltest.ClientSecret,
		PublicURL:    pub,
		SecretKey:    []byte("server-secret-key"),
		Endpoint:     google.Endpoint(),
		RevokeURL:    google.RevokeURL(),
		CalendarAPI:  google.CalendarAPI(),
	})
	if err != nil {
		t.Fatal(err)
	}
	notifier := notifytest.New(t, st)
	svc, err := plan.NewService(st, fail, runner, googleSvc, notifier, func() {}, pub, time.Now, logger)
	if err != nil {
		t.Fatal(err)
	}
	fakeChat := chattest.New(t)
	chatSvc, err := chat.NewService(st, srvtest.NewTestBox(t), pub, fakeChat.Transport())
	if err != nil {
		t.Fatal(err)
	}
	shop, err := shopping.NewService(st, title, runner, notifier, chatSvc, logger)
	if err != nil {
		t.Fatal(err)
	}
	h, err := server.NewRouter(logger, "dev", nil, srvtest.StubAuth{}, srvtest.StubSessions{}, srvtest.NewTestSettings(t), srvtest.NewTestSearch(t), srvtest.NewTestAI(t), chatSvc,
		srvtest.NewTestDinnersOn(t, st), srvtest.NewTestSuggest(t), svc, shop, googleSvc, srvtest.NewTestPush(t), srvtest.NewTestIdeas(t), srvtest.NewTestHome(t), runner, srvtest.NewTestPhotos(t), fstest.MapFS{"index.html": {Data: []byte(srvtest.FixtureIndex)}})
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
	return plansEnv{h: h, st: st, svc: svc, shop: shop, runner: runner, questLog: dinner("Pad Thai", "quest_log", "stovetop", 35),
		google: google, gcal: googleSvc, chat: fakeChat, chatSvc: chatSvc, logs: logs}
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

	rec, body := srvtest.CallDinners(t, e.h, http.MethodGet, plansPath+"2026-10-12", "")
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
	for _, key := range []string{"id", "name", "status", "photo", "minutes", "method", "origin", "meal_source"} {
		if _, ok := d[key]; !ok {
			t.Errorf("dinner has no %s: %v", key, d)
		}
	}
	if d["name"] != "Beef Chili" || d["status"] != "regular" || d["minutes"] != 240.0 || d["method"] != "crockpot" || d["origin"] != "regular" || d["photo"] != nil {
		t.Errorf("Monday dinner = %v", d)
	}
	if tuesday := days[1].(map[string]any); tuesday["start_by"] != nil {
		t.Errorf("Tuesday start_by = %v, want null for a stovetop dinner", tuesday["start_by"])
	}

	// A dinner archived after the plan was made stays on it, archived.
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE dinners SET status = 'archived' WHERE id = ?`, d["id"]); err != nil {
		t.Fatal(err)
	}
	rec, body = srvtest.CallDinners(t, e.h, http.MethodGet, plansPath+"2026-10-12", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET plan after archive = %d: %s", rec.Code, rec.Body)
	}
	days, _ = body["days"].([]any)
	if len(days) != 7 {
		t.Fatalf("days after archive = %v, want 7", body["days"])
	}
	if d := days[0].(map[string]any)["dinner"].(map[string]any); d["name"] != "Beef Chili" || d["status"] != "archived" {
		t.Errorf("Monday dinner after archive = %v, want Beef Chili archived", d)
	}

	rec, body = srvtest.CallDinners(t, e.h, http.MethodGet, plansPath+"2026-10-19", "")
	if rec.Code != http.StatusNotFound || srvtest.ErrorCode(body) != "not_found" {
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
		rec, body := srvtest.CallDinners(t, e.h, tc.method, tc.target, tc.body)
		if rec.Code != http.StatusBadRequest || srvtest.ErrorCode(body) != "invalid_week" {
			t.Errorf("%s %s = %d %v, want 400 invalid_week", tc.method, tc.target, rec.Code, body)
		}
	}
}

func TestPostPlanDraft(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)

	rec, body := srvtest.CallDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", "{}")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST draft = %d: %s", rec.Code, rec.Body)
	}
	jobID, _ := body["jobId"].(string)
	if jobID == "" || len(body) != 1 {
		t.Fatalf("POST draft = %v, want only a jobId", body)
	}
	rec, job := srvtest.CallDinners(t, e.h, http.MethodGet, "/api/v1/jobs/"+jobID, "")
	if rec.Code != http.StatusOK || job["kind"] != plan.KindDraftWeek || job["status"] != "queued" {
		t.Errorf("GET job = %d %v, want a queued %s job", rec.Code, job, plan.KindDraftWeek)
	}
	// A second draft of the week joins the queued one.
	rec, body = srvtest.CallDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", `{"confirm":false}`)
	if rec.Code != http.StatusAccepted || body["jobId"] != jobID {
		t.Errorf("second POST draft = %d %v, want 202 with job %s", rec.Code, body, jobID)
	}
	// A week with no plan needs no confirm.
	rec, body = srvtest.CallDinners(t, e.h, http.MethodPost, plansPath+"2026-10-19/draft", "{}")
	if rec.Code != http.StatusAccepted || body["jobId"] == jobID {
		t.Errorf("POST draft of a new week = %d %v, want 202 with its own job", rec.Code, body)
	}

	// A confirmed draft does not join the unconfirmed one and enqueues
	// nothing.
	rec, body = srvtest.CallDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", `{"confirm":true}`)
	if rec.Code != http.StatusConflict || srvtest.ErrorCode(body) != "draft_running" {
		t.Errorf("confirmed POST draft = %d %v, want 409 draft_running", rec.Code, body)
	}
	var queued int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM jobs WHERE kind = ?`, plan.KindDraftWeek).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 2 {
		t.Errorf("%d draft jobs, want one per week", queued)
	}

	rec, body = srvtest.CallDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", `{"confirm":"yes"}`)
	if rec.Code != http.StatusBadRequest || srvtest.ErrorCode(body) != "invalid_request" {
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

	rec, _ := srvtest.CallDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", "{}")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST draft = %d: %s", rec.Code, rec.Body)
	}
	rec, body := srvtest.CallDinners(t, e.h, http.MethodPost, "/api/v1/jobs",
		`{"kind":"`+plan.KindDraftWeek+`","payload":{"week_start":"2026-10-12","confirm":true}}`)
	if rec.Code != http.StatusBadRequest || srvtest.ErrorCode(body) != "service_only_kind" {
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
			e.h.ServeHTTP(recs[i], srvtest.SignedIn(http.MethodPost, plansPath+"2026-10-12/draft", strings.NewReader("{}")))
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
			rec, body := srvtest.CallDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", b)
			if rec.Code != http.StatusConflict || srvtest.ErrorCode(body) != "confirm_required" {
				t.Errorf("%s plan: POST draft %s = %d %v, want 409 confirm_required", status, b, rec.Code, body)
			}
		}
		rec, body := srvtest.CallDinners(t, e.h, http.MethodPost, plansPath+"2026-10-12/draft", `{"confirm":true}`)
		if rec.Code != http.StatusAccepted || body["jobId"] == nil {
			t.Errorf("%s plan: POST draft with confirm = %d %v, want 202", status, rec.Code, body)
		}
	}
}

func TestPutPlanDay(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.setStatus(t, "synced")

	rec, body := srvtest.CallDinners(t, e.h, http.MethodPut, plansPath+"2026-10-12/days/2026-10-14", `{"dinner_id":"`+e.questLog+`"}`)
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
		{"2026-10-12/days/2026-10-14", `{"dinner_id":"` + e.questLog + `","manual_label":"mummy dogs"}`, http.StatusBadRequest, "invalid_request"},
		{"2026-10-12/days/2026-10-14", `{"manual_label":"   "}`, http.StatusBadRequest, "invalid_request"},
		{"2026-10-12/days/2026-10-14", `{"clear":false}`, http.StatusBadRequest, "invalid_request"},
		{"2026-10-12/days/2026-10-14", `{"dinner_id":7}`, http.StatusBadRequest, "invalid_request"},
		{"2026-10-12/days/2026-10-14", `{"dinner_id":"x","extra":1}`, http.StatusBadRequest, "invalid_request"},
		{"2026-10-12/days/2026-10-20", `{"dinner_id":"` + e.questLog + `"}`, http.StatusBadRequest, "invalid_date"},
		{"2026-10-12/days/2026-10-32", `{"dinner_id":"` + e.questLog + `"}`, http.StatusBadRequest, "invalid_date"},
		{"2026-10-12/days/2026-10-14", `{"dinner_id":"no-such-dinner"}`, http.StatusNotFound, "not_found"},
		{"2026-10-19/days/2026-10-20", `{"dinner_id":"` + e.questLog + `"}`, http.StatusNotFound, "not_found"},
		{"2026-10-12/days/2026-10-15", `{"dinner_id":"` + e.questLog + `"}`, http.StatusConflict, "dinner_already_planned"},
	} {
		rec, body := srvtest.CallDinners(t, e.h, http.MethodPut, plansPath+tc.target, tc.body)
		if rec.Code != tc.status || srvtest.ErrorCode(body) != tc.code {
			t.Errorf("PUT %s %s = %d %v, want %d %s", tc.target, tc.body, rec.Code, body, tc.status, tc.code)
		}
	}
	if _, err := e.st.Writer.ExecContext(t.Context(), `UPDATE dinners SET status = 'archived' WHERE id = ?`, e.questLog); err != nil {
		t.Fatal(err)
	}
	rec, body = srvtest.CallDinners(t, e.h, http.MethodPut, plansPath+"2026-10-12/days/2026-10-15", `{"dinner_id":"`+e.questLog+`"}`)
	if rec.Code != http.StatusConflict || srvtest.ErrorCode(body) != "dinner_not_plannable" {
		t.Errorf("PUT archived dinner = %d %v, want 409 dinner_not_plannable", rec.Code, body)
	}
}

// TestPutPlanDayEveningExactlyOne: PUT sets a Delivery evening or a
// Dining-out evening, replacing a dinner, and clears it. A body that sets
// more than one of dinner_id, manual_label, meal_source and clear, or a
// meal_source that is not delivery or dining out, is 400 invalid_request
// and leaves the day as it was.
func TestPutPlanDayEveningExactlyOne(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	var before int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM dinners`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	rec, body := srvtest.CallDinners(t, e.h, http.MethodGet, plansPath+"2026-10-12", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET plan = %d: %s", rec.Code, rec.Body)
	}
	saturday := body["days"].([]any)[5].(map[string]any)
	if saturday["dinner"] == nil || saturday["meal_source"] != "cook" || saturday["manual_label"] != nil {
		t.Fatalf("Saturday before = %v, want a cooked dinner", saturday)
	}

	rec, body = srvtest.CallDinners(t, e.h, http.MethodPut, plansPath+"2026-10-12/days/2026-10-17", `{"meal_source":"delivery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT delivery = %d: %s", rec.Code, rec.Body)
	}
	saturday = body["days"].([]any)[5].(map[string]any)
	if saturday["dinner"] != nil || saturday["manual_label"] != nil || saturday["meal_source"] != "delivery" || saturday["is_new"] != false || saturday["start_by"] != nil {
		t.Fatalf("Saturday = %v, want a Delivery evening", saturday)
	}
	monday := body["days"].([]any)[0].(map[string]any)
	if monday["dinner"] == nil || monday["meal_source"] != "cook" {
		t.Fatalf("Monday = %v, want its dinner", monday)
	}
	var after int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM dinners`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("dinners = %d, want %d", after, before)
	}

	rec, body = srvtest.CallDinners(t, e.h, http.MethodPut, plansPath+"2026-10-12/days/2026-10-17", `{"meal_source":"dining_out"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT dining out = %d: %s", rec.Code, rec.Body)
	}
	saturday = body["days"].([]any)[5].(map[string]any)
	if saturday["dinner"] != nil || saturday["manual_label"] != nil || saturday["meal_source"] != "dining_out" {
		t.Fatalf("Saturday = %v, want a Dining-out evening", saturday)
	}

	for _, raw := range []string{
		`{}`,
		`{"meal_source":"delivery","dinner_id":"` + e.questLog + `"}`,
		`{"meal_source":"delivery","manual_label":"mummy dogs"}`,
		`{"meal_source":"delivery","clear":true}`,
		`{"dinner_id":"` + e.questLog + `","manual_label":"mummy dogs","meal_source":"delivery","clear":true}`,
		`{"meal_source":"cook"}`,
		`{"meal_source":"takeout"}`,
		`{"meal_source":""}`,
		`{"meal_source":1}`,
		`{"clear":false}`,
	} {
		rec, body = srvtest.CallDinners(t, e.h, http.MethodPut, plansPath+"2026-10-12/days/2026-10-17", raw)
		apiErr, _ := body["error"].(map[string]any)
		if rec.Code != http.StatusBadRequest || apiErr["code"] != "invalid_request" || apiErr["retryable"] != false || apiErr["message"] == "" {
			t.Errorf("PUT %s = %d %v, want 400 invalid_request", raw, rec.Code, body)
		}
	}
	rec, body = srvtest.CallDinners(t, e.h, http.MethodGet, plansPath+"2026-10-12", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET plan = %d: %s", rec.Code, rec.Body)
	}
	saturday = body["days"].([]any)[5].(map[string]any)
	if saturday["meal_source"] != "dining_out" || saturday["dinner"] != nil || saturday["manual_label"] != nil {
		t.Fatalf("Saturday after refused bodies = %v, want dining out unchanged", saturday)
	}

	rec, body = srvtest.CallDinners(t, e.h, http.MethodPut, plansPath+"2026-10-12/days/2026-10-17", `{"clear":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT clear = %d: %s", rec.Code, rec.Body)
	}
	saturday = body["days"].([]any)[5].(map[string]any)
	if saturday["dinner"] != nil || saturday["manual_label"] != nil || saturday["meal_source"] != "cook" {
		t.Fatalf("Saturday after clear = %v, want nothing planned", saturday)
	}
}

func TestPutManualNight(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	var before int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM dinners`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	rec, body := srvtest.CallDinners(t, e.h, http.MethodPut, plansPath+"2026-10-12/days/2026-10-17", `{"manual_label":" mummy dogs "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT manual = %d: %s", rec.Code, rec.Body)
	}
	saturday := body["days"].([]any)[5].(map[string]any)
	if saturday["date"] != "2026-10-17" || saturday["dinner"] != nil || saturday["manual_label"] != "mummy dogs" || saturday["is_new"] != false {
		t.Fatalf("Saturday = %v, want mummy dogs and no dinner", saturday)
	}
	var after int
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM dinners`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("dinners = %d, want %d", after, before)
	}
	rec, body = srvtest.CallDinners(t, e.h, http.MethodPut, plansPath+"2026-10-12/days/2026-10-17", `{"clear":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT clear = %d: %s", rec.Code, rec.Body)
	}
	saturday = body["days"].([]any)[5].(map[string]any)
	if saturday["dinner"] != nil || saturday["manual_label"] != nil {
		t.Fatalf("Saturday after clear = %v, want nothing planned", saturday)
	}
}

// pushCalendar is the calendar the push tests choose.
var pushCalendar = gcaltest.Calendar{ID: "dinners@group.calendar.google.com", Summary: "Dinners", AccessRole: "owner"}

// connectCalendar connects the fake Google and, when choose, chooses
// pushCalendar.
func (e plansEnv) connectCalendar(t *testing.T, choose bool) {
	t.Helper()
	e.google.SetCalendars(0, pushCalendar)
	e.google.Connect(t, e.gcal)
	if !choose {
		return
	}
	if _, err := e.gcal.SelectCalendar(t.Context(), pushCalendar.ID); err != nil {
		t.Fatal(err)
	}
}

// push calls POST /plans/{weekStart}/push-calendar and checks that neither
// the response nor any log line holds a secret.
func (e plansEnv) push(t *testing.T, weekStart string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec, body := srvtest.CallDinners(t, e.h, http.MethodPost, plansPath+weekStart+"/push-calendar", "")
	for _, secret := range []string{gcaltest.RefreshToken, gcaltest.AccessToken, gcaltest.ClientSecret} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Errorf("response holds a secret: %s", rec.Body)
		}
		if strings.Contains(e.logs.String(), secret) {
			t.Errorf("logs hold a secret: %s", e.logs)
		}
	}
	return rec, body
}

func TestPushPlanToCalendar(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.connectCalendar(t, true)

	rec, body := e.push(t, "2026-10-12")
	if rec.Code != http.StatusOK {
		t.Fatalf("push = %d: %s", rec.Code, rec.Body)
	}
	if body["status"] != "synced" || body["calendar_synced_at"] == nil {
		t.Errorf("pushed plan = %v, want synced", body)
	}
	if days, _ := body["days"].([]any); len(days) != 7 {
		t.Errorf("days = %v, want 7", body["days"])
	}
	if n := len(e.google.Events(pushCalendar.ID)); n != 7 {
		t.Errorf("calendar has %d events, want 7", n)
	}
	monday := e.google.Events(pushCalendar.ID)[plan.EventID(mustLocalDate(t, "2026-10-12"), mustLocalDate(t, "2026-10-12"))]
	if !strings.HasPrefix(monday.Description, "Beef Chili\nStart by 1:30 pm\nhttps://mealquest.example/dinner/") {
		t.Errorf("Monday's description = %q", monday.Description)
	}
}

func TestPushPlanToCalendarErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		week   string
		setup  func(t *testing.T, e plansEnv)
		status int
		code   string
	}{
		{"not connected", "2026-10-12", func(*testing.T, plansEnv) {}, http.StatusConflict, "not_connected"},
		{"no calendar chosen", "2026-10-12", func(t *testing.T, e plansEnv) { e.connectCalendar(t, false) }, http.StatusConflict, "no_calendar"},
		{"calendar deleted", "2026-10-12", func(t *testing.T, e plansEnv) {
			e.connectCalendar(t, true)
			e.google.SetCalendars(0)
		}, http.StatusConflict, "calendar_unavailable"},
		{"calendar read-only", "2026-10-12", func(t *testing.T, e plansEnv) {
			e.connectCalendar(t, true)
			e.google.SetCalendars(0, gcaltest.Calendar{ID: pushCalendar.ID, Summary: "Dinners", AccessRole: "reader"})
		}, http.StatusConflict, "calendar_unavailable"},
		{"grant revoked", "2026-10-12", func(t *testing.T, e plansEnv) {
			e.connectCalendar(t, true)
			e.google.RevokeGrant()
		}, http.StatusConflict, "google_reconnect_required"},
		{"401 after a refresh", "2026-10-12", func(t *testing.T, e plansEnv) {
			e.connectCalendar(t, true)
			e.google.RejectAccessToken()
		}, http.StatusConflict, "google_reconnect_required"},
		{"Google unavailable", "2026-10-12", func(t *testing.T, e plansEnv) {
			e.connectCalendar(t, true)
			e.google.FailEventWrites(3, 3, http.StatusServiceUnavailable)
		}, http.StatusServiceUnavailable, "google_unavailable"},
		{"Google fails part way", "2026-10-12", func(t *testing.T, e plansEnv) {
			e.connectCalendar(t, true)
			e.google.FailEventWrite(3, http.StatusBadRequest)
		}, http.StatusBadGateway, "google_failed"},
		{"no plan", "2026-10-19", func(t *testing.T, e plansEnv) { e.connectCalendar(t, true) }, http.StatusNotFound, "not_found"},
		{"not a Monday", "2026-10-13", func(t *testing.T, e plansEnv) { e.connectCalendar(t, true) }, http.StatusBadRequest, "invalid_week"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newPlansRouter(t)
			tc.setup(t, e)
			rec, body := e.push(t, tc.week)
			if rec.Code != tc.status || srvtest.ErrorCode(body) != tc.code {
				t.Errorf("push = %d %s, want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
			if apiErr, _ := body["error"].(map[string]any); apiErr["retryable"] != (tc.code == "google_unavailable") {
				t.Errorf("push = %s, want retryable only when Google is unavailable", rec.Body)
			}
			if tc.code == "google_reconnect_required" {
				_, status := srvtest.CallDinners(t, e.h, http.MethodGet, srvtest.GooglePath, "")
				if status["status"] != "reconnect_required" || status["last_sync_error"] != gcal.ReconnectReason {
					t.Errorf("GET %s after the push = %v, want reconnect_required with the reason", srvtest.GooglePath, status)
				}
			}
			p, err := e.svc.Get(t.Context(), mustLocalDate(t, "2026-10-12"))
			if err != nil {
				t.Fatal(err)
			}
			if p.Status != plan.StatusDraft || p.CalendarSyncedAt != nil {
				t.Errorf("plan = %s synced at %v after a failed push, want draft", p.Status, p.CalendarSyncedAt)
			}
		})
	}
}

// TestPushPlanAfterRefusalWithNoCalendar pushes after Google refused the
// grant while listing calendars, before any was chosen: the push is 409
// google_reconnect_required, not no_calendar, since only connecting again
// restores it. After connecting again and choosing a calendar it succeeds.
func TestPushPlanAfterRefusalWithNoCalendar(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.connectCalendar(t, false)
	e.google.RevokeGrant()
	if _, err := e.gcal.Calendars(t.Context()); !errors.Is(err, gcal.ErrReconnect) {
		t.Fatalf("Calendars = %v, want ErrReconnect", err)
	}

	rec, body := e.push(t, "2026-10-12")
	if rec.Code != http.StatusConflict || srvtest.ErrorCode(body) != "google_reconnect_required" {
		t.Errorf("push = %d %s, want 409 google_reconnect_required", rec.Code, rec.Body)
	}

	e.connectCalendar(t, true)
	if rec, _ := e.push(t, "2026-10-12"); rec.Code != http.StatusOK {
		t.Errorf("push after connecting again = %d: %s", rec.Code, rec.Body)
	}
}

func TestPushPlanChangedWhilePushing(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	e.connectCalendar(t, true)
	arrived, release := e.google.HoldEvents()
	defer release()
	pushed := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, srvtest.SignedIn(http.MethodPost, plansPath+"2026-10-12/push-calendar", nil))
		pushed <- rec
	}()
	<-arrived
	if _, err := e.svc.Swap(t.Context(), mustLocalDate(t, "2026-10-12"), "2026-10-13", e.questLog); err != nil {
		t.Fatal(err)
	}
	release()
	rec := <-pushed
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"plan_changed"`) {
		t.Errorf("push = %d %s, want 409 plan_changed", rec.Code, rec.Body)
	}
}

func mustLocalDate(t *testing.T, s string) store.LocalDate {
	t.Helper()
	d, err := store.ParseLocalDate(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
