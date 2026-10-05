package harness

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// RunStatus is the outcome of a run, stored in ai_runs.status.
type RunStatus string

// The run outcomes.
const (
	// StatusOK means the first reply was usable.
	StatusOK RunStatus = "ok"
	// StatusRepaired means the first reply was not usable and the reply to
	// the repair turn was.
	StatusRepaired RunStatus = "repaired"
	// StatusBadOutput means neither reply was usable, post-processing a
	// valid reply failed, or the skill's prompt could not be rendered.
	StatusBadOutput RunStatus = "bad_output"
	// StatusProviderError means the provider could not be built or failed
	// on either turn.
	StatusProviderError RunStatus = "provider_error"
)

// RunRetention is how long ai_runs rows are kept. The daily purge calls
// Purge with time.Now().Add(-RunRetention).
const RunRetention = 30 * 24 * time.Hour

// Purge deletes the ai_runs rows created before olderThan and returns how
// many it deleted. The suggestion_batches and week_plans references to
// those rows are cleared first, in the same transaction.
func Purge(ctx context.Context, st *store.Store, olderThan time.Time) (int64, error) {
	if olderThan.IsZero() {
		return 0, errors.New("harness: purge ai_runs: zero cutoff")
	}
	cutoff := store.Timestamp{Time: olderThan.Truncate(time.Millisecond)}.String()
	var deleted int64
	err := st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		if _, err := q.ClearSuggestionBatchAIRunsBefore(ctx, cutoff); err != nil {
			return err
		}
		if _, err := q.ClearWeekPlanAIRunsBefore(ctx, cutoff); err != nil {
			return err
		}
		n, err := q.DeleteAIRunsBefore(ctx, cutoff)
		deleted = n
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("harness: purge ai_runs: %w", err)
	}
	return deleted, nil
}

// runLog collects one run's turns for its ai_runs row.
type runLog struct {
	start     store.Timestamp
	requests  []loggedRequest
	responses []loggedResponse
	tokensIn  int64
	tokensOut int64
	// problems lists what was wrong with the first reply of a repaired
	// run; it is the row's error text.
	problems []string
}

// loggedRequest is one turn's llm.Request as stored in
// ai_runs.request_redacted.
type loggedRequest struct {
	System      string          `json:"system,omitempty"`
	Messages    []loggedMessage `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	SchemaName  string          `json:"schema_name,omitempty"`
	Schema      string          `json:"schema,omitempty"`
}

type loggedMessage struct {
	Role    llm.Role `json:"role"`
	Content string   `json:"content"`
}

// loggedResponse is one turn's llm.Response, or its error, as stored in
// ai_runs.response_redacted.
type loggedResponse struct {
	Text         string `json:"text,omitempty"`
	JSON         string `json:"json,omitempty"`
	Model        string `json:"model,omitempty"`
	FinishReason string `json:"finish_reason,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	Error        string `json:"error,omitempty"`
}

// add records one turn: the request sent and the reply or error. Every
// string is redacted as it is recorded.
func (l *runLog) add(apiKey string, req llm.Request, resp llm.Response, err error) {
	r := loggedRequest{
		System:      redact(req.System, apiKey),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	for _, m := range req.Messages {
		r.Messages = append(r.Messages, loggedMessage{Role: m.Role, Content: redact(m.Content, apiKey)})
	}
	if req.Schema != nil {
		r.SchemaName = redact(req.Schema.Name, apiKey)
		r.Schema = redact(string(req.Schema.Schema), apiKey)
	}
	l.requests = append(l.requests, r)

	lr := loggedResponse{
		Text:         redact(resp.Text, apiKey),
		JSON:         redact(string(resp.JSON), apiKey),
		Model:        redact(resp.Model, apiKey),
		FinishReason: redact(resp.FinishReason, apiKey),
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	}
	if err != nil {
		lr.Error = redact(err.Error(), apiKey)
	}
	l.responses = append(l.responses, lr)
	l.tokensIn += int64(resp.Usage.InputTokens)
	l.tokensOut += int64(resp.Usage.OutputTokens)
}

// save writes the run's ai_runs row. It is written even when ctx is done,
// so a cancelled run is recorded too.
func (l *runLog) save(ctx context.Context, cfg Config, skill string, version int, status RunStatus, runErr error) error {
	// A run that failed before its first turn stores an empty list.
	request, err := marshalRedacted(nonNil(l.requests), cfg.APIKey)
	if err != nil {
		return err
	}
	response, err := marshalRedacted(nonNil(l.responses), cfg.APIKey)
	if err != nil {
		return err
	}
	var errText sql.NullString
	switch {
	case runErr != nil:
		errText = sql.NullString{String: redact(runErr.Error(), cfg.APIKey), Valid: true}
	case len(l.problems) > 0:
		errText = sql.NullString{String: redact("first reply: "+strings.Join(l.problems, "; "), cfg.APIKey), Valid: true}
	}
	now := store.Now()
	return db.New(cfg.Store.Writer).CreateAIRun(context.WithoutCancel(ctx), db.CreateAIRunParams{
		ID:               store.NewID(),
		Skill:            skill,
		SkillVersion:     strconv.Itoa(version),
		Provider:         cfg.Settings.Provider,
		Model:            cfg.Settings.Model,
		Temperature:      cfg.Settings.Temperature,
		Status:           string(status),
		LatencyMs:        sql.NullInt64{Int64: now.Sub(l.start.Time).Milliseconds(), Valid: true},
		TokensIn:         reported(l.tokensIn),
		TokensOut:        reported(l.tokensOut),
		Error:            errText,
		RequestRedacted:  request,
		ResponseRedacted: sql.NullString{String: response, Valid: true},
		CreatedAt:        l.start.String(),
		UpdatedAt:        now.String(),
	})
}

// nonNil is s, or an empty slice when s is nil, so it encodes as [].
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// reported is n as a column value, NULL when no turn reported a count.
func reported(n int64) sql.NullInt64 {
	return sql.NullInt64{Int64: n, Valid: n > 0}
}

// marshalRedacted encodes v as JSON without HTML escaping, so the stored
// text matches what was redacted, and redacts the result once more in case
// the encoding joined text into the key.
func marshalRedacted(v any, apiKey string) (string, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("encode ai_runs body: %w", err)
	}
	return llm.Redact(string(bytes.TrimSuffix(b.Bytes(), []byte("\n"))), apiKey), nil
}
