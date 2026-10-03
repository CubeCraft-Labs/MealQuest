package server

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

type draftPayload struct{}

type draftResult struct {
	Days int `json:"days"`
}

func (d draftResult) PublicJSON() ([]byte, error) { return json.Marshal(d) }

// startedRunner registers a "draft" kind that publishes {"days":1}, waits
// for release and returns {"days":7}, or fails with a *jobs.Failure when
// fail is closed first. It starts the runner and stops it when the test ends.
func startedRunner(t *testing.T, release, fail <-chan struct{}) (*jobs.Runner, *jobs.Kind[draftPayload]) {
	t.Helper()
	runner := newTestRunner(t)
	kind, err := jobs.Register(runner, "draft", func(ctx context.Context, _ draftPayload, publish func(draftResult) error) (draftResult, error) {
		if err := publish(draftResult{Days: 1}); err != nil {
			return draftResult{}, err
		}
		select {
		case <-release:
			return draftResult{Days: 7}, nil
		case <-fail:
			return draftResult{}, &jobs.Failure{Code: "no_regulars"}
		case <-ctx.Done():
			return draftResult{}, ctx.Err()
		}
	}, "no_regulars")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := runner.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		runner.Wait()
	})
	return runner, kind
}

// pollJob GETs the job until want accepts the body, failing after five
// seconds.
func pollJob(t *testing.T, h http.Handler, id string, want func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec, body := serve(t, h, signedIn(http.MethodGet, "/api/v1/jobs/"+id, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
		}
		if want(body) {
			if keys := slices.Sorted(maps.Keys(body)); !slices.Equal(keys, []string{"error", "id", "kind", "result", "status"}) {
				t.Errorf("body fields = %v, want id, kind, status, result, error", keys)
			}
			return body
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never reached the expected state; last %v", id, body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func resultDays(body map[string]any) float64 {
	result, _ := body["result"].(map[string]any)
	days, _ := result["days"].(float64)
	return days
}

func TestGetJobShowsProgressThenResult(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	runner, kind := startedRunner(t, release, nil)
	router := newTestRouterWith(t, "dev", newTestSettings(t), runner)
	id, err := kind.Enqueue(context.Background(), draftPayload{})
	if err != nil {
		t.Fatal(err)
	}

	mid := pollJob(t, router, id, func(b map[string]any) bool { return resultDays(b) == 1 })
	if mid["id"] != id || mid["kind"] != "draft" || mid["status"] != "running" || mid["error"] != nil {
		t.Errorf("mid-run body = %v, want running draft job %s with no error", mid, id)
	}

	close(release)
	done := pollJob(t, router, id, func(b map[string]any) bool { return b["status"] == "succeeded" })
	if resultDays(done) != 7 || done["error"] != nil {
		t.Errorf("finished body = %v, want result {\"days\":7} and no error", done)
	}
}

func TestGetJobShowsFailure(t *testing.T) {
	t.Parallel()
	fail := make(chan struct{})
	close(fail)
	runner, kind := startedRunner(t, nil, fail)
	router := newTestRouterWith(t, "dev", newTestSettings(t), runner)
	id, err := kind.Enqueue(context.Background(), draftPayload{})
	if err != nil {
		t.Fatal(err)
	}

	body := pollJob(t, router, id, func(b map[string]any) bool { return b["status"] == "failed" })
	if body["error"] != "no_regulars" {
		t.Errorf("error = %v, want the Failure code", body["error"])
	}
}

func TestGetJobUnknownIDIsNotFound(t *testing.T) {
	t.Parallel()
	router := newTestRouter(t, "dev")

	rec, _ := serve(t, router, signedIn(http.MethodGet, "/api/v1/jobs/"+store.NewID(), nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if want := (errorBody{Error: apiError{Code: "not_found", Message: "job not found"}}); body != want {
		t.Errorf("body = %+v, want %+v", body, want)
	}
}

func TestGetJobRequiresSession(t *testing.T) {
	t.Parallel()
	router := newTestRouter(t, "dev")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/jobs/"+store.NewID(), nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
