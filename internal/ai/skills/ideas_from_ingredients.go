package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

const (
	// IdeasRequested is how many ideas ideas_from_ingredients asks the
	// model for; validation and grounding drop some.
	IdeasRequested = 8
	// IdeasKept is the most ideas the household is shown.
	IdeasKept = 5
	// MaxIngredients is the most ingredients the household may list.
	MaxIngredients = 20
	// IngredientLimit is the most characters one listed ingredient may
	// have.
	IngredientLimit = 80
)

// IdeasRequest is what ideas_from_ingredients reads.
type IdeasRequest struct {
	// Ingredients are the ingredients the household has on hand, in its
	// order, as CleanIngredients returns them.
	Ingredients []string
	// QuickOnly asks the model for Quick dinners only. It is a hint: the
	// caller enforces it after grounding, from the grounded recipe.
	QuickOnly bool
}

// Idea is one dinner idea. Method, EstMinutes and Difficulty are the
// model's estimates until the idea is grounded in a recipe.
type Idea struct {
	Name string `json:"name"`
	// Uses are the household's ingredients the idea uses, as the household
	// wrote them and in its order. PostProcess drops every entry that is
	// not one of them.
	Uses []string `json:"uses"`
	// Missing are the ingredients the idea needs that the household does
	// not have, in the model's order.
	Missing    []string `json:"missing"`
	EstMinutes int      `json:"est_minutes"`
	// Method is one of stovetop, oven, crockpot, grill and other.
	Method string `json:"method"`
	// Difficulty is easy, medium or hard.
	Difficulty string `json:"difficulty"`
	// SearchQuery is a web search query that finds a recipe for the dinner.
	SearchQuery string `json:"search_query"`
	// Summary is the line the household reads under the idea, computed
	// by IdeaSummary from Uses and Missing. It is never part of the
	// model's reply.
	Summary string `json:"-"`
}

// Ideas is ideas_from_ingredients' output.
type Ideas struct {
	// Ideas are the ideas that passed every rule, in the model's order.
	Ideas []Idea `json:"ideas"`
	// Dropped are the ideas the model gave that broke a rule, in the
	// model's order. It is never part of the model's reply.
	Dropped []DroppedIdea `json:"-"`
}

// DroppedIdea is an idea dropped by PostProcess.
type DroppedIdea struct {
	Name string
	// Why says which rule it broke.
	Why string
}

// IdeasFromIngredients runs ideas_from_ingredients v1 on in and returns
// ideas whose uses hold only the household's own ingredients: an entry
// naming anything else is removed, and an idea left using none of them is
// dropped, as is one repeating an earlier idea. Each kept idea carries its
// summary line, computed from the corrected uses and missing lists. scope
// names the request (see harness.Run; IdeasScope). An ingredient list
// CleanIngredients refuses fails before any LLM call.
func IdeasFromIngredients(ctx context.Context, cfg harness.Config, scope string, in IdeasRequest) (Ideas, error) {
	ings, err := CleanIngredients(in.Ingredients)
	if err != nil {
		return Ideas{}, fmt.Errorf("skills: ideas_from_ingredients: %w", err)
	}
	in.Ingredients = ings
	return harness.Run(ctx, cfg, ideasFromIngredients{in: in}, scope, in)
}

// IdeasScope is the harness scope of a request: its normalized
// ingredients, in order, and whether it is for Quick dinners only, so two
// requests share a run only when the model would be asked the same.
func IdeasScope(in IdeasRequest) string {
	parts := make([]string, len(in.Ingredients))
	for i, ing := range in.Ingredients {
		parts[i] = harness.Normalize(ing)
	}
	return "quick_only=" + strconv.FormatBool(in.QuickOnly) + ";" + strings.Join(parts, "|")
}

// CleanIngredients returns the household's ingredient list with each entry
// on one line, or why it cannot be used: it is empty or longer than
// MaxIngredients, or an entry is blank, longer than IngredientLimit
// characters, has nothing left once normalized (harness.Normalize), or
// normalizes the same as an earlier one.
func CleanIngredients(list []string) ([]string, error) {
	if len(list) == 0 {
		return nil, errors.New("list at least one ingredient")
	}
	if len(list) > MaxIngredients {
		return nil, fmt.Errorf("%d ingredients listed; list at most %d", len(list), MaxIngredients)
	}
	out := make([]string, len(list))
	seen := map[string]string{}
	for i, ing := range list {
		ing = oneLine(ing)
		switch {
		case ing == "":
			return nil, fmt.Errorf("ingredient %d is blank", i+1)
		case utf8.RuneCountInString(ing) > IngredientLimit:
			return nil, fmt.Errorf("ingredient %d is over %d characters", i+1, IngredientLimit)
		}
		n := harness.Normalize(ing)
		if n == "" {
			return nil, fmt.Errorf("ingredient %d, %q, names no ingredient", i+1, ing)
		}
		if first, ok := seen[n]; ok {
			return nil, fmt.Errorf("ingredient %d, %q, repeats %q", i+1, ing, first)
		}
		seen[n] = ing
		out[i] = ing
	}
	return out, nil
}

// IdeaSummary is the line shown under an idea: how many of the household's
// total ingredients it uses, then what is missing, in the order given.
// "Uses all 5 · missing: 1 lime" when every ingredient is used, "Uses 3 of
// 5 · missing: 1 lime, cumin" when some are. With nothing missing the
// second part is "nothing missing", so an idea using all 5 and needing
// nothing else prints "Uses all 5 · nothing missing".
func IdeaSummary(uses, missing []string, total int) string {
	used := fmt.Sprintf("Uses %d of %d", len(uses), total)
	if len(uses) == total {
		used = fmt.Sprintf("Uses all %d", total)
	}
	if len(missing) == 0 {
		return used + " · nothing missing"
	}
	return used + " · missing: " + strings.Join(missing, ", ")
}

// ideasFromIngredients is the skill. It holds its input because
// post-processing compares the output with it.
type ideasFromIngredients struct {
	in IdeasRequest
}

var _ harness.Skill[IdeasRequest, Ideas] = ideasFromIngredients{}

func (ideasFromIngredients) Name() string { return "ideas_from_ingredients" }

func (ideasFromIngredients) Version() int { return 1 }

func (ideasFromIngredients) Timeout() time.Duration { return harness.DefaultTimeout }

// ideasPrompt is the template data of ideas_from_ingredients.v1.tmpl.
type ideasPrompt struct {
	// Count is how many ideas to ask for.
	Count     int
	QuickOnly bool
}

// PromptInput puts the ingredients, already cleaned by
// IdeasFromIngredients, in the household context block.
func (ideasFromIngredients) PromptInput(in IdeasRequest) (harness.PromptInput, error) {
	return harness.PromptInput{
		Data:    ideasPrompt{Count: IdeasRequested, QuickOnly: in.QuickOnly},
		Context: []harness.ContextSection{{Title: "Ingredients on hand", Lines: in.Ingredients}},
	}, nil
}

func (ideasFromIngredients) Schema() llm.JSONSchema {
	return llm.JSONSchema{Name: "ideas_from_ingredients", Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"ideas": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"name": {"type": "string"},
						"uses": {"type": "array", "items": {"type": "string"}},
						"missing": {"type": "array", "items": {"type": "string"}},
						"est_minutes": {"type": "integer"},
						"method": {"type": "string", "enum": ["stovetop", "oven", "crockpot", "grill", "other"]},
						"difficulty": {"type": "string", "enum": ["easy", "medium", "hard"]},
						"search_query": {"type": "string"}
					},
					"required": ["name", "uses", "missing", "est_minutes", "method", "difficulty", "search_query"],
					"additionalProperties": false
				}
			}
		},
		"required": ["ideas"],
		"additionalProperties": false
	}`)}
}

// UnmarshalJSON decodes an idea, requiring each of its fields as the
// schema does: encoding/json would otherwise read a missing or null one as
// empty.
func (i *Idea) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, nil, "name", "uses", "missing", "est_minutes", "method", "difficulty", "search_query"); err != nil {
		return fmt.Errorf("idea %s", err)
	}
	type plain Idea
	return json.Unmarshal(data, (*plain)(i))
}

// UnmarshalJSON decodes the output, requiring its one field as the schema
// does.
func (out *Ideas) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, nil, "ideas"); err != nil {
		return fmt.Errorf("output %s", err)
	}
	type plain Ideas
	return json.Unmarshal(data, (*plain)(out))
}

// Validate finds what makes the reply unusable as a whole: no ideas, too
// many, or one that is malformed. A well-formed idea that breaks a rule is
// dropped by PostProcess instead.
func (ideasFromIngredients) Validate(out Ideas) []string {
	var problems []string
	switch n := len(out.Ideas); {
	case n == 0 && len(out.Dropped) == 0:
		problems = append(problems, fmt.Sprintf("ideas is empty; give %d ideas", IdeasRequested))
	case n > IdeasRequested:
		problems = append(problems, fmt.Sprintf("ideas has %d entries; give at most %d ideas", n, IdeasRequested))
	}
	for i, idea := range out.Ideas {
		for _, f := range []struct{ field, value string }{{"name", idea.Name}, {"search_query", idea.SearchQuery}} {
			if strings.TrimSpace(f.value) == "" {
				problems = append(problems, fmt.Sprintf("idea %d has an empty %s; give one", i+1, f.field))
			}
		}
		if strings.TrimSpace(idea.Name) != "" && harness.Normalize(idea.Name) == "" {
			problems = append(problems, fmt.Sprintf("idea %d name %q has only filler words; name the dish", i+1, idea.Name))
		}
		if !slices.Contains(methods, idea.Method) {
			problems = append(problems, fmt.Sprintf("idea %d method %q is not one of %s", i+1, idea.Method, strings.Join(methods, ", ")))
		}
		if idea.EstMinutes <= 0 {
			problems = append(problems, fmt.Sprintf("idea %d est_minutes %d is not positive; give the minutes from prep to table", i+1, idea.EstMinutes))
		}
		if !slices.Contains(difficulties, idea.Difficulty) {
			problems = append(problems, fmt.Sprintf("idea %d difficulty %q is not one of %s", i+1, idea.Difficulty, strings.Join(difficulties, ", ")))
		}
	}
	return problems
}

// PostProcess corrects every idea against the household's ingredients and
// moves each that breaks a rule to Dropped. Uses keeps only entries whose
// normalized form (harness.Normalize) is that of a listed ingredient,
// written as the household wrote it, once each and in the household's
// order; anything else the model claimed is removed, never trusted.
// Missing loses blank entries, repeats and anything the household listed.
// An idea is dropped when it repeats an earlier idea or uses none of the
// household's ingredients. The summary line is computed from the
// corrected lists.
func (s ideasFromIngredients) PostProcess(out Ideas) (Ideas, error) {
	listed := map[string]int{}
	for i, ing := range s.in.Ingredients {
		listed[harness.Normalize(ing)] = i
	}
	result := Ideas{Ideas: []Idea{}}
	for _, idea := range out.Ideas {
		idea = Idea{
			Name:        oneLine(idea.Name),
			Uses:        s.uses(idea.Uses, listed),
			Missing:     missing(idea.Missing, listed),
			EstMinutes:  idea.EstMinutes,
			Method:      idea.Method,
			Difficulty:  idea.Difficulty,
			SearchQuery: oneLine(idea.SearchQuery),
		}
		if why := dropIdea(idea, result.Ideas); why != "" {
			result.Dropped = append(result.Dropped, DroppedIdea{Name: idea.Name, Why: why})
			continue
		}
		idea.Summary = IdeaSummary(idea.Uses, idea.Missing, len(s.in.Ingredients))
		result.Ideas = append(result.Ideas, idea)
	}
	return result, nil
}

// uses returns the household's ingredients that claimed names, as the
// household wrote them and in its order. A claimed entry that names none
// of them is left out.
func (s ideasFromIngredients) uses(claimed []string, listed map[string]int) []string {
	used := make([]bool, len(s.in.Ingredients))
	for _, c := range claimed {
		if i, ok := listed[harness.Normalize(c)]; ok {
			used[i] = true
		}
	}
	out := []string{}
	for i, ing := range s.in.Ingredients {
		if used[i] {
			out = append(out, ing)
		}
	}
	return out
}

// missing returns claimed on one line each, in order, without blank
// entries, repeats or any the household listed.
func missing(claimed []string, listed map[string]int) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, c := range claimed {
		c = oneLine(c)
		n := harness.Normalize(c)
		if n == "" || seen[n] {
			continue
		}
		if _, ok := listed[n]; ok {
			continue
		}
		seen[n] = true
		out = append(out, c)
	}
	return out
}

// dropIdea returns why idea must be dropped, or "" when it may be kept.
func dropIdea(idea Idea, kept []Idea) string {
	for _, k := range kept {
		if harness.Similar(idea.Name, k.Name) {
			return fmt.Sprintf("repeats the idea %q", k.Name)
		}
	}
	if len(idea.Uses) == 0 {
		return "uses none of the household's ingredients"
	}
	return ""
}
