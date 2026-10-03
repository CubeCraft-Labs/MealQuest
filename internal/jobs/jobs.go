// Package jobs runs long work in the background. A job is a row in the jobs
// table: Enqueue inserts it queued, and a fixed pool of workers claims queued
// jobs oldest first, so the table itself is the queue and a job enqueued
// before a restart still runs after it.
//
// Each job kind has one handler, registered before Start. A handler takes a
// typed payload and returns a result implementing Public; it may publish
// partial results while it runs. The runner stores only what a result's
// PublicJSON returns and, for a failed job, only a failure code: either a
// *Failure code the kind declared at registration or one of the runner's own
// codes. Handler error text, which may quote a URL, token or response body
// from a dependency, is never stored, returned by the API or logged.
package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
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

// Failure codes the runner stores itself. A kind cannot declare them.
const (
	// CodeInterrupted is the error of a job a previous process left running.
	// It is fixed framework text, not a snake-case code, so no kind can
	// declare it.
	CodeInterrupted = "interrupted by restart"
	// CodeInternal is the error of a job whose handler failed with anything
	// but a *Failure with a code its kind declared, or whose payload or
	// result could not be encoded. The underlying error is neither stored
	// nor logged.
	CodeInternal = "internal_error"
	// CodeUnknownKind is the error of a job whose kind has no handler.
	CodeUnknownKind = "unknown_kind"
)

// codePattern is the form of every failure code: lower-case snake case.
var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ErrNotFound is returned by Get for an unknown job id.
var ErrNotFound = errors.New("jobs: job not found")

// Failure is a handler error reporting a failure code the household may
// see. Code must be one the job's kind declared at registration; the runner
// stores it as the job's error. Any other handler error, including a Failure
// with an undeclared code, is stored as CodeInternal.
type Failure struct {
	Code string
}

func (f *Failure) Error() string { return "jobs: failure " + f.Code }

// Public is a job result. PublicJSON returns the JSON the runner stores and
// GET /api/v1/jobs/{id} returns, so a result type states exactly which of its
// data is public; nothing else of the value is ever encoded.
type Public interface {
	PublicJSON() ([]byte, error)
}

// Handler runs one job with its payload. It may call publish any number of
// times with a partial result, which Get returns while the job runs; its
// return value is the final result. ctx is cancelled when the runner stops.
type Handler[P any, R Public] func(ctx context.Context, payload P, publish func(partial R) error) (R, error)

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

// runFunc is a registered Handler with its payload and results as JSON. It
// returns the failure code to store, or "" on success; the logged reason
// never quotes an error.
type runFunc func(ctx context.Context, payload string, publish func(json.RawMessage) error) (result json.RawMessage, code, reason string)

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
// its jobs. failureCodes are the *Failure codes h may report. It must be
// called before Start, once per kind.
func Register[P any, R Public](r *Runner, kind string, h Handler[P, R], failureCodes ...string) (*Kind[P], error) {
	if kind == "" {
		return nil, errors.New("jobs: register: empty kind")
	}
	if h == nil {
		return nil, fmt.Errorf("jobs: register %q: nil handler", kind)
	}
	declared := make(map[string]bool, len(failureCodes))
	for _, code := range failureCodes {
		if !codePattern.MatchString(code) {
			return nil, fmt.Errorf("jobs: register %q: failure code %q is not lower-case snake case", kind, code)
		}
		if code == CodeInternal || code == CodeUnknownKind {
			return nil, fmt.Errorf("jobs: register %q: failure code %q is reserved for the runner", kind, code)
		}
		declared[code] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return nil, fmt.Errorf("jobs: register %q: runner already started", kind)
	}
	if _, ok := r.handlers[kind]; ok {
		return nil, fmt.Errorf("jobs: register %q: kind already registered", kind)
	}
	r.handlers[kind] = func(ctx context.Context, payload string, publish func(json.RawMessage) error) (json.RawMessage, string, string) {
		var p P
		dec := json.NewDecoder(bytes.NewReader([]byte(payload)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return nil, CodeInternal, "payload does not decode"
		}
		result, err := h(ctx, p, func(partial R) error {
			data, err := publicJSON(partial)
			if err != nil {
				return err
			}
			return publish(data)
		})
		if err != nil {
			var failure *Failure
			if !errors.As(err, &failure) {
				return nil, CodeInternal, "handler error"
			}
			if !declared[failure.Code] {
				return nil, CodeInternal, "undeclared failure code"
			}
			return nil, failure.Code, "handler failure"
		}
		data, err := publicJSON(result)
		if err != nil {
			return nil, CodeInternal, "result does not encode"
		}
		return data, "", ""
	}
	return &Kind[P]{r: r, name: kind}, nil
}

// publicJSON returns v's PublicJSON, refusing anything that is not one JSON
// value. Its error never quotes PublicJSON's error.
func publicJSON(v Public) (json.RawMessage, error) {
	data, err := v.PublicJSON()
	if err != nil || !json.Valid(data) {
		return nil, errors.New("jobs: result has no valid public JSON")
	}
	return data, nil
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
// CodeInterrupted, then starts the workers. They stop when ctx is cancelled;
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
		Error: sql.NullString{String: CodeInterrupted, Valid: true},
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
		logger.ErrorContext(ctx, "job failed", "code", CodeUnknownKind, "reason", "no handler registered for the kind")
		r.fail(write, logger, job.ID, CodeUnknownKind)
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

	result, code, reason := h(ctx, job.Payload, publish)
	if code != "" {
		if ctx.Err() != nil {
			logger.WarnContext(write, "job interrupted by shutdown; it fails at the next start")
			return
		}
		level := slog.LevelInfo
		if code == CodeInternal {
			level = slog.LevelError
		}
		logger.Log(ctx, level, "job failed", "code", code, "reason", reason)
		r.fail(write, logger, job.ID, code)
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

// fail records the job as failed with code. The record errors it logs come
// from the database, never from a handler.
func (r *Runner) fail(ctx context.Context, logger *slog.Logger, id, code string) {
	n, err := db.New(r.st.Writer).FailJob(ctx, db.FailJobParams{
		Error: sql.NullString{String: code, Valid: true},
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
