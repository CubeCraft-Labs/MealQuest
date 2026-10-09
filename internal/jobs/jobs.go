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
	"time"

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

// Retry backoff. A worker whose claim or final job write fails with a
// database error retries after retryMin, doubling up to retryMax, so a job
// queued when the error clears is claimed without waiting for a wake token,
// and a job the worker has finished is never left running.
const (
	retryMin = 50 * time.Millisecond
	retryMax = 5 * time.Second
)

// FinalWriteTimeout bounds each attempt at a job's final write, and at a
// handler's own last write, so a writer connection that stays busy cannot
// hold a stopping worker, and with it shutdown, open.
const FinalWriteTimeout = 5 * time.Second

// FinalWriteContext returns the context for one attempt at a final write
// of a job: it is not cancelled with ctx, so a job that finishes during
// shutdown is still recorded, but it ends after FinalWriteTimeout. A
// handler makes its own last write, the one recording what the job did,
// with it too, and returns the error when the attempt times out, so its
// worker can stop; the next Start fails the job as interrupted.
func FinalWriteContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), FinalWriteTimeout)
}

// codePattern is the form of every failure code: lower-case snake case.
var codePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ErrNotFound is returned by Get for an unknown job id.
var ErrNotFound = errors.New("jobs: job not found")

// ErrUnknownKind is returned by Runner.Enqueue for a kind with no
// registered handler.
var ErrUnknownKind = errors.New("jobs: unknown job kind")

// ErrServiceOnly is returned by Runner.Enqueue for a kind made ServiceOnly,
// whose jobs only its own service enqueues.
var ErrServiceOnly = errors.New("jobs: job kind is enqueued only by its service")

// ErrInvalidPayload is returned by Runner.Enqueue for a payload that does
// not decode into the kind's payload type.
var ErrInvalidPayload = errors.New("jobs: payload does not decode")

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

// handler is a registered kind: run runs a job, check reports whether a
// payload decodes into the kind's payload type, serviceOnly is set when
// Runner.Enqueue refuses the kind, and failedResult, when set, is what Get
// shows of a failed job's stored result (see Kind.FailedResult).
type handler struct {
	run          runFunc
	check        func(payload []byte) bool
	serviceOnly  bool
	failedResult func(stored json.RawMessage) (json.RawMessage, error)
}

// Runner owns the job handlers and the worker pool.
type Runner struct {
	st      *store.Store
	logger  *slog.Logger
	workers int
	// wake holds up to one token per worker; Enqueue adds one so an idle
	// worker looks for the new job.
	wake chan struct{}
	// claim claims the oldest queued job, marking it running at now.
	claim func(ctx context.Context, now sql.NullString) (db.Job, error)

	mu       sync.Mutex
	handlers map[string]handler
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
		claim:    db.New(st.Writer).ClaimNextJob,
		handlers: make(map[string]handler),
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
	run := func(ctx context.Context, payload string, publish func(json.RawMessage) error) (json.RawMessage, string, string) {
		p, err := decodePayload[P]([]byte(payload))
		if err != nil {
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
	check := func(payload []byte) bool {
		_, err := decodePayload[P](payload)
		return err == nil
	}
	r.handlers[kind] = handler{run: run, check: check}
	return &Kind[P]{r: r, name: kind}, nil
}

// decodePayload decodes payload into P, refusing unknown fields.
func decodePayload[P any](payload []byte) (P, error) {
	var p P
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	err := dec.Decode(&p)
	return p, err
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
	return k.r.enqueue(ctx, k.name, data)
}

// EnqueueTx stores a queued job with payload in tx and returns its id, so
// the job exists exactly when what else tx writes does. Once tx has
// committed, call Wake so an idle worker runs it.
func (k *Kind[P]) EnqueueTx(ctx context.Context, tx *sql.Tx, payload P) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("jobs: enqueue %s: encode payload: %w", k.name, err)
	}
	return createJob(ctx, db.New(tx), k.name, data)
}

// ServiceOnly makes Runner.Enqueue refuse the kind with ErrServiceOnly, so
// its jobs are enqueued only through k, by the service that checks what
// must hold before one starts. It must be called before Start.
func (k *Kind[P]) ServiceOnly() error {
	k.r.mu.Lock()
	defer k.r.mu.Unlock()
	if k.r.started {
		return fmt.Errorf("jobs: service-only %q: runner already started", k.name)
	}
	h := k.r.handlers[k.name]
	h.serviceOnly = true
	k.r.handlers[k.name] = h
	return nil
}

// FailedResult makes Get return, for a failed job of the kind with a
// stored result, show's view of that result instead of the result itself.
// It is applied on every read, so it holds however the job failed: by its
// handler, as interrupted by restart, or after a write meant to tidy the
// result failed. A show error fails Get. A kind that never calls it shows a
// failed job's last result as stored. It must be called before Start.
func (k *Kind[P]) FailedResult(show func(stored json.RawMessage) (json.RawMessage, error)) error {
	if show == nil {
		return fmt.Errorf("jobs: failed result %q: nil func", k.name)
	}
	k.r.mu.Lock()
	defer k.r.mu.Unlock()
	if k.r.started {
		return fmt.Errorf("jobs: failed result %q: runner already started", k.name)
	}
	h := k.r.handlers[k.name]
	h.failedResult = show
	k.r.handlers[k.name] = h
	return nil
}

// Wake tells an idle worker to look for a job, as Enqueue does itself:
// call it after committing the transaction a job was enqueued in.
func (k *Kind[P]) Wake() { k.r.wakeOne() }

// Enqueue stores a queued job of kind with its JSON payload and returns its
// id, as Kind.Enqueue does for a kind known at compile time. It returns
// ErrUnknownKind for a kind with no registered handler, ErrServiceOnly for
// a ServiceOnly kind and ErrInvalidPayload for a payload that does not
// decode into the kind's payload type; none stores a job.
func (r *Runner) Enqueue(ctx context.Context, kind string, payload json.RawMessage) (string, error) {
	r.mu.Lock()
	h, ok := r.handlers[kind]
	r.mu.Unlock()
	if !ok {
		return "", ErrUnknownKind
	}
	if h.serviceOnly {
		return "", ErrServiceOnly
	}
	if !h.check(payload) {
		return "", ErrInvalidPayload
	}
	return r.enqueue(ctx, kind, payload)
}

// enqueue stores a queued job of kind with its encoded payload and wakes a
// worker.
func (r *Runner) enqueue(ctx context.Context, kind string, payload []byte) (string, error) {
	id, err := createJob(ctx, db.New(r.st.Writer), kind, payload)
	if err != nil {
		return "", err
	}
	r.wakeOne()
	return id, nil
}

// createJob stores a queued job of kind with its encoded payload through q
// and returns its id.
func createJob(ctx context.Context, q *db.Queries, kind string, payload []byte) (string, error) {
	id := store.NewID()
	now := store.Now().String()
	if err := q.CreateJob(ctx, db.CreateJobParams{
		ID:        id,
		Kind:      kind,
		Payload:   string(payload),
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		return "", fmt.Errorf("jobs: enqueue %s: %w", kind, err)
	}
	return id, nil
}

// wakeOne gives an idle worker a token to look for a queued job.
func (r *Runner) wakeOne() {
	select {
	case r.wake <- struct{}{}:
	default: // every worker already has a token and will look again
	}
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

// Get returns the job with id, or ErrNotFound. A failed job's result is
// shown through its kind's FailedResult, when the kind set one.
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
	if job.Status == StatusFailed && job.Result != nil {
		r.mu.Lock()
		show := r.handlers[job.Kind].failedResult
		r.mu.Unlock()
		if show != nil {
			shown, err := show(job.Result)
			if err != nil {
				return Job{}, fmt.Errorf("jobs: show failed %s result: %w", job.Kind, err)
			}
			if !json.Valid(shown) {
				return Job{}, fmt.Errorf("jobs: show failed %s result: not one JSON value", job.Kind)
			}
			job.Result = shown
		}
	}
	return job, nil
}

// work claims and runs queued jobs until ctx is cancelled, waiting for a
// wake token whenever none is queued. A claim that fails with a database
// error is retried with backoff rather than waiting for a token, so a job
// already queued is not stranded by a transient error.
func (r *Runner) work(ctx context.Context) {
	defer r.wg.Done()
	backoff := retryMin
	for ctx.Err() == nil {
		job, err := r.claim(ctx, sql.NullString{String: store.Now().String(), Valid: true})
		switch {
		case err == nil:
			backoff = retryMin
			r.run(ctx, job)
			continue
		case errors.Is(err, sql.ErrNoRows):
			backoff = retryMin
			select {
			case <-r.wake:
			case <-ctx.Done():
			}
			continue
		case ctx.Err() != nil:
			return
		}
		r.logger.ErrorContext(ctx, "claim job failed; retrying", "reason", "database error", "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
		backoff = min(2*backoff, retryMax)
	}
}

// run runs a claimed job to completion. The final write is made even when
// ctx has been cancelled, so a handler that finishes during shutdown is
// recorded.
func (r *Runner) run(ctx context.Context, job db.Job) {
	logger := r.logger.With("job_id", job.ID, "kind", job.Kind)
	q := db.New(r.st.Writer)

	h, ok := r.handlers[job.Kind] // read-only once started
	if !ok {
		logger.ErrorContext(ctx, "job failed", "code", CodeUnknownKind, "reason", "no handler registered for the kind")
		r.fail(ctx, logger, job.ID, CodeUnknownKind)
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

	result, code, reason := h.run(ctx, job.Payload, publish)
	if code != "" {
		if ctx.Err() != nil {
			logger.WarnContext(context.WithoutCancel(ctx), "job interrupted by shutdown; it fails at the next start")
			return
		}
		level := slog.LevelInfo
		if code == CodeInternal {
			level = slog.LevelError
		}
		logger.Log(ctx, level, "job failed", "code", code, "reason", reason)
		r.fail(ctx, logger, job.ID, code)
		return
	}

	r.record(ctx, logger, "record job success", func(write context.Context) (int64, error) {
		return q.SucceedJob(write, db.SucceedJobParams{
			Result: sql.NullString{String: string(result), Valid: true},
			Now:    sql.NullString{String: store.Now().String(), Valid: true},
			ID:     job.ID,
		})
	})
}

// fail records the job as failed with code. The record errors it logs come
// from the database, never from a handler.
func (r *Runner) fail(ctx context.Context, logger *slog.Logger, id, code string) {
	r.record(ctx, logger, "record job failure", func(write context.Context) (int64, error) {
		return db.New(r.st.Writer).FailJob(write, db.FailJobParams{
			Error: sql.NullString{String: code, Valid: true},
			Now:   sql.NullString{String: store.Now().String(), Valid: true},
			ID:    id,
		})
	})
}

// record makes a job's final write, which must change the one running job.
// A database error is retried with backoff until the write lands, so a
// worker never moves on from a job it has finished while leaving it
// running. Each attempt is made with FinalWriteContext, so even when ctx
// has been cancelled, and is bounded by FinalWriteTimeout; once ctx is
// cancelled, a failed or timed-out attempt is not retried and the job
// stays running, so the next Start fails it as interrupted.
func (r *Runner) record(ctx context.Context, logger *slog.Logger, what string, write func(context.Context) (int64, error)) {
	backoff := retryMin
	for {
		attempt, cancel := FinalWriteContext(ctx)
		n, err := write(attempt)
		cancel()
		if err == nil {
			if n != 1 {
				logger.ErrorContext(ctx, what, "error", "job is not running")
			}
			return
		}
		if ctx.Err() != nil {
			logger.ErrorContext(context.WithoutCancel(ctx), what+" failed during shutdown; it fails at the next start", "error", err)
			return
		}
		logger.ErrorContext(ctx, what+" failed; retrying", "error", err, "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
		}
		backoff = min(2*backoff, retryMax)
	}
}
