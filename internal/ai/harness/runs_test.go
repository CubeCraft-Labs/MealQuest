package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm/presets"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// runRow is one ai_runs row as stored.
type runRow struct {
	ID, Skill, SkillVersion, Provider, Model, Status string
	Temperature                                      float64
	LatencyMs, TokensIn, TokensOut                   sql.NullInt64
	Error                                            sql.NullString
	Request                                          string
	Response                                         sql.NullString
	CreatedAt, UpdatedAt                             string
}

// text is every column of r, for scanning for secrets.
func (r runRow) text() string {
	return strings.Join([]string{r.ID, r.Skill, r.SkillVersion, r.Provider, r.Model, r.Status,
		r.Error.String, r.Request, r.Response.String, r.CreatedAt, r.UpdatedAt}, "\n")
}

// runRows returns every ai_runs row, oldest first.
func runRows(t *testing.T, st *store.Store) []runRow {
	t.Helper()
	rows, err := st.Reader.Query(`SELECT id, skill, skill_version, provider, model, temperature, status, latency_ms,
		tokens_in, tokens_out, error, request_redacted, response_redacted, created_at, updated_at FROM ai_runs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []runRow
	for rows.Next() {
		var r runRow
		if err := rows.Scan(&r.ID, &r.Skill, &r.SkillVersion, &r.Provider, &r.Model, &r.Temperature, &r.Status, &r.LatencyMs,
			&r.TokensIn, &r.TokensOut, &r.Error, &r.Request, &r.Response, &r.CreatedAt, &r.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// failingPostProcess is echoSkill whose PostProcess fails.
type failingPostProcess struct{ echoSkill }

func (failingPostProcess) PostProcess(echoOutput) (echoOutput, error) {
	return echoOutput{}, errors.New("cannot finish")
}

func used(js string, in, out int) llm.GenerateResult {
	r := reply(js)
	r.Response.Usage = llm.Usage{InputTokens: in, OutputTokens: out}
	return r
}

func TestRunWritesOneRowWithStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		skill     Skill[echoInput, echoOutput]
		script    []llm.GenerateResult
		status    RunStatus
		turns     int
		tokensIn  sql.NullInt64
		tokensOut sql.NullInt64
		errText   string
	}{
		{
			name: "ok", skill: echoSkill{},
			script: []llm.GenerateResult{used(`{"echo":"taco"}`, 100, 7)},
			status: StatusOK, turns: 1,
			tokensIn: sql.NullInt64{Int64: 100, Valid: true}, tokensOut: sql.NullInt64{Int64: 7, Valid: true},
		},
		{
			name: "repaired", skill: echoSkill{},
			script: []llm.GenerateResult{used(`{"echo":"TACO"}`, 100, 7), used(`{"echo":"taco"}`, 130, 6)},
			status: StatusRepaired, turns: 2,
			tokensIn: sql.NullInt64{Int64: 230, Valid: true}, tokensOut: sql.NullInt64{Int64: 13, Valid: true},
			errText: "first reply: echo must be lower case",
		},
		{
			name: "bad output", skill: echoSkill{},
			script: []llm.GenerateResult{reply(`{"echo":"TACO"}`), reply(`{"echo":""}`)},
			status: StatusBadOutput, turns: 2,
			errText: "echo must not be empty",
		},
		{
			name: "post-process failure", skill: failingPostProcess{},
			script: []llm.GenerateResult{used(`{"echo":"taco"}`, 100, 7)},
			status: StatusBadOutput, turns: 1,
			tokensIn: sql.NullInt64{Int64: 100, Valid: true}, tokensOut: sql.NullInt64{Int64: 7, Valid: true},
			errText: "post-process: cannot finish",
		},
		{
			name: "provider error", skill: echoSkill{},
			script: []llm.GenerateResult{{Err: llm.ErrAuth}},
			status: StatusProviderError, turns: 1,
			errText: llm.ErrAuth.Error(),
		},
		{
			name: "provider error on repair", skill: echoSkill{},
			script: []llm.GenerateResult{used(`{"echo":"TACO"}`, 100, 7), {Err: llm.ErrTimeout}},
			status: StatusProviderError, turns: 2,
			tokensIn: sql.NullInt64{Int64: 100, Valid: true}, tokensOut: sql.NullInt64{Int64: 7, Valid: true},
			errText: llm.ErrTimeout.Error(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f llm.Fake
			f.QueueGenerate(tc.script...)
			cfg := testConfig(t, &f)
			before := store.Now()
			_, runErr := Run(context.Background(), cfg, tc.skill, echoInput{Word: "taco"})
			if (runErr == nil) != (tc.status == StatusOK || tc.status == StatusRepaired) {
				t.Fatalf("Run err = %v for status %s", runErr, tc.status)
			}
			assertCalls(t, &f, tc.turns)

			rows := runRows(t, cfg.Store)
			if len(rows) != 1 {
				t.Fatalf("%d ai_runs rows, want 1", len(rows))
			}
			r := rows[0]
			if r.Status != string(tc.status) || r.Skill != "echo" || r.SkillVersion != "1" ||
				r.Provider != "anthropic" || r.Model != "claude-test" || r.Temperature != 0.7 {
				t.Fatalf("row = %+v", r)
			}
			if !r.LatencyMs.Valid || r.LatencyMs.Int64 < 0 {
				t.Fatalf("latency_ms = %+v", r.LatencyMs)
			}
			if r.TokensIn != tc.tokensIn || r.TokensOut != tc.tokensOut {
				t.Fatalf("tokens = %+v/%+v, want %+v/%+v", r.TokensIn, r.TokensOut, tc.tokensIn, tc.tokensOut)
			}
			if tc.errText == "" && r.Error.Valid {
				t.Fatalf("error = %q, want NULL", r.Error.String)
			}
			if tc.errText != "" && !strings.Contains(r.Error.String, tc.errText) {
				t.Fatalf("error = %q, want it to hold %q", r.Error.String, tc.errText)
			}
			var created store.Timestamp
			if err := created.Scan(r.CreatedAt); err != nil || created.Before(before.Time) {
				t.Fatalf("created_at = %q (%v), want at or after %s", r.CreatedAt, err, before)
			}

			var reqs []loggedRequest
			if err := json.Unmarshal([]byte(r.Request), &reqs); err != nil || len(reqs) != tc.turns {
				t.Fatalf("request_redacted = %s (%v), want %d turns", r.Request, err, tc.turns)
			}
			if reqs[0].SchemaName != "echo" || reqs[0].Messages[0].Content != "Repeat: taco" || *reqs[0].Temperature != 0.7 {
				t.Fatalf("first request = %+v", reqs[0])
			}
			if tc.turns == 2 && (len(reqs[1].Messages) != 3 || !strings.Contains(reqs[1].Messages[2].Content, "not usable")) {
				t.Fatalf("repair request = %+v", reqs[1])
			}
			var resps []loggedResponse
			if err := json.Unmarshal([]byte(r.Response.String), &resps); err != nil || len(resps) != tc.turns {
				t.Fatalf("response_redacted = %s (%v), want %d turns", r.Response.String, err, tc.turns)
			}
			if tc.status == StatusProviderError && resps[tc.turns-1].Error != r.Error.String {
				t.Fatalf("last response error = %q, want %q", resps[tc.turns-1].Error, r.Error.String)
			}
		})
	}
}

func TestRunRowPerRun(t *testing.T) {
	var f llm.Fake
	cfg := testConfig(t, &f)
	f.QueueGenerate(reply(`{"echo":"taco"}`), reply(`{"echo":"TACO"}`), reply(`{"echo":"taco"}`), llm.GenerateResult{Err: llm.ErrAuth})
	for range 3 {
		_, _ = Run(context.Background(), cfg, echoSkill{}, echoInput{Word: "taco"})
	}
	assertCalls(t, &f, 4)
	var got []string
	for _, r := range runRows(t, cfg.Store) {
		got = append(got, r.Status)
	}
	if strings.Join(got, ",") != "ok,repaired,provider_error" {
		t.Fatalf("statuses = %v, want ok,repaired,provider_error", got)
	}
}

func TestRunRecordsCancelledRun(t *testing.T) {
	var f llm.Fake
	cfg := testConfig(t, &f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.QueueGenerate(llm.GenerateResult{Err: context.Canceled})
	if _, err := Run(ctx, cfg, echoSkill{}, echoInput{Word: "taco"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if rows := runRows(t, cfg.Store); len(rows) != 1 || rows[0].Status != string(StatusProviderError) {
		t.Fatalf("rows = %+v, want one provider_error row", rows)
	}
}

func TestRunFailsWhenRowCannotBeWritten(t *testing.T) {
	var f llm.Fake
	cfg := testConfig(t, &f)
	cfg.Settings.Provider = "gemini" // refused by the provider CHECK
	f.QueueGenerate(reply(`{"echo":"taco"}`))
	_, err := Run(context.Background(), cfg, echoSkill{}, echoInput{Word: "taco"})
	if err == nil || !strings.Contains(err.Error(), "record ai_runs row") {
		t.Fatalf("err = %v, want the row write failure", err)
	}
}

// The seeded secrets. The key and the webhook are what the household
// stores; the rest are credentials a reply or error may quote.
const (
	seededKey     = "sk-seeded-0123456789abcdef"
	seededWebhook = "https://chat.googleapis.com/v1/spaces/AAAAkitchen/messages?key=WEBHOOKKEY42&token=WEBHOOKTOKEN42"
	seededBearer  = "BEARERTOKEN42"
	seededHeader  = "HEADERKEY42"
	seededURLKey  = "URLKEY42"
)

var seededSecrets = []string{seededKey, seededWebhook, "WEBHOOKKEY42", "WEBHOOKTOKEN42", "AAAAkitchen", seededBearer, seededHeader, seededURLKey}

func TestRunRowsHoldNoSecrets(t *testing.T) {
	// leaky quotes every seeded credential the way a model or vendor
	// might, including the key inside a longer token.
	leaky := "key " + seededKey + " webhook " + seededWebhook +
		"\nAuthorization: Bearer " + seededBearer +
		"\nx-api-key: " + seededHeader +
		"\nsee https://maps.example.com/api?api_key=" + seededURLKey + "&q=tacos" +
		" and xx" + seededKey + "yy"
	leakyJSON, err := json.Marshal(map[string]string{"echo": leaky})
	if err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string][]llm.GenerateResult{
		"repaired": {
			{Response: llm.Response{Text: leaky, JSON: leakyJSON, Model: seededKey}},
			reply(`{"echo":"taco"}`),
		},
		"bad output": {
			{Response: llm.Response{Text: leaky}, Err: fmt.Errorf("%w: %s", llm.ErrBadOutput, leaky)},
			{Response: llm.Response{Text: leaky, JSON: leakyJSON}},
		},
		"provider error": {
			{Err: llm.NewProviderError(500, []byte(leaky), "")},
		},
		"provider error on repair": {
			reply(`{"echo":"TACO"}`),
			{Err: errors.New(leaky)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var f llm.Fake
			f.QueueGenerate(script...)
			cfg := testConfig(t, &f)
			cfg.APIKey = seededKey
			cfg.NewProvider = func(_ llm.Settings, key string) (llm.Provider, error) {
				if key != seededKey {
					t.Errorf("provider built with key %q, want the seeded key", key)
				}
				return &f, nil
			}
			cfg.Persona = "Our kitchen. " + leaky
			_, _ = Run(context.Background(), cfg, echoSkill{}, echoInput{Word: "tacos " + seededWebhook})
			assertCalls(t, &f, len(script))

			rows := runRows(t, cfg.Store)
			if len(rows) != 1 {
				t.Fatalf("%d ai_runs rows, want 1", len(rows))
			}
			text := rows[0].text()
			for _, s := range seededSecrets {
				if strings.Contains(text, s) {
					t.Errorf("stored row holds %q:\n%s", s, text)
				}
			}
			for _, kept := range []string{"Our kitchen.", "Repeat: tacos", redactionMarker} {
				if !strings.Contains(text, kept) {
					t.Errorf("stored row lacks %q:\n%s", kept, text)
				}
			}
		})
	}
}

// TestRunRowsHoldNoProviderKey builds the provider the production way,
// presets.FromSettings, against a server that quotes the bearer key it
// receives with no Authorization label: in the reply text, the reply JSON
// and the model name. The repair turn then sends the quoted key back.
func TestRunRowsHoldNoProviderKey(t *testing.T) {
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || key == "" {
			t.Errorf("Authorization = %q, want a bearer key", r.Header.Get("Authorization"))
		}
		turn++
		echo := key // lower case, so usable
		if turn == 1 {
			echo = "Your key is " + key // upper case, so repaired
		}
		content, err := json.Marshal(map[string]string{"echo": echo})
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"model":   "model-" + key,
			"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(content)}}},
			"usage":   map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		}); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()

	cfg := Config{
		Settings:    llm.Settings{Provider: "openai_compatible", BaseURL: srv.URL, Model: "local-test", Temperature: 0.7},
		APIKey:      seededKey,
		NewProvider: presets.FromSettings,
		Store:       emptyHousehold(t),
	}
	out, err := Run(context.Background(), cfg, echoSkill{}, echoInput{Word: "taco"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Echo != seededKey || turn != 2 {
		t.Fatalf("Echo = %q after %d turns, want the provider's key after 2", out.Echo, turn)
	}
	rows := runRows(t, cfg.Store)
	if len(rows) != 1 || rows[0].Status != string(StatusRepaired) {
		t.Fatalf("rows = %+v, want one repaired row", rows)
	}
	text := rows[0].text()
	if strings.Contains(text, seededKey) {
		t.Fatalf("stored row holds the provider's key:\n%s", text)
	}
	if !strings.Contains(text, "Your key is "+redactionMarker) || !strings.Contains(text, "model-"+redactionMarker) {
		t.Fatalf("stored row lacks the redacted quotes:\n%s", text)
	}
}

func TestRedact(t *testing.T) {
	const key = "sk-abc123"
	for _, tc := range []struct{ in, want string }{
		{"key sk-abc123 here", "key [REDACTED] here"},
		{"Authorization: Bearer xyz.789\nnext", "Authorization: [REDACTED]\nnext"},
		{"proxy-authorization: Basic Zm9v", "proxy-authorization: [REDACTED]"},
		{`{"Authorization":"Bearer xyz","a":1}`, `{"Authorization":"[REDACTED]","a":1}`},
		{`{"x-api-key": "abc", "b": 2}`, `{"b":2,"x-api-key":"[REDACTED]"}`},
		{`{"X-Api-Key":{"v":"abc"},"n":1.50}`, `{"X-Api-Key":"[REDACTED]","n":1.50}`},
		{`{"m":"see https://chat.googleapis.com/v1/spaces/A?key=k\u0026token=t\nAuthorization: Bearer xyz"}`, `{"m":"see [REDACTED]\nAuthorization: [REDACTED]"}`},
		{`["sk-abc\u0031\u0032\u0033", "{\"Authorization\":\"Bearer xyz\"}"]`, `["[REDACTED]","{\"Authorization\":\"[REDACTED]\"}"]`},
		{"{\"a\":1} trailing \\u0026 Authorization: x", "{\"a\":1} trailing \\u0026 Authorization: [REDACTED]"},
		{"nAuthorization: Bearer xyz", "nAuthorization: [REDACTED]"},
		{"X-Goog-Api-Key=abc&x=1", "X-Goog-Api-Key=[REDACTED]"},
		{"authorization failed, try again", "authorization failed, try again"},
		{"post to https://chat.googleapis.com/v1/spaces/A/messages?key=k&token=t now", "post to [REDACTED] now"},
		{"chat.googleapis.com/v1/spaces/A/messages?key=k", "[REDACTED]"},
		{"<https://chat.googleapis.com/v1/spaces/A/messages>", "<[REDACTED]>"},
		{"https://maps.example.com/api?api_key=abc&q=1", "[REDACTED]"},
		{"https://example.com/x?q=1&access_token=abc", "[REDACTED]"},
		{"https://example.com/x?%6Bey=abc", "[REDACTED]"},
		{"https://example.com/x?%zz=abc", "[REDACTED]"},
		{"https://example.com/cb#access_token=abc", "[REDACTED]"},
		{"https://example.com/x?sig=abc;q=1", "[REDACTED]"},
		{"https://user:pw@example.com/x", "[REDACTED]"},
		{"https://www.allrecipes.com/recipe/123/chili?servings=4#step-2", "https://www.allrecipes.com/recipe/123/chili?servings=4#step-2"},
		{"mailto-ish user@example.com is fine", "mailto-ish user@example.com is fine"},
		{"https://example.com/a@b", "https://example.com/a@b"},
		{"sk-abcsk-abc123123", ""},
	} {
		got := redact(tc.in, key)
		if strings.Contains(got, key) {
			t.Errorf("redact(%q) = %q holds the key", tc.in, got)
		}
		if tc.want != "" && got != tc.want {
			t.Errorf("redact(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := redact("plain text", ""); got != "plain text" {
		t.Errorf("redact with no key = %q", got)
	}
}

func TestPurgeRemovesOnlyOldRows(t *testing.T) {
	ctx := context.Background()
	st := emptyHousehold(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	cutoff := now.Add(-RunRetention)
	q := db.New(st.Writer)
	insert := func(id string, created time.Time) {
		t.Helper()
		ts := store.Timestamp{Time: created}.String()
		if err := q.CreateAIRun(ctx, db.CreateAIRunParams{
			ID: id, Skill: "echo", SkillVersion: "1", Provider: "anthropic", Model: "m", Temperature: 0.7,
			Status: string(StatusOK), RequestRedacted: "[]", CreatedAt: ts, UpdatedAt: ts,
		}); err != nil {
			t.Fatal(err)
		}
	}
	insert("old-year", now.Add(-365*24*time.Hour))
	insert("old-31d", now.Add(-31*24*time.Hour))
	insert("old-just", cutoff.Add(-time.Millisecond))
	insert("at-cutoff", cutoff)
	insert("new-29d", now.Add(-29*24*time.Hour))
	insert("new-now", now)
	for _, stmt := range []string{
		`INSERT INTO suggestion_batches (id, trigger, status, ai_run_id, created_at, updated_at) VALUES ('batch-old', 'manual', 'done', 'old-31d', 'c', 'u')`,
		`INSERT INTO suggestion_batches (id, trigger, status, ai_run_id, created_at, updated_at) VALUES ('batch-new', 'manual', 'done', 'new-29d', 'c', 'u')`,
		`INSERT INTO week_plans (id, week_start, status, ai_run_id, created_at, updated_at) VALUES ('plan-old', '2026-08-30', 'draft', 'old-year', 'c', 'u')`,
		`INSERT INTO week_plans (id, week_start, status, ai_run_id, created_at, updated_at) VALUES ('plan-new', '2026-10-04', 'draft', 'new-now', 'c', 'u')`,
	} {
		if _, err := st.Writer.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	n, err := Purge(ctx, st, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("Purge deleted %d rows, want 3", n)
	}
	var ids []string
	for _, r := range runRows(t, st) {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, ","); got != "at-cutoff,new-29d,new-now" {
		t.Fatalf("rows left = %s, want at-cutoff,new-29d,new-now", got)
	}
	for table, want := range map[string]string{
		"suggestion_batches": "batch-new=new-29d,batch-old=NULL",
		"week_plans":         "plan-new=new-now,plan-old=NULL",
	} {
		var got []string
		rows, err := st.Reader.Query(`SELECT id || '=' || coalesce(ai_run_id, 'NULL') FROM ` + table + ` ORDER BY id`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			got = append(got, s)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%s references = %v, want %s", table, got, want)
		}
	}

	if n, err := Purge(ctx, st, cutoff); err != nil || n != 0 {
		t.Fatalf("second Purge = %d, %v; want 0, nil", n, err)
	}
	if _, err := Purge(ctx, st, time.Time{}); err == nil {
		t.Fatal("Purge with a zero cutoff succeeded")
	}
}
