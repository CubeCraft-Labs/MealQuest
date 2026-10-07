package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
	// Aliased: the package tests name a fixture household.
	hh "code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/household"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/store"
)

// NoteLimit is the most characters a drafted week's note may have.
const NoteLimit = 160

// WeekLength is how many dinners a drafted week holds, one a day from
// Monday to Sunday.
const WeekLength = 7

// Candidate kinds: a Regular or a Quest log dinner.
const (
	KindRegular  = "regular"
	KindQuestLog = "quest_log"
)

// WeekRequest is what draft_week reads.
type WeekRequest struct {
	// Context is the household context (see harness.BuildContext).
	Context harness.HouseholdContext
	// WeekStart is the Monday of the week to draft, household-local.
	WeekStart store.LocalDate
	// Pool holds the dinners the week may be drafted from. It must hold at
	// least WeekLength dinners, each once.
	Pool []Candidate
	// LastWeek holds the IDs of the dinners planned in the week before
	// WeekStart. They need not be in Pool.
	LastWeek []string
	// DinnerTime is the household's dinner time, "HH:MM", from which a
	// crock-pot dinner's StartBy is computed.
	DinnerTime string
}

// Candidate is one dinner of the pool.
type Candidate struct {
	ID   string
	Name string
	// Kind is KindRegular or KindQuestLog.
	Kind string
	// Method is one of stovetop, oven, crockpot, grill and other, or ""
	// when unknown.
	Method string
	// Minutes from prep to table, or 0 when unknown.
	Minutes int
}

// NotEnoughDinnersError is a pool too small to fill a week. DraftWeek
// returns it before any LLM call.
type NotEnoughDinnersError struct {
	// Pool is how many dinners the pool holds.
	Pool int
}

func (e *NotEnoughDinnersError) Error() string {
	return fmt.Sprintf("not enough dinners: the pool holds %d, a week needs %d", e.Pool, WeekLength)
}

// Week is draft_week's output.
type Week struct {
	// Days are the week's dinners, Monday to Sunday.
	Days []WeekDay `json:"days"`
	// Note is a short word on the week, at most NoteLimit characters. It
	// may be empty.
	Note string `json:"note"`
	// Warnings say what the draft did that the rules discourage but allow,
	// such as repeating last week's dinners. It is never part of the
	// model's reply.
	Warnings []string `json:"-"`
}

// WeekDay is one day's dinner.
type WeekDay struct {
	// Date is the household-local date, YYYY-MM-DD.
	Date string `json:"date"`
	// DinnerID is the ID of a dinner of the pool.
	DinnerID string `json:"dinner_id"`
	// IsNew is true for a Quest log dinner. It is computed from the pool,
	// never part of the model's reply.
	IsNew bool `json:"-"`
	// StartBy is when a crock-pot dinner must start to be ready at the
	// household's dinner time; it is zero for any other dinner and for a
	// crock-pot dinner whose minutes are unknown. It is computed, never
	// part of the model's reply.
	StartBy time.Time `json:"-"`
}

// DraftWeek runs draft_week v1 on in and returns a week of seven dinners
// from the pool, one a day from Monday to Sunday, none twice. A pool of
// fewer than WeekLength dinners fails with *NotEnoughDinnersError before
// any LLM call, and any other invalid request fails before it too. scope
// names the week (see harness.Run).
func DraftWeek(ctx context.Context, cfg harness.Config, scope string, in WeekRequest) (Week, error) {
	s, err := newDraftWeek(in)
	if err != nil {
		return Week{}, err
	}
	return harness.Run(ctx, cfg, s, scope, in)
}

// draftWeek is the skill. It holds its input, the week's dates and the
// pool by ID because validation and post-processing compare the output
// with them.
type draftWeek struct {
	in    WeekRequest
	dates [WeekLength]store.LocalDate
	pool  map[string]Candidate
	zone  *time.Location
}

var _ harness.Skill[WeekRequest, Week] = draftWeek{}

// newDraftWeek checks in and returns the skill for it.
func newDraftWeek(in WeekRequest) (draftWeek, error) {
	pool := map[string]Candidate{}
	for i, c := range in.Pool {
		if err := c.check(); err != nil {
			return draftWeek{}, fmt.Errorf("draft_week: pool dinner %d: %w", i+1, err)
		}
		if _, dup := pool[c.ID]; dup {
			return draftWeek{}, fmt.Errorf("draft_week: pool dinner %d: id %q is in the pool twice", i+1, c.ID)
		}
		pool[c.ID] = c
	}
	if len(pool) < WeekLength {
		return draftWeek{}, &NotEnoughDinnersError{Pool: len(pool)}
	}
	dates, err := hh.WeekDays(in.WeekStart)
	if err != nil {
		return draftWeek{}, fmt.Errorf("draft_week: %w", err)
	}
	zone, err := hh.LoadZone(in.Context.Timezone)
	if err != nil {
		return draftWeek{}, fmt.Errorf("draft_week: %w", err)
	}
	// The dinner time is checked here so a bad one fails before the LLM
	// call rather than in PostProcess.
	if _, err := hh.LocalTime(zone, in.WeekStart, in.DinnerTime); err != nil {
		return draftWeek{}, fmt.Errorf("draft_week: dinner time: %w", err)
	}
	return draftWeek{in: in, dates: dates, pool: pool, zone: zone}, nil
}

// check reports a candidate the pool cannot hold.
func (c Candidate) check() error {
	switch {
	case strings.TrimSpace(c.ID) == "" || strings.ContainsAny(c.ID, "\r\n"):
		return fmt.Errorf("id %q is empty or has a line break", c.ID)
	case strings.TrimSpace(c.Name) == "":
		return fmt.Errorf("%s has an empty name", c.ID)
	case c.Kind != KindRegular && c.Kind != KindQuestLog:
		return fmt.Errorf("%s kind %q is not %s or %s", c.ID, c.Kind, KindRegular, KindQuestLog)
	case c.Method != "" && !slices.Contains(methods, c.Method):
		return fmt.Errorf("%s method %q is not one of %s", c.ID, c.Method, strings.Join(methods, ", "))
	case c.Minutes < 0:
		return fmt.Errorf("%s minutes %d is negative", c.ID, c.Minutes)
	}
	return nil
}

func (draftWeek) Name() string { return "draft_week" }

func (draftWeek) Version() int { return 1 }

func (draftWeek) Timeout() time.Duration { return harness.DefaultTimeout }

// weekPrompt is the template data of draft_week.v1.tmpl.
type weekPrompt struct {
	// Days are the week's dates, Monday to Sunday, e.g. "Mon 2026-10-12".
	Days []string
}

func (s draftWeek) PromptInput(in WeekRequest) (harness.PromptInput, error) {
	var days []string
	for _, d := range s.dates {
		y, m, day := d.Date()
		days = append(days, time.Date(y, m, day, 0, 0, 0, 0, time.UTC).Format("Mon")+" "+d.String())
	}
	var pool []string
	for _, c := range in.Pool {
		pool = append(pool, c.line(slices.Contains(in.LastWeek, c.ID)))
	}
	sections := append(in.Context.Sections(), harness.ContextSection{Title: "Candidate pool", Lines: pool})
	return harness.PromptInput{Data: weekPrompt{Days: days}, Context: sections}, nil
}

// line renders c as `id "ID": Name (Regular, method, N min, planned last
// week)`, leaving out what is unknown.
func (c Candidate) line(lastWeek bool) string {
	attrs := []string{"Regular"}
	if c.Kind == KindQuestLog {
		attrs = []string{"Quest log"}
	}
	if c.Method != "" {
		attrs = append(attrs, c.Method)
	}
	if c.Minutes > 0 {
		attrs = append(attrs, fmt.Sprintf("%d min", c.Minutes))
	}
	if lastWeek {
		attrs = append(attrs, "planned last week")
	}
	return fmt.Sprintf("id %q: %s (%s)", c.ID, oneLine(c.Name), strings.Join(attrs, ", "))
}

func (draftWeek) Schema() llm.JSONSchema {
	return llm.JSONSchema{Name: "draft_week", Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"days": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"date": {"type": "string"},
						"dinner_id": {"type": "string"}
					},
					"required": ["date", "dinner_id"],
					"additionalProperties": false
				}
			},
			"note": {"type": "string"}
		},
		"required": ["days", "note"],
		"additionalProperties": false
	}`)}
}

// UnmarshalJSON decodes a day, requiring each of its fields as the schema
// does.
func (d *WeekDay) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, nil, "date", "dinner_id"); err != nil {
		return fmt.Errorf("day %s", err)
	}
	type plain WeekDay
	return json.Unmarshal(data, (*plain)(d))
}

// UnmarshalJSON decodes the output, requiring each of its fields as the
// schema does.
func (w *Week) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, nil, "days", "note"); err != nil {
		return fmt.Errorf("output %s", err)
	}
	type plain Week
	return json.Unmarshal(data, (*plain)(w))
}

// Validate finds every broken rule: a week that is not exactly the seven
// dates Monday to Sunday in order, a dinner not in the pool or planned
// twice, or a note over NoteLimit characters.
func (s draftWeek) Validate(out Week) []string {
	var problems []string
	if n := len(out.Days); n != WeekLength {
		problems = append(problems, fmt.Sprintf("days has %d entries; give exactly %d, one for each date from %s to %s",
			n, WeekLength, s.dates[0], s.dates[WeekLength-1]))
	}
	first := map[string]int{} // dinner ID -> first day planning it
	for i, d := range out.Days {
		if i < WeekLength && d.Date != s.dates[i].String() {
			problems = append(problems, fmt.Sprintf("day %d date %q is not %s; give the dates %s to %s in order",
				i+1, d.Date, s.dates[i], s.dates[0], s.dates[WeekLength-1]))
		}
		c, ok := s.pool[d.DinnerID]
		if !ok {
			problems = append(problems, fmt.Sprintf("day %d dinner_id %q is not in the candidate pool; use only ids from it", i+1, d.DinnerID))
			continue
		}
		if j, dup := first[d.DinnerID]; dup {
			problems = append(problems, fmt.Sprintf("day %d dinner_id %q (%s) is also on day %d; plan each dinner at most once",
				i+1, d.DinnerID, oneLine(c.Name), j+1))
			continue
		}
		first[d.DinnerID] = i
	}
	if n := utf8.RuneCountInString(out.Note); n > NoteLimit {
		problems = append(problems, fmt.Sprintf("note has %d characters; keep it to at most %d", n, NoteLimit))
	}
	return problems
}

// PostProcess tidies the note, marks each Quest log dinner new, computes
// each crock-pot dinner's StartBy, and warns when the week repeats more of
// last week's dinners than the pool made necessary.
func (s draftWeek) PostProcess(out Week) (Week, error) {
	result := Week{Note: oneLine(out.Note)}
	var repeats []string
	for i, d := range out.Days {
		c := s.pool[d.DinnerID]
		day := WeekDay{Date: d.Date, DinnerID: d.DinnerID, IsNew: c.Kind == KindQuestLog}
		if c.Method == "crockpot" && c.Minutes > 0 {
			dinner, err := hh.LocalTime(s.zone, s.dates[i], s.in.DinnerTime)
			if err != nil {
				return Week{}, err
			}
			day.StartBy = dinner.Add(-time.Duration(c.Minutes) * time.Minute)
		}
		if slices.Contains(s.in.LastWeek, d.DinnerID) {
			repeats = append(repeats, oneLine(c.Name))
		}
		result.Days = append(result.Days, day)
	}
	fresh := 0
	for id := range s.pool {
		if !slices.Contains(s.in.LastWeek, id) {
			fresh++
		}
	}
	if allowed := max(0, WeekLength-fresh); len(repeats) > allowed {
		result.Warnings = append(result.Warnings, fmt.Sprintf("repeats %d of last week's dinners (%s) though the pool needed at most %d",
			len(repeats), strings.Join(repeats, ", "), allowed))
	}
	return result, nil
}
