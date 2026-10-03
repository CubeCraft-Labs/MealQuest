package db_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

const (
	created = "2026-10-03T12:00:00.000Z"
	updated = "2026-10-04T12:00:00.000Z"
)

// openQueries returns queries on the writer of a freshly migrated database
// in a temp directory.
func openQueries(t *testing.T) (*store.Store, *db.Queries) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if err := store.Migrate(ctx, s.Writer, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return s, db.New(s.Writer)
}

func str(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
func num(n int64) sql.NullInt64   { return sql.NullInt64{Int64: n, Valid: true} }

func mustNoRows(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("%s: got %v, want sql.ErrNoRows", what, err)
	}
}

func createPhoto(t *testing.T, q *db.Queries, sha string) db.Photo {
	t.Helper()
	p, err := q.CreatePhoto(context.Background(), db.CreatePhotoParams{
		ID:        store.NewID(),
		SourceUrl: "https://example.com/" + sha + ".jpg",
		LocalPath: "photos/" + sha + ".jpg",
		Width:     800,
		Height:    600,
		Sha256:    sha,
		FetchedAt: created,
		CreatedAt: created,
		UpdatedAt: created,
	})
	if err != nil {
		t.Fatalf("CreatePhoto: %v", err)
	}
	return p
}

func upsertRecipe(t *testing.T, q *db.Queries, id, url, title, at string) db.Recipe {
	t.Helper()
	r, err := q.UpsertRecipe(context.Background(), db.UpsertRecipeParams{
		ID:           id,
		SourceUrl:    url,
		SiteName:     str("Example"),
		Title:        title,
		TotalMinutes: num(30),
		PrepMinutes:  num(10),
		CookMinutes:  num(20),
		Yield:        str("4 servings"),
		RawJsonld:    `{"@type":"Recipe"}`,
		FetchedAt:    at,
		CreatedAt:    at,
		UpdatedAt:    at,
	})
	if err != nil {
		t.Fatalf("UpsertRecipe: %v", err)
	}
	return r
}

func TestDinners(t *testing.T) {
	ctx := context.Background()
	_, q := openQueries(t)
	recipe := upsertRecipe(t, q, store.NewID(), "https://example.com/tacos", "Tacos", created)
	photo := createPhoto(t, q, "aa11")

	create := db.CreateDinnerParams{
		ID:              store.NewID(),
		Name:            "Taco Tuesday",
		NormalizedName:  "taco tuesday",
		Status:          "regular",
		Origin:          "regular",
		Method:          str("stovetop"),
		MinutesTotal:    num(30),
		Difficulty:      str("easy"),
		Cuisine:         str("Mexican"),
		IsFavorite:      0,
		RecipeID:        str(recipe.ID),
		PhotoID:         str(photo.ID),
		GroundingStatus: "grounded",
		UpdatedBy:       str("alex@example.com"),
		CreatedAt:       created,
		UpdatedAt:       created,
	}
	want := db.Dinner(create)
	got, err := q.CreateDinner(ctx, create)
	if err != nil {
		t.Fatalf("CreateDinner: %v", err)
	}
	if got != want {
		t.Fatalf("CreateDinner returned %+v, want %+v", got, want)
	}
	if got, err = q.GetDinner(ctx, create.ID); err != nil || got != want {
		t.Fatalf("GetDinner = %+v, %v; want %+v", got, err, want)
	}
	_, err = q.GetDinner(ctx, "missing")
	mustNoRows(t, "GetDinner missing", err)

	// Second dinner sorts before the first by normalized name, in a
	// different status so the status filter is exercised.
	other, err := q.CreateDinner(ctx, db.CreateDinnerParams{
		ID:              store.NewID(),
		Name:            "Apple Pork Chops",
		NormalizedName:  "apple pork chops",
		Status:          "quest_log",
		Origin:          "side_quest",
		GroundingStatus: "pending",
		CreatedAt:       created,
		UpdatedAt:       created,
	})
	if err != nil {
		t.Fatalf("CreateDinner other: %v", err)
	}
	third, err := q.CreateDinner(ctx, db.CreateDinnerParams{
		ID:              store.NewID(),
		Name:            "Beef Stew",
		NormalizedName:  "beef stew",
		Status:          "regular",
		Origin:          "manual",
		GroundingStatus: "manual",
		CreatedAt:       created,
		UpdatedAt:       created,
	})
	if err != nil {
		t.Fatalf("CreateDinner third: %v", err)
	}
	listIDs := func(status string) []string {
		t.Helper()
		ds, err := q.ListDinnersByStatus(ctx, status)
		if err != nil {
			t.Fatalf("ListDinnersByStatus(%s): %v", status, err)
		}
		ids := []string{}
		for _, d := range ds {
			ids = append(ids, d.ID)
		}
		return ids
	}
	assertIDs(t, "regular", listIDs("regular"), third.ID, create.ID)
	assertIDs(t, "quest_log", listIDs("quest_log"), other.ID)
	assertIDs(t, "tried", listIDs("tried"))

	update := db.UpdateDinnerParams{
		ID:              create.ID,
		Name:            "Fish Tacos",
		NormalizedName:  "fish tacos",
		Status:          "tried",
		Origin:          "side_quest",
		Method:          str("grill"),
		MinutesTotal:    num(25),
		Difficulty:      str("medium"),
		Cuisine:         sql.NullString{},
		RecipeID:        sql.NullString{},
		PhotoID:         str(photo.ID),
		GroundingStatus: "failed",
		UpdatedBy:       str("sam@example.com"),
		UpdatedAt:       updated,
	}
	got, err = q.UpdateDinner(ctx, update)
	if err != nil {
		t.Fatalf("UpdateDinner: %v", err)
	}
	want = db.Dinner{
		ID: create.ID, Name: update.Name, NormalizedName: update.NormalizedName,
		Status: update.Status, Origin: update.Origin, Method: update.Method,
		MinutesTotal: update.MinutesTotal, Difficulty: update.Difficulty,
		Cuisine: update.Cuisine, IsFavorite: 0, RecipeID: update.RecipeID,
		PhotoID: update.PhotoID, GroundingStatus: update.GroundingStatus,
		UpdatedBy: update.UpdatedBy, CreatedAt: created, UpdatedAt: updated,
	}
	if got != want {
		t.Fatalf("UpdateDinner returned %+v, want %+v", got, want)
	}
	if got, err = q.GetDinner(ctx, create.ID); err != nil || got != want {
		t.Fatalf("GetDinner after update = %+v, %v; want %+v", got, err, want)
	}
	update.ID = "missing"
	_, err = q.UpdateDinner(ctx, update)
	mustNoRows(t, "UpdateDinner missing", err)

	got, err = q.SetDinnerFavorite(ctx, db.SetDinnerFavoriteParams{
		ID: create.ID, IsFavorite: 1, UpdatedBy: str("alex@example.com"), UpdatedAt: updated,
	})
	if err != nil {
		t.Fatalf("SetDinnerFavorite: %v", err)
	}
	want.IsFavorite, want.UpdatedBy = 1, str("alex@example.com")
	if got != want {
		t.Fatalf("SetDinnerFavorite returned %+v, want %+v", got, want)
	}
	got, err = q.SetDinnerFavorite(ctx, db.SetDinnerFavoriteParams{
		ID: create.ID, IsFavorite: 0, UpdatedBy: str("sam@example.com"), UpdatedAt: updated,
	})
	if err != nil || got.IsFavorite != 0 || got.UpdatedBy != str("sam@example.com") {
		t.Fatalf("SetDinnerFavorite off = %+v, %v", got, err)
	}
	_, err = q.SetDinnerFavorite(ctx, db.SetDinnerFavoriteParams{ID: "missing", IsFavorite: 1, UpdatedAt: updated})
	mustNoRows(t, "SetDinnerFavorite missing", err)

	const archivedAt = "2026-10-05T12:00:00.000Z"
	got, err = q.ArchiveDinner(ctx, db.ArchiveDinnerParams{
		ID: create.ID, UpdatedBy: str("alex@example.com"), UpdatedAt: archivedAt,
	})
	if err != nil {
		t.Fatalf("ArchiveDinner: %v", err)
	}
	want.IsFavorite, want.Status, want.UpdatedBy, want.UpdatedAt = 0, "archived", str("alex@example.com"), archivedAt
	if got != want {
		t.Fatalf("ArchiveDinner returned %+v, want %+v", got, want)
	}
	assertIDs(t, "archived", listIDs("archived"), create.ID)
	assertIDs(t, "tried after archive", listIDs("tried"))
	_, err = q.ArchiveDinner(ctx, db.ArchiveDinnerParams{ID: "missing", UpdatedAt: archivedAt})
	mustNoRows(t, "ArchiveDinner missing", err)
}

func assertIDs(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got ids %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: got ids %v, want %v", what, got, want)
		}
	}
}

func TestRecipeUpsertWithIngredientsAndSteps(t *testing.T) {
	ctx := context.Background()
	s, q := openQueries(t)
	const url = "https://example.com/chili"

	_, err := q.GetRecipeBySourceURL(ctx, url)
	mustNoRows(t, "GetRecipeBySourceURL before insert", err)

	// saveRecipe is how a caller stores a fetched recipe: upsert by
	// source_url, then replace the children, all in one transaction.
	saveRecipe := func(title, at string, ingredients, steps []string) db.Recipe {
		t.Helper()
		var r db.Recipe
		err := s.WithTx(ctx, func(tx *sql.Tx) error {
			qtx := q.WithTx(tx)
			var err error
			r, err = qtx.UpsertRecipe(ctx, db.UpsertRecipeParams{
				ID: store.NewID(), SourceUrl: url, SiteName: str("Example"), Title: title,
				TotalMinutes: num(60), RawJsonld: `{"name":"` + title + `"}`,
				FetchedAt: at, CreatedAt: at, UpdatedAt: at,
			})
			if err != nil {
				return err
			}
			if err := qtx.DeleteRecipeIngredients(ctx, r.ID); err != nil {
				return err
			}
			if err := qtx.DeleteRecipeSteps(ctx, r.ID); err != nil {
				return err
			}
			for i, text := range ingredients {
				if err := qtx.CreateRecipeIngredient(ctx, db.CreateRecipeIngredientParams{
					ID: store.NewID(), RecipeID: r.ID, Position: int64(i), RawText: text,
					Name: str(text), Qty: str("1"), Unit: str("cup"), Note: sql.NullString{},
					CreatedAt: at, UpdatedAt: at,
				}); err != nil {
					return err
				}
			}
			for i, text := range steps {
				if err := qtx.CreateRecipeStep(ctx, db.CreateRecipeStepParams{
					ID: store.NewID(), RecipeID: r.ID, Position: int64(i), Text: text,
					CreatedAt: at, UpdatedAt: at,
				}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("save recipe %q: %v", title, err)
		}
		return r
	}
	assertRecipe := func(id, title, createdAt, updatedAt string, ingredients, steps []string) {
		t.Helper()
		r, err := q.GetRecipe(ctx, id)
		if err != nil {
			t.Fatalf("GetRecipe: %v", err)
		}
		if r.Title != title || r.SourceUrl != url || r.CreatedAt != createdAt || r.UpdatedAt != updatedAt || r.FetchedAt != updatedAt {
			t.Fatalf("GetRecipe = %+v; want title %q, created %s, updated %s", r, title, createdAt, updatedAt)
		}
		byURL, err := q.GetRecipeBySourceURL(ctx, url)
		if err != nil || byURL != r {
			t.Fatalf("GetRecipeBySourceURL = %+v, %v; want %+v", byURL, err, r)
		}
		ings, err := q.ListRecipeIngredients(ctx, id)
		if err != nil {
			t.Fatalf("ListRecipeIngredients: %v", err)
		}
		gotIngs := []string{}
		for i, ing := range ings {
			if ing.RecipeID != id || ing.Position != int64(i) || ing.Name != str(ing.RawText) {
				t.Fatalf("ingredient %d = %+v", i, ing)
			}
			gotIngs = append(gotIngs, ing.RawText)
		}
		assertIDs(t, "ingredients", gotIngs, ingredients...)
		sts, err := q.ListRecipeSteps(ctx, id)
		if err != nil {
			t.Fatalf("ListRecipeSteps: %v", err)
		}
		gotSteps := []string{}
		for i, st := range sts {
			if st.RecipeID != id || st.Position != int64(i) {
				t.Fatalf("step %d = %+v", i, st)
			}
			gotSteps = append(gotSteps, st.Text)
		}
		assertIDs(t, "steps", gotSteps, steps...)
	}

	first := saveRecipe("Chili", created, []string{"beans", "beef", "tomatoes"}, []string{"Brown the beef.", "Simmer everything."})
	assertRecipe(first.ID, "Chili", created, created, []string{"beans", "beef", "tomatoes"}, []string{"Brown the beef.", "Simmer everything."})

	// Re-fetching the same URL keeps the id and created_at and replaces the
	// fields and children.
	second := saveRecipe("White Chili", updated, []string{"chicken", "white beans"}, []string{"Simmer."})
	if second.ID != first.ID {
		t.Fatalf("upsert changed id: %s -> %s", first.ID, second.ID)
	}
	assertRecipe(first.ID, "White Chili", created, updated, []string{"chicken", "white beans"}, []string{"Simmer."})

	// A different recipe's children are not listed.
	upsertRecipe(t, q, store.NewID(), "https://example.com/other", "Other", created)
	var n int
	if err := s.Reader.QueryRowContext(ctx, "SELECT count(*) FROM recipes").Scan(&n); err != nil || n != 2 {
		t.Fatalf("recipes count = %d, %v; want 2", n, err)
	}
	_, err = q.GetRecipe(ctx, "missing")
	mustNoRows(t, "GetRecipe missing", err)
}

func TestPhotos(t *testing.T) {
	ctx := context.Background()
	_, q := openQueries(t)

	_, err := q.GetPhotoBySHA256(ctx, "bb22")
	mustNoRows(t, "GetPhotoBySHA256 before insert", err)

	first := createPhoto(t, q, "bb22")
	createPhoto(t, q, "cc33")
	if first.Sha256 != "bb22" || first.Width != 800 || first.Height != 600 || first.CreatedAt != created {
		t.Fatalf("CreatePhoto returned %+v", first)
	}
	got, err := q.GetPhotoBySHA256(ctx, "bb22")
	if err != nil || got != first {
		t.Fatalf("GetPhotoBySHA256 = %+v, %v; want %+v", got, err, first)
	}

	// sha256 is not unique in the schema; the earliest photo is returned.
	createPhoto(t, q, "bb22")
	if got, err = q.GetPhotoBySHA256(ctx, "bb22"); err != nil || got != first {
		t.Fatalf("GetPhotoBySHA256 with duplicate = %+v, %v; want %+v", got, err, first)
	}
}

func TestHouseholdSettings(t *testing.T) {
	ctx := context.Background()
	_, q := openQueries(t)
	_, err := q.GetHouseholdSettings(ctx)
	mustNoRows(t, "GetHouseholdSettings before upsert", err)

	p := db.UpsertHouseholdSettingsParams{
		Timezone: "America/New_York", DinnerTime: "17:30", ReminderTime: "16:30",
		ReviewTime: "09:00", PlanDraftDow: 5, PlanDraftTime: "09:00",
		SuggestionsDow: 0, SuggestionsTime: "08:00", ChatAutosendDow: 6,
		ChatAutosendTime: "08:00", CreatedAt: created, UpdatedAt: created,
	}
	want := db.HouseholdSetting{
		ID: 1, Timezone: p.Timezone, DinnerTime: p.DinnerTime, ReminderTime: p.ReminderTime,
		ReviewTime: p.ReviewTime, PlanDraftDow: p.PlanDraftDow, PlanDraftTime: p.PlanDraftTime,
		SuggestionsDow: p.SuggestionsDow, SuggestionsTime: p.SuggestionsTime,
		ChatAutosendDow: p.ChatAutosendDow, ChatAutosendTime: p.ChatAutosendTime,
		CreatedAt: created, UpdatedAt: created,
	}
	assertSettings(t, "insert", q.UpsertHouseholdSettings, q.GetHouseholdSettings, p, want)

	p = db.UpsertHouseholdSettingsParams{
		Timezone: "America/Chicago", DinnerTime: "18:00", ReminderTime: "17:00",
		ReviewTime: "10:00", PlanDraftDow: 4, PlanDraftTime: "20:00",
		SuggestionsDow: 1, SuggestionsTime: "07:00", ChatAutosendDow: 0,
		ChatAutosendTime: "09:30", OnboardingCompletedAt: str(updated),
		UpdatesSeenAt: str(updated), CreatedAt: updated, UpdatedAt: updated,
	}
	want = db.HouseholdSetting{
		ID: 1, Timezone: p.Timezone, DinnerTime: p.DinnerTime, ReminderTime: p.ReminderTime,
		ReviewTime: p.ReviewTime, PlanDraftDow: p.PlanDraftDow, PlanDraftTime: p.PlanDraftTime,
		SuggestionsDow: p.SuggestionsDow, SuggestionsTime: p.SuggestionsTime,
		ChatAutosendDow: p.ChatAutosendDow, ChatAutosendTime: p.ChatAutosendTime,
		OnboardingCompletedAt: p.OnboardingCompletedAt, UpdatesSeenAt: p.UpdatesSeenAt,
		CreatedAt: created, UpdatedAt: updated,
	}
	assertSettings(t, "update", q.UpsertHouseholdSettings, q.GetHouseholdSettings, p, want)
}

func TestAISettings(t *testing.T) {
	ctx := context.Background()
	_, q := openQueries(t)
	_, err := q.GetAISettings(ctx)
	mustNoRows(t, "GetAISettings before upsert", err)

	p := db.UpsertAISettingsParams{
		Provider: "ollama", BaseUrl: "http://ollama:11434", Model: "llama3",
		Temperature: 0.7, CreatedAt: created, UpdatedAt: created,
	}
	want := db.AiSetting{
		ID: 1, Provider: p.Provider, BaseUrl: p.BaseUrl, Model: p.Model,
		Temperature: p.Temperature, CreatedAt: created, UpdatedAt: created,
	}
	assertSettings(t, "insert", q.UpsertAISettings, q.GetAISettings, p, want)

	p = db.UpsertAISettingsParams{
		Provider: "anthropic", BaseUrl: "https://api.anthropic.com", Model: "claude",
		Temperature: 0.2, SystemPrompt: str("Be brief."), ApiKeyCt: []byte{1, 2, 3},
		ApiKeyLast4: str("wxyz"), KeyVerifiedAt: str(updated), CreatedAt: updated, UpdatedAt: updated,
	}
	want = db.AiSetting{
		ID: 1, Provider: p.Provider, BaseUrl: p.BaseUrl, Model: p.Model,
		Temperature: p.Temperature, SystemPrompt: p.SystemPrompt, ApiKeyCt: p.ApiKeyCt,
		ApiKeyLast4: p.ApiKeyLast4, KeyVerifiedAt: p.KeyVerifiedAt,
		CreatedAt: created, UpdatedAt: updated,
	}
	assertSettings(t, "update", q.UpsertAISettings, q.GetAISettings, p, want)
}

func TestRecipeSearchSettings(t *testing.T) {
	ctx := context.Background()
	_, q := openQueries(t)
	_, err := q.GetRecipeSearchSettings(ctx)
	mustNoRows(t, "GetRecipeSearchSettings before upsert", err)

	p := db.UpsertRecipeSearchSettingsParams{
		Provider: "searxng", BaseUrl: str("http://searxng:8080"), CreatedAt: created, UpdatedAt: created,
	}
	want := db.RecipeSearchSetting{
		ID: 1, Provider: p.Provider, BaseUrl: p.BaseUrl, CreatedAt: created, UpdatedAt: created,
	}
	assertSettings(t, "insert", q.UpsertRecipeSearchSettings, q.GetRecipeSearchSettings, p, want)

	p = db.UpsertRecipeSearchSettingsParams{
		Provider: "brave", ApiKeyCt: []byte{9, 8, 7}, CreatedAt: updated, UpdatedAt: updated,
	}
	want = db.RecipeSearchSetting{
		ID: 1, Provider: p.Provider, ApiKeyCt: p.ApiKeyCt, CreatedAt: created, UpdatedAt: updated,
	}
	assertSettings(t, "update", q.UpsertRecipeSearchSettings, q.GetRecipeSearchSettings, p, want)
}

func TestIntegrationsGoogle(t *testing.T) {
	ctx := context.Background()
	_, q := openQueries(t)
	_, err := q.GetIntegrationsGoogle(ctx)
	mustNoRows(t, "GetIntegrationsGoogle before upsert", err)

	p := db.UpsertIntegrationsGoogleParams{
		RefreshTokenCt: []byte{1}, ConnectedAt: created, CreatedAt: created, UpdatedAt: created,
	}
	want := db.IntegrationsGoogle{
		ID: 1, RefreshTokenCt: p.RefreshTokenCt, ConnectedAt: created, CreatedAt: created, UpdatedAt: created,
	}
	assertSettings(t, "insert", q.UpsertIntegrationsGoogle, q.GetIntegrationsGoogle, p, want)

	p = db.UpsertIntegrationsGoogleParams{
		RefreshTokenCt: []byte{2, 3}, CalendarID: str("family@group.calendar.google.com"),
		CalendarName: str("Family"), ConnectedAt: updated, LastSyncError: str("quota exceeded"),
		CreatedAt: updated, UpdatedAt: updated,
	}
	want = db.IntegrationsGoogle{
		ID: 1, RefreshTokenCt: p.RefreshTokenCt, CalendarID: p.CalendarID,
		CalendarName: p.CalendarName, ConnectedAt: updated, LastSyncError: p.LastSyncError,
		CreatedAt: created, UpdatedAt: updated,
	}
	assertSettings(t, "update", q.UpsertIntegrationsGoogle, q.GetIntegrationsGoogle, p, want)
}

func TestChatSettings(t *testing.T) {
	ctx := context.Background()
	_, q := openQueries(t)
	_, err := q.GetChatSettings(ctx)
	mustNoRows(t, "GetChatSettings before upsert", err)

	p := db.UpsertChatSettingsParams{
		WebhookUrlCt: []byte{4, 5}, CreatedAt: created, UpdatedAt: created,
	}
	want := db.ChatSetting{
		ID: 1, WebhookUrlCt: p.WebhookUrlCt, CreatedAt: created, UpdatedAt: created,
	}
	assertSettings(t, "insert", q.UpsertChatSettings, q.GetChatSettings, p, want)

	p = db.UpsertChatSettingsParams{
		WebhookUrlCt: []byte{6}, SpaceLabel: str("Kitchen"), LastTestAt: str(updated),
		LastTestStatus: str("ok"), CreatedAt: updated, UpdatedAt: updated,
	}
	want = db.ChatSetting{
		ID: 1, WebhookUrlCt: p.WebhookUrlCt, SpaceLabel: p.SpaceLabel, LastTestAt: p.LastTestAt,
		LastTestStatus: p.LastTestStatus, CreatedAt: created, UpdatedAt: updated,
	}
	assertSettings(t, "update", q.UpsertChatSettings, q.GetChatSettings, p, want)
}

// assertSettings upserts p and checks that both the returned row and a
// fresh Get equal want. On update, want keeps the original created_at.
func assertSettings[P, R any](
	t *testing.T, what string,
	upsert func(context.Context, P) (R, error), get func(context.Context) (R, error),
	p P, want R,
) {
	t.Helper()
	ctx := context.Background()
	got, err := upsert(ctx, p)
	if err != nil {
		t.Fatalf("%s upsert: %v", what, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s upsert returned %+v, want %+v", what, got, want)
	}
	got, err = get(ctx)
	if err != nil {
		t.Fatalf("%s get: %v", what, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s get = %+v, want %+v", what, got, want)
	}
}
