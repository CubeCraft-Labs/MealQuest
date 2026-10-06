package household

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// DefaultPersonaPrompt is the default AI system prompt (CTXD PROMPTS.MD §4).
// SeedAISettings stores it on first boot and aisettings.Service.ResetPrompt
// restores it.
const DefaultPersonaPrompt = `You are MealQuest, a dinner planning assistant for one household. Suggest dinners
similar to their regular rotation, nudging them to try something a little new each
week. Prefer weeknight-friendly recipes with common grocery-store ingredients.
Flag crock pot meals and anything 30 minutes or less as "quick". Keep explanations
short, warm and specific — mention the household's own regular dinners when you
explain why something fits. Never suggest a dinner they already make or have
declined.`

// Seed values for the AI settings (CTXD CONTEXT.MD). The base URL and model
// are empty until the household picks a provider preset; there is no key.
const (
	SeedAIProvider    = "anthropic"
	SeedAIBaseURL     = ""
	SeedAIModel       = ""
	SeedAITemperature = 0.7
)

// seedNotificationPrefs are the notification preferences for a new
// household, one per event: push for every event, chat only for the plan
// and the shopping list.
var seedNotificationPrefs = []struct {
	Event      string
	Push, Chat bool
}{
	{Event: "plan_ready", Push: true, Chat: true},
	{Event: "list_ready", Push: true, Chat: true},
	{Event: "day_of_reminder", Push: true, Chat: false},
	{Event: "new_suggestions", Push: true, Chat: false},
	{Event: "rotation_review", Push: true, Chat: false},
}

// SeedAISettings creates the AI settings row with the seed values and
// DefaultPersonaPrompt if it does not exist. An existing row is never
// changed.
func SeedAISettings(ctx context.Context, st *store.Store) error {
	return st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		_, err := q.GetAISettings(ctx)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("household: read AI settings: %w", err)
		}
		now := store.Now().String()
		_, err = q.UpsertAISettings(ctx, db.UpsertAISettingsParams{
			Provider:     SeedAIProvider,
			BaseUrl:      SeedAIBaseURL,
			Model:        SeedAIModel,
			Temperature:  SeedAITemperature,
			SystemPrompt: sql.NullString{String: DefaultPersonaPrompt, Valid: true},
			CreatedAt:    now,
			UpdatedAt:    now,
		})
		if err != nil {
			return fmt.Errorf("household: seed AI settings: %w", err)
		}
		return nil
	})
}

// SeedNotificationPrefs creates the seed notification preference row of
// every event that has none. Existing rows are never changed.
func SeedNotificationPrefs(ctx context.Context, st *store.Store) error {
	return st.WithTx(ctx, func(tx *sql.Tx) error {
		now := store.Now().String()
		for _, p := range seedNotificationPrefs {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO notification_prefs (event, push, chat, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?)
				ON CONFLICT (event) DO NOTHING`,
				p.Event, p.Push, p.Chat, now, now)
			if err != nil {
				return fmt.Errorf("household: seed notification preference %s: %w", p.Event, err)
			}
		}
		return nil
	})
}
