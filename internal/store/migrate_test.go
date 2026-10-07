package store

import (
	"context"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

var testMigrations = fstest.MapFS{
	"0001_first.sql":  {Data: []byte("CREATE TABLE first (id INTEGER PRIMARY KEY);")},
	"0002_second.sql": {Data: []byte("CREATE TABLE second (id INTEGER PRIMARY KEY);\nCREATE TABLE third (id INTEGER PRIMARY KEY);")},
	"README.md":       {Data: []byte("not a migration")},
}

type migrationRow struct {
	version   string
	appliedAt string
}

func migrationRows(t *testing.T, s *Store) []migrationRow {
	t.Helper()
	rows, err := s.Reader.Query("SELECT version, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []migrationRow
	for rows.Next() {
		var r migrationRow
		if err := rows.Scan(&r.version, &r.appliedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func versions(rows []migrationRow) string {
	vs := make([]string, len(rows))
	for i, r := range rows {
		vs[i] = r.version
	}
	return strings.Join(vs, ",")
}

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	if err := s.Reader.QueryRow("SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = ?", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestMigrateFreshDatabaseReachesLatest(t *testing.T) {
	s := openTestStore(t)
	if err := Migrate(context.Background(), s.Writer, testMigrations); err != nil {
		t.Fatal(err)
	}
	rows := migrationRows(t, s)
	if got := versions(rows); got != "0001_first,0002_second" {
		t.Fatalf("versions = %q", got)
	}
	for _, r := range rows {
		if _, err := time.Parse(time.RFC3339, r.appliedAt); err != nil || !strings.HasSuffix(r.appliedAt, "Z") {
			t.Errorf("applied_at %q is not UTC RFC 3339", r.appliedAt)
		}
	}
	for _, table := range []string{"first", "second", "third"} {
		if !tableExists(t, s, table) {
			t.Errorf("table %s missing", table)
		}
	}
}

func TestMigrateSecondRunIsNoOp(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := Migrate(ctx, s.Writer, testMigrations); err != nil {
		t.Fatal(err)
	}
	before := migrationRows(t, s)
	if err := Migrate(ctx, s.Writer, testMigrations); err != nil {
		t.Fatalf("second run: %v", err)
	}
	after := migrationRows(t, s)
	if len(after) != len(before) {
		t.Fatalf("rows after second run = %v, want %v", after, before)
	}
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("row %d changed: %v -> %v", i, before[i], after[i])
		}
	}
}

func TestMigrateBrokenMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := Migrate(ctx, s.Writer, testMigrations); err != nil {
		t.Fatal(err)
	}

	broken := fstest.MapFS{"0003_broken.sql": {Data: []byte(
		// The first statement succeeds; the second is invalid, so the
		// table created by the first must be rolled back with it.
		"CREATE TABLE fourth (id INTEGER PRIMARY KEY);\nCREATE TABLE oops (;",
	)}}
	for name, f := range testMigrations {
		broken[name] = f
	}
	err := Migrate(ctx, s.Writer, broken)
	if err == nil {
		t.Fatal("broken migration returned no error")
	}
	if !strings.Contains(err.Error(), "0003_broken.sql") {
		t.Errorf("error %q does not name the failing migration", err)
	}
	if got := versions(migrationRows(t, s)); got != "0001_first,0002_second" {
		t.Errorf("versions after failure = %q, want previous version intact", got)
	}
	if tableExists(t, s, "fourth") {
		t.Error("table from the failed migration was not rolled back")
	}
	for _, table := range []string{"first", "second", "third"} {
		if !tableExists(t, s, table) {
			t.Errorf("previous table %s missing after failed migration", table)
		}
	}
}

func TestMigrateAppliesInFilenameOrder(t *testing.T) {
	s := openTestStore(t)
	// 0002 depends on 0001; MapFS iteration order is random, so this only
	// passes if migrations are sorted by name.
	ordered := fstest.MapFS{
		"0002_child.sql":  {Data: []byte("CREATE TABLE child (parent_id INTEGER REFERENCES parent(id)); INSERT INTO child VALUES (1);")},
		"0001_parent.sql": {Data: []byte("CREATE TABLE parent (id INTEGER PRIMARY KEY); INSERT INTO parent VALUES (1);")},
	}
	if err := Migrate(context.Background(), s.Writer, ordered); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateUnknownAppliedVersionFails(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := Migrate(ctx, s.Writer, testMigrations); err != nil {
		t.Fatal(err)
	}
	older := fstest.MapFS{"0001_first.sql": testMigrations["0001_first.sql"]}
	if err := Migrate(ctx, s.Writer, older); err == nil {
		t.Fatal("migrating a database newer than the build returned no error")
	}
}

func TestMigrateNoMigrationsFails(t *testing.T) {
	s := openTestStore(t)
	if err := Migrate(context.Background(), s.Writer, fstest.MapFS{}); err == nil {
		t.Fatal("empty migrations returned no error")
	}
}

func TestMigrateEmbeddedMigrations(t *testing.T) {
	s := openTestStore(t)
	if err := Migrate(context.Background(), s.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if got := versions(migrationRows(t, s)); got != "0000_bootstrap,0001_init,0002_household_settings_updated_by,0003_jobs_status,0004_ai_provider_presets,0005_ai_provider_xai,0006_ai_runs_status,0007_recipe_grounding,0008_dinner_recipe_url,0009_dinner_grounding_error,0010_dinner_grounding_revision,0011_ground_dinner_job_revision,0012_suggestion_name,0013_week_plan_revision,0014_rotation_review_name" {
		t.Errorf("versions = %q, want 0000_bootstrap,0001_init,0002_household_settings_updated_by,0003_jobs_status,0004_ai_provider_presets,0005_ai_provider_xai,0006_ai_runs_status,0007_recipe_grounding,0008_dinner_recipe_url,0009_dinner_grounding_error,0010_dinner_grounding_revision,0011_ground_dinner_job_revision,0012_suggestion_name,0013_week_plan_revision,0014_rotation_review_name", got)
	}
}

func TestMigrateBootstrapThenEmbeddedInitApplies(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	bootstrap, err := fs.ReadFile(migrations.FS, "0000_bootstrap.sql")
	if err != nil {
		t.Fatal(err)
	}
	// A database migrated by the bootstrap-only build upgrades to the
	// embedded set, which adds the later migrations alongside the unchanged
	// bootstrap.
	if err := Migrate(ctx, s.Writer, fstest.MapFS{"0000_bootstrap.sql": {Data: bootstrap}}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, s.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if got := versions(migrationRows(t, s)); got != "0000_bootstrap,0001_init,0002_household_settings_updated_by,0003_jobs_status,0004_ai_provider_presets,0005_ai_provider_xai,0006_ai_runs_status,0007_recipe_grounding,0008_dinner_recipe_url,0009_dinner_grounding_error,0010_dinner_grounding_revision,0011_ground_dinner_job_revision,0012_suggestion_name,0013_week_plan_revision,0014_rotation_review_name" {
		t.Errorf("versions = %q, want 0000_bootstrap,0001_init,0002_household_settings_updated_by,0003_jobs_status,0004_ai_provider_presets,0005_ai_provider_xai,0006_ai_runs_status,0007_recipe_grounding,0008_dinner_recipe_url,0009_dinner_grounding_error,0010_dinner_grounding_revision,0011_ground_dinner_job_revision,0012_suggestion_name,0013_week_plan_revision,0014_rotation_review_name", got)
	}
	if !tableExists(t, s, "dinners") {
		t.Error("table from 0001_init was not created")
	}
}

// schemaSQL returns every schema object's definition, so a test can prove a
// later Migrate created or altered nothing.
func schemaSQL(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.Reader.Query("SELECT type || ' ' || name || ': ' || coalesce(sql, '') FROM sqlite_schema ORDER BY type, name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var def string
		if err := rows.Scan(&def); err != nil {
			t.Fatal(err)
		}
		out = append(out, def)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMigrateReopenSameDataDirIsNoOp(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	first, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, first.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	before, beforeSchema := migrationRows(t, first), schemaSQL(t, first)
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := Migrate(ctx, second.Writer, migrations.FS); err != nil {
		t.Fatalf("migrate after reopen: %v", err)
	}
	if after := migrationRows(t, second); !slices.Equal(after, before) {
		t.Errorf("schema_migrations changed on reopen:\n got %v\nwant %v", after, before)
	}
	if afterSchema := schemaSQL(t, second); !slices.Equal(afterSchema, beforeSchema) {
		t.Errorf("schema changed on reopen:\n got %v\nwant %v", afterSchema, beforeSchema)
	}
}

// TestMigrateJobsStatusMapsLegacyRows upgrades a database holding jobs in
// every 0001 status and checks 0003 carries each row over with its status
// mapped and every other column unchanged.
func TestMigrateJobsStatusMapsLegacyRows(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	before := fstest.MapFS{}
	for _, name := range []string{"0000_bootstrap.sql", "0001_init.sql", "0002_household_settings_updated_by.sql"} {
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: body}
	}
	if err := Migrate(ctx, s.Writer, before); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]string{"pending": "queued", "running": "running", "done": "succeeded", "failed": "failed"}
	for old := range legacy {
		if _, err := s.Writer.Exec(`INSERT INTO jobs (id, kind, status, payload, result, error, started_at, finished_at, created_at, updated_at)
			VALUES (?, 'suggest', ?, '{"n":1}', '{"r":2}', 'boom', 's', 'f', 'c', 'u')`, "job-"+old, old); err != nil {
			t.Fatalf("insert %s job: %v", old, err)
		}
	}

	if err := Migrate(ctx, s.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	for old, want := range legacy {
		var kind, status, payload, result, errText, startedAt, finishedAt, createdAt, updatedAt string
		if err := s.Reader.QueryRow(`SELECT kind, status, payload, result, error, started_at, finished_at, created_at, updated_at FROM jobs WHERE id = ?`, "job-"+old).
			Scan(&kind, &status, &payload, &result, &errText, &startedAt, &finishedAt, &createdAt, &updatedAt); err != nil {
			t.Fatalf("read %s job: %v", old, err)
		}
		if status != want {
			t.Errorf("%s job status = %q, want %q", old, status, want)
		}
		got := []string{kind, payload, result, errText, startedAt, finishedAt, createdAt, updatedAt}
		if wantCols := []string{"suggest", `{"n":1}`, `{"r":2}`, "boom", "s", "f", "c", "u"}; !slices.Equal(got, wantCols) {
			t.Errorf("%s job columns = %v, want %v", old, got, wantCols)
		}
	}
	if tableExists(t, s, "jobs_new") {
		t.Error("jobs_new left behind")
	}
}

// TestMigrateAIProviderPresetsKeepsRowsAndReferences upgrades a database
// holding ai_settings and ai_runs rows, with suggestion_batches and
// week_plans referencing the run, and checks 0004 carries every row over,
// keeps the references enforced and accepts the new providers.
func TestMigrateAIProviderPresetsKeepsRowsAndReferences(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	before := fstest.MapFS{}
	for _, name := range []string{"0000_bootstrap.sql", "0001_init.sql", "0002_household_settings_updated_by.sql", "0003_jobs_status.sql"} {
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: body}
	}
	if err := Migrate(ctx, s.Writer, before); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO ai_settings (id, provider, base_url, model, temperature, system_prompt, api_key_ct, api_key_last4, key_verified_at, created_at, updated_at)
			VALUES (1, 'anthropic', 'https://api.anthropic.com', 'claude', 0.7, 'sp', x'0102', 'abcd', 'v', 'c', 'u')`,
		`INSERT INTO ai_runs (id, skill, skill_version, provider, model, temperature, status, latency_ms, tokens_in, tokens_out, error, request_redacted, response_redacted, created_at, updated_at)
			VALUES ('run-1', 'suggest', '1', 'ollama', 'llama', 0.5, 'ok', 10, 20, 30, 'e', 'req', 'resp', 'c', 'u')`,
		`INSERT INTO suggestion_batches (id, trigger, status, ai_run_id, created_at, updated_at)
			VALUES ('batch-1', 'manual', 'done', 'run-1', 'c', 'u')`,
		`INSERT INTO week_plans (id, week_start, status, ai_run_id, created_at, updated_at)
			VALUES ('plan-1', '2026-10-04', 'draft', 'run-1', 'c', 'u')`,
	} {
		if _, err := s.Writer.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if err := Migrate(ctx, s.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}

	var provider, baseURL, model, systemPrompt, last4, verifiedAt, createdAt, updatedAt string
	var temperature float64
	var keyCT []byte
	if err := s.Reader.QueryRow(`SELECT provider, base_url, model, temperature, system_prompt, api_key_ct, api_key_last4, key_verified_at, created_at, updated_at FROM ai_settings WHERE id = 1`).
		Scan(&provider, &baseURL, &model, &temperature, &systemPrompt, &keyCT, &last4, &verifiedAt, &createdAt, &updatedAt); err != nil {
		t.Fatalf("read ai_settings: %v", err)
	}
	settings := []any{provider, baseURL, model, temperature, systemPrompt, string(keyCT), last4, verifiedAt, createdAt, updatedAt}
	if want := []any{"anthropic", "https://api.anthropic.com", "claude", 0.7, "sp", "\x01\x02", "abcd", "v", "c", "u"}; !slices.Equal(settings, want) {
		t.Errorf("ai_settings = %v, want %v", settings, want)
	}

	var skill, skillVersion, status, errText, request, response string
	var latency, tokensIn, tokensOut int
	if err := s.Reader.QueryRow(`SELECT skill, skill_version, provider, model, temperature, status, latency_ms, tokens_in, tokens_out, error, request_redacted, response_redacted, created_at, updated_at FROM ai_runs WHERE id = 'run-1'`).
		Scan(&skill, &skillVersion, &provider, &model, &temperature, &status, &latency, &tokensIn, &tokensOut, &errText, &request, &response, &createdAt, &updatedAt); err != nil {
		t.Fatalf("read ai_runs: %v", err)
	}
	run := []any{skill, skillVersion, provider, model, temperature, status, latency, tokensIn, tokensOut, errText, request, response, createdAt, updatedAt}
	if want := []any{"suggest", "1", "ollama", "llama", 0.5, "ok", 10, 20, 30, "e", "req", "resp", "c", "u"}; !slices.Equal(run, want) {
		t.Errorf("ai_runs = %v, want %v", run, want)
	}

	rows, err := s.Reader.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Error("foreign_key_check reported violations after 0004")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Writer.Exec(`DELETE FROM ai_runs WHERE id = 'run-1'`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Errorf("deleting a referenced ai_run: error %v, want FOREIGN KEY constraint failure", err)
	}

	if _, err := s.Writer.Exec(`UPDATE ai_settings SET provider = 'openrouter' WHERE id = 1`); err != nil {
		t.Errorf("ai_settings.provider openrouter rejected: %v", err)
	}
	if _, err := s.Writer.Exec(`UPDATE ai_runs SET provider = 'openai' WHERE id = 'run-1'`); err != nil {
		t.Errorf("ai_runs.provider openai rejected: %v", err)
	}
	for _, table := range []string{"ai_settings_copy", "ai_runs_copy"} {
		if tableExists(t, s, table) {
			t.Errorf("%s left behind", table)
		}
	}
}

// TestMigrateAIProviderXAIKeepsRowsAndReferences upgrades a database at 0004
// holding ai_settings and ai_runs rows with 0004's providers, with
// suggestion_batches and week_plans referencing the run, and checks 0005
// carries every row over, keeps the references enforced, accepts xai and
// still rejects an unknown provider.
func TestMigrateAIProviderXAIKeepsRowsAndReferences(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	before := fstest.MapFS{}
	for _, name := range []string{"0000_bootstrap.sql", "0001_init.sql", "0002_household_settings_updated_by.sql", "0003_jobs_status.sql", "0004_ai_provider_presets.sql"} {
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: body}
	}
	if err := Migrate(ctx, s.Writer, before); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO ai_settings (id, provider, base_url, model, temperature, system_prompt, api_key_ct, api_key_last4, key_verified_at, created_at, updated_at)
			VALUES (1, 'openrouter', 'https://openrouter.ai/api/v1', 'openai/gpt-4o', 0.7, 'sp', x'0102', 'abcd', 'v', 'c', 'u')`,
		`INSERT INTO ai_runs (id, skill, skill_version, provider, model, temperature, status, latency_ms, tokens_in, tokens_out, error, request_redacted, response_redacted, created_at, updated_at)
			VALUES ('run-1', 'suggest', '1', 'openai', 'gpt-4o', 0.5, 'ok', 10, 20, 30, 'e', 'req', 'resp', 'c', 'u')`,
		`INSERT INTO suggestion_batches (id, trigger, status, ai_run_id, created_at, updated_at)
			VALUES ('batch-1', 'manual', 'done', 'run-1', 'c', 'u')`,
		`INSERT INTO week_plans (id, week_start, status, ai_run_id, created_at, updated_at)
			VALUES ('plan-1', '2026-10-04', 'draft', 'run-1', 'c', 'u')`,
	} {
		if _, err := s.Writer.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if err := Migrate(ctx, s.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}

	var provider, baseURL, model, systemPrompt, last4, verifiedAt, createdAt, updatedAt string
	var temperature float64
	var keyCT []byte
	if err := s.Reader.QueryRow(`SELECT provider, base_url, model, temperature, system_prompt, api_key_ct, api_key_last4, key_verified_at, created_at, updated_at FROM ai_settings WHERE id = 1`).
		Scan(&provider, &baseURL, &model, &temperature, &systemPrompt, &keyCT, &last4, &verifiedAt, &createdAt, &updatedAt); err != nil {
		t.Fatalf("read ai_settings: %v", err)
	}
	settings := []any{provider, baseURL, model, temperature, systemPrompt, string(keyCT), last4, verifiedAt, createdAt, updatedAt}
	if want := []any{"openrouter", "https://openrouter.ai/api/v1", "openai/gpt-4o", 0.7, "sp", "\x01\x02", "abcd", "v", "c", "u"}; !slices.Equal(settings, want) {
		t.Errorf("ai_settings = %v, want %v", settings, want)
	}

	var skill, skillVersion, status, errText, request, response string
	var latency, tokensIn, tokensOut int
	if err := s.Reader.QueryRow(`SELECT skill, skill_version, provider, model, temperature, status, latency_ms, tokens_in, tokens_out, error, request_redacted, response_redacted, created_at, updated_at FROM ai_runs WHERE id = 'run-1'`).
		Scan(&skill, &skillVersion, &provider, &model, &temperature, &status, &latency, &tokensIn, &tokensOut, &errText, &request, &response, &createdAt, &updatedAt); err != nil {
		t.Fatalf("read ai_runs: %v", err)
	}
	run := []any{skill, skillVersion, provider, model, temperature, status, latency, tokensIn, tokensOut, errText, request, response, createdAt, updatedAt}
	if want := []any{"suggest", "1", "openai", "gpt-4o", 0.5, "ok", 10, 20, 30, "e", "req", "resp", "c", "u"}; !slices.Equal(run, want) {
		t.Errorf("ai_runs = %v, want %v", run, want)
	}

	rows, err := s.Reader.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Error("foreign_key_check reported violations after 0005")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Writer.Exec(`DELETE FROM ai_runs WHERE id = 'run-1'`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Errorf("deleting a referenced ai_run: error %v, want FOREIGN KEY constraint failure", err)
	}

	for _, table := range []string{"ai_settings", "ai_runs"} {
		if _, err := s.Writer.Exec(`UPDATE ` + table + ` SET provider = 'xai'`); err != nil {
			t.Errorf("%s.provider xai rejected: %v", table, err)
		}
		if _, err := s.Writer.Exec(`UPDATE ` + table + ` SET provider = 'grok'`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
			t.Errorf("%s.provider grok: error %v, want CHECK constraint failure", table, err)
		}
	}
	for _, table := range []string{"ai_settings_copy", "ai_runs_copy"} {
		if tableExists(t, s, table) {
			t.Errorf("%s left behind", table)
		}
	}
}

// TestMigrateAIRunsStatus upgrades a database at 0005 and checks 0006
// keeps an ok run and its references, accepts every harness outcome, and
// stops on a run with a pre-harness status rather than guess its outcome.
func TestMigrateAIRunsStatus(t *testing.T) {
	ctx := context.Background()
	before := fstest.MapFS{}
	for _, name := range []string{"0000_bootstrap.sql", "0001_init.sql", "0002_household_settings_updated_by.sql", "0003_jobs_status.sql", "0004_ai_provider_presets.sql", "0005_ai_provider_xai.sql"} {
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: body}
	}
	seedRun := func(t *testing.T, s *Store, status string) {
		t.Helper()
		if err := Migrate(ctx, s.Writer, before); err != nil {
			t.Fatal(err)
		}
		for _, stmt := range []string{
			`INSERT INTO ai_runs (id, skill, skill_version, provider, model, temperature, status, latency_ms, tokens_in, tokens_out, error, request_redacted, response_redacted, created_at, updated_at)
				VALUES ('run-1', 'suggest', '1', 'xai', 'grok', 0.5, '` + status + `', 10, 20, 30, 'e', 'req', 'resp', 'c', 'u')`,
			`INSERT INTO suggestion_batches (id, trigger, status, ai_run_id, created_at, updated_at)
				VALUES ('batch-1', 'manual', 'done', 'run-1', 'c', 'u')`,
		} {
			if _, err := s.Writer.Exec(stmt); err != nil {
				t.Fatalf("seed: %v", err)
			}
		}
	}

	t.Run("ok row kept", func(t *testing.T) {
		s := openTestStore(t)
		seedRun(t, s, "ok")
		if err := Migrate(ctx, s.Writer, migrations.FS); err != nil {
			t.Fatal(err)
		}
		var status, request string
		if err := s.Reader.QueryRow(`SELECT status, request_redacted FROM ai_runs WHERE id = 'run-1'`).Scan(&status, &request); err != nil {
			t.Fatal(err)
		}
		if status != "ok" || request != "req" {
			t.Errorf("ai_runs row = %s, %s; want ok, req", status, request)
		}
		if _, err := s.Writer.Exec(`DELETE FROM ai_runs WHERE id = 'run-1'`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
			t.Errorf("deleting a referenced ai_run: error %v, want FOREIGN KEY constraint failure", err)
		}
		for _, status := range []string{"repaired", "bad_output", "provider_error", "ok"} {
			if _, err := s.Writer.Exec(`UPDATE ai_runs SET status = ? WHERE id = 'run-1'`, status); err != nil {
				t.Errorf("ai_runs.status %s rejected: %v", status, err)
			}
		}
		if tableExists(t, s, "ai_runs_copy") {
			t.Error("ai_runs_copy left behind")
		}
	})

	for _, legacy := range []string{"running", "error"} {
		t.Run(legacy+" row stops the migration", func(t *testing.T) {
			s := openTestStore(t)
			seedRun(t, s, legacy)
			if err := Migrate(ctx, s.Writer, migrations.FS); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
				t.Fatalf("Migrate error = %v, want a CHECK constraint failure", err)
			}
			var status string
			if err := s.Reader.QueryRow(`SELECT status FROM ai_runs WHERE id = 'run-1'`).Scan(&status); err != nil || status != legacy {
				t.Errorf("after the failed migration status = %q, %v; want %q kept", status, err, legacy)
			}
		})
	}
}

// TestMigrateSuggestionName upgrades a database at 0011 holding a pending
// and a declined suggestion and checks 0012 keeps both, gives each its
// dinner's name, and refuses a suggestion with no name.
func TestMigrateSuggestionName(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	before := fstest.MapFS{}
	for _, name := range []string{
		"0000_bootstrap.sql", "0001_init.sql", "0002_household_settings_updated_by.sql", "0003_jobs_status.sql",
		"0004_ai_provider_presets.sql", "0005_ai_provider_xai.sql", "0006_ai_runs_status.sql", "0007_recipe_grounding.sql",
		"0008_dinner_recipe_url.sql", "0009_dinner_grounding_error.sql", "0010_dinner_grounding_revision.sql",
		"0011_ground_dinner_job_revision.sql",
	} {
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: body}
	}
	if err := Migrate(ctx, s.Writer, before); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO dinners (id, name, normalized_name, status, origin, is_favorite, grounding_status, created_at, updated_at)
			VALUES ('dinner-1', 'Pho', 'pho', 'suggested', 'side_quest', 0, 'grounded', 'c', 'u'),
			       ('dinner-2', 'Ramen', 'ramen', 'archived', 'side_quest', 0, 'grounded', 'c', 'u')`,
		`INSERT INTO suggestion_batches (id, trigger, status, created_at, updated_at)
			VALUES ('batch-1', 'manual', 'done', 'c', 'u')`,
		`INSERT INTO suggestions (id, dinner_id, batch_id, reason, cited_regular_ids, status, decided_at, created_at, updated_at)
			VALUES ('sugg-1', 'dinner-1', 'batch-1', 'why 1', '[]', 'pending', NULL, 'c1', 'u1'),
			       ('sugg-2', 'dinner-2', 'batch-1', 'why 2', '["dinner-1"]', 'declined', 'd2', 'c2', 'u2')`,
	} {
		if _, err := s.Writer.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if err := Migrate(ctx, s.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Reader.Query(`SELECT id, dinner_id, batch_id, name, reason, cited_regular_ids, status, COALESCE(decided_at, ''), created_at, updated_at
		FROM suggestions ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for rows.Next() {
		r := make([]string, 10)
		if err := rows.Scan(&r[0], &r[1], &r[2], &r[3], &r[4], &r[5], &r[6], &r[7], &r[8], &r[9]); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"sugg-1", "dinner-1", "batch-1", "Pho", "why 1", "[]", "pending", "", "c1", "u1"},
		{"sugg-2", "dinner-2", "batch-1", "Ramen", "why 2", `["dinner-1"]`, "declined", "d2", "c2", "u2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("suggestions after 0012 =\n %v\nwant\n %v", got, want)
	}
	if _, err := s.Writer.Exec(`UPDATE suggestions SET name = '' WHERE id = 'sugg-1'`); err == nil || !strings.Contains(err.Error(), "CHECK constraint failed") {
		t.Errorf("empty suggestion name: error %v, want CHECK constraint failure", err)
	}
	if _, err := s.Writer.Exec(`UPDATE suggestions SET dinner_id = 'nope' WHERE id = 'sugg-1'`); err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Errorf("unknown dinner: error %v, want FOREIGN KEY constraint failure", err)
	}
	if tableExists(t, s, "suggestions_copy") {
		t.Error("suggestions_copy left behind")
	}
}

// TestMigrateRotationReviewName upgrades a database at 0013 holding a
// pending, a kept and a dropped Rotation check and checks 0014 keeps all
// three, gives only the drop its dinner's name, and refuses a drop with no
// name or a name on any other check.
func TestMigrateRotationReviewName(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	before := fstest.MapFS{}
	for _, name := range []string{
		"0000_bootstrap.sql", "0001_init.sql", "0002_household_settings_updated_by.sql", "0003_jobs_status.sql",
		"0004_ai_provider_presets.sql", "0005_ai_provider_xai.sql", "0006_ai_runs_status.sql", "0007_recipe_grounding.sql",
		"0008_dinner_recipe_url.sql", "0009_dinner_grounding_error.sql", "0010_dinner_grounding_revision.sql",
		"0011_ground_dinner_job_revision.sql", "0012_suggestion_name.sql", "0013_week_plan_revision.sql",
	} {
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: body}
	}
	if err := Migrate(ctx, s.Writer, before); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO dinners (id, name, normalized_name, status, origin, is_favorite, grounding_status, created_at, updated_at)
			VALUES ('dinner-1', 'Pho', 'pho', 'tried', 'side_quest', 0, 'grounded', 'c', 'u'),
			       ('dinner-2', 'Ramen', 'ramen', 'regular', 'side_quest', 0, 'grounded', 'c', 'u'),
			       ('dinner-3', 'Laksa', 'laksa', 'archived', 'side_quest', 0, 'grounded', 'c', 'u')`,
		`INSERT INTO rotation_reviews (id, dinner_id, plan_date, status, decision, decided_at, updated_by, created_at, updated_at)
			VALUES ('review-1', 'dinner-1', '2026-10-12', 'pending', NULL, NULL, NULL, 'c1', 'u1'),
			       ('review-2', 'dinner-2', '2026-10-13', 'decided', 'keep', 'd2', 'member-1', 'c2', 'u2'),
			       ('review-3', 'dinner-3', '2026-10-14', 'decided', 'drop', 'd3', 'member-1', 'c3', 'u3')`,
	} {
		if _, err := s.Writer.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if err := Migrate(ctx, s.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Reader.Query(`SELECT id, dinner_id, plan_date, status, COALESCE(decision, ''), COALESCE(name, ''),
		COALESCE(decided_at, ''), COALESCE(updated_by, ''), created_at, updated_at
		FROM rotation_reviews ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got [][]string
	for rows.Next() {
		r := make([]string, 10)
		if err := rows.Scan(&r[0], &r[1], &r[2], &r[3], &r[4], &r[5], &r[6], &r[7], &r[8], &r[9]); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"review-1", "dinner-1", "2026-10-12", "pending", "", "", "", "", "c1", "u1"},
		{"review-2", "dinner-2", "2026-10-13", "decided", "keep", "", "d2", "member-1", "c2", "u2"},
		{"review-3", "dinner-3", "2026-10-14", "decided", "drop", "Laksa", "d3", "member-1", "c3", "u3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rotation_reviews after 0014 =\n %v\nwant\n %v", got, want)
	}
	for _, tc := range []struct{ name, stmt, want string }{
		{"drop with no name", `UPDATE rotation_reviews SET name = NULL WHERE id = 'review-3'`, "CHECK constraint failed"},
		{"empty name", `UPDATE rotation_reviews SET name = '' WHERE id = 'review-3'`, "CHECK constraint failed"},
		{"name on a keep", `UPDATE rotation_reviews SET name = 'Ramen' WHERE id = 'review-2'`, "CHECK constraint failed"},
		{"name on a pending check", `UPDATE rotation_reviews SET name = 'Pho' WHERE id = 'review-1'`, "CHECK constraint failed"},
		{"unknown dinner", `UPDATE rotation_reviews SET dinner_id = 'nope' WHERE id = 'review-1'`, "FOREIGN KEY constraint failed"},
		{"second check of a dinner and date", `INSERT INTO rotation_reviews (id, dinner_id, plan_date, status, created_at, updated_at)
			VALUES ('review-4', 'dinner-1', '2026-10-12', 'pending', 'c', 'u')`, "UNIQUE constraint failed"},
	} {
		if _, err := s.Writer.Exec(tc.stmt); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want %s", tc.name, err, tc.want)
		}
	}
	if tableExists(t, s, "rotation_reviews_copy") {
		t.Error("rotation_reviews_copy left behind")
	}
}
