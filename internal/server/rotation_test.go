package server

import (
	"net/http"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const rotationReviewsPath = "/api/v1/rotation-reviews"

// triedPadThai plans the Quest log dinner Pad Thai on Wednesday 2026-10-14
// and marks the day tried, so Pad Thai has a pending Rotation check.
func triedPadThai(t *testing.T, e plansEnv) {
	t.Helper()
	rec, _ := callDinners(t, e.h, http.MethodPut, plansPath+"2026-10-12/days/2026-10-14", `{"dinner_id":"`+e.questLog+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("swap Pad Thai in = %d: %s", rec.Code, rec.Body)
	}
	day, err := store.ParseLocalDate("2026-10-14")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.MarkTried(t.Context(), day); err != nil || n != 1 {
		t.Fatalf("MarkTried = %d, %v, want 1", n, err)
	}
}

func TestGetRotationReviews(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)

	rec, body := callDinners(t, e.h, http.MethodGet, rotationReviewsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	if reviews, ok := body["reviews"].([]any); !ok || len(reviews) != 0 {
		t.Errorf("GET before MarkTried = %v, want an empty reviews list", body)
	}

	triedPadThai(t, e)
	rec, body = callDinners(t, e.h, http.MethodGet, rotationReviewsPath, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body)
	}
	reviews, _ := body["reviews"].([]any)
	if len(reviews) != 1 {
		t.Fatalf("reviews = %v, want one", body)
	}
	r := reviews[0].(map[string]any)
	d, _ := r["dinner"].(map[string]any)
	if len(r) != 2 || r["plan_date"] != "2026-10-14" || len(d) != 3 ||
		d["id"] != e.questLog || d["name"] != "Pad Thai" || d["photo"] != nil {
		t.Errorf("review = %v, want {dinner {id, name, photo}, plan_date}", r)
	}
}

func TestPostRotationReview(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ decision, status string }{
		{"keep", "regular"},
		{"drop", "archived"},
		{"not_made", "quest_log"},
	} {
		t.Run(tc.decision, func(t *testing.T) {
			t.Parallel()
			e := newPlansRouter(t)
			triedPadThai(t, e)
			target := dinnersPath + "/" + e.questLog + "/rotation-review"

			rec, body := callDinners(t, e.h, http.MethodPost, target, `{"decision":"`+tc.decision+`"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("POST = %d: %s", rec.Code, rec.Body)
			}
			if body["decision"] != tc.decision || body["dinner_status"] != tc.status || body["plan_date"] != "2026-10-14" ||
				body["updated_by"] != testSession.Subject || body["decided_at"] == "" {
				t.Errorf("decided = %v", body)
			}
			var status, origin, updatedBy string
			if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT status, origin, updated_by FROM dinners WHERE id = ?`, e.questLog).
				Scan(&status, &origin, &updatedBy); err != nil {
				t.Fatal(err)
			}
			if status != tc.status || origin != "regular" || updatedBy != testSession.Subject {
				t.Errorf("dinner = %s %s %s, want %s regular %s", status, origin, updatedBy, tc.status, testSession.Subject)
			}

			rec, body = callDinners(t, e.h, http.MethodPost, target, `{"decision":"keep"}`)
			if rec.Code != http.StatusConflict || errorCode(body) != "no_pending_review" {
				t.Errorf("second decision = %d %v, want 409 no_pending_review", rec.Code, body)
			}
			rec, body = callDinners(t, e.h, http.MethodGet, rotationReviewsPath, "")
			if reviews, _ := body["reviews"].([]any); rec.Code != http.StatusOK || len(reviews) != 0 {
				t.Errorf("GET after deciding = %d %v, want no reviews", rec.Code, body)
			}
		})
	}
}

// A dinner that has never had a Rotation check is 404; a second decision
// on one already decided is 409 (TestPostRotationReview).
func TestPostRotationReviewNeverChecked(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	rec, body := callDinners(t, e.h, http.MethodPost, dinnersPath+"/"+e.questLog+"/rotation-review", `{"decision":"keep"}`)
	if rec.Code != http.StatusNotFound || errorCode(body) != "not_found" {
		t.Errorf("never checked = %d %v, want 404 not_found", rec.Code, body)
	}
	var status string
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT status FROM dinners WHERE id = ?`, e.questLog).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "quest_log" {
		t.Errorf("status after a refused decision = %s, want quest_log", status)
	}
}

func TestPostRotationReviewRefused(t *testing.T) {
	t.Parallel()
	e := newPlansRouter(t)
	triedPadThai(t, e)
	target := dinnersPath + "/" + e.questLog + "/rotation-review"

	for _, tc := range []struct {
		name, target, body string
		code               int
		errCode            string
	}{
		{"no decision", target, `{}`, http.StatusBadRequest, "invalid_request"},
		{"null decision", target, `{"decision":null}`, http.StatusBadRequest, "invalid_request"},
		{"unknown field", target, `{"decision":"keep","status":"regular"}`, http.StatusBadRequest, "invalid_request"},
		{"unknown decision", target, `{"decision":"promote"}`, http.StatusBadRequest, "invalid_decision"},
		{"unknown dinner", dinnersPath + "/nope/rotation-review", `{"decision":"keep"}`, http.StatusNotFound, "not_found"},
	} {
		rec, body := callDinners(t, e.h, http.MethodPost, tc.target, tc.body)
		if rec.Code != tc.code || errorCode(body) != tc.errCode {
			t.Errorf("%s = %d %v, want %d %s", tc.name, rec.Code, body, tc.code, tc.errCode)
		}
	}
	var status string
	if err := e.st.Reader.QueryRowContext(t.Context(), `SELECT status FROM dinners WHERE id = ?`, e.questLog).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "tried" {
		t.Errorf("status after refused decisions = %s, want tried", status)
	}
}
