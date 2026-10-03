package household

import (
	"context"
	"database/sql"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// notificationPrefs returns the stored notification preferences by event as
// [push, chat].
func notificationPrefs(t *testing.T, st *store.Store) map[string][2]int64 {
	t.Helper()
	rows, err := st.Reader.QueryContext(context.Background(), `SELECT event, push, chat FROM notification_prefs`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := make(map[string][2]int64)
	for rows.Next() {
		var event string
		var push, chat int64
		if err := rows.Scan(&event, &push, &chat); err != nil {
			t.Fatal(err)
		}
		got[event] = [2]int64{push, chat}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSeedAISettingsCreatesRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := migratedStore(t, t.TempDir())

	if err := SeedAISettings(ctx, st); err != nil {
		t.Fatal(err)
	}
	got, err := db.New(st.Reader).GetAISettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := db.AiSetting{
		ID:           1,
		Provider:     "anthropic",
		BaseUrl:      "",
		Model:        "",
		Temperature:  0.7,
		SystemPrompt: sql.NullString{String: DefaultPersonaPrompt, Valid: true},
		CreatedAt:    got.CreatedAt,
		UpdatedAt:    got.UpdatedAt,
	}
	if got.ID != want.ID || got.Provider != want.Provider || got.BaseUrl != want.BaseUrl ||
		got.Model != want.Model || got.Temperature != want.Temperature || got.SystemPrompt != want.SystemPrompt ||
		got.ApiKeyCt != nil || got.ApiKeyLast4.Valid || got.KeyVerifiedAt.Valid {
		t.Errorf("seeded AI settings = %+v\nwant %+v", got, want)
	}
	var created store.Timestamp
	if err := created.Scan(got.CreatedAt); err != nil {
		t.Errorf("created_at: %v", err)
	}
	if got.UpdatedAt != got.CreatedAt {
		t.Errorf("updated_at = %q, want created_at %q", got.UpdatedAt, got.CreatedAt)
	}
}

func TestDefaultPersonaPrompt(t *testing.T) {
	t.Parallel()
	const want = "You are MealQuest, a dinner planning assistant for one household. Suggest dinners\n" +
		"similar to their regular rotation, nudging them to try something a little new each\n" +
		"week. Prefer weeknight-friendly recipes with common grocery-store ingredients.\n" +
		"Flag crock pot meals and anything 30 minutes or less as \"quick\". Keep explanations\n" +
		"short, warm and specific — mention the household's own regular dinners when you\n" +
		"explain why something fits. Never suggest a dinner they already make or have\n" +
		"declined."
	if DefaultPersonaPrompt != want {
		t.Errorf("DefaultPersonaPrompt = %q\nwant %q", DefaultPersonaPrompt, want)
	}
}

func TestSeedAISettingsNeverOverwrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	// First boot seeds; the household then configures a provider.
	first := migratedStore(t, dir)
	if err := SeedAISettings(ctx, first); err != nil {
		t.Fatal(err)
	}
	seeded, err := db.New(first.Reader).GetAISettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := db.New(first.Writer).UpsertAISettings(ctx, db.UpsertAISettingsParams{
		Provider:      "ollama",
		BaseUrl:       "http://ollama:11434",
		Model:         "llama3",
		Temperature:   0.2,
		SystemPrompt:  sql.NullString{String: "Be brief.", Valid: true},
		ApiKeyCt:      []byte{1, 2, 3},
		ApiKeyLast4:   sql.NullString{String: "wxyz", Valid: true},
		KeyVerifiedAt: sql.NullString{String: store.Now().String(), Valid: true},
		CreatedAt:     seeded.CreatedAt,
		UpdatedAt:     store.Now().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// A later boot reopens the same database and seeds again.
	st := migratedStore(t, dir)
	if err := SeedAISettings(ctx, st); err != nil {
		t.Fatalf("reboot: %v", err)
	}
	got, err := db.New(st.Reader).GetAISettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Provider != changed.Provider || got.BaseUrl != changed.BaseUrl || got.Model != changed.Model ||
		got.Temperature != changed.Temperature || got.SystemPrompt != changed.SystemPrompt ||
		string(got.ApiKeyCt) != string(changed.ApiKeyCt) || got.ApiKeyLast4 != changed.ApiKeyLast4 ||
		got.KeyVerifiedAt != changed.KeyVerifiedAt || got.CreatedAt != changed.CreatedAt || got.UpdatedAt != changed.UpdatedAt {
		t.Errorf("reboot changed AI settings:\n got %+v\nwant %+v", got, changed)
	}
}

func TestSeedNotificationPrefsCreatesRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := migratedStore(t, t.TempDir())

	if err := SeedNotificationPrefs(ctx, st); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int64{
		"plan_ready":      {1, 1},
		"list_ready":      {1, 1},
		"day_of_reminder": {1, 0},
		"new_suggestions": {1, 0},
		"rotation_review": {1, 0},
	}
	got := notificationPrefs(t, st)
	if len(got) != len(want) {
		t.Errorf("seeded %d notification preferences, want %d: %v", len(got), len(want), got)
	}
	for event, w := range want {
		if g, ok := got[event]; !ok || g != w {
			t.Errorf("%s [push, chat] = %v (present %t), want %v", event, g, ok, w)
		}
	}

	var created, updated string
	err := st.Reader.QueryRowContext(ctx, `SELECT created_at, updated_at FROM notification_prefs WHERE event = 'plan_ready'`).Scan(&created, &updated)
	if err != nil {
		t.Fatal(err)
	}
	var ts store.Timestamp
	if err := ts.Scan(created); err != nil {
		t.Errorf("created_at: %v", err)
	}
	if updated != created {
		t.Errorf("updated_at = %q, want created_at %q", updated, created)
	}
}

func TestSeedNotificationPrefsNeverOverwrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	// First boot seeds; the household then changes two preferences, and one
	// event's row goes missing.
	first := migratedStore(t, dir)
	if err := SeedNotificationPrefs(ctx, first); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE notification_prefs SET push = 0, chat = 0 WHERE event = 'plan_ready'`,
		`UPDATE notification_prefs SET chat = 1 WHERE event = 'rotation_review'`,
		`DELETE FROM notification_prefs WHERE event = 'new_suggestions'`,
	} {
		if _, err := first.Writer.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// A later boot keeps the changed rows and seeds only the missing one.
	st := migratedStore(t, dir)
	if err := SeedNotificationPrefs(ctx, st); err != nil {
		t.Fatalf("reboot: %v", err)
	}
	want := map[string][2]int64{
		"plan_ready":      {0, 0},
		"list_ready":      {1, 1},
		"day_of_reminder": {1, 0},
		"new_suggestions": {1, 0},
		"rotation_review": {1, 1},
	}
	got := notificationPrefs(t, st)
	if len(got) != len(want) {
		t.Errorf("%d notification preferences after reboot, want %d: %v", len(got), len(want), got)
	}
	for event, w := range want {
		if g, ok := got[event]; !ok || g != w {
			t.Errorf("after reboot %s [push, chat] = %v (present %t), want %v", event, g, ok, w)
		}
	}
}
