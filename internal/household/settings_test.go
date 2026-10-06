package household

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// migratedStore opens a temp store in dir migrated with the embedded
// migrations, closed when the test ends.
func migratedStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Migrate(ctx, st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return st
}

// seededService returns a settings service over a migrated temp database
// seeded with zone America/New_York.
func seededService(t *testing.T) *SettingsService {
	t.Helper()
	st := migratedStore(t, t.TempDir())
	if err := SeedSettings(context.Background(), st, "America/New_York"); err != nil {
		t.Fatal(err)
	}
	return NewSettingsService(st)
}

func ptr[T any](v T) *T { return &v }

func TestSeedSettingsCreatesRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := migratedStore(t, t.TempDir())

	if err := SeedSettings(ctx, st, "America/Chicago"); err != nil {
		t.Fatal(err)
	}
	got, err := NewSettingsService(st).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{
		Timezone:         "America/Chicago",
		DinnerTime:       "17:30",
		ReminderTime:     "16:30",
		ReviewTime:       "09:00",
		PlanDraftDow:     5,
		PlanDraftTime:    "09:00",
		SuggestionsDow:   0,
		SuggestionsTime:  "08:00",
		ChatAutosendDow:  6,
		ChatAutosendTime: "08:00",
		CreatedAt:        got.CreatedAt,
		UpdatedAt:        got.UpdatedAt,
	}
	if !equalSettings(got, want) {
		t.Errorf("seeded settings = %+v\nwant %+v", got, want)
	}
	var created store.Timestamp
	if err := created.Scan(got.CreatedAt); err != nil {
		t.Errorf("created_at: %v", err)
	}
	if got.UpdatedAt != got.CreatedAt {
		t.Errorf("updated_at = %q, want created_at %q", got.UpdatedAt, got.CreatedAt)
	}
}

func TestSeedSettingsNeverOverwrites(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	// First boot seeds; the household then changes a setting.
	first := migratedStore(t, dir)
	if err := SeedSettings(ctx, first, "America/Chicago"); err != nil {
		t.Fatal(err)
	}
	changed, err := NewSettingsService(first).Update(ctx, SettingsPatch{DinnerTime: ptr("18:15")}, "sam")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// Later boots reopen the same database with a different default zone,
	// an invalid one and none at all.
	for _, defaultTZ := range []string{"Europe/London", "Mars/Olympus_Mons", ""} {
		st := migratedStore(t, dir)
		if err := SeedSettings(ctx, st, defaultTZ); err != nil {
			t.Fatalf("reboot with default zone %q: %v", defaultTZ, err)
		}
		got, err := NewSettingsService(st).Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !equalSettings(got, changed) {
			t.Errorf("reboot with default zone %q changed settings:\n got %+v\nwant %+v", defaultTZ, got, changed)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSeedSettingsRequiresDefaultZone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name      string
		defaultTZ string
		is        error
	}{
		{"unset", "", ErrNoDefaultZone},
		{"not an IANA zone", "Mars/Olympus_Mons", ErrInvalidDefaultZone},
		{"Local", "Local", ErrInvalidDefaultZone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := migratedStore(t, t.TempDir())
			err := SeedSettings(ctx, st, tt.defaultTZ)
			if err == nil {
				t.Fatal("SeedSettings succeeded")
			}
			if !errors.Is(err, tt.is) {
				t.Errorf("error = %v, want %v", err, tt.is)
			}
			if _, err := NewSettingsService(st).Get(ctx); err == nil {
				t.Error("a settings row was created despite the error")
			}
		})
	}
}

func TestUpdateAppliesPatchAndRecordsUpdatedBy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := seededService(t)
	before, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Update(ctx, SettingsPatch{
		Timezone:         ptr("Europe/Paris"),
		DinnerTime:       ptr("19:00"),
		ReminderTime:     ptr("00:00"),
		ReviewTime:       ptr("23:59"),
		PlanDraftDow:     ptr(4),
		PlanDraftTime:    ptr("10:30"),
		SuggestionsDow:   ptr(1),
		SuggestionsTime:  ptr("07:45"),
		ChatAutosendDow:  ptr(0),
		ChatAutosendTime: ptr("12:00"),
	}, "sam")
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{
		Timezone:         "Europe/Paris",
		DinnerTime:       "19:00",
		ReminderTime:     "00:00",
		ReviewTime:       "23:59",
		PlanDraftDow:     4,
		PlanDraftTime:    "10:30",
		SuggestionsDow:   1,
		SuggestionsTime:  "07:45",
		ChatAutosendDow:  0,
		ChatAutosendTime: "12:00",
		UpdatedBy:        ptr("sam"),
		CreatedAt:        before.CreatedAt,
		UpdatedAt:        got.UpdatedAt,
	}
	if !equalSettings(got, want) {
		t.Errorf("Update = %+v\nwant %+v", got, want)
	}
	if got.UpdatedAt < before.UpdatedAt {
		t.Errorf("updated_at went back: %s < %s", got.UpdatedAt, before.UpdatedAt)
	}
	stored, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !equalSettings(stored, got) {
		t.Errorf("stored = %+v\nwant %+v", stored, got)
	}

	// A partial patch changes only its fields and records its own member.
	got, err = svc.Update(ctx, SettingsPatch{DinnerTime: ptr("18:00")}, "alex")
	if err != nil {
		t.Fatal(err)
	}
	want.DinnerTime = "18:00"
	want.UpdatedBy = ptr("alex")
	want.UpdatedAt = got.UpdatedAt
	if !equalSettings(got, want) {
		t.Errorf("partial Update = %+v\nwant %+v", got, want)
	}
}

func TestUpdateCompletesOnboardingOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := seededService(t)

	got, err := svc.Update(ctx, SettingsPatch{OnboardingCompleted: ptr(true)}, "sam")
	if err != nil {
		t.Fatal(err)
	}
	if got.OnboardingCompletedAt == nil || *got.OnboardingCompletedAt != got.UpdatedAt {
		t.Fatalf("onboarding_completed_at = %v, want the update time %s", got.OnboardingCompletedAt, got.UpdatedAt)
	}
	first := *got.OnboardingCompletedAt

	// Completing again keeps the first time.
	got, err = svc.Update(ctx, SettingsPatch{OnboardingCompleted: ptr(true)}, "alex")
	if err != nil {
		t.Fatal(err)
	}
	if got.OnboardingCompletedAt == nil || *got.OnboardingCompletedAt != first {
		t.Errorf("second completion: onboarding_completed_at = %v, want %s", got.OnboardingCompletedAt, first)
	}

	// Other changes leave it alone.
	got, err = svc.Update(ctx, SettingsPatch{DinnerTime: ptr("18:00")}, "sam")
	if err != nil {
		t.Fatal(err)
	}
	if got.OnboardingCompletedAt == nil || *got.OnboardingCompletedAt != first {
		t.Errorf("after another change: onboarding_completed_at = %v, want %s", got.OnboardingCompletedAt, first)
	}
}

func TestUpdateRequiresMember(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := seededService(t)
	notified := 0
	svc.Subscribe(func(Settings) { notified++ })

	if _, err := svc.Update(ctx, SettingsPatch{DinnerTime: ptr("18:00")}, ""); err == nil {
		t.Fatal("Update with no member succeeded")
	}
	got, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.DinnerTime != SeedDinnerTime || got.UpdatedBy != nil || notified != 0 {
		t.Errorf("Update with no member changed settings or notified: %+v, notified %d", got, notified)
	}
}

func TestUpdateValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name   string
		patch  SettingsPatch
		fields []string
	}{
		{"empty patch", SettingsPatch{}, []string{"settings"}},
		{"unknown zone", SettingsPatch{Timezone: ptr("Mars/Olympus_Mons")}, []string{"timezone"}},
		{"empty zone", SettingsPatch{Timezone: ptr("")}, []string{"timezone"}},
		{"Local zone", SettingsPatch{Timezone: ptr("Local")}, []string{"timezone"}},
		{"zone path traversal", SettingsPatch{Timezone: ptr("../../etc/passwd")}, []string{"timezone"}},
		{"hour 24", SettingsPatch{DinnerTime: ptr("24:00")}, []string{"dinner_time"}},
		{"minute 60", SettingsPatch{ReminderTime: ptr("16:60")}, []string{"reminder_time"}},
		{"unpadded hour", SettingsPatch{ReviewTime: ptr("9:00")}, []string{"review_time"}},
		{"seconds", SettingsPatch{PlanDraftTime: ptr("09:00:00")}, []string{"plan_draft_time"}},
		{"12-hour", SettingsPatch{SuggestionsTime: ptr("08:00 AM")}, []string{"suggestions_time"}},
		{"empty time", SettingsPatch{ChatAutosendTime: ptr("")}, []string{"chat_autosend_time"}},
		{"dow 7", SettingsPatch{PlanDraftDow: ptr(7)}, []string{"plan_draft_dow"}},
		{"dow -1", SettingsPatch{SuggestionsDow: ptr(-1)}, []string{"suggestions_dow"}},
		{"dow 100", SettingsPatch{ChatAutosendDow: ptr(100)}, []string{"chat_autosend_dow"}},
		{"onboarding undone", SettingsPatch{OnboardingCompleted: ptr(false)}, []string{"onboarding_completed"}},
		{
			"every problem is reported",
			SettingsPatch{Timezone: ptr("Nowhere"), DinnerTime: ptr("5pm"), PlanDraftDow: ptr(9)},
			[]string{"timezone", "dinner_time", "plan_draft_dow"},
		},
		{
			"valid fields do not mask an invalid one",
			SettingsPatch{DinnerTime: ptr("18:00"), ChatAutosendDow: ptr(8)},
			[]string{"chat_autosend_dow"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc := seededService(t)
			before, err := svc.Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			published := 0
			svc.Subscribe(func(Settings) { published++ })

			_, err = svc.Update(ctx, tt.patch, "sam")
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Update error = %v, want *ValidationError", err)
			}
			var fields []string
			for _, p := range verr.Problems {
				fields = append(fields, p.Field)
			}
			if !slices.Equal(fields, tt.fields) {
				t.Errorf("problem fields = %v, want %v", fields, tt.fields)
			}

			after, err := svc.Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !equalSettings(after, before) {
				t.Errorf("invalid patch changed settings:\n got %+v\nwant %+v", after, before)
			}
			if published != 0 {
				t.Errorf("invalid patch published %d times", published)
			}
		})
	}
}

func TestUpdatePublishesToSubscribers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc := seededService(t)

	var mu sync.Mutex
	var first, second []Settings
	unsubscribe := svc.Subscribe(func(s Settings) {
		mu.Lock()
		defer mu.Unlock()
		first = append(first, s)
	})
	svc.Subscribe(func(s Settings) {
		mu.Lock()
		defer mu.Unlock()
		second = append(second, s)
	})

	updated, err := svc.Update(ctx, SettingsPatch{Timezone: ptr("Asia/Tokyo")}, "sam")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(first) != 1 || !equalSettings(first[0], updated) {
		t.Errorf("first subscriber got %+v, want [%+v]", first, updated)
	}
	if len(second) != 1 || !equalSettings(second[0], updated) {
		t.Errorf("second subscriber got %+v, want [%+v]", second, updated)
	}
	mu.Unlock()

	unsubscribe()
	updated, err = svc.Update(ctx, SettingsPatch{DinnerTime: ptr("18:45")}, "sam")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(first) != 1 {
		t.Errorf("unsubscribed subscriber got %d notifications, want 1", len(first))
	}
	if len(second) != 2 || !equalSettings(second[1], updated) {
		t.Errorf("second subscriber got %+v, want a second notification %+v", second, updated)
	}
}

func TestUpdateWithoutSeedFails(t *testing.T) {
	t.Parallel()
	svc := NewSettingsService(migratedStore(t, t.TempDir()))
	if _, err := svc.Update(context.Background(), SettingsPatch{DinnerTime: ptr("18:00")}, ""); err == nil {
		t.Fatal("Update succeeded with no settings row")
	}
}

// equalSettings compares settings by value, following the nullable pointers.
func equalSettings(a, b Settings) bool {
	eq := func(x, y *string) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	if !eq(a.OnboardingCompletedAt, b.OnboardingCompletedAt) || !eq(a.UpdatesSeenAt, b.UpdatesSeenAt) || !eq(a.UpdatedBy, b.UpdatedBy) {
		return false
	}
	a.OnboardingCompletedAt, a.UpdatesSeenAt, a.UpdatedBy = nil, nil, nil
	b.OnboardingCompletedAt, b.UpdatesSeenAt, b.UpdatedBy = nil, nil, nil
	return a == b
}
