package shopping_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/db"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/shopping"
)

// titled is archive_title's reply giving title.
func titled(title string) llm.GenerateResult {
	js := fmt.Sprintf(`{"title":%q}`, title)
	return llm.GenerateResult{Response: llm.Response{Text: js, JSON: json.RawMessage(js)}}
}

// titleWeek stores three dinners, one with no recipe, and plans them on
// the week's first three days.
func titleWeek(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.dinner(t, "Beef Chili", ing{"ground beef", "1", "lb"})
	e.dinner(t, "Fish Tacos")
	e.dinner(t, "Pad Thai", ing{"rice noodles", "8", "oz"})
	e.plan(t, "Pad Thai", "Fish Tacos", "Beef Chili")
	return e
}

// storedTitle returns the week's list's stored title; nil when NULL.
func (e *env) storedTitle(t *testing.T) *string {
	t.Helper()
	var title sql.NullString
	if err := e.st.Reader.QueryRow(`SELECT title FROM shopping_lists WHERE week_start = ?`, week.String()).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if !title.Valid {
		return nil
	}
	return &title.String
}

// runStatuses returns the status of every archive_title run recorded in
// ai_runs, oldest first.
func (e *env) runStatuses(t *testing.T) []string {
	t.Helper()
	rows, err := e.st.Reader.Query(`SELECT status FROM ai_runs WHERE skill = 'archive_title' ORDER BY created_at, rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func wantTitle(t *testing.T, what string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Errorf("%s = %v, want %q", what, deref(got), want)
	}
}

func wantUntitled(t *testing.T, what string, got *string) {
	t.Helper()
	if got != nil {
		t.Errorf("%s = %q, want none", what, *got)
	}
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// TestGenerateTitlesNewList: a new list is titled by archive_title from
// the plan's dinners in day order, a dinner with no group included, and
// the title is stored and returned.
func TestGenerateTitlesNewList(t *testing.T) {
	e := titleWeek(t)
	e.model.QueueGenerate(titled("Noodles, Tacos and Chili"))

	g := e.generate(t)

	wantTitle(t, "returned title", g.List.Title, "Noodles, Tacos and Chili")
	wantTitle(t, "stored title", e.storedTitle(t), "Noodles, Tacos and Chili")
	got, err := e.svc.Get(t.Context(), week)
	if err != nil {
		t.Fatal(err)
	}
	wantTitle(t, "title read back", got.Title, "Noodles, Tacos and Chili")
	reqs := e.model.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d model calls, want 1", len(reqs))
	}
	want := "Title the week whose dinners are, in day order from Monday:\n- Pad Thai\n- Fish Tacos\n- Beef Chili"
	if user := reqs[0].Messages[0].Content; user != want {
		t.Errorf("user message = %q, want %q", user, want)
	}
	if s := e.runStatuses(t); len(s) != 1 || s[0] != string(harness.StatusOK) {
		t.Errorf("archive_title runs = %q, want one ok", s)
	}
}

// TestGenerateTitleTooLongRepaired: a title over 48 characters is repaired
// through the repair turn, and the repaired title is stored, not a
// truncation of the first.
func TestGenerateTitleTooLongRepaired(t *testing.T) {
	e := titleWeek(t)
	long := "Pad Thai Monday, Fish Taco Tuesday and Chili Wednesday"
	e.model.QueueGenerate(titled(long), titled("Noodles, Tacos and Chili"))

	g := e.generate(t)

	wantTitle(t, "returned title", g.List.Title, "Noodles, Tacos and Chili")
	wantTitle(t, "stored title", e.storedTitle(t), "Noodles, Tacos and Chili")
	reqs := e.model.Requests()
	if len(reqs) != 2 || !strings.Contains(reqs[1].Messages[2].Content, "has 54 characters; shorten it to at most 48") {
		t.Fatalf("model calls = %+v, want a repair turn asking for a shorter title", reqs)
	}
	if s := e.runStatuses(t); len(s) != 1 || s[0] != string(harness.StatusRepaired) {
		t.Errorf("archive_title runs = %q, want one repaired", s)
	}
}

// TestGenerateTitleFailureKeepsList: archive_title giving an emoji twice
// fails, and the list is still made, whole and untitled, with the failure
// logged and recorded in ai_runs.
func TestGenerateTitleFailureKeepsList(t *testing.T) {
	e := titleWeek(t)
	e.model.QueueGenerate(titled("Taco Week 🌮"), titled("Taco Week 🌮"))

	g, err := e.svc.Generate(t.Context(), week)
	if err != nil {
		t.Fatalf("Generate = %v, want the list made though its title failed", err)
	}

	want := []string{
		"0 2026-10-12 Pad Thai: 0 rice noodles [8 oz] needed",
		"1 2026-10-14 Beef Chili: 0 ground beef [1 lb] needed",
	}
	wantLines(t, "list", lines(g.List), want)
	wantLines(t, "stored list", e.stored(t), want)
	if len(g.Skipped) != 1 || g.Skipped[0].DinnerName != "Fish Tacos" {
		t.Errorf("skipped = %+v, want Fish Tacos", g.Skipped)
	}
	wantUntitled(t, "returned title", g.List.Title)
	wantUntitled(t, "stored title", e.storedTitle(t))
	if g.List.Status != shopping.StatusCurrent {
		t.Errorf("status = %q, want current", g.List.Status)
	}
	log := e.log.String()
	for _, part := range []string{"level=WARN", "Supply run left untitled", "week=2026-10-12", "has an emoji (U+1F32E)"} {
		if !strings.Contains(log, part) {
			t.Errorf("log lacks %q:\n%s", part, log)
		}
	}
	if s := e.runStatuses(t); len(s) != 1 || s[0] != string(harness.StatusBadOutput) {
		t.Errorf("archive_title runs = %q, want one bad_output", s)
	}
}

// TestGenerateRetriesTitle: a list left untitled is titled by the week's
// next generation, Generate or GeneratePushed; once titled, a remake keeps
// its title and asks for none.
func TestGenerateRetriesTitle(t *testing.T) {
	e := titleWeek(t)
	e.model.QueueGenerate(llm.GenerateResult{Err: fmt.Errorf("%w: model down", llm.ErrProvider)})
	first := e.generate(t)
	wantUntitled(t, "first title", first.List.Title)
	if !strings.Contains(e.log.String(), "model down") {
		t.Errorf("log lacks the failure:\n%s", e.log.String())
	}

	e.model.QueueGenerate(titled("Noodles, Tacos and Chili"))
	plan, err := db.New(e.st.Reader).GetWeekPlan(t.Context(), week.String())
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.GeneratePushed(t.Context(), week, plan.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if second.List.ID != first.List.ID {
		t.Fatalf("second generation made list %s, want list %s again", second.List.ID, first.List.ID)
	}
	wantTitle(t, "retried title", second.List.Title, "Noodles, Tacos and Chili")
	wantTitle(t, "stored title", e.storedTitle(t), "Noodles, Tacos and Chili")

	e.swap(t, "2026-10-12", "Beef Chili")
	third := e.generate(t)
	wantTitle(t, "title after a remake", third.List.Title, "Noodles, Tacos and Chili")
	if n := len(e.model.Requests()); n != 2 {
		t.Errorf("%d model calls, want 2: a titled list is not titled again", n)
	}
}

// TestGenerateTitleNotStoredOnFrozenList: a list frozen into the Supply
// archive while its title is written keeps no title, and the failure is
// logged.
func TestGenerateTitleNotStoredOnFrozenList(t *testing.T) {
	e := titleWeek(t)
	e.model.QueueGenerate(titled("Noodles, Tacos and Chili"))
	e.beforeTitle = func() {
		if _, err := e.st.Writer.Exec(`UPDATE shopping_lists SET status = 'archived', frozen_at = '2026-10-19T04:05:00Z'`); err != nil {
			t.Error(err)
		}
	}

	g := e.generate(t)

	wantUntitled(t, "returned title", g.List.Title)
	wantUntitled(t, "stored title", e.storedTitle(t))
	if !strings.Contains(e.log.String(), "the Supply run was frozen or replaced before its title was stored") {
		t.Errorf("log lacks the failure:\n%s", e.log.String())
	}
}
