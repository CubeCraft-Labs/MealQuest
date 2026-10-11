package household

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// Seed values for a new household (CTXD CONTEXT.MD). Days count from
// Sunday = 0, as time.Weekday does.
const (
	SeedDinnerTime       = "17:30"
	SeedReminderTime     = "16:30"
	SeedReviewTime       = "09:00" // rotation check
	SeedPlanDraftDow     = 5       // Friday
	SeedPlanDraftTime    = "09:00"
	SeedSuggestionsDow   = 0 // Sunday
	SeedSuggestionsTime  = "08:00"
	SeedChatAutosendDow  = 6 // Saturday
	SeedChatAutosendTime = "08:00"
)

// ErrNoDefaultZone is returned by SeedSettings when the household settings
// do not exist yet and no default zone was configured to create them with.
var ErrNoDefaultZone = errors.New("household settings do not exist yet and no default time zone is configured")

// ErrInvalidDefaultZone is returned by SeedSettings when the household
// settings do not exist yet and the configured default zone cannot be loaded.
var ErrInvalidDefaultZone = errors.New("household settings do not exist yet and the default time zone is invalid")

// Settings are the shared household settings. Times are household-local
// 24-hour HH:MM in Timezone; days of the week count from Sunday = 0.
type Settings struct {
	Timezone              string  `json:"timezone"`
	DinnerTime            string  `json:"dinner_time"`
	ReminderTime          string  `json:"reminder_time"`
	ReviewTime            string  `json:"review_time"`
	PlanDraftDow          int     `json:"plan_draft_dow"`
	PlanDraftTime         string  `json:"plan_draft_time"`
	SuggestionsDow        int     `json:"suggestions_dow"`
	SuggestionsTime       string  `json:"suggestions_time"`
	ChatAutosendDow       int     `json:"chat_autosend_dow"`
	ChatAutosendTime      string  `json:"chat_autosend_time"`
	OnboardingCompletedAt *string `json:"onboarding_completed_at"`
	UpdatesSeenAt         *string `json:"updates_seen_at"`
	UpdatedBy             *string `json:"updated_by"`
	CreatedAt             string  `json:"created_at"`
	UpdatedAt             string  `json:"updated_at"`
}

// SettingsPatch changes the household settings. Nil fields are left as they
// are.
type SettingsPatch struct {
	Timezone         *string
	DinnerTime       *string
	ReminderTime     *string
	ReviewTime       *string
	PlanDraftDow     *int
	PlanDraftTime    *string
	SuggestionsDow   *int
	SuggestionsTime  *string
	ChatAutosendDow  *int
	ChatAutosendTime *string
	// OnboardingCompleted, when true, records that the household finished
	// onboarding: onboarding_completed_at becomes the time of the update,
	// unless it is already set, when the first time is kept. Onboarding is
	// never undone, so false is invalid, and it cannot first be completed
	// with fewer than MinRegulars Regulars.
	OnboardingCompleted *bool
}

// MinRegulars is how many Regulars the household must have to complete
// onboarding.
const MinRegulars = 3

// Problem is one invalid field of a SettingsPatch, named by its JSON key.
type Problem struct {
	Field  string
	Reason string
}

// ValidationError lists every invalid field of a SettingsPatch.
type ValidationError struct {
	Problems []Problem
}

func (e *ValidationError) Error() string {
	parts := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		parts[i] = p.Field + ": " + p.Reason
	}
	return "invalid household settings: " + strings.Join(parts, "; ")
}

// SeedSettings creates the household settings row with zone defaultTZ and the
// seed times and days if it does not exist. An existing row is never changed,
// and defaultTZ is then ignored, valid or not. With no row, an empty
// defaultTZ is ErrNoDefaultZone and an unloadable one ErrInvalidDefaultZone.
func SeedSettings(ctx context.Context, st *store.Store, defaultTZ string) error {
	return st.WithTx(ctx, func(tx *sql.Tx) error {
		q := db.New(tx)
		_, err := q.GetHouseholdSettings(ctx)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("household: read settings: %w", err)
		}
		if defaultTZ == "" {
			return ErrNoDefaultZone
		}
		if _, err := LoadZone(defaultTZ); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidDefaultZone, err)
		}
		now := store.Now().String()
		_, err = q.UpsertHouseholdSettings(ctx, db.UpsertHouseholdSettingsParams{
			Timezone:         defaultTZ,
			DinnerTime:       SeedDinnerTime,
			ReminderTime:     SeedReminderTime,
			ReviewTime:       SeedReviewTime,
			PlanDraftDow:     SeedPlanDraftDow,
			PlanDraftTime:    SeedPlanDraftTime,
			SuggestionsDow:   SeedSuggestionsDow,
			SuggestionsTime:  SeedSuggestionsTime,
			ChatAutosendDow:  SeedChatAutosendDow,
			ChatAutosendTime: SeedChatAutosendTime,
			CreatedAt:        now,
			UpdatedAt:        now,
		})
		if err != nil {
			return fmt.Errorf("household: seed settings: %w", err)
		}
		return nil
	})
}

// SettingsService reads and changes the household settings, and tells
// in-process subscribers (the scheduler) about every change.
type SettingsService struct {
	st *store.Store

	// updateMu serialises Update with its publish, so subscribers see
	// changes in commit order.
	updateMu sync.Mutex

	subsMu sync.Mutex
	subs   map[int]func(Settings)
	nextID int
}

// NewSettingsService returns a service over st. Call SeedSettings first.
func NewSettingsService(st *store.Store) *SettingsService {
	return &SettingsService{st: st, subs: make(map[int]func(Settings))}
}

// Get returns the household settings.
func (s *SettingsService) Get(ctx context.Context) (Settings, error) {
	row, err := db.New(s.st.Reader).GetHouseholdSettings(ctx)
	if err != nil {
		return Settings{}, fmt.Errorf("household: read settings: %w", err)
	}
	return fromRow(row), nil
}

// Update validates patch, applies it, records updatedBy (the signed-in
// member's subject, which must not be empty) and publishes the new settings
// to every subscriber. An invalid patch is a *ValidationError and changes
// nothing, as does completing onboarding with fewer than MinRegulars
// Regulars.
func (s *SettingsService) Update(ctx context.Context, patch SettingsPatch, updatedBy string) (Settings, error) {
	if updatedBy == "" {
		return Settings{}, errors.New("household: update settings: no member to record as updated_by")
	}
	if err := patch.validate(); err != nil {
		return Settings{}, err
	}

	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	var row db.HouseholdSetting
	err := s.st.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		row, err = applyPatch(ctx, db.New(tx), patch, updatedBy)
		return err
	})
	if err != nil {
		return Settings{}, err
	}

	settings := fromRow(row)
	s.publish(settings)
	return settings, nil
}

// MarkUpdatesSeen records that the household has seen its Recent updates
// through seenAt: updates_seen_at moves forward to seenAt, and is left as it
// is when it is already at or after it. It is not a settings edit, so
// updated_by and updated_at are kept and subscribers are not told.
func (s *SettingsService) MarkUpdatesSeen(ctx context.Context, seenAt time.Time) error {
	if seenAt.IsZero() {
		return errors.New("household: mark updates seen: zero time")
	}
	_, err := db.New(s.st.Writer).MarkUpdatesSeen(ctx, store.Timestamp{Time: seenAt.UTC().Truncate(time.Millisecond)}.String())
	if err != nil {
		return fmt.Errorf("household: mark updates seen: %w", err)
	}
	return nil
}

// applyPatch applies a validated patch to the household settings row in q's
// transaction, recording updatedBy, and returns the new row.
func applyPatch(ctx context.Context, q *db.Queries, patch SettingsPatch, updatedBy string) (db.HouseholdSetting, error) {
	cur, err := q.GetHouseholdSettings(ctx)
	if err != nil {
		return db.HouseholdSetting{}, fmt.Errorf("household: read settings: %w", err)
	}
	p := db.UpsertHouseholdSettingsParams{
		Timezone:              cur.Timezone,
		DinnerTime:            cur.DinnerTime,
		ReminderTime:          cur.ReminderTime,
		ReviewTime:            cur.ReviewTime,
		PlanDraftDow:          cur.PlanDraftDow,
		PlanDraftTime:         cur.PlanDraftTime,
		SuggestionsDow:        cur.SuggestionsDow,
		SuggestionsTime:       cur.SuggestionsTime,
		ChatAutosendDow:       cur.ChatAutosendDow,
		ChatAutosendTime:      cur.ChatAutosendTime,
		OnboardingCompletedAt: cur.OnboardingCompletedAt,
		UpdatesSeenAt:         cur.UpdatesSeenAt,
		UpdatedBy:             sql.NullString{String: updatedBy, Valid: true},
		CreatedAt:             cur.CreatedAt,
		UpdatedAt:             store.Now().String(),
	}
	setString(&p.Timezone, patch.Timezone)
	setString(&p.DinnerTime, patch.DinnerTime)
	setString(&p.ReminderTime, patch.ReminderTime)
	setString(&p.ReviewTime, patch.ReviewTime)
	setDow(&p.PlanDraftDow, patch.PlanDraftDow)
	setString(&p.PlanDraftTime, patch.PlanDraftTime)
	setDow(&p.SuggestionsDow, patch.SuggestionsDow)
	setString(&p.SuggestionsTime, patch.SuggestionsTime)
	setDow(&p.ChatAutosendDow, patch.ChatAutosendDow)
	setString(&p.ChatAutosendTime, patch.ChatAutosendTime)
	if patch.OnboardingCompleted != nil && !p.OnboardingCompletedAt.Valid {
		n, err := q.CountRegularDinners(ctx)
		if err != nil {
			return db.HouseholdSetting{}, fmt.Errorf("household: count Regulars: %w", err)
		}
		if n < MinRegulars {
			return db.HouseholdSetting{}, &ValidationError{Problems: []Problem{{
				Field:  "onboarding_completed",
				Reason: fmt.Sprintf("add at least %d Regulars first (have %d)", MinRegulars, n),
			}}}
		}
		p.OnboardingCompletedAt = sql.NullString{String: p.UpdatedAt, Valid: true}
	}
	row, err := q.UpsertHouseholdSettings(ctx, p)
	if err != nil {
		return db.HouseholdSetting{}, fmt.Errorf("household: update settings: %w", err)
	}
	return row, nil
}

// Subscribe registers fn to receive the new settings after every successful
// Update and UpdateNotifications. fn runs synchronously on the updating goroutine, so it must return
// promptly and must not call Update. The returned function unsubscribes fn.
func (s *SettingsService) Subscribe(fn func(Settings)) (unsubscribe func()) {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	id := s.nextID
	s.nextID++
	s.subs[id] = fn
	return func() {
		s.subsMu.Lock()
		defer s.subsMu.Unlock()
		delete(s.subs, id)
	}
}

func (s *SettingsService) publish(settings Settings) {
	s.subsMu.Lock()
	fns := make([]func(Settings), 0, len(s.subs))
	for _, fn := range s.subs {
		fns = append(fns, fn)
	}
	s.subsMu.Unlock()
	for _, fn := range fns {
		fn(settings)
	}
}

func (p SettingsPatch) validate() error {
	var problems []Problem
	fail := func(field, reason string) {
		problems = append(problems, Problem{Field: field, Reason: reason})
	}
	if p == (SettingsPatch{}) {
		fail("settings", "no settings to change")
	}
	if p.Timezone != nil {
		if _, err := LoadZone(*p.Timezone); err != nil {
			fail("timezone", err.Error())
		}
	}
	for _, t := range []struct {
		field string
		value *string
	}{
		{"dinner_time", p.DinnerTime},
		{"reminder_time", p.ReminderTime},
		{"review_time", p.ReviewTime},
		{"plan_draft_time", p.PlanDraftTime},
		{"suggestions_time", p.SuggestionsTime},
		{"chat_autosend_time", p.ChatAutosendTime},
	} {
		if t.value != nil && !clockTime.MatchString(*t.value) {
			fail(t.field, fmt.Sprintf("invalid time %q (want 24-hour HH:MM, 00:00-23:59)", *t.value))
		}
	}
	for _, d := range []struct {
		field string
		value *int
	}{
		{"plan_draft_dow", p.PlanDraftDow},
		{"suggestions_dow", p.SuggestionsDow},
		{"chat_autosend_dow", p.ChatAutosendDow},
	} {
		if d.value != nil && (*d.value < 0 || *d.value > 6) {
			fail(d.field, fmt.Sprintf("invalid day of the week %d (want 0-6, Sunday = 0)", *d.value))
		}
	}
	if p.OnboardingCompleted != nil && !*p.OnboardingCompleted {
		fail("onboarding_completed", "onboarding cannot be undone (want true)")
	}
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// clockTime matches a zero-padded 24-hour HH:MM time.
var clockTime = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func setString(dst *string, v *string) {
	if v != nil {
		*dst = *v
	}
}

func setDow(dst *int64, v *int) {
	if v != nil {
		*dst = int64(*v)
	}
}

func fromRow(r db.HouseholdSetting) Settings {
	return Settings{
		Timezone:              r.Timezone,
		DinnerTime:            r.DinnerTime,
		ReminderTime:          r.ReminderTime,
		ReviewTime:            r.ReviewTime,
		PlanDraftDow:          int(r.PlanDraftDow),
		PlanDraftTime:         r.PlanDraftTime,
		SuggestionsDow:        int(r.SuggestionsDow),
		SuggestionsTime:       r.SuggestionsTime,
		ChatAutosendDow:       int(r.ChatAutosendDow),
		ChatAutosendTime:      r.ChatAutosendTime,
		OnboardingCompletedAt: nullable(r.OnboardingCompletedAt),
		UpdatesSeenAt:         nullable(r.UpdatesSeenAt),
		UpdatedBy:             nullable(r.UpdatedBy),
		CreatedAt:             r.CreatedAt,
		UpdatedAt:             r.UpdatedAt,
	}
}

func nullable(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}
