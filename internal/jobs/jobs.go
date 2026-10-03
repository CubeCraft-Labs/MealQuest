// Package jobs runs long work in the background. A job is a row in the jobs
// table: Enqueue inserts it queued, and a fixed pool of workers claims queued
// jobs oldest first, so the table itself is the queue and a job enqueued
// before a restart still runs after it.
//
// Each job kind has one handler, registered before Start. A handler takes a
// typed payload and returns a typed result, which the runner stores as JSON;
// it may publish partial results while it runs. Only typed data and
// *Failure messages are stored, so no handler error text, which may quote a
// URL, token or response body from a dependency, can reach the table or the
// API.
package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Status is the state of a job.
type Status string

// Job states. A job moves queued → running → succeeded or failed.
const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
)

// Stored error messages the runner writes itself.
const (
	// MessageInterrupted is the error of a job a previous process left running.
	MessageInterrupted = "interrupted by restart"
	// MessageInternal is the error of a job whose handler failed with an error
	// that is not a *Failure. The handler's error is logged, not stored.
	MessageInternal = "internal error"
)

// ErrNotFound is returned by Get for an unknown job id.
var ErrNotFound = errors.New("jobs: job not found")

// Failure is a handler error whose Message is safe to show the household and
// is stored as the job's error. Every other handler error is stored as
// MessageInternal.
type Failure struct {
	Message string
}

func (f *Failure) Error() string { return f.Message }

// Handler runs one job with its payload. It may call publish any number of
// times with a partial result, which Get returns while the job runs; its
// return value is the final result. ctx is cancelled when the runner stops.
type Handler[P, R any] func(ctx context.Context, payload P, publish func(partial R) error) (R, error)

// Job is a job as reported by GET /api/v1/jobs/{id}. Result is the latest
// partial or final result, and is null until the handler publishes one.
// Error is set only on a failed job.
type Job struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Status Status          `json:"status"`
	Result json.RawMessage `json:"result"`
	Error  *string         `json:"error"`
}

// runFunc is a registered Handler with its payload and results as JSON.
type runFunc func(ctx context.Context, payload string, publish func(json.RawMessage) error) (json.RawMessage, error)

// Runner owns the job handlers and the worker pool.
type Runner struct {
	st      *store.Store
	logger  *slog.Logger
	workers int
	// wake holds up to one token per worker; Enqueue adds one so an idle
	// worker looks for the new job.
	wake chan struct{}

	mu       sync.Mutex
	handlers map[string]runFunc
	started  bool

	wg sync.WaitGroup
}

// NewRunner returns a runner over st with workers concurrent workers.
func NewRunner(st *store.Store, logger *slog.Logger, workers int) (*Runner, error) {
	if st == nil {
		return nil, errors.New("jobs: nil store")
	}
	if logger == nil {
		return nil, errors.New("jobs: nil logger")
	}
	if workers < 1 {
		return nil, fmt.Errorf("jobs: %d workers, want at least 1", workers)
	}
	return &Runner{
		st:       st,
		logger:   logger,
		workers:  workers,
		wake:     make(chan struct{}, workers),
		handlers: make(map[string]runFunc),
	}, nil
}

// Kind enqueues jobs of one registered kind.
type Kind[P any] struct {
	r    *Runner
	name string
}

// Register makes h the handler for kind and returns the Kind that enqueues
// its jobs. It must be called before Start, once per kind.
func Register[P, R any](r *Runner, kind string, h Handler[P, R]) (*Kind[P], error) {
	if kind == "" {
		return nil, errors.New("jobs: register: empty kind")
	}
	if h == nil {
		return nil, fmt.Errorf("jobs: register %q: nil handler", kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return nil, fmt.Errorf("jobs: register %q: runner already started", kind)
	}
	if _, ok := r.handlers[kind]; ok {
		return nil, fmt.Errorf("jobs: register %q: kind already registered", kind)
	}
	r.handlers[kind] = func(ctx context.Context, payload string, publish func(json.RawMessage) error) (json.RawMessage, error) {
		var p P
		dec := json.NewDecoder(bytes.NewReader([]byte(payload)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decode payload: %w", err)
		}
		result, err := h(ctx, p, func(partial R) error {
			data, err := json.Marshal(partial)
			if err != nil {
				return fmt.Errorf("jobs: encode partial result: %w", err)
			}
			return publish(data)
		})
		if err != nil {
			return nil, err
		}
		data, err := json.Marshal(result)
		if err != nil {
			return nil, fmt.Errorf("encode result: %w", err)
		}
		return data, nil
	}
	return &Kind[P]{r: r, name: kind}, nil
}

// Enqueue stores a queued job with payload and returns its id. A job
// enqueued before Start waits for it.
func (k *Kind[P]) Enqueue(ctx context.Context, payload P) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("jobs: enqueue %s: encode payload: %w", k.name, err)
	}
	id := store.NewID()
	now := store.Now().String()
	if err := db.New(k.r.st.Writer).CreateJob(ctx, db.CreateJobParams{
		ID:        id,
		Kind:      k.name,
		Payload:   string(data),
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		return "", fmt.Errorf("jobs: enqueue %s: %w", k.name, err)
	}
	select {
	case k.r.wake <- struct{}{}:
	default: // every worker already has a token and will look again
	}
	return id, nil
}

// Start fails every job a previous process left running with
// MessageInterrupted, then starts the workers. They stop when ctx is cancelled;
// Wait waits for them. A job whose handler returns an error after ctx is
// cancelled is left running, so the next Start fails it as interrupted.
func (r *Runner) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return errors.New("jobs: runner already started")
	}
	now := store.Now().String()
	n, err := db.New(r.st.Writer).FailRunningJobs(ctx, db.FailRunningJobsParams{
		Error: sql.NullString{String: MessageInterrupted, Valid: true},
		Now:   sql.NullString{String: now, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("jobs: fail interrupted jobs: %w", err)
	}
	if n > 0 {
		r.logger.WarnContext(ctx, "failed jobs interrupted by restart", "count", n)
	}
	r.started = true
	for range r.workers {
		r.wg.Add(1)
		go r.work(ctx)
	}
	return nil
}

// Wait blocks until every worker has stopped.
func (r *Runner) Wait() {
	r.wg.Wait()
}

// Get returns the job with id, or ErrNotFound.
func (r *Runner) Get(ctx context.Context, id string) (Job, error) {
	row, err := db.New(r.st.Reader).GetJob(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("jobs: read job: %w", err)
	}
	job := Job{ID: row.ID, Kind: row.Kind, Status: Status(row.Status)}
	if row.Result.Valid {
		job.Result = json.RawMessage(row.Result.String)
	}
	if row.Error.Valid {
		job.Error = &row.Error.String
	}
	return job, nil
}

// work claims and runs queued jobs until ctx is cancelled, waiting for a
// wake token whenever none is queued.
func (r *Runner) work(ctx context.Context) {
	defer r.wg.Done()
	q := db.New(r.st.Writer)
	for ctx.Err() == nil {
		job, err := q.ClaimNextJob(ctx, sql.NullString{String: store.Now().String(), Valid: true})
		if err == nil {
			r.run(ctx, job)
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) && ctx.Err() == nil {
			r.logger.ErrorContext(ctx, "claim job", "error", err)
		}
		select {
		case <-r.wake:
		case <-ctx.Done():
		}
	}
}

// run runs a claimed job to completion. The final write is made even when
// ctx has been cancelled, so a handler that finishes during shutdown is
// recorded.
func (r *Runner) run(ctx context.Context, job db.Job) {
	write := context.WithoutCancel(ctx)
	logger := r.logger.With("job_id", job.ID, "kind", job.Kind)
	q := db.New(r.st.Writer)

	h, ok := r.handlers[job.Kind] // read-only once started
	if !ok {
		logger.ErrorContext(ctx, "no handler for job kind")
		r.fail(write, logger, job.ID, "no handler for job kind "+job.Kind)
		return
	}

	publish := func(partial json.RawMessage) error {
		n, err := q.PublishJobResult(ctx, db.PublishJobResultParams{
			Result:    sql.NullString{String: string(partial), Valid: true},
			UpdatedAt: store.Now().String(),
			ID:        job.ID,
		})
		if err != nil {
			return fmt.Errorf("jobs: publish result: %w", err)
		}
		if n != 1 {
			return fmt.Errorf("jobs: publish result: job %s is not running", job.ID)
		}
		return nil
	}

	result, err := h(ctx, job.Payload, publish)
	if err != nil {
		if ctx.Err() != nil {
			logger.WarnContext(write, "job interrupted by shutdown; it fails at the next start", "error", err)
			return
		}
		var failure *Failure
		message := MessageInternal
		if errors.As(err, &failure) {
			message = failure.Message
		} else {
			logger.ErrorContext(ctx, "job failed", "error", err)
		}
		r.fail(write, logger, job.ID, message)
		return
	}

	n, err := q.SucceedJob(write, db.SucceedJobParams{
		Result: sql.NullString{String: string(result), Valid: true},
		Now:    sql.NullString{String: store.Now().String(), Valid: true},
		ID:     job.ID,
	})
	if err == nil && n != 1 {
		err = errors.New("job is not running")
	}
	if err != nil {
		logger.ErrorContext(write, "record job success", "error", err)
	}
}

func (r *Runner) fail(ctx context.Context, logger *slog.Logger, id, message string) {
	n, err := db.New(r.st.Writer).FailJob(ctx, db.FailJobParams{
		Error: sql.NullString{String: message, Valid: true},
		Now:   sql.NullString{String: store.Now().String(), Valid: true},
		ID:    id,
	})
	if err == nil && n != 1 {
		err = errors.New("job is not running")
	}
	if err != nil {
		logger.ErrorContext(ctx, "record job failure", "error", err)
	}
}
