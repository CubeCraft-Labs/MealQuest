// Package skills holds the concrete AI skills run by package harness. Each
// skill's prompt template is in package prompts.
package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/harness"
	"code.cubecraftlabs.com/Maniacs_Mansion/Meal-Quest/internal/ai/llm"
)

// RecipeText is what normalize_recipe reads: the parts of a recipe page's
// schema.org Recipe (see recipes.Extract) that it normalizes.
type RecipeText struct {
	Title string
	// Ingredients are the raw ingredient lines, in order. There is at
	// least one and none is blank.
	Ingredients []string
	// Steps are the instructions, in order; there may be none.
	Steps []string
	// PrepMinutes, CookMinutes and TotalMinutes are the JSON-LD times,
	// nil when the page gives none. A TotalMinutes that is present must be
	// positive: a recipe cannot take no time, so NormalizeRecipe rejects a
	// zero total rather than guess one.
	PrepMinutes  *int
	CookMinutes  *int
	TotalMinutes *int
}

// Ingredient is one normalized ingredient line. Qty, Unit and Note are
// empty when the line gives none; the model must still give all four, so a
// reply leaving one out or null does not decode.
type Ingredient struct {
	Name string `json:"name"`
	Qty  string `json:"qty"`
	Unit string `json:"unit"`
	Note string `json:"note"`
}

// NormalizedRecipe is normalize_recipe's output.
type NormalizedRecipe struct {
	// Ingredients holds one ingredient per input line, in the same order.
	Ingredients []Ingredient `json:"ingredients"`
	// Method is one of stovetop, oven, crockpot, grill and other.
	Method string `json:"method"`
	// Difficulty is easy, medium or hard. The model may leave it empty;
	// NormalizeRecipe always returns one.
	Difficulty string `json:"difficulty"`
	// MinutesTotal is prep to table in minutes, nil when unknown.
	MinutesTotal *int `json:"minutes_total"`
}

// NormalizeRecipe runs normalize_recipe v1 on in and returns its output.
// The run is at temperature 0 whatever cfg.Settings.Temperature says: the
// same page must normalize the same way every time. scope names the recipe,
// e.g. its source URL (see harness.Run).
//
// Values the code can derive win over the model's: the JSON-LD
// TotalMinutes, when given, is minutes_total; a title or step mentioning a slow cooker
// or crock pot makes the method crockpot; and a difficulty the model left
// out is derived from the step count and minutes.
func NormalizeRecipe(ctx context.Context, cfg harness.Config, scope string, in RecipeText) (NormalizedRecipe, error) {
	cfg.Settings.Temperature = 0
	return harness.Run(ctx, cfg, normalizeRecipe{in: in}, scope, in)
}

// normalizeRecipe is the skill. It holds its input because validation and
// post-processing compare the output with it.
type normalizeRecipe struct {
	in RecipeText
}

var _ harness.Skill[RecipeText, NormalizedRecipe] = normalizeRecipe{}

func (normalizeRecipe) Name() string { return "normalize_recipe" }

func (normalizeRecipe) Version() int { return 1 }

func (normalizeRecipe) Timeout() time.Duration { return harness.DefaultTimeout }

// recipePrompt is the template data of normalize_recipe.v1.tmpl. Lists are
// numbered and every line is on one line.
type recipePrompt struct {
	Title       string
	Times       string
	Ingredients []string
	Steps       []string
}

func (normalizeRecipe) PromptInput(in RecipeText) (harness.PromptInput, error) {
	title := oneLine(in.Title)
	if title == "" {
		return harness.PromptInput{}, errors.New("recipe has no title")
	}
	if len(in.Ingredients) == 0 {
		return harness.PromptInput{}, errors.New("recipe has no ingredient lines")
	}
	if in.TotalMinutes != nil && *in.TotalMinutes <= 0 {
		return harness.PromptInput{}, fmt.Errorf("recipe total time is %d minutes; a given total must be positive", *in.TotalMinutes)
	}
	p := recipePrompt{Title: title}
	for i, line := range in.Ingredients {
		line = oneLine(line)
		if line == "" {
			return harness.PromptInput{}, fmt.Errorf("ingredient line %d is blank", i+1)
		}
		p.Ingredients = append(p.Ingredients, fmt.Sprintf("%d. %s", i+1, line))
	}
	for i, step := range in.Steps {
		step = oneLine(step)
		if step == "" {
			return harness.PromptInput{}, fmt.Errorf("step %d is blank", i+1)
		}
		p.Steps = append(p.Steps, fmt.Sprintf("%d. %s", i+1, step))
	}
	var times []string
	for _, t := range []struct {
		label   string
		minutes *int
	}{{"prep", in.PrepMinutes}, {"cook", in.CookMinutes}, {"total", in.TotalMinutes}} {
		if t.minutes != nil {
			times = append(times, fmt.Sprintf("%s %d min", t.label, *t.minutes))
		}
	}
	p.Times = strings.Join(times, ", ")
	return harness.PromptInput{Data: p}, nil
}

// oneLine collapses every run of white space in s, line breaks included,
// to one space.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (normalizeRecipe) Schema() llm.JSONSchema {
	return llm.JSONSchema{Name: "normalize_recipe", Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"ingredients": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"name": {"type": "string"},
						"qty": {"type": "string"},
						"unit": {"type": "string"},
						"note": {"type": "string"}
					},
					"required": ["name", "qty", "unit", "note"],
					"additionalProperties": false
				}
			},
			"method": {"type": "string", "enum": ["stovetop", "oven", "crockpot", "grill", "other"]},
			"difficulty": {"type": ["string", "null"], "enum": ["easy", "medium", "hard", null]},
			"minutes_total": {"type": ["integer", "null"]}
		},
		"required": ["ingredients", "method", "difficulty", "minutes_total"],
		"additionalProperties": false
	}`)}
}

// UnmarshalJSON decodes an ingredient, requiring each of its four fields
// as the schema does: encoding/json would otherwise read a missing or null
// one as empty.
func (ing *Ingredient) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, nil, "name", "qty", "unit", "note"); err != nil {
		return fmt.Errorf("ingredient %s", err)
	}
	type plain Ingredient
	return json.Unmarshal(data, (*plain)(ing))
}

// UnmarshalJSON decodes the output, requiring each of its fields as the
// schema does. Only difficulty and minutes_total may be null.
func (r *NormalizedRecipe) UnmarshalJSON(data []byte) error {
	if err := requireFields(data, []string{"difficulty", "minutes_total"}, "ingredients", "method", "difficulty", "minutes_total"); err != nil {
		return fmt.Errorf("output %s", err)
	}
	type plain NormalizedRecipe
	return json.Unmarshal(data, (*plain)(r))
}

// requireFields checks that the JSON object data has exactly the fields
// named, none of them null unless listed in nullable.
func requireFields(data []byte, nullable []string, fields ...string) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	if obj == nil {
		return errors.New("is null, not an object")
	}
	for _, f := range fields {
		v, ok := obj[f]
		if !ok {
			return fmt.Errorf("has no %q field; give every field the schema requires", f)
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) && !slices.Contains(nullable, f) {
			return fmt.Errorf("field %q is null, which the schema does not allow", f)
		}
	}
	for f := range obj {
		if !slices.Contains(fields, f) {
			return fmt.Errorf("has unknown field %q", f)
		}
	}
	return nil
}

var (
	methods      = []string{"stovetop", "oven", "crockpot", "grill", "other"}
	difficulties = []string{"easy", "medium", "hard"}
)

func (s normalizeRecipe) Validate(out NormalizedRecipe) []string {
	var problems []string
	lines := s.in.Ingredients
	if len(out.Ingredients) != len(lines) {
		problems = append(problems, fmt.Sprintf("ingredients has %d entries but there are %d ingredient lines; give exactly one ingredient per line, in order",
			len(out.Ingredients), len(lines)))
	}
	for i, ing := range out.Ingredients {
		if strings.TrimSpace(ing.Name) == "" {
			problems = append(problems, fmt.Sprintf("ingredient %d has an empty name; name the ingredient", i+1))
		}
	}
	if len(out.Ingredients) == len(lines) {
		problems = append(problems, orderProblems(out.Ingredients, lines)...)
	}
	if !slices.Contains(methods, out.Method) {
		problems = append(problems, fmt.Sprintf("method %q is not one of %s", out.Method, strings.Join(methods, ", ")))
	}
	if out.Difficulty != "" && !slices.Contains(difficulties, out.Difficulty) {
		problems = append(problems, fmt.Sprintf("difficulty %q is not one of %s, or null", out.Difficulty, strings.Join(difficulties, ", ")))
	}
	if out.MinutesTotal != nil && *out.MinutesTotal <= 0 && jsonLDMinutes(s.in) == nil {
		problems = append(problems, fmt.Sprintf("minutes_total %d is not positive; give the minutes or null", *out.MinutesTotal))
	}
	return problems
}

// orderProblems finds ingredients out of input order: an ingredient whose
// name has more of its words in another line than in its own. Sharing a
// word is not enough: "sesame oil" on the line "2 tbsp olive oil" has one
// of its two words there but both in "1 tbsp sesame oil", so it is out of
// order. Ingredients that match no line at all, e.g. a renamed one, are
// accepted, as are ties, e.g. a repeated line.
func orderProblems(ings []Ingredient, lines []string) []string {
	var problems []string
	for i, ing := range ings {
		name := words(ing.Name)
		own := matchedWords(name, words(lines[i]))
		best, bestLine := own, i
		for j, line := range lines {
			if n := matchedWords(name, words(line)); n > best {
				best, bestLine = n, j
			}
		}
		if bestLine != i {
			problems = append(problems, fmt.Sprintf("ingredient %d %q matches line %d %q, not line %d %q; keep the ingredients in line order",
				i+1, ing.Name, bestLine+1, lines[bestLine], i+1, lines[i]))
		}
	}
	return problems
}

// words returns the lower-case words of s of three or more letters.
func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if len([]rune(w)) >= 3 {
			out = append(out, w)
		}
	}
	return out
}

// matchedWords counts the words of name that are in line, up to a suffix,
// as "tomato" and "tomatoes" are the same word.
func matchedWords(name, line []string) int {
	n := 0
	for _, x := range name {
		if slices.ContainsFunc(line, func(y string) bool { return strings.HasPrefix(x, y) || strings.HasPrefix(y, x) }) {
			n++
		}
	}
	return n
}

// slowCooker matches a mention of a slow cooker or crock pot.
var slowCooker = regexp.MustCompile(`(?i)\b(slow[\s-]?cooker|crock[\s-]?pot)`)

func (s normalizeRecipe) PostProcess(out NormalizedRecipe) (NormalizedRecipe, error) {
	ings := make([]Ingredient, len(out.Ingredients))
	for i, ing := range out.Ingredients {
		ings[i] = Ingredient{
			Name: oneLine(ing.Name),
			Qty:  oneLine(ing.Qty),
			Unit: oneLine(ing.Unit),
			Note: oneLine(ing.Note),
		}
	}
	out.Ingredients = ings
	if m := jsonLDMinutes(s.in); m != nil {
		out.MinutesTotal = m
	}
	if mentionsSlowCooker(s.in) {
		out.Method = "crockpot"
	}
	if out.Difficulty == "" {
		minutes := 0
		if out.MinutesTotal != nil {
			minutes = *out.MinutesTotal
		}
		out.Difficulty = deriveDifficulty(len(s.in.Steps), minutes, out.Method == "crockpot")
	}
	return out, nil
}

// jsonLDMinutes returns a copy of the JSON-LD total time, or nil when the
// page gives none. PromptInput has already rejected a total that is not
// positive.
func jsonLDMinutes(in RecipeText) *int {
	if in.TotalMinutes == nil {
		return nil
	}
	m := *in.TotalMinutes
	return &m
}

func mentionsSlowCooker(in RecipeText) bool {
	if slowCooker.MatchString(in.Title) {
		return true
	}
	for _, step := range in.Steps {
		if slowCooker.MatchString(step) {
			return true
		}
	}
	return false
}

// deriveDifficulty grades a recipe by its step count and minutes, 0 when
// unknown. A crockpot recipe's minutes are mostly unattended, so they do
// not count.
func deriveDifficulty(steps, minutes int, crockpot bool) string {
	if crockpot {
		minutes = 0
	}
	switch {
	case steps > 10 || minutes > 90:
		return "hard"
	case steps <= 5 && minutes <= 45:
		return "easy"
	default:
		return "medium"
	}
}
