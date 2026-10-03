package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/jobs"
)

// TestNewJobRunnerRunsNoop proves the runner run builds can execute a job:
// a noop job enqueued before Start, as at boot, succeeds with its result.
func TestNewJobRunnerRunsNoop(t *testing.T) {
	ctx := context.Background()
	db, err := openStore(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	runner, kinds, err := newJobRunner(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	id, err := kinds.builtins.Noop.Enqueue(ctx, jobs.NoopPayload{})
	if err != nil {
		t.Fatal(err)
	}

	jobsCtx, stopJobs := context.WithCancel(ctx)
	if err := runner.Start(jobsCtx); err != nil {
		stopJobs()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopJobs()
		runner.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		job, err := runner.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.Status == jobs.StatusSucceeded || job.Status == jobs.StatusFailed {
			if job.Kind != jobs.KindNoop || job.Status != jobs.StatusSucceeded || string(job.Result) != `{"ok":true}` || job.Error != nil {
				t.Fatalf("noop job = %+v (result %s), want succeeded with {\"ok\":true}", job, job.Result)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("noop job never finished; last %+v", job)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
