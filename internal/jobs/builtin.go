package jobs

import (
	"context"
	"encoding/json"
)

// KindNoop is the framework's built-in job kind: it does no work and
// succeeds with {"ok":true}. It is the runner's own trivial kind, as Feature
// 09's trivial test skill is the harness's, and the startup and readiness
// job: a noop job reaching succeeded proves the runner claims, runs and
// records jobs.
const KindNoop = "noop"

// NoopPayload is the payload of a noop job.
type NoopPayload struct{}

// NoopResult is the result of a noop job.
type NoopResult struct{}

// PublicJSON returns {"ok":true}.
func (NoopResult) PublicJSON() ([]byte, error) {
	return json.Marshal(struct {
		OK bool `json:"ok"`
	}{OK: true})
}

func runNoop(context.Context, NoopPayload, func(NoopResult) error) (NoopResult, error) {
	return NoopResult{}, nil
}

// Builtins enqueues the framework's built-in job kinds.
type Builtins struct {
	Noop *Kind[NoopPayload]
}

// RegisterBuiltins registers the framework's built-in job kinds on r.
func RegisterBuiltins(r *Runner) (*Builtins, error) {
	noop, err := Register(r, KindNoop, runNoop)
	if err != nil {
		return nil, err
	}
	return &Builtins{Noop: noop}, nil
}
