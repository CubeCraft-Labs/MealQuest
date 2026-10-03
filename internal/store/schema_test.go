package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// schemaTables is every table in the v1 data model, in insert order: parents
// before the children that reference them.
var schemaTables = []string{
	"ai_runs",
	"recipes",
	"recipe_ingredients",
	"recipe_steps",
	"photos",
	"dinners",
	"suggestion_batches",
	"suggestions",
	"week_plans",
	"plan_days",
	"rotation_reviews",
	"shopping_lists",
	"shopping_groups",
	"shopping_items",
	"notification_prefs",
	"push_subscriptions",
	"notification_outbox",
	"jobs",
	"schedule_runs",
	"sessions",
	// Single-row settings tables; row is always id 1.
	"household_settings",
	"ai_settings",
	"recipe_search_settings",
	"integrations_google",
	"chat_settings",
}

var singleRowTables = []string{
	"household_settings",
	"ai_settings",
	"recipe_search_settings",
	"integrations_google",
	"chat_settings",
}

const testTime = "2026-10-03T12:00:00Z"

// rowID is the id of the i-th test row of table. The schema stores ids as
// opaque TEXT, so deterministic ids keep the fixtures readable.
func rowID(table string, i int) string { return fmt.Sprintf("%s-%d", table, i) }

// testRow returns a valid row for table. Rows with different i differ in
// every unique column; references point at row 0 of the parent table.
func testRow(table string, i int) map[string]any {
	ts := map[string]any{"created_at": testTime, "updated_at": testTime}
	r := map[string]any{"id": rowID(table, i)}
	switch table {
	case "household_settings":
		r = map[string]any{"id": 1, "timezone": "America/New_York"}
	case "ai_runs":
		r["skill"] = "suggest"
		r["skill_version"] = "1"
		r["provider"] = "anthropic"
		r["model"] = "claude"
		r["temperature"] = 0.7
		r["status"] = "ok"
		r["request_redacted"] = "{}"
	case "recipes":
		r["source_url"] = fmt.Sprintf("https://example.com/recipe/%d", i)
		r["title"] = "Chili"
		r["raw_jsonld"] = "{}"
		r["fetched_at"] = testTime
	case "recipe_ingredients":
		r["recipe_id"] = rowID("recipes", 0)
		r["position"] = i
		r["raw_text"] = "1 onion"
	case "recipe_steps":
		r["recipe_id"] = rowID("recipes", 0)
		r["position"] = i
		r["text"] = "Chop the onion."
	case "photos":
		r["source_url"] = "https://example.com/chili.jpg"
		r["local_path"] = "photos/chili.jpg"
		r["width"] = 800
		r["height"] = 600
		r["sha256"] = "abc"
		r["fetched_at"] = testTime
	case "dinners":
		r["name"] = "Chili"
		r["normalized_name"] = "chili"
		r["status"] = "regular"
		r["origin"] = "regular"
		r["method"] = "crockpot"
		r["difficulty"] = "easy"
		r["is_favorite"] = 0
		r["recipe_id"] = rowID("recipes", 0)
		r["photo_id"] = rowID("photos", 0)
		r["grounding_status"] = "grounded"
	case "suggestion_batches":
		r["trigger"] = "scheduled"
		r["status"] = "done"
		r["ai_run_id"] = rowID("ai_runs", 0)
	case "suggestions":
		r["dinner_id"] = rowID("dinners", 0)
		r["batch_id"] = rowID("suggestion_batches", 0)
		r["reason"] = "Like your chili."
		r["cited_regular_ids"] = "[]"
		r["status"] = "pending"
	case "week_plans":
		r["week_start"] = fmt.Sprintf("2026-%02d-05", i+1)
		r["status"] = "draft"
		r["ai_run_id"] = rowID("ai_runs", 0)
	case "plan_days":
		r["plan_id"] = rowID("week_plans", 0)
		r["date"] = fmt.Sprintf("2026-01-%02d", i+5)
		r["dinner_id"] = rowID("dinners", 0)
		r["is_new"] = 0
	case "rotation_reviews":
		r["dinner_id"] = rowID("dinners", 0)
		r["plan_date"] = fmt.Sprintf("2026-01-%02d", i+5)
		r["status"] = "decided"
		r["decision"] = "keep"
	case "shopping_lists":
		r["week_start"] = fmt.Sprintf("2026-%02d-05", i+1)
		r["plan_id"] = rowID("week_plans", 0)
		r["title"] = "Supply run"
		r["status"] = "current"
	case "shopping_groups":
		r["list_id"] = rowID("shopping_lists", 0)
		r["dinner_id"] = rowID("dinners", 0)
		r["position"] = i
	case "shopping_items":
		r["group_id"] = rowID("shopping_groups", 0)
		r["name"] = "Onion"
		r["needed"] = 1
		r["position"] = i
	case "ai_settings":
		r = map[string]any{"id": 1, "provider": "ollama", "base_url": "http://ollama:11434", "model": "llama", "temperature": 0.7}
	case "recipe_search_settings":
		r = map[string]any{"id": 1, "provider": "searxng", "base_url": "http://searxng:8080"}
	case "integrations_google":
		r = map[string]any{"id": 1, "refresh_token_ct": []byte{1}, "connected_at": testTime}
	case "chat_settings":
		r = map[string]any{"id": 1, "webhook_url_ct": []byte{1}, "last_test_status": "ok"}
	case "notification_prefs":
		events := []string{"plan_ready", "list_ready", "day_of_reminder", "new_suggestions", "rotation_review"}
		r = map[string]any{"event": events[i], "push": 1, "chat": 0}
	case "push_subscriptions":
		r["endpoint"] = fmt.Sprintf("https://push.example.com/%d", i)
		r["p256dh"] = "key"
		r["auth"] = "auth"
		r["failure_count"] = 0
	case "notification_outbox":
		r["event_key"] = fmt.Sprintf("plan_ready:2026-W%02d", i+1)
		r["channel"] = "push"
		r["payload"] = "{}"
		r["status"] = "pending"
		r["attempts"] = 0
	case "jobs":
		r["kind"] = "suggest"
		r["status"] = "pending"
		r["payload"] = "{}"
	case "schedule_runs":
		r["schedule"] = "plan_draft"
		r["period_key"] = fmt.Sprintf("2026-W%02d", i+1)
		r["created_at"] = testTime
		return r
	case "sessions":
		return map[string]any{
			"id_hash":      fmt.Sprintf("hash-%d", i),
			"subject":      "sub",
			"email":        "kitchen@example.com",
			"name":         "Our kitchen",
			"groups":       `["mealquest-admin"]`,
			"csrf_token":   "csrf",
			"created_at":   testTime,
			"last_seen_at": testTime,
			"expires_at":   testTime,
		}
	default:
		panic("testRow: unknown table " + table)
	}
	for k, v := range ts {
		r[k] = v
	}
	return r
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertSchemaRow(db execer, table string, row map[string]any) error {
	cols := make([]string, 0, len(row))
	for c := range row {
		cols = append(cols, c)
	}
	slices.Sort(cols)
	quoted := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, c := range cols {
		quoted[i] = `"` + c + `"`
		args[i] = row[c]
	}
	q := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`,
		table, strings.Join(quoted, ", "), strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", "))
	_, err := db.ExecContext(context.Background(), q, args...)
	return err
}

// migratedStore opens a temp store migrated with the embedded migrations.
func migratedStore(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	if err := Migrate(context.Background(), s.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return s
}

// seededStore is a migrated store holding row 0 of every multi-row table.
// Single-row tables are left empty so tests can insert their only row.
func seededStore(t *testing.T) *Store {
	t.Helper()
	s := migratedStore(t)
	for _, table := range schemaTables {
		if slices.Contains(singleRowTables, table) {
			continue
		}
		if err := insertSchemaRow(s.Writer, table, testRow(table, 0)); err != nil {
			t.Fatalf("seed %s: %v", table, err)
		}
	}
	return s
}

// inRolledBackTx runs fn in a writer transaction that is always rolled back,
// so each case starts from the seeded state.
func inRolledBackTx(t *testing.T, s *Store, fn func(tx *sql.Tx)) {
	t.Helper()
	tx, err := s.Writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
	}()
	fn(tx)
}

func wantConstraintError(t *testing.T, err error, kind, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: accepted, want %s constraint failure", what, kind)
	}
	if !strings.Contains(err.Error(), kind+" constraint failed") {
		t.Fatalf("%s: error %q, want %s constraint failure", what, err, kind)
	}
}

func TestSchemaTablesExist(t *testing.T) {
	s := migratedStore(t)
	if len(schemaTables) != 25 {
		t.Fatalf("schemaTables lists %d tables, want 25", len(schemaTables))
	}
	for _, table := range schemaTables {
		if !tableExists(t, s, table) {
			t.Errorf("table %s missing", table)
		}
	}
}

func TestSchemaForeignKeysAreValid(t *testing.T) {
	s := seededStore(t)
	rows, err := s.Writer.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("foreign_key_check reported violations in the seeded data")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestSchemaEnumChecksRejectInvalidValues(t *testing.T) {
	cases := []struct {
		table, column string
		bad           any
	}{
		{"household_settings", "plan_draft_dow", 7},
		{"household_settings", "suggestions_dow", -1},
		{"household_settings", "chat_autosend_dow", 7},
		{"ai_runs", "provider", "openai"},
		{"ai_runs", "status", "done"},
		{"dinners", "status", "deck"},
		{"dinners", "origin", "ai"},
		{"dinners", "method", "microwave"},
		{"dinners", "difficulty", "extreme"},
		{"dinners", "is_favorite", 2},
		{"dinners", "grounding_status", "unknown"},
		{"suggestion_batches", "trigger", "cron"},
		{"suggestion_batches", "status", "ok"},
		{"suggestions", "status", "maybe"},
		{"week_plans", "status", "final"},
		{"plan_days", "is_new", 2},
		{"rotation_reviews", "status", "open"},
		{"rotation_reviews", "decision", "promote"},
		{"shopping_lists", "status", "frozen"},
		{"shopping_items", "needed", 2},
		{"ai_settings", "provider", "openai"},
		{"recipe_search_settings", "provider", "google"},
		{"chat_settings", "last_test_status", "pending"},
		{"notification_prefs", "event", "weekly_digest"},
		{"notification_prefs", "push", 2},
		{"notification_prefs", "chat", 2},
		{"notification_outbox", "channel", "email"},
		{"notification_outbox", "status", "queued"},
		{"jobs", "status", "queued"},
		{"schedule_runs", "schedule", "hourly"},
	}
	s := seededStore(t)
	for _, c := range cases {
		t.Run(c.table+"."+c.column, func(t *testing.T) {
			row := testRow(c.table, 1)
			if slices.Contains(singleRowTables, c.table) {
				row = testRow(c.table, 0)
			}
			inRolledBackTx(t, s, func(tx *sql.Tx) {
				if err := insertSchemaRow(tx, c.table, row); err != nil {
					t.Fatalf("valid row rejected: %v", err)
				}
			})
			row[c.column] = c.bad
			inRolledBackTx(t, s, func(tx *sql.Tx) {
				wantConstraintError(t, insertSchemaRow(tx, c.table, row), "CHECK", fmt.Sprintf("%s.%s = %v", c.table, c.column, c.bad))
			})
		})
	}
}

func TestSchemaRotationReviewEventAllowed(t *testing.T) {
	s := migratedStore(t)
	if err := insertSchemaRow(s.Writer, "notification_prefs", testRow("notification_prefs", 4)); err != nil {
		t.Fatalf("rotation_review event rejected: %v", err)
	}
}

func TestSchemaDinnerStatuses(t *testing.T) {
	s := seededStore(t)
	for _, status := range []string{"suggested", "quest_log", "tried", "regular", "archived"} {
		inRolledBackTx(t, s, func(tx *sql.Tx) {
			row := testRow("dinners", 1)
			row["status"] = status
			if err := insertSchemaRow(tx, "dinners", row); err != nil {
				t.Errorf("status %s rejected: %v", status, err)
			}
		})
	}
}

func TestSchemaUniqueConstraintsRejectDuplicates(t *testing.T) {
	cases := []struct {
		table   string
		columns []string
	}{
		{"recipes", []string{"source_url"}},
		{"week_plans", []string{"week_start"}},
		{"plan_days", []string{"plan_id", "date"}},
		{"rotation_reviews", []string{"dinner_id", "plan_date"}},
		{"shopping_lists", []string{"week_start"}},
		{"push_subscriptions", []string{"endpoint"}},
		{"notification_outbox", []string{"event_key", "channel"}},
		{"schedule_runs", []string{"schedule", "period_key"}},
		{"notification_prefs", []string{"event"}},
	}
	s := seededStore(t)
	for _, c := range cases {
		t.Run(c.table+"("+strings.Join(c.columns, ",")+")", func(t *testing.T) {
			row := testRow(c.table, 1)
			inRolledBackTx(t, s, func(tx *sql.Tx) {
				if err := insertSchemaRow(tx, c.table, row); err != nil {
					t.Fatalf("distinct row rejected: %v", err)
				}
			})
			seeded := testRow(c.table, 0)
			for _, col := range c.columns {
				row[col] = seeded[col]
			}
			inRolledBackTx(t, s, func(tx *sql.Tx) {
				wantConstraintError(t, insertSchemaRow(tx, c.table, row), "UNIQUE", "duplicate "+c.table)
			})
		})
	}
}

func TestSchemaSingleRowTablesRejectSecondRow(t *testing.T) {
	s := migratedStore(t)
	for _, table := range singleRowTables {
		if err := insertSchemaRow(s.Writer, table, testRow(table, 0)); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		row := testRow(table, 0)
		row["id"] = 2
		wantConstraintError(t, insertSchemaRow(s.Writer, table, row), "CHECK", table+" second row")
	}
}

func TestSchemaDeleteParentWithChildrenRefused(t *testing.T) {
	cases := []struct{ parent, child, column string }{
		{"recipes", "recipe_ingredients", "recipe_id"},
		{"recipes", "recipe_steps", "recipe_id"},
		{"recipes", "dinners", "recipe_id"},
		{"photos", "dinners", "photo_id"},
		{"dinners", "suggestions", "dinner_id"},
		{"suggestion_batches", "suggestions", "batch_id"},
		{"week_plans", "plan_days", "plan_id"},
		{"dinners", "plan_days", "dinner_id"},
		{"dinners", "rotation_reviews", "dinner_id"},
		{"week_plans", "shopping_lists", "plan_id"},
		{"shopping_lists", "shopping_groups", "list_id"},
		{"dinners", "shopping_groups", "dinner_id"},
		{"shopping_groups", "shopping_items", "group_id"},
	}
	s := seededStore(t)
	for _, c := range cases {
		t.Run(c.parent+"<-"+c.child+"."+c.column, func(t *testing.T) {
			// A fresh parent with exactly one child, through this column only.
			parentID := rowID(c.parent, 1)
			child := testRow(c.child, 1)
			child[c.column] = parentID
			del := fmt.Sprintf(`DELETE FROM "%s" WHERE id = ?`, c.parent)
			ctx := context.Background()

			inRolledBackTx(t, s, func(tx *sql.Tx) {
				if err := insertSchemaRow(tx, c.parent, testRow(c.parent, 1)); err != nil {
					t.Fatalf("insert parent: %v", err)
				}
				if err := insertSchemaRow(tx, c.child, child); err != nil {
					t.Fatalf("insert child: %v", err)
				}
				_, err := tx.ExecContext(ctx, del, parentID)
				wantConstraintError(t, err, "FOREIGN KEY", "delete "+c.parent)
				var n int
				if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM "%s" WHERE id = ?`, c.parent), parentID).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 1 {
					t.Errorf("parent row gone after refused delete")
				}
			})
			// Control: without the child the same delete succeeds.
			inRolledBackTx(t, s, func(tx *sql.Tx) {
				if err := insertSchemaRow(tx, c.parent, testRow(c.parent, 1)); err != nil {
					t.Fatalf("insert parent: %v", err)
				}
				if _, err := tx.ExecContext(ctx, del, parentID); err != nil {
					t.Fatalf("delete childless parent: %v", err)
				}
			})
		})
	}
}

func TestSchemaAIRunPurgeClearsReferences(t *testing.T) {
	s := seededStore(t)
	ctx := context.Background()
	if _, err := s.Writer.ExecContext(ctx, `DELETE FROM ai_runs WHERE id = ?`, rowID("ai_runs", 0)); err != nil {
		t.Fatalf("purge ai_runs: %v", err)
	}
	for _, table := range []string{"suggestion_batches", "week_plans"} {
		var ref sql.NullString
		if err := s.Writer.QueryRowContext(ctx, fmt.Sprintf(`SELECT ai_run_id FROM "%s" WHERE id = ?`, table), rowID(table, 0)).Scan(&ref); err != nil {
			t.Fatal(err)
		}
		if ref.Valid {
			t.Errorf("%s.ai_run_id = %q after purge, want NULL", table, ref.String)
		}
	}
}
