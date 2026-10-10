package plan_test

import (
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/plan"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

const reviewer = "member-1"

// Pad Thai and Shawarma Bowls, the seeded Quest log dinners, are planned
// on these days of fullWeek.
var (
	padThaiDay  = mustDate("2026-10-14")
	shawarmaDay = mustDate("2026-10-16")
)

// putWeek stores fullWeek as the plan of week with status, as a draft and
// Push to Calendar would.
func (e *env) putWeek(t *testing.T, status string) {
	t.Helper()
	q := db.New(e.st.Writer)
	now := store.Now().String()
	id := store.NewID()
	if err := q.CreateWeekPlan(t.Context(), db.CreateWeekPlanParams{ID: id, WeekStart: week.String(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i, name := range fullWeek {
		if err := q.PutPlanDay(t.Context(), db.PutPlanDayParams{ID: store.NewID(), PlanID: id, Date: weekDates[i],
			DinnerID: sql.NullString{String: e.ids[name], Valid: true}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	e.setStatus(t, status)
}

func (e *env) dinnerRow(t *testing.T, name string) db.Dinner {
	t.Helper()
	d, err := db.New(e.st.Reader).GetDinner(t.Context(), e.ids[name])
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// review is a rotation_reviews row as stored.
type review struct {
	planDate, status                     string
	decision, name, decidedAt, updatedBy sql.NullString
}

func (e *env) reviews(t *testing.T, name string) []review {
	t.Helper()
	rows, err := e.st.Reader.QueryContext(t.Context(),
		`SELECT plan_date, status, decision, name, decided_at, updated_by FROM rotation_reviews WHERE dinner_id = ? ORDER BY plan_date`, e.ids[name])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []review
	for rows.Next() {
		var r review
		if err := rows.Scan(&r.planDate, &r.status, &r.decision, &r.name, &r.decidedAt, &r.updatedBy); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func (e *env) markTried(t *testing.T, date store.LocalDate, want int) {
	t.Helper()
	n, err := e.svc.MarkTried(t.Context(), date)
	if err != nil {
		t.Fatal(err)
	}
	if n != want {
		t.Fatalf("MarkTried(%s) = %d, want %d", date, n, want)
	}
}

func TestMarkTried(t *testing.T) {
	e := newEnv(t)
	// Any plan status: a plan pushed to Calendar and edited still counts.
	e.putWeek(t, plan.StatusChanged)

	e.markTried(t, padThaiDay, 1)
	if got := e.dinnerRow(t, "Pad Thai").Status; got != "tried" {
		t.Errorf("Pad Thai status = %s, want tried", got)
	}
	got := e.reviews(t, "Pad Thai")
	if len(got) != 1 || got[0].planDate != padThaiDay.String() || got[0].status != "pending" ||
		got[0].decision.Valid || got[0].name.Valid || got[0].decidedAt.Valid || got[0].updatedBy.Valid {
		t.Fatalf("Pad Thai reviews = %+v, want one pending for %s", got, padThaiDay)
	}
	if got := e.dinnerRow(t, "Shawarma Bowls").Status; got != "quest_log" {
		t.Errorf("Shawarma Bowls, planned on another day, status = %s, want quest_log", got)
	}

	// Idempotent: a second run moves nothing and opens no second check.
	e.markTried(t, padThaiDay, 0)
	if got := e.reviews(t, "Pad Thai"); len(got) != 1 || got[0].status != "pending" {
		t.Errorf("Pad Thai reviews after a second run = %+v, want the one pending", got)
	}

	// A day planning a Regular, or with no plan, moves nothing.
	e.markTried(t, week, 0)
	e.markTried(t, mustDate("2026-10-20"), 0)
	if got := e.dinnerRow(t, "Beef Chili").Status; got != "regular" {
		t.Errorf("Beef Chili status = %s, want regular", got)
	}
	if got := e.reviews(t, "Beef Chili"); len(got) != 0 {
		t.Errorf("Beef Chili reviews = %+v, want none", got)
	}
}

// A dinner returned to the Quest log ("We didn't make it") is not marked
// tried again for the same day.
func TestMarkTriedAfterNotMade(t *testing.T) {
	e := newEnv(t)
	e.putWeek(t, plan.StatusDraft)
	e.markTried(t, padThaiDay, 1)
	if _, err := e.svc.DecideReview(t.Context(), e.ids["Pad Thai"], plan.DecisionNotMade, reviewer); err != nil {
		t.Fatal(err)
	}
	e.markTried(t, padThaiDay, 0)
	if got := e.dinnerRow(t, "Pad Thai").Status; got != "quest_log" {
		t.Errorf("Pad Thai status = %s, want quest_log", got)
	}
	if got := e.reviews(t, "Pad Thai"); len(got) != 1 || got[0].status != "decided" {
		t.Errorf("Pad Thai reviews = %+v, want the one decided", got)
	}
}

func TestPendingReviews(t *testing.T) {
	e := newEnv(t)
	e.putWeek(t, plan.StatusDraft)
	got, err := e.svc.PendingReviews(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("pending before MarkTried = %+v, want none", got)
	}

	e.markTried(t, shawarmaDay, 1)
	e.markTried(t, padThaiDay, 1)
	got, err = e.svc.PendingReviews(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []plan.Review{
		{Dinner: plan.ReviewDinner{ID: e.ids["Pad Thai"], Name: "Pad Thai"}, PlanDate: padThaiDay.String()},
		{Dinner: plan.ReviewDinner{ID: e.ids["Shawarma Bowls"], Name: "Shawarma Bowls"}, PlanDate: shawarmaDay.String()},
	}
	if !slices.Equal(got, want) {
		t.Errorf("pending = %+v, want %+v", got, want)
	}
}

func TestDecideReview(t *testing.T) {
	for _, tc := range []struct {
		decision, status string
	}{
		{plan.DecisionKeep, "regular"},
		{plan.DecisionDrop, "archived"},
		{plan.DecisionNotMade, "quest_log"},
	} {
		t.Run(tc.decision, func(t *testing.T) {
			e := newEnv(t)
			e.putWeek(t, plan.StatusDraft)
			e.markTried(t, padThaiDay, 1)

			d, err := e.svc.DecideReview(t.Context(), e.ids["Pad Thai"], tc.decision, reviewer)
			if err != nil {
				t.Fatal(err)
			}
			if d.Dinner.ID != e.ids["Pad Thai"] || d.Dinner.Name != "Pad Thai" || d.PlanDate != padThaiDay.String() ||
				d.Decision != tc.decision || d.DinnerStatus != tc.status || d.DecidedAt == "" || d.UpdatedBy != reviewer {
				t.Errorf("decided = %+v", d)
			}
			row := e.dinnerRow(t, "Pad Thai")
			if row.Status != tc.status || row.Origin != "side_quest" || row.Name != "Pad Thai" || row.UpdatedBy.String != reviewer {
				t.Errorf("Pad Thai = status %s origin %s name %s updated_by %v, want %s side_quest Pad Thai %s",
					row.Status, row.Origin, row.Name, row.UpdatedBy, tc.status, reviewer)
			}
			// Only a drop records the dinner's name.
			wantPassed := tc.decision == plan.DecisionDrop
			wantName := sql.NullString{}
			if wantPassed {
				wantName = sql.NullString{String: "Pad Thai", Valid: true}
			}
			got := e.reviews(t, "Pad Thai")
			if len(got) != 1 || got[0].status != "decided" || got[0].decision.String != tc.decision ||
				got[0].decidedAt.String != d.DecidedAt || got[0].updatedBy.String != reviewer ||
				got[0].name != wantName {
				t.Errorf("reviews = %+v, want one decided %s by %s at %s, named only by a drop", got, tc.decision, reviewer, d.DecidedAt)
			}
			if pending, err := e.svc.PendingReviews(t.Context()); err != nil || len(pending) != 0 {
				t.Errorf("pending after deciding = %+v, %v, want none", pending, err)
			}

			// A second decision, of any kind, changes nothing.
			for _, again := range []string{plan.DecisionKeep, plan.DecisionDrop, plan.DecisionNotMade} {
				if _, err := e.svc.DecideReview(t.Context(), e.ids["Pad Thai"], again, reviewer); !errors.Is(err, plan.ErrNoPendingReview) {
					t.Errorf("second decision %s: %v, want ErrNoPendingReview", again, err)
				}
			}
			if got := e.dinnerRow(t, "Pad Thai").Status; got != tc.status {
				t.Errorf("status after a second decision = %s, want %s", got, tc.status)
			}

			passed, err := db.New(e.st.Reader).ListPassedNames(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			recent, err := db.New(e.st.Reader).ListRecentPassedNames(t.Context(), 10)
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(passed, "Pad Thai") != wantPassed || slices.Contains(recent, "Pad Thai") != wantPassed {
				t.Errorf("passed names = %v, recent %v; want Pad Thai among them: %t", passed, recent, wantPassed)
			}
		})
	}
}

func TestDecideReviewRefused(t *testing.T) {
	e := newEnv(t)
	e.putWeek(t, plan.StatusDraft)
	e.markTried(t, padThaiDay, 1)
	ctx := t.Context()

	var invalid *plan.InvalidDecisionError
	if _, err := e.svc.DecideReview(ctx, e.ids["Pad Thai"], "promote", reviewer); !errors.As(err, &invalid) {
		t.Errorf("decision promote: %v, want *InvalidDecisionError", err)
	}
	if _, err := e.svc.DecideReview(ctx, "nope", plan.DecisionKeep, reviewer); !errors.Is(err, plan.ErrDinnerNotFound) {
		t.Errorf("unknown dinner: %v, want ErrDinnerNotFound", err)
	}
	// Never tried: Shawarma Bowls is in the Quest log with no check.
	if _, err := e.svc.DecideReview(ctx, e.ids["Shawarma Bowls"], plan.DecisionKeep, reviewer); !errors.Is(err, plan.ErrReviewNotFound) {
		t.Errorf("dinner never tried: %v, want ErrReviewNotFound", err)
	}
	if got := e.dinnerRow(t, "Shawarma Bowls").Status; got != "quest_log" {
		t.Errorf("Shawarma Bowls status = %s, want quest_log", got)
	}
	if _, err := e.svc.DecideReview(ctx, e.ids["Pad Thai"], plan.DecisionKeep, ""); err == nil {
		t.Error("decision with no member succeeded")
	}

	// A tried dinner archived meanwhile is neither kept nor listed.
	if _, err := e.st.Writer.ExecContext(ctx, `UPDATE dinners SET status = 'archived' WHERE id = ?`, e.ids["Pad Thai"]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.DecideReview(ctx, e.ids["Pad Thai"], plan.DecisionKeep, reviewer); !errors.Is(err, plan.ErrDinnerNotTried) {
		t.Errorf("archived dinner: %v, want ErrDinnerNotTried", err)
	}
	if got := e.dinnerRow(t, "Pad Thai").Status; got != "archived" {
		t.Errorf("Pad Thai status = %s, want archived", got)
	}
	if got := e.reviews(t, "Pad Thai"); len(got) != 1 || got[0].status != "pending" {
		t.Errorf("Pad Thai reviews = %+v, want the one still pending", got)
	}
	if pending, err := e.svc.PendingReviews(ctx); err != nil || len(pending) != 0 {
		t.Errorf("pending = %+v, %v, want none for an archived dinner", pending, err)
	}
}

// TestOnlyKeepPromotesToRegular guards D-021: no code path but the Rotation
// check's keep may make an existing dinner a Regular. Every query writing
// dinners.status and every non-test use of the regular status must be one
// of those reviewed below; a new one fails here until it is reviewed.
func TestOnlyKeepPromotesToRegular(t *testing.T) {
	// Each query writing dinners.status, and why it cannot promote.
	statusWriters := map[string]string{
		"CreateDinner":        "inserts a new dinner; only CreateRegular inserts one as a Regular",
		"UpdateDinner":        "dinners.Service.Update writes the status it read back",
		"ArchiveDinner":       "sets archived",
		"MoveSuggestedDinner": "Try it / Pass move a suggested dinner to quest_log or archived",
		"MarkDinnerTried":     "sets tried",
		"MoveTriedDinner":     "DecideReview's decisionStatus: only keep maps to regular",
	}
	files, err := filepath.Glob("../../migrations/queries/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("no query files: %v", err)
	}
	writes := regexp.MustCompile(`(?is)^\s*(INSERT\s+INTO\s+dinners\b|UPDATE\s+dinners\s+SET\b.*\bstatus\s*=)`)
	nameRe := regexp.MustCompile(`^\s*(\w+)`)
	found := map[string]bool{}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, stmt := range strings.Split(string(src), "-- name:")[1:] {
			name := nameRe.FindStringSubmatch(stmt)[1]
			var body []string
			for _, line := range strings.Split(stmt, "\n")[1:] {
				if !strings.HasPrefix(strings.TrimSpace(line), "--") {
					body = append(body, line)
				}
			}
			sqlText := strings.Join(body, "\n")
			if !writes.MatchString(sqlText) {
				continue
			}
			found[name] = true
			if _, ok := statusWriters[name]; !ok {
				t.Errorf("%s: query %s writes dinners.status; review it against D-021 and list it here", f, name)
			}
			if regexp.MustCompile(`(?i)\bstatus\s*=\s*'regular'`).MatchString(sqlText) {
				t.Errorf("%s: query %s sets status regular outright", f, name)
			}
		}
	}
	for name := range statusWriters {
		if !found[name] {
			t.Errorf("reviewed query %s no longer writes dinners.status; drop it from the list", name)
		}
	}

	// Each non-test Go file naming the regular status, and how often.
	uses := map[string]int{
		"dinners/dinners.go":      6, // the constant (2), listable, CreateRegular's status and origin, AddToQuestLog reusing a Regular as it is
		"plan/plan.go":            1, // plannable
		"plan/rotation.go":        1, // decisionStatus[DecisionKeep]
		"suggest/batch.go":        1, // reads which dinners are Regulars to cite
		"ai/harness/context.go":   1, // reads the Regulars
		"ai/skills/draft_week.go": 1, // KindRegular, a planned day's label
	}
	regular := regexp.MustCompile(`StatusRegular\b|"regular"`)
	got := map[string]int{}
	err = filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "db" {
			return filepath.SkipDir // generated; the queries are checked above
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if n := len(regular.FindAllIndex(src, -1)); n > 0 {
			got[filepath.ToSlash(strings.TrimPrefix(path, ".."+string(filepath.Separator)))] = n
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, n := range got {
		if uses[path] != n {
			t.Errorf("%s names the regular status %d times, reviewed %d; review it against D-021", path, n, uses[path])
		}
	}
	for path, n := range uses {
		if got[path] == 0 {
			t.Errorf("%s no longer names the regular status (reviewed %d); update the list", path, n)
		}
	}
}
