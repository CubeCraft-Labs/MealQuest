package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// sentinelSecret stands in for a credential a handler holds while it runs.
const sentinelSecret = "sk-sentinel-5f2c9a"

type countPayload struct {
	Steps int `json:"steps"`
}

type countResult struct {
	Done int `json:"done"`
}

func (c countResult) PublicJSON() ([]byte, error) { return json.Marshal(c) }

func migratedStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
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
	return st
}

func newRunner(t *testing.T, st *store.Store, workers int) *Runner {
	t.Helper()
	return newRunnerLogging(t, st, workers, io.Discard)
}

func newRunnerLogging(t *testing.T, st *store.Store, workers int, w io.Writer) *Runner {
	t.Helper()
	r, err := NewRunner(st, slog.New(slog.NewTextHandler(w, nil)), workers)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// logBuffer collects log output written by concurrent workers.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
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

// start starts r and stops it when the test ends. The returned function
// stops it early and waits for its workers.
func start(t *testing.T, r *Runner) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if err := r.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	stop = func() {
		cancel()
		r.Wait()
	}
	t.Cleanup(stop)
	return stop
}

func register[P any, R Public](t *testing.T, r *Runner, kind string, h Handler[P, R], failureCodes ...string) *Kind[P] {
	t.Helper()
	k, err := Register(r, kind, h, failureCodes...)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func enqueue[P any](t *testing.T, k *Kind[P], payload P) string {
	t.Helper()
	id, err := k.Enqueue(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// waitFor polls Get until done accepts the job, failing after five seconds.
func waitFor(t *testing.T, r *Runner, id string, done func(Job) bool) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, err := r.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if done(job) {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s never reached the expected state; last %+v (result %s)", id, job, job.Result)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func finished(j Job) bool { return j.Status == StatusSucceeded || j.Status == StatusFailed }

// rawRow returns every column of the job's row joined, for leak checks.
func rawRow(t *testing.T, st *store.Store, id string) string {
	t.Helper()
	var cols [10]any
	ptrs := make([]any, len(cols))
	for i := range cols {
		ptrs[i] = &cols[i]
	}
	if err := st.Reader.QueryRow("SELECT * FROM jobs WHERE id = ?", id).Scan(ptrs...); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(cols[:]...)
}

func TestJobSucceeds(t *testing.T) {
	t.Parallel()
	st := migratedStore(t)
	r := newRunner(t, st, 2)
	count := register(t, r, "count", func(_ context.Context, p countPayload, _ func(countResult) error) (countResult, error) {
		return countResult{Done: p.Steps}, nil
	})
	start(t, r)

	id := enqueue(t, count, countPayload{Steps: 3})
	job := waitFor(t, r, id, finished)
	if job.ID != id || job.Kind != "count" || job.Status != StatusSucceeded || job.Error != nil {
		t.Fatalf("job = %+v, want succeeded count job %s with no error", job, id)
	}
	if string(job.Result) != `{"done":3}` {
		t.Errorf("result = %s, want {\"done\":3}", job.Result)
	}

	var startedAt, finishedAt, createdAt, updatedAt, payload string
	if err := st.Reader.QueryRow("SELECT started_at, finished_at, created_at, updated_at, payload FROM jobs WHERE id = ?", id).
		Scan(&startedAt, &finishedAt, &createdAt, &updatedAt, &payload); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{"started_at": startedAt, "finished_at": finishedAt, "created_at": createdAt, "updated_at": updatedAt} {
		var ts store.Timestamp
		if err := ts.Scan(v); err != nil {
			t.Errorf("%s = %q: %v", name, v, err)
		}
	}
	if payload != `{"steps":3}` {
		t.Errorf("payload = %s, want {\"steps\":3}", payload)
	}
}

func TestJobFailsWithFailureCode(t *testing.T) {
	t.Parallel()
	st := migratedStore(t)
	r := newRunner(t, st, 1)
	k := register(t, r, "suggest", func(context.Context, countPayload, func(countResult) error) (countResult, error) {
		return countResult{}, fmt.Errorf("suggest: %w", &Failure{Code: "no_regulars"})
	}, "no_regulars")
	start(t, r)

	job := waitFor(t, r, enqueue(t, k, countPayload{}), finished)
	if job.Status != StatusFailed || job.Error == nil || *job.Error != "no_regulars" {
		t.Fatalf("job = %+v, want failed with code no_regulars", job)
	}
	if job.Result != nil {
		t.Errorf("result = %s, want none", job.Result)
	}
	var stored string
	if err := st.Reader.QueryRow("SELECT error FROM jobs WHERE id = ?", job.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "no_regulars" {
		t.Errorf("stored error = %q, want only the code", stored)
	}
}

func TestUndeclaredFailureCodeIsInternal(t *testing.T) {
	t.Parallel()
	r := newRunner(t, migratedStore(t), 1)
	k := register(t, r, "suggest", func(context.Context, countPayload, func(countResult) error) (countResult, error) {
		return countResult{}, &Failure{Code: "not_declared"}
	}, "no_regulars")
	start(t, r)

	job := waitFor(t, r, enqueue(t, k, countPayload{}), finished)
	if job.Status != StatusFailed || job.Error == nil || *job.Error != CodeInternal {
		t.Fatalf("job = %+v, want failed with %q", job, CodeInternal)
	}
}

// credential holds a secret the way config.Secret does: unexported, so
// encoding/json cannot see it.
type credential struct{ value string }

func (credential) String() string { return "[REDACTED]" }

// leakyResult holds a secret in exported and unexported fields; only
// Dinners is public.
type leakyResult struct {
	Dinners []string
	Key     credential
	Token   string
	apiKey  string
}

func (l leakyResult) PublicJSON() ([]byte, error) {
	return json.Marshal(struct {
		Dinners []string `json:"dinners"`
	}{l.Dinners})
}

// badResult has no valid public JSON; its encoding error quotes a secret.
type badResult struct{}

func (badResult) PublicJSON() ([]byte, error) {
	return nil, fmt.Errorf("encode with key %s", sentinelSecret)
}

// TestSecretsNeverStoredOrLogged proves a handler holding a secret cannot
// surface it in the jobs row, Get's result or the log through its result,
// partial results, *Failure, error text or result encoding error.
func TestSecretsNeverStoredOrLogged(t *testing.T) {
	t.Parallel()
	st := migratedStore(t)
	var logs logBuffer
	r := newRunnerLogging(t, st, 1, &logs)
	secretResult := leakyResult{Dinners: []string{"Tacos"}, Key: credential{sentinelSecret}, Token: sentinelSecret, apiKey: sentinelSecret}
	typed := register(t, r, "typed", func(_ context.Context, _ countPayload, publish func(leakyResult) error) (leakyResult, error) {
		if err := publish(secretResult); err != nil {
			return leakyResult{}, err
		}
		return secretResult, nil
	})
	leakyError := register(t, r, "leaky-error", func(context.Context, countPayload, func(leakyResult) error) (leakyResult, error) {
		return leakyResult{}, fmt.Errorf("GET https://api.example.com/v1?key=%s: 401 %s", sentinelSecret, sentinelSecret)
	})
	leakyFailure := register(t, r, "leaky-failure", func(context.Context, countPayload, func(leakyResult) error) (leakyResult, error) {
		return leakyResult{}, &Failure{Code: sentinelSecret}
	}, "quota_exceeded")
	failure := register(t, r, "failure", func(context.Context, countPayload, func(leakyResult) error) (leakyResult, error) {
		return leakyResult{}, fmt.Errorf("call with key %s: %w", sentinelSecret, &Failure{Code: "quota_exceeded"})
	}, "quota_exceeded")
	badEncoding := register(t, r, "bad-encoding", func(context.Context, countPayload, func(badResult) error) (badResult, error) {
		return badResult{}, nil
	})
	stop := start(t, r)

	cases := []struct {
		kind       *Kind[countPayload]
		wantStatus Status
		wantResult string
		wantError  string
	}{
		{typed, StatusSucceeded, `{"dinners":["Tacos"]}`, ""},
		{leakyError, StatusFailed, "", CodeInternal},
		{leakyFailure, StatusFailed, "", CodeInternal},
		{failure, StatusFailed, "", "quota_exceeded"},
		{badEncoding, StatusFailed, "", CodeInternal},
	}
	var ids []string
	for _, c := range cases {
		job := waitFor(t, r, enqueue(t, c.kind, countPayload{}), finished)
		ids = append(ids, job.ID)
		gotError := ""
		if job.Error != nil {
			gotError = *job.Error
		}
		if job.Status != c.wantStatus || string(job.Result) != c.wantResult || gotError != c.wantError {
			t.Errorf("%s job = %+v (result %s), want %s with result %q and error %q", job.Kind, job, job.Result, c.wantStatus, c.wantResult, c.wantError)
		}
		body, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), sentinelSecret) {
			t.Errorf("%s job as served contains the secret: %s", job.Kind, body)
		}
	}
	stop()
	for _, id := range ids {
		if row := rawRow(t, st, id); strings.Contains(row, sentinelSecret) {
			t.Errorf("job %s row contains the secret: %s", id, row)
		}
	}
	if strings.Contains(logs.String(), sentinelSecret) {
		t.Errorf("log contains the secret:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "code="+CodeInternal) {
		t.Errorf("log does not record the internal_error code:\n%s", logs.String())
	}
}

func TestProgressiveResultsVisibleMidRun(t *testing.T) {
	t.Parallel()
	r := newRunner(t, migratedStore(t), 1)
	release := make(chan struct{})
	k := register(t, r, "draft", func(ctx context.Context, p countPayload, publish func(countResult) error) (countResult, error) {
		for i := 1; i < p.Steps; i++ {
			if err := publish(countResult{Done: i}); err != nil {
				return countResult{}, err
			}
			select {
			case <-release:
			case <-ctx.Done():
				return countResult{}, ctx.Err()
			}
		}
		return countResult{Done: p.Steps}, nil
	})
	start(t, r)

	id := enqueue(t, k, countPayload{Steps: 3})
	for step := 1; step < 3; step++ {
		want := fmt.Sprintf(`{"done":%d}`, step)
		job := waitFor(t, r, id, func(j Job) bool { return string(j.Result) == want })
		if job.Status != StatusRunning || job.Error != nil {
			t.Fatalf("mid-run job = %+v, want running with no error", job)
		}
		release <- struct{}{}
	}
	job := waitFor(t, r, id, finished)
	if job.Status != StatusSucceeded || string(job.Result) != `{"done":3}` {
		t.Fatalf("job = %+v (result %s), want succeeded with {\"done\":3}", job, job.Result)
	}
}

func TestRestartFailsInterruptedJobsAndRunsQueuedOnes(t *testing.T) {
	t.Parallel()
	st := migratedStore(t)

	first := newRunner(t, st, 1)
	started := make(chan struct{})
	k := register(t, first, "slow", func(ctx context.Context, _ countPayload, publish func(countResult) error) (countResult, error) {
		if err := publish(countResult{Done: 1}); err != nil {
			return countResult{}, err
		}
		close(started)
		<-ctx.Done()
		return countResult{}, ctx.Err()
	})
	stop := start(t, first)
	interrupted := enqueue(t, k, countPayload{})
	<-started
	waiting := enqueue(t, k, countPayload{}) // the only worker is busy
	stop()

	if job := waitFor(t, first, interrupted, func(Job) bool { return true }); job.Status != StatusRunning {
		t.Fatalf("after shutdown job = %+v, want left running", job)
	}

	second := newRunner(t, st, 1)
	register(t, second, "slow", func(context.Context, countPayload, func(countResult) error) (countResult, error) {
		return countResult{Done: 9}, nil
	})
	start(t, second)

	job := waitFor(t, second, interrupted, finished)
	if job.Status != StatusFailed || job.Error == nil || *job.Error != "interrupted by restart" {
		t.Errorf("interrupted job = %+v, want failed with %q", job, "interrupted by restart")
	}
	if string(job.Result) != `{"done":1}` {
		t.Errorf("interrupted job result = %s, want the last partial result kept", job.Result)
	}
	if job := waitFor(t, second, waiting, finished); job.Status != StatusSucceeded {
		t.Errorf("queued job = %+v, want run by the new runner", job)
	}
}

func TestWorkerPoolIsBounded(t *testing.T) {
	t.Parallel()
	r := newRunner(t, migratedStore(t), 2)
	var running, peak atomic.Int32
	release := make(chan struct{})
	k := register(t, r, "busy", func(context.Context, countPayload, func(countResult) error) (countResult, error) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		running.Add(-1)
		return countResult{}, nil
	})
	start(t, r)

	ids := make([]string, 5)
	for i := range ids {
		ids[i] = enqueue(t, k, countPayload{})
	}
	deadline := time.Now().Add(5 * time.Second)
	for running.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatal("two workers never ran at once")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	for _, id := range ids {
		if job := waitFor(t, r, id, finished); job.Status != StatusSucceeded {
			t.Errorf("job = %+v, want succeeded", job)
		}
	}
	if got := peak.Load(); got != 2 {
		t.Errorf("peak concurrent jobs = %d, want 2", got)
	}
}

func TestJobWithoutHandlerFails(t *testing.T) {
	t.Parallel()
	st := migratedStore(t)
	old := newRunner(t, st, 1)
	k := register(t, old, "retired", func(context.Context, countPayload, func(countResult) error) (countResult, error) {
		return countResult{}, nil
	})
	id := enqueue(t, k, countPayload{}) // never started

	r := newRunner(t, st, 1)
	start(t, r)
	job := waitFor(t, r, id, finished)
	if job.Status != StatusFailed || job.Error == nil || *job.Error != CodeUnknownKind {
		t.Errorf("job = %+v, want failed for a missing handler", job)
	}
}

func TestGetUnknownID(t *testing.T) {
	t.Parallel()
	r := newRunner(t, migratedStore(t), 1)
	if _, err := r.Get(context.Background(), store.NewID()); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get unknown id error = %v, want ErrNotFound", err)
	}
}

func TestRegisterRefusesBadRegistrations(t *testing.T) {
	t.Parallel()
	r := newRunner(t, migratedStore(t), 1)
	h := func(context.Context, countPayload, func(countResult) error) (countResult, error) {
		return countResult{}, nil
	}
	register(t, r, "count", h)

	if _, err := Register(r, "count", h); err == nil {
		t.Error("duplicate kind registered")
	}
	if _, err := Register(r, "", h); err == nil {
		t.Error("empty kind registered")
	}
	if _, err := Register[countPayload, countResult](r, "nil", nil); err == nil {
		t.Error("nil handler registered")
	}
	for _, code := range []string{"", "Bad Code", "sk-sentinel", CodeInternal, CodeInterrupted, CodeUnknownKind} {
		if _, err := Register(r, "code", h, code); err == nil {
			t.Errorf("failure code %q accepted", code)
		}
	}
	start(t, r)
	if _, err := Register(r, "late", h); err == nil {
		t.Error("kind registered after Start")
	}
	if err := r.Start(context.Background()); err == nil {
		t.Error("second Start succeeded")
	}
}

func TestNewRunnerRefusesBadConfig(t *testing.T) {
	t.Parallel()
	st := migratedStore(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewRunner(st, logger, 0); err == nil {
		t.Error("zero workers accepted")
	}
	if _, err := NewRunner(nil, logger, 1); err == nil {
		t.Error("nil store accepted")
	}
	if _, err := NewRunner(st, nil, 1); err == nil {
		t.Error("nil logger accepted")
	}
}

func TestNoopSucceeds(t *testing.T) {
	t.Parallel()
	r := newRunner(t, migratedStore(t), 1)
	builtins, err := RegisterBuiltins(r)
	if err != nil {
		t.Fatal(err)
	}
	start(t, r)

	job := waitFor(t, r, enqueue(t, builtins.Noop, NoopPayload{}), finished)
	if job.Kind != KindNoop || job.Status != StatusSucceeded || string(job.Result) != `{"ok":true}` || job.Error != nil {
		t.Errorf("noop job = %+v (result %s), want succeeded with {\"ok\":true}", job, job.Result)
	}
}
