package harness

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/migrations"
)

// contextNow is 2026-10-05 01:30 UTC: still Sunday 2026-10-04 in Los
// Angeles, the seeded household's zone, but Monday in UTC.
var contextNow = time.Date(2026, 10, 5, 1, 30, 0, 0, time.UTC)

// ample is a budget nothing in these tests reaches.
const ample = 1 << 20

// emptyHousehold returns a migrated temp store whose household has
// settings in America/Los_Angeles and nothing else.
func emptyHousehold(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
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
	if err := household.SeedSettings(ctx, st, "America/Los_Angeles"); err != nil {
		t.Fatal(err)
	}
	return st
}

// seeder inserts household rows with fixed ids and timestamps.
type seeder struct {
	t  *testing.T
	st *store.Store
	n  int
}

func (s *seeder) exec(query string, args ...any) {
	s.t.Helper()
	if _, err := s.st.Writer.Exec(query, args...); err != nil {
		s.t.Fatalf("%s: %v", query, err)
	}
}

func (s *seeder) id(prefix string) string {
	s.n++
	return fmt.Sprintf("%s%04d", prefix, s.n)
}

// dinner inserts a dinner and returns its id. Empty cuisine and method and
// zero minutes are stored as NULL.
func (s *seeder) dinner(name, status, cuisine, method string, minutes int, favorite bool) string {
	s.t.Helper()
	id := s.id("dinner")
	s.exec(`INSERT INTO dinners (id, name, normalized_name, status, origin, method, minutes_total,
		cuisine, is_favorite, grounding_status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'manual', NULLIF(?, ''), NULLIF(?, 0), NULLIF(?, ''), ?, 'manual',
		'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		id, name, strings.ToLower(name), status, method, minutes, cuisine, favorite)
	return id
}

// pass inserts a passed (declined) Side quest decided at decidedAt.
func (s *seeder) pass(name, decidedAt string) {
	s.t.Helper()
	batch := s.id("batch")
	s.exec(`INSERT INTO suggestion_batches (id, trigger, status, created_at, updated_at)
		VALUES (?, 'manual', 'done', ?, ?)`, batch, decidedAt, decidedAt)
	s.exec(`INSERT INTO suggestions (id, dinner_id, batch_id, reason, cited_regular_ids, status,
		decided_at, created_at, updated_at) VALUES (?, ?, ?, 'why', '[]', 'declined', ?, ?, ?)`,
		s.id("suggestion"), s.dinner(name, "archived", "", "", 0, false), batch, decidedAt, decidedAt, decidedAt)
}

// plan inserts the week plan starting weekStart with one dinner per date;
// an empty name leaves that day without a dinner.
func (s *seeder) plan(weekStart string, days map[string]string) {
	s.t.Helper()
	planID := s.id("plan")
	s.exec(`INSERT INTO week_plans (id, week_start, status, created_at, updated_at)
		VALUES (?, ?, 'draft', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`, planID, weekStart)
	for date, name := range days {
		var dinnerID any
		if name != "" {
			dinnerID = s.dinner(name, "tried", "", "", 0, false)
		}
		s.exec(`INSERT INTO plan_days (id, plan_id, date, dinner_id, is_new, created_at, updated_at)
			VALUES (?, ?, ?, ?, 0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			s.id("day"), planID, date, dinnerID)
	}
}

// seededHousehold returns a household with Regulars, a Quest log, three
// passes and plans in and around the four plan weeks, which for Sunday
// 2026-10-04 are the weeks of Monday 2026-09-07 to Sunday 2026-10-04.
func seededHousehold(t *testing.T) *store.Store {
	t.Helper()
	st := emptyHousehold(t)
	s := &seeder{t: t, st: st}
	s.dinner("Tacos", "regular", "Mexican", "stovetop", 30, true)
	s.dinner("Chili", "regular", "Tex-Mex", "crockpot", 240, false)
	s.dinner("Baked ziti", "regular", "", "oven", 0, false)
	s.dinner("Mystery stew", "regular", "", "", 0, false)
	s.dinner("Pho", "quest_log", "Vietnamese", "stovetop", 60, false)
	s.dinner("Bibimbap", "quest_log", "", "", 0, false)
	s.dinner("Old pie", "archived", "", "", 0, false)
	s.pass("Liver and onions", "2026-09-01T12:00:00Z")
	s.pass("Tripe soup", "2026-09-20T12:00:00Z")
	s.pass("Natto bowl", "2026-09-10T12:00:00Z")
	s.plan("2026-08-31", map[string]string{"2026-09-06": "Too old"})
	s.plan("2026-09-14", map[string]string{"2026-09-14": "Lasagna", "2026-09-16": "Curry"})
	s.plan("2026-09-28", map[string]string{"2026-09-28": "Pizza", "2026-09-29": "", "2026-10-04": "Ramen"})
	s.plan("2026-10-05", map[string]string{"2026-10-05": "Next week"})
	return st
}

const seededBlock = `## Household context
### Today
- Sunday 2026-10-04 (America/Los_Angeles)

### Regulars
- Baked ziti (oven)
- Chili (Tex-Mex, crockpot, 240 min)
- Mystery stew
- Tacos (Mexican, stovetop, 30 min, favorite)

### Quest log
- Bibimbap
- Pho

### Passed, oldest first
- Liver and onions
- Natto bowl
- Tripe soup

### Recent plans
- 2026-09-14 Mon: Lasagna
- 2026-09-16 Wed: Curry
- 2026-09-28 Mon: Pizza
- 2026-10-04 Sun: Ramen`

func TestBuildContextSeeded(t *testing.T) {
	t.Parallel()
	h, err := BuildContext(context.Background(), seededHousehold(t), contextNow, ample)
	if err != nil {
		t.Fatal(err)
	}
	block, err := h.Block()
	if err != nil {
		t.Fatal(err)
	}
	if block != seededBlock {
		t.Fatalf("block:\n%s\n--- want ---\n%s", block, seededBlock)
	}
	want := RegularDinner{Name: "Tacos", Cuisine: "Mexican", Method: "stovetop", Minutes: 30, Favorite: true}
	if got := h.Regulars[3]; got != want {
		t.Fatalf("Regulars[3] = %+v, want %+v", got, want)
	}
}

func TestBuildContextUsesHouseholdZone(t *testing.T) {
	t.Parallel()
	st := emptyHousehold(t)
	ctx := context.Background()
	for now, want := range map[time.Time]string{
		contextNow: "Sunday 2026-10-04 (America/Los_Angeles)",
		// Midnight in Los Angeles (UTC-7 in October) is 07:00 UTC.
		time.Date(2026, 10, 5, 6, 59, 59, 0, time.UTC): "Sunday 2026-10-04 (America/Los_Angeles)",
		time.Date(2026, 10, 5, 7, 0, 0, 0, time.UTC):   "Monday 2026-10-05 (America/Los_Angeles)",
	} {
		h, err := BuildContext(ctx, st, now, ample)
		if err != nil {
			t.Fatal(err)
		}
		if got := h.Sections()[0].Lines[0]; got != want {
			t.Errorf("at %s, today = %q, want %q", now, got, want)
		}
	}
}

func TestBuildContextKeepsLastPasses(t *testing.T) {
	t.Parallel()
	st := emptyHousehold(t)
	s := &seeder{t: t, st: st}
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var all []string
	for i := range PassedLimit + 2 {
		name := fmt.Sprintf("Pass %02d", i)
		all = append(all, name)
		s.pass(name, start.AddDate(0, 0, i).Format(time.RFC3339))
	}
	h, err := BuildContext(context.Background(), st, contextNow, ample)
	if err != nil {
		t.Fatal(err)
	}
	if want := all[2:]; !slices.Equal(h.Passed, want) {
		t.Fatalf("Passed = %q, want the last %d oldest first: %q", h.Passed, PassedLimit, want)
	}
}

func TestBuildContextTrimsOldestPlansThenOldestPasses(t *testing.T) {
	t.Parallel()
	st := seededHousehold(t)
	ctx := context.Background()
	full, err := BuildContext(ctx, st, contextNow, ample)
	if err != nil {
		t.Fatal(err)
	}
	fullSize := utf8.RuneCountInString(seededBlock)
	// lineSize is the size of the block line for item.
	lineSize := func(item string) int { return utf8.RuneCountInString("\n- " + item) }
	// A section left empty renders "(none)".
	none := utf8.RuneCountInString("\n(none)")
	floorBlock, err := HouseholdContext{Today: full.Today, Timezone: full.Timezone,
		Regulars: full.Regulars, QuestLog: full.QuestLog}.Block()
	if err != nil {
		t.Fatal(err)
	}
	floor := utf8.RuneCountInString(floorBlock)

	for _, tc := range []struct {
		name           string
		budget         int
		wantPlansStart int
		wantPassStart  int
	}{
		{name: "fits exactly", budget: fullSize, wantPlansStart: 0, wantPassStart: 0},
		{name: "one character over", budget: fullSize - 1, wantPlansStart: 1, wantPassStart: 0},
		{
			name:           "all but newest plan",
			budget:         fullSize - lineSize("2026-09-14 Mon: Lasagna") - lineSize("2026-09-16 Wed: Curry") - lineSize("2026-09-28 Mon: Pizza"),
			wantPlansStart: 3, wantPassStart: 0,
		},
		{
			name: "no plans, oldest pass",
			budget: fullSize - lineSize("2026-09-14 Mon: Lasagna") - lineSize("2026-09-16 Wed: Curry") -
				lineSize("2026-09-28 Mon: Pizza") - lineSize("2026-10-04 Sun: Ramen") + none - 1,
			wantPlansStart: 4, wantPassStart: 1,
		},
		{name: "only what is never trimmed", budget: floor, wantPlansStart: 4, wantPassStart: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, err := BuildContext(ctx, st, contextNow, tc.budget)
			if err != nil {
				t.Fatal(err)
			}
			block, err := h.Block()
			if err != nil {
				t.Fatal(err)
			}
			if size := utf8.RuneCountInString(block); size > tc.budget {
				t.Fatalf("block is %d characters, over the budget of %d", size, tc.budget)
			}
			if want := full.Plans[tc.wantPlansStart:]; !slices.Equal(h.Plans, want) {
				t.Errorf("Plans = %v, want %v", h.Plans, want)
			}
			if want := full.Passed[tc.wantPassStart:]; !slices.Equal(h.Passed, want) {
				t.Errorf("Passed = %q, want %q", h.Passed, want)
			}
			if !slices.Equal(h.Regulars, full.Regulars) || !slices.Equal(h.QuestLog, full.QuestLog) || h.Today != full.Today {
				t.Errorf("trimmed what is never trimmed: %+v", h)
			}
		})
	}
}

func TestBuildContextBudgetErrors(t *testing.T) {
	t.Parallel()
	st := seededHousehold(t)
	full, err := BuildContext(context.Background(), st, contextNow, ample)
	if err != nil {
		t.Fatal(err)
	}
	floorBlock, err := HouseholdContext{Today: full.Today, Timezone: full.Timezone,
		Regulars: full.Regulars, QuestLog: full.QuestLog}.Block()
	if err != nil {
		t.Fatal(err)
	}
	for _, budget := range []int{0, -1, utf8.RuneCountInString(floorBlock) - 1} {
		if _, err := BuildContext(context.Background(), st, contextNow, budget); err == nil {
			t.Errorf("budget %d: no error", budget)
		}
	}
}

const emptyBlock = `## Household context
### Today
- Sunday 2026-10-04 (America/Los_Angeles)

### Regulars
(none)

### Quest log
(none)

### Passed, oldest first
(none)

### Recent plans
(none)`

func TestBuildContextEmptyHousehold(t *testing.T) {
	t.Parallel()
	h, err := BuildContext(context.Background(), emptyHousehold(t), contextNow, ample)
	if err != nil {
		t.Fatal(err)
	}
	block, err := h.Block()
	if err != nil {
		t.Fatal(err)
	}
	if block != emptyBlock {
		t.Fatalf("block:\n%s\n--- want ---\n%s", block, emptyBlock)
	}
	// The empty context renders into a whole prompt as layer 3.
	p, err := Render("", echoSkill{}, echoInput{Word: "Taco", Context: h.Sections()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.System, "\n\n"+emptyBlock+"\n\n## Output contract\n") {
		t.Fatalf("layer 3 is not the empty block:\n%s", p.System)
	}
}

func TestBuildContextNoSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st.Writer, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildContext(ctx, st, contextNow, ample); err == nil {
		t.Fatal("no error without household settings")
	}
}
