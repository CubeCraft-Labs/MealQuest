package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// ReasonLimit is the most characters a Side quest's reason, its "Why
// this?", may have.
const ReasonLimit = 180

// SideQuestsRequest is what suggest_side_quests reads.
type SideQuestsRequest struct {
	// Context is the household context (see harness.BuildContext). It must
	// hold at least one Regular, since every Side quest cites one.
	Context harness.HouseholdContext
	// Needed is how many Side quests the caller wants to keep. The skill
	// asks the model for twice as many, since validation and grounding
	// drop some. It must be positive.
	Needed int
	// Pending holds the names of the Side quests not yet tried or passed.
	Pending []string
	// Declined holds the name of every passed Side quest, not only the
	// recent ones Context holds.
	Declined []string
}

// SideQuest is one suggested new dinner. Method, EstMinutes and Difficulty
// are the model's estimates until the dinner is grounded in a recipe.
type SideQuest struct {
	Name    string `json:"name"`
	Cuisine string `json:"cuisine"`
	// Method is one of stovetop, oven, crockpot, grill and other.
	Method     string `json:"method"`
	EstMinutes int    `json:"est_minutes"`
	// Difficulty is easy, medium or hard.
	Difficulty string `json:"difficulty"`
	// Reason is the "Why this?": at most ReasonLimit characters, naming at
	// least one of CitedRegulars.
	Reason string `json:"reason"`
	// CitedRegulars are names of the household's Regulars, as the
	// household writes them.
	CitedRegulars []string `json:"cited_regulars"`
	// SearchQuery is a web search query that finds a recipe for the dinner.
	SearchQuery string `json:"search_query"`
}

// SideQuests is suggest_side_quests' output.
type SideQuests struct {
	// Suggestions are the Side quests that passed every rule, in the
	// model's order. There may be more or fewer than Needed.
	Suggestions []SideQuest `json:"suggestions"`
	// Dropped are the Side quests the model gave that broke a rule, in the
	// model's order. It is never part of the model's reply.
	Dropped []DroppedSideQuest `json:"-"`
}

// DroppedSideQuest is a Side quest dropped by PostProcess.
type DroppedSideQuest struct {
	Name string
	// Why says which rule it broke.
	Why string
}

// SuggestSideQuests runs suggest_side_quests v1 on in and returns the Side
// quests that cite only real Regulars, give a short reason naming one of
// them, and repeat no Regular, Quest log dinner, pending or passed Side
// quest, nor each other. The rest are in Dropped. scope names the batch
// (see harness.Run).
func SuggestSideQuests(ctx context.Context, cfg harness.Config, scope string, in SideQuestsRequest) (SideQuests, error) {
	return harness.Run(ctx, cfg, suggestSideQuests{in: in}, scope, in)
}

// suggestSideQuests is the skill. It holds its input because validation
// and post-processing compare the output with it.
type suggestSideQuests struct {
	in SideQuestsRequest
}

var _ harness.Skill[SideQuestsRequest, SideQuests] = suggestSideQuests{}

func (suggestSideQuests) Name() string { return "suggest_side_quests" }

func (suggestSideQuests) Version() int { return 1 }

func (suggestSideQuests) Timeout() time.Duration { return harness.DefaultTimeout }

// sideQuestsPrompt is the template data of suggest_side_quests.v1.tmpl.
type sideQuestsPrompt struct {
	// Count is how many Side quests to ask for.
	Count int
}

// requested is how many Side quests the model is asked for.
func (s suggestSideQuests) requested() int { return 2 * s.in.Needed }

func (suggestSideQuests) PromptInput(in SideQuestsRequest) (harness.PromptInput, error) {
	if in.Needed <= 0 {
		return harness.PromptInput{}, fmt.Errorf("%d Side quests needed; the number must be positive", in.Needed)
	}
	if len(in.Context.Regulars) == 0 {
		return harness.PromptInput{}, errors.New("household has no Regulars for Side quests to be based on")
	}
	sections := append(in.Context.Sections(), harness.ContextSection{Title: "Pending Side quests", Lines: in.Pending})
	return harness.PromptInput{Data: sideQuestsPrompt{Count: suggestSideQuests{in: in}.requested()}, Context: sections}, nil
}

func (suggestSideQuests) Schema() llm.JSONSchema {
	return llm.JSONSchema{Name: "suggest_side_quests", Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"suggestions": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"name": {"type": "string"},
						"cuisine": {"type": "string"},
						"method": {"type": "string", "enum": ["stovetop", "oven", "crockpot", "grill", "other"]},
						"est_minutes": {"type": "integer"},
						"difficulty": {"type": "string", "enum": ["easy", "medium", "hard"]},
						"reason": {"type": "string"},
						"cited_regulars": {"type": "array", "items": {"type": "string"}},
						"search_query": {"type": "string"}
					},
					"required": ["name", "cuisine", "method", "est_minutes", "difficulty", "reason", "cited_regulars", "search_query"],
					"additionalProperties": false
				}
			}
		},
		"required": ["suggestions"],
		"additionalProperties": false
	}`)}
}

// UnmarshalJSON decodes a Side quest, requiring each of its fields as the
// schema does: encoding/json would otherwise read a missing or null one as
// empty.
func (q *SideQuest) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, nil, "name", "cuisine", "method", "est_minutes", "difficulty", "reason", "cited_regulars", "search_query"); err != nil {
		return fmt.Errorf("suggestion %s", err)
	}
	type plain SideQuest
	return json.Unmarshal(data, (*plain)(q))
}

// UnmarshalJSON decodes the output, requiring its one field as the schema
// does.
func (out *SideQuests) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, nil, "suggestions"); err != nil {
		return fmt.Errorf("output %s", err)
	}
	type plain SideQuests
	return json.Unmarshal(data, (*plain)(out))
}

// Validate finds what makes the reply unusable as a whole: a wrong number
// of Side quests or one that is malformed. A well-formed Side quest that
// breaks a rule is dropped by PostProcess instead.
func (s suggestSideQuests) Validate(out SideQuests) []string {
	var problems []string
	switch n := len(out.Suggestions); {
	case n == 0 && len(out.Dropped) == 0:
		problems = append(problems, fmt.Sprintf("suggestions is empty; give %d Side quests", s.requested()))
	case n > s.requested():
		problems = append(problems, fmt.Sprintf("suggestions has %d entries; give at most %d Side quests", n, s.requested()))
	}
	for i, q := range out.Suggestions {
		for _, f := range []struct{ field, value string }{
			{"name", q.Name}, {"cuisine", q.Cuisine}, {"reason", q.Reason}, {"search_query", q.SearchQuery},
		} {
			if strings.TrimSpace(f.value) == "" {
				problems = append(problems, fmt.Sprintf("suggestion %d has an empty %s; give one", i+1, f.field))
			}
		}
		if !slices.Contains(methods, q.Method) {
			problems = append(problems, fmt.Sprintf("suggestion %d method %q is not one of %s", i+1, q.Method, strings.Join(methods, ", ")))
		}
		if q.EstMinutes <= 0 {
			problems = append(problems, fmt.Sprintf("suggestion %d est_minutes %d is not positive; give the minutes from prep to table", i+1, q.EstMinutes))
		}
		if !slices.Contains(difficulties, q.Difficulty) {
			problems = append(problems, fmt.Sprintf("suggestion %d difficulty %q is not one of %s", i+1, q.Difficulty, strings.Join(difficulties, ", ")))
		}
	}
	return problems
}

// PostProcess tidies every Side quest's text, writes its cited Regulars as
// the household does, and moves each one that breaks a rule to Dropped
// (see drop).
func (s suggestSideQuests) PostProcess(out SideQuests) (SideQuests, error) {
	regulars := map[string]string{} // normalized name -> name
	var names []string
	for _, r := range s.in.Context.Regulars {
		if n := harness.Normalize(r.Name); n != "" {
			regulars[n] = r.Name
		}
		names = append(names, r.Name)
	}
	known := harness.NewDeduper(harness.DedupeSources{
		Regulars: names,
		QuestLog: s.in.Context.QuestLog,
		Pending:  s.in.Pending,
		Declined: s.in.Declined,
	})
	result := SideQuests{Suggestions: []SideQuest{}}
	for _, q := range out.Suggestions {
		q = SideQuest{
			Name:          oneLine(q.Name),
			Cuisine:       oneLine(q.Cuisine),
			Method:        q.Method,
			EstMinutes:    q.EstMinutes,
			Difficulty:    q.Difficulty,
			Reason:        oneLine(q.Reason),
			CitedRegulars: q.CitedRegulars,
			SearchQuery:   oneLine(q.SearchQuery),
		}
		cited, why := drop(q, regulars, known, result.Suggestions)
		if why != "" {
			result.Dropped = append(result.Dropped, DroppedSideQuest{Name: q.Name, Why: why})
			continue
		}
		q.CitedRegulars = cited
		result.Suggestions = append(result.Suggestions, q)
	}
	return result, nil
}

// drop returns why q must be dropped, or "" and its cited Regulars as the
// household writes them when it may be kept. q is dropped when it repeats
// a known dinner or a Side quest already kept, cites no Regular or one not
// in regulars (keyed by normalized name), or has a reason over ReasonLimit
// characters or naming none of its cited Regulars.
func drop(q SideQuest, regulars map[string]string, known *harness.Deduper, kept []SideQuest) ([]string, string) {
	if known.IsDuplicate(q.Name) {
		return nil, "repeats a Regular, Quest log dinner, pending or passed Side quest"
	}
	for _, k := range kept {
		if harness.Similar(q.Name, k.Name) {
			return nil, fmt.Sprintf("repeats the Side quest %q in the same batch", k.Name)
		}
	}
	if len(q.CitedRegulars) == 0 {
		return nil, "cites no Regular"
	}
	var cited []string
	for _, c := range q.CitedRegulars {
		name, ok := regulars[harness.Normalize(c)]
		if !ok {
			return nil, fmt.Sprintf("cites %q, which is not a Regular", c)
		}
		cited = append(cited, name)
	}
	if n := utf8.RuneCountInString(q.Reason); n > ReasonLimit {
		return nil, fmt.Sprintf("reason has %d characters, over %d", n, ReasonLimit)
	}
	reason := " " + harness.Normalize(q.Reason) + " "
	if !slices.ContainsFunc(cited, func(name string) bool {
		return strings.Contains(reason, " "+harness.Normalize(name)+" ")
	}) {
		return nil, "reason names none of its cited Regulars"
	}
	return cited, ""
}
